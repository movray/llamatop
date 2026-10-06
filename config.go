// Configuration: built-in defaults, overridden by the config file (-c),
// then by LLAMA_API_KEY, then by command-line flags.

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/charmbracelet/lipgloss"
)

// duration reads and writes values like "1s" or "15m" in the config file.
type duration struct{ time.Duration }

func (d *duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	d.Duration = v
	return err
}

func (d duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

type config struct {
	Server serverConfig `toml:"server"`
	AI     aiConfig     `toml:"ai"`
	UI     uiConfig     `toml:"ui"`
	Alerts alertConfig  `toml:"alerts"`
	Bench  benchConfig  `toml:"benchmark"`
	Colors colorConfig  `toml:"colors"`
}

type serverConfig struct {
	URL      string   `toml:"url"`
	APIKey   string   `toml:"api_key"`
	Interval duration `toml:"interval"`
	Timeout  duration `toml:"timeout"` // per poll request
}

type aiConfig struct {
	URL         string  `toml:"url"`     // empty = the monitored server
	APIKey      string  `toml:"api_key"` // empty = server.api_key
	MaxTokens   int     `toml:"max_tokens"`
	Temperature float64 `toml:"temperature"`
	Thinking    bool    `toml:"thinking"` // let reasoning models think (slower)
}

type uiConfig struct {
	View    string   `toml:"view"`  // start view
	Range   duration `toml:"range"` // start time range in History
	Fill    bool     `toml:"fill"`  // fill the area under the chart line
	History duration `toml:"history"`
	LogSize int      `toml:"log_size"`
}

type alertConfig struct {
	Enabled bool       `toml:"enabled"`
	KVWarn  float64    `toml:"kv_warn_pct"`
	KVCrit  float64    `toml:"kv_crit_pct"`
	Ping    duration   `toml:"ping"`
	GenDrop float64    `toml:"gen_drop"` // alert below this share of the usual gen speed
	Hold    holdConfig `toml:"hold"`
}

// How long a condition must persist before its alert fires.
type holdConfig struct {
	Down   duration `toml:"down"`
	Health duration `toml:"health"`
	KV     duration `toml:"kv"`
	Queue  duration `toml:"queue"`
	Ping   duration `toml:"ping"`
	Gen    duration `toml:"gen"`
}

type benchConfig struct {
	OutputTokens int `toml:"output_tokens"`
	ShortPrompt  int `toml:"short_prompt"`
	LongPrompt   int `toml:"long_prompt"`
	Parallel     int `toml:"parallel"` // 0 = as many as there are slots
}

type colorConfig struct {
	Accent string `toml:"accent"`
	Green  string `toml:"green"`
	Yellow string `toml:"yellow"`
	Red    string `toml:"red"`
	Cyan   string `toml:"cyan"`
	Dim    string `toml:"dim"`
	Border string `toml:"border"`
}

func defaultConfig() config {
	d := func(v time.Duration) duration { return duration{v} }
	return config{
		Server: serverConfig{URL: "http://localhost:8080", Interval: d(time.Second), Timeout: d(3 * time.Second)},
		AI:     aiConfig{MaxTokens: 1200, Temperature: 0.3},
		UI:     uiConfig{View: "overview", Range: d(5 * time.Minute), Fill: true, History: d(time.Hour), LogSize: 500},
		Alerts: alertConfig{Enabled: true, KVWarn: 85, KVCrit: 95, Ping: d(500 * time.Millisecond), GenDrop: 0.5,
			Hold: holdConfig{Down: d(3 * time.Second), Health: d(5 * time.Second), KV: d(10 * time.Second),
				Queue: d(10 * time.Second), Ping: d(10 * time.Second), Gen: d(15 * time.Second)}},
		Bench: benchConfig{OutputTokens: 128, ShortPrompt: 64, LongPrompt: 2048},
		Colors: colorConfig{Accent: "#7D56F4", Green: "#04B575", Yellow: "#E5C07B", Red: "#E06C75",
			Cyan: "#56B6C2", Dim: "244", Border: "238"},
	}
}

// Effective configuration, set once at startup, and the file it came from.
var (
	cfg         = defaultConfig()
	cfgFile     string
	showVersion bool
)

// defaultConfigFile returns ~/.config/llamatop/config.toml (or below
// $XDG_CONFIG_HOME) if it exists. Any other stat error returns the path,
// so loading it reports the problem instead of silently skipping the file.
func defaultConfigFile() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	p := filepath.Join(dir, "llamatop", "config.toml")
	if _, err := os.Stat(p); errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	return p
}

