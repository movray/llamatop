package main

import (
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func writeTemp(t *testing.T, content string) string {
	p := filepath.Join(t.TempDir(), "c.toml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// The example file documents the defaults, so it must match them exactly.
func TestExampleConfigIsDefault(t *testing.T) {
	c := defaultConfig()
	c.Server.URL = "changed"
	if err := loadConfigFile("llamatop.example.toml", &c); err != nil {
		t.Fatal(err)
	}
	if want := defaultConfig(); !reflect.DeepEqual(c, want) {
		t.Errorf("example differs from defaults:\n got %+v\nwant %+v", c, want)
	}
}

func TestConfigPrecedence(t *testing.T) {
	p := writeTemp(t, "[server]\nurl = \"http://file:1\"\ninterval = \"2s\"\n[alerts]\nkv_warn_pct = 70.0\n")
	c := defaultConfig()
	if err := loadConfigFile(p, &c); err != nil {
		t.Fatal(err)
	}
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	registerFlags(fs, &c)
	if err := fs.Parse([]string{"-c", p, "-i", "5s"}); err != nil {
		t.Fatal(err)
	}
	if c.Server.URL != "http://file:1" || c.Server.Interval.Duration != 5*time.Second || c.Alerts.KVWarn != 70 || c.Alerts.KVCrit != 95 {
		t.Errorf("got url %s interval %s kv %v/%v", c.Server.URL, c.Server.Interval, c.Alerts.KVWarn, c.Alerts.KVCrit)
	}
}

func TestConfigPath(t *testing.T) {
	for _, args := range [][]string{{"-c", "x"}, {"-i", "1s", "--config", "x"}, {"-c=x"}, {"--config=x"}} {
		if p := configPath(args); p != "x" {
			t.Errorf("%v: got %q", args, p)
		}
	}
}

func TestConfigErrors(t *testing.T) {
	c := defaultConfig()
	if err := loadConfigFile(writeTemp(t, "[server]\nurll = \"x\"\n"), &c); err == nil || !strings.Contains(err.Error(), "server.urll") {
		t.Errorf("unknown key not reported: %v", err)
	}
	c = defaultConfig()
	c.Alerts.KVWarn, c.UI.View, c.Colors.Red = 99, "nope", "red"
	err := c.validate()
	for _, want := range []string{"kv_warn_pct", "ui.view", "colors.red"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("validation misses %s: %v", want, err)
		}
	}
	if d := defaultConfig(); d.validate() != nil {
		t.Errorf("defaults invalid: %v", d.validate())
	}
}

func TestDefaultConfigFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if p := defaultConfigFile(); p != "" {
		t.Errorf("missing file: got %q, want none", p)
	}
	want := filepath.Join(dir, "llamatop", "config.toml")
	os.MkdirAll(filepath.Dir(want), 0o700)
	os.WriteFile(want, []byte("[ui]\nview = \"history\"\n"), 0o600)
	if p := defaultConfigFile(); p != want {
		t.Errorf("got %q, want %q", p, want)
	}
}
