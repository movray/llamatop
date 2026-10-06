// llamatop – terminal dashboard for llama.cpp llama-server (Bubble Tea + Lip Gloss).
// Polls /health, /slots, /props, /v1/models, /lora-adapters and (if enabled) /metrics.
//
//	go build -o llamatop .
//	./llamatop -u http://localhost:8080 -i 1s
//	./llamatop -sim                     # built-in simulated server
//	./llamatop -once -output json       # snapshot for scripts
//	./llamatop -c llamatop.toml         # settings from a config file
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/term"
)

// Set by the release build: -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() {
	once, output, sim, printCfg, err := readConfig()
	if showVersion {
		fmt.Println("llamatop", version)
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "llamatop:", err)
		os.Exit(2)
	}
	applyConfig()
	if printCfg {
		writeConfig(os.Stdout, cfg, cfgFile)
		return
	}
	if sim {
		simURL, err := startSim()
		if err != nil {
			fmt.Fprintln(os.Stderr, "llamatop: simulator:", err)
			os.Exit(1)
		}
		cfg.Server.URL = simURL
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	u := &ui{
		ctx:    ctx,
		cancel: cancel,
		c: &client{
			base:   strings.TrimRight(cfg.Server.URL, "/"),
			key:    cfg.Server.APIKey,
			hc:     &http.Client{Timeout: cfg.Server.Timeout.Duration},
			hcLong: &http.Client{},
		},
		m:        newMonitor(),
		interval: cfg.Server.Interval.Duration,
		view:     viewIndex(cfg.UI.View),
		zoom:     slices.Index(zooms, cfg.UI.Range.Duration),
		reqSel:   -1,
	}
	if cfg.AI.URL != "" || cfg.AI.APIKey != "" {
		ai := *u.c
		if cfg.AI.URL != "" {
			ai.base = strings.TrimRight(cfg.AI.URL, "/")
		}
		if cfg.AI.APIKey != "" {
			ai.key = cfg.AI.APIKey
		}
		u.ai = &ai
	}

	if once {
		if w, _, err := term.GetSize(os.Stdout.Fd()); err == nil {
			u.width = w
		}
		// Rates need two samples.
		u.m.update(u.c.poll(ctx, true))
		time.Sleep(u.interval)
		u.m.update(u.c.poll(ctx, false))
		if output == "json" {
			if err := u.writeJSON(os.Stdout); err != nil {
				fmt.Fprintln(os.Stderr, "llamatop:", err)
				os.Exit(1)
			}
			return
		}
		fmt.Println(u.View())
		return
	}

	// Bubble Tea restores the terminal even on panic and SIGTERM.
	if _, err := tea.NewProgram(u, tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "llamatop:", err)
		os.Exit(1)
	}
}