func loadConfigFile(path string, c *config) error {
	md, err := toml.DecodeFile(path, c)
	if err != nil {
		return err
	}
	if und := md.Undecoded(); len(und) > 0 {
		keys := make([]string, len(und))
		for i, k := range und {
			keys[i] = k.String()
		}
		return fmt.Errorf("unknown keys: %s", strings.Join(keys, ", "))
	}
	return nil
}

var colorRe = regexp.MustCompile(`^(#[0-9a-fA-F]{6}|[0-9]{1,3})$`)

func (c *config) validate() error {
	var errs []error
	check := func(ok bool, format string, args ...any) {
		if !ok {
			errs = append(errs, fmt.Errorf(format, args...))
		}
	}
	check(strings.HasPrefix(c.Server.URL, "http://") || strings.HasPrefix(c.Server.URL, "https://"), "server.url must start with http:// or https://")
	check(c.Server.Interval.Duration >= 100*time.Millisecond, "server.interval must be at least 100ms")
	check(c.Server.Timeout.Duration > 0, "server.timeout must be positive")
	check(c.AI.URL == "" || strings.HasPrefix(c.AI.URL, "http"), "ai.url must start with http:// or https://")
	check(c.AI.MaxTokens > 0, "ai.max_tokens must be positive")
	check(viewIndex(c.UI.View) >= 0, "ui.view must be one of %s", strings.Join(viewKeys, ", "))
	check(c.UI.History.Duration >= time.Minute, "ui.history must be at least 1m")
	check(c.UI.Range.Duration > 0 && c.UI.Range.Duration <= c.UI.History.Duration, "ui.range must be positive and not longer than ui.history")
	check(c.UI.LogSize > 0, "ui.log_size must be positive")
	check(c.Alerts.KVWarn > 0 && c.Alerts.KVWarn < c.Alerts.KVCrit && c.Alerts.KVCrit <= 100, "alerts: need 0 < kv_warn_pct < kv_crit_pct <= 100")
	check(c.Alerts.GenDrop > 0 && c.Alerts.GenDrop < 1, "alerts.gen_drop must be between 0 and 1")
	check(c.Bench.OutputTokens > 0 && c.Bench.ShortPrompt > 0 && c.Bench.LongPrompt > 0, "benchmark: token counts must be positive")
	check(c.Bench.Parallel >= 0, "benchmark.parallel must be 0 (all slots) or more")
	for name, v := range map[string]string{"accent": c.Colors.Accent, "green": c.Colors.Green, "yellow": c.Colors.Yellow,
		"red": c.Colors.Red, "cyan": c.Colors.Cyan, "dim": c.Colors.Dim, "border": c.Colors.Border} {
		check(colorRe.MatchString(v), "colors.%s must be #RRGGBB or an ANSI number 0-255, got %q", name, v)
	}
	return errors.Join(errs...)
}

var viewKeys = []string{"overview", "history", "requests", "tools", "analysis"}

func viewIndex(name string) int { return slices.Index(viewKeys, strings.ToLower(name)) }

// configPath finds -c/-config before the real flag parsing, because the file
// has to be loaded first so that flags can override it.
func configPath(args []string) string {
	for i, a := range args {
		for _, n := range []string{"-c", "--c", "-config", "--config"} {
			if a == n && i+1 < len(args) {
				return args[i+1]
			}
			if v, ok := strings.CutPrefix(a, n+"="); ok {
				return v
			}
		}
	}
	return ""
}

// Flags write straight into cfg, with the current values (defaults and the
// config file) as their defaults, so only flags given explicitly override.
func registerFlags(fs *flag.FlagSet, c *config) {
	fs.String("c", "", "config file (TOML); default ~/.config/llamatop/config.toml if it exists")
	fs.String("config", "", "same as -c")

	fs.StringVar(&c.Server.URL, "u", c.Server.URL, "llama-server base URL")
	// Keys via Func, so -h never prints a key from the config file as default.
	fs.Func("k", "API key (or LLAMA_API_KEY)", func(s string) error { c.Server.APIKey = s; return nil })
	fs.DurationVar(&c.Server.Interval.Duration, "i", c.Server.Interval.Duration, "poll interval")
	fs.DurationVar(&c.Server.Timeout.Duration, "timeout", c.Server.Timeout.Duration, "timeout per poll request")

	fs.StringVar(&c.AI.URL, "ai-url", c.AI.URL, "llama-server for the AI analysis (default: the monitored server)")
	fs.Func("ai-key", "API key for -ai-url (default: -k)", func(s string) error { c.AI.APIKey = s; return nil })
	fs.IntVar(&c.AI.MaxTokens, "ai-max-tokens", c.AI.MaxTokens, "max tokens of the AI answer")
	fs.BoolVar(&c.AI.Thinking, "ai-thinking", c.AI.Thinking, "let reasoning models think before answering (slower)")

	fs.StringVar(&c.UI.View, "view", c.UI.View, "start view: "+strings.Join(viewKeys, ", "))
	fs.DurationVar(&c.UI.Range.Duration, "range", c.UI.Range.Duration, "start time range in History")
	fs.BoolVar(&c.UI.Fill, "fill", c.UI.Fill, "fill the area under the chart line")
	fs.DurationVar(&c.UI.History.Duration, "history", c.UI.History.Duration, "how long the history is kept")
	fs.IntVar(&c.UI.LogSize, "log-size", c.UI.LogSize, "completed requests kept in the log")

	fs.BoolVar(&c.Alerts.Enabled, "alerts", c.Alerts.Enabled, "enable alerts")
	fs.Float64Var(&c.Alerts.KVWarn, "kv-warn", c.Alerts.KVWarn, "KV cache warning threshold in percent")
	fs.Float64Var(&c.Alerts.KVCrit, "kv-crit", c.Alerts.KVCrit, "KV cache critical threshold in percent")
	fs.DurationVar(&c.Alerts.Ping.Duration, "alert-ping", c.Alerts.Ping.Duration, "alert when the ping is slower")
	fs.Float64Var(&c.Alerts.GenDrop, "gen-drop", c.Alerts.GenDrop, "alert when gen speed falls below this share of the usual")

	fs.IntVar(&c.Bench.OutputTokens, "bench-tokens", c.Bench.OutputTokens, "benchmark: output tokens per request")
	fs.IntVar(&c.Bench.ShortPrompt, "bench-short", c.Bench.ShortPrompt, "benchmark: short prompt length in tokens")
	fs.IntVar(&c.Bench.LongPrompt, "bench-long", c.Bench.LongPrompt, "benchmark: long prompt length in tokens")
	fs.IntVar(&c.Bench.Parallel, "bench-parallel", c.Bench.Parallel, "benchmark: parallel requests, 0 = all slots")
}

// Printed by -print-config; keys are masked.
func writeConfig(w io.Writer, c config, file string) error {
	if file == "" {
		file = "none, built-in defaults"
	}
	fmt.Fprintf(w, "# config file: %s\n\n", file)
	mask := func(s string) string {
		if s != "" {
			return "***"
		}
		return s
	}
	c.Server.APIKey, c.AI.APIKey = mask(c.Server.APIKey), mask(c.AI.APIKey)
	return toml.NewEncoder(w).Encode(c)
}

// applyConfig pushes cfg into the parts that are set up at package init.
func applyConfig() {
	col := cfg.Colors
	cAccent, cGreen, cYellow, cRed = lipgloss.Color(col.Accent), lipgloss.Color(col.Green), lipgloss.Color(col.Yellow), lipgloss.Color(col.Red)
	cCyan, cDim, cBorder = lipgloss.Color(col.Cyan), lipgloss.Color(col.Dim), lipgloss.Color(col.Border)
	initStyles()
	for i, c := range [numSeries]lipgloss.Color{cGreen, cYellow, cCyan, cAccent, cRed, cDim} {
		seriesInfo[i].color = c
	}

	// Time ranges for History: the standard steps plus the configured one,
	// none longer than the history that is kept.
	zs := []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour, cfg.UI.Range.Duration}
	zooms = zooms[:0]
	for _, z := range zs {
		if z <= cfg.UI.History.Duration && !slices.Contains(zooms, z) {
			zooms = append(zooms, z)
		}
	}
	slices.Sort(zooms)
}

func readConfig() (once bool, output string, sim, printCfg bool, err error) {
	cfgFile = configPath(os.Args[1:])
	if cfgFile == "" {
		cfgFile = defaultConfigFile()
	}
	if cfgFile != "" {
		if err := loadConfigFile(cfgFile, &cfg); err != nil {
			return false, "", false, false, fmt.Errorf("config %s: %w", cfgFile, err)
		}
	}
	if k := os.Getenv("LLAMA_API_KEY"); k != "" {
		cfg.Server.APIKey = k
	}
	fset := flag.CommandLine
	registerFlags(fset, &cfg)
	fset.BoolVar(&once, "once", false, "take a snapshot (two polls, one interval apart), print, exit")
	fset.StringVar(&output, "output", "table", "output format for -once: table or json")
	fset.BoolVar(&sim, "sim", false, "monitor a built-in simulated llama-server instead of -u")
	fset.BoolVar(&printCfg, "print-config", false, "print the effective configuration as TOML and exit")
	fset.BoolVar(&showVersion, "version", false, "print the version and exit")
	fset.Parse(os.Args[1:])
	if output != "table" && output != "json" {
		return false, "", false, false, errors.New("-output must be table or json")
	}
	return once, output, sim, printCfg, cfg.validate()
}
