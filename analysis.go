// AI analysis: the monitoring data is summarised as text and sent to a
// llama-server (by default the monitored one) for an assessment.

package main

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const analysisSystem = `You are an expert in operating llama.cpp llama-server.
You get monitoring data of one server. Assess it for an operator:
bottlenecks, misconfiguration (slot count, context size, KV cache, batching),
and concrete llama-server flags or settings worth trying.
Answer in English as plain text without markdown headings or tables:
at most 8 short bullet points starting with "- ", then one line starting with "Overall:".
Only use the data given; say so if data is missing.`

type analysisState struct {
	cancel context.CancelFunc
	report string // data sent with the last request
	text   string
	err    error
	at     time.Time
	took   time.Duration
	off    int  // first visible line
	page   int  // visible lines, set while rendering
	raw    bool // show the data sent instead of the answer
}

func (a *analysisState) running() bool { return a.cancel != nil }

type analysisMsg struct {
	text string
	err  error
	took time.Duration
}

var thinkRe = regexp.MustCompile(`(?s)<think>.*?</think>`)

func analyzeCmd(ctx context.Context, c *client, report string) tea.Cmd {
	return func() tea.Msg {
		t0 := time.Now()
		var out struct {
			Choices []struct {
				Message struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
				} `json:"message"`
			} `json:"choices"`
		}
		err := c.post(ctx, "/v1/chat/completions", map[string]any{
			"messages": []map[string]string{
				{"role": "system", "content": analysisSystem},
				{"role": "user", "content": report},
			},
			"max_tokens":  cfg.AI.MaxTokens,
			"temperature": cfg.AI.Temperature,
			// Reasoning models answer much faster without thinking; ignored by others.
			"chat_template_kwargs": map[string]any{"enable_thinking": cfg.AI.Thinking},
		}, &out)
		msg := analysisMsg{err: err, took: time.Since(t0)}
		if err == nil && len(out.Choices) > 0 {
			m := out.Choices[0].Message
			msg.text = strings.TrimSpace(thinkRe.ReplaceAllString(m.Content, ""))
			if msg.text == "" {
				msg.text = strings.TrimSpace(m.ReasoningContent)
			}
		}
		if err == nil && msg.text == "" {
			msg.err = fmt.Errorf("empty answer from the server")
		}
		return msg
	}
}

func (u *ui) startAnalysis() tea.Cmd {
	a := &u.analysis
	if a.running() {
		return nil
	}
	var ctx context.Context
	ctx, a.cancel = context.WithCancel(u.ctx)
	a.report, a.text, a.err, a.off, a.raw = u.analysisReport(), "", nil, 0, false
	a.at = time.Now()
	return analyzeCmd(ctx, u.aiClient(), a.report)
}

func (u *ui) analysisDone(msg analysisMsg) {
	a := &u.analysis
	if a.cancel == nil {
		return
	}
	a.cancel()
	a.cancel = nil
	a.text, a.err, a.took = msg.text, msg.err, msg.took
}

// Plain-text summary of everything llamatop knows.
func (u *ui) analysisReport() string {
	m, s := u.m, u.m.cur
	var b strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }

	line("Server: %s, health %s, ping %d ms", u.c.base, s.health, s.ping.Milliseconds())
	if u.router {
		var names []string
		for _, r := range u.models {
			names = append(names, r.ID+" ("+r.state()+")")
		}
		line("Router mode, at most %d model(s) loaded at once (0 = unlimited): %s", u.maxInst, strings.Join(names, ", "))
		line("The data below is for model %s only", u.model)
	}
	if p := m.props; p != nil {
		line("Model: %s (alias %s, %s), build %s", filepath.Base(p.ModelPath), p.ModelAlias, p.ModelFtype, p.BuildInfo)
		line("Slots: %d, context per slot %d", p.TotalSlots, p.DefaultGen.NCtx)
	}
	if mm := m.model; mm != nil {
		line("Parameters %s, file size %s, trained context %d", fmtSI(float64(mm.NParams)), fmtSI(float64(mm.Size))+"B", mm.NCtxTrain)
	}
	for _, a := range m.lora {
		line("LoRA adapter %s scale %s", filepath.Base(a.Path), fmtVal(a.Scale))
	}

	line("\nNow: gen %.1f t/s, prefill %.1f t/s, KV cache %.1f%%", m.genRate, m.ppRate, m.kvPct)
	for _, sl := range s.slots {
		state := "idle"
		switch {
		case sl.IsProcessing && sl.decoded() == 0:
			state = "prefill"
		case sl.IsProcessing:
			state = "generating"
		case sl.NPromptTokens > 0:
			state = "idle with cached context"
		}
		line("  slot %d: %s, %d/%d tokens", sl.ID, state, sl.used(), sl.NCtx)
	}

	end := u.now()
	line("\nLast 15 min (avg / max per poll):")
	for i, info := range seriesInfo {
		var n int
		var sum, hi float64
		for _, p := range m.hist[i] {
			if end.Sub(p.t) <= 15*time.Minute {
				n++
				sum += p.v
				hi = max(hi, p.v)
			}
		}
		if n > 0 {
			line("  %s: %s / %s %s", info.name, fmtSI(sum/float64(n)), fmtSI(hi), info.unit)
		}
	}

	if st := m.stats(end); st.n > 0 {
		t := m.tot
		line("\nCompleted requests: %d since %s (times accurate to ±%s)", t.reqs, m.since.Format("15:04"), u.interval)
		if st.window > 0 {
			line("  rate %.1f req/min, %s prompt tokens/min, %s gen tokens/min", st.perMin, fmtSI(st.promptMin), fmtSI(st.genMin))
		}
		line("  prompt cache hit %.0f%% of prompt tokens", frac(t.cache, t.prompt)*100)
		d := func(name string, x dist, f func(float64) string) {
			if x.n > 0 {
				line("  %s: avg %s, p50 %s, p95 %s, max %s (n=%d)", name, f(x.avg), f(x.p50), f(x.p95), f(x.max), x.n)
			}
		}
		d("TTFT", st.ttft, fmtSec)
		d("duration", st.dur, fmtSec)
		d("gen t/s per request", st.rate, func(v float64) string { return fmt.Sprintf("%.1f", v) })
		d("prompt tokens", st.prompt, fmtSI)
		d("gen tokens", st.gen, fmtSI)
	}

	if ms := s.metrics; len(ms) > 0 {
		line("\nServer counters (/metrics):")
		keys := make([]string, 0, len(ms))
		for k := range ms {
			// Reset on every scrape, see serverLines.
			if k != "prompt_tokens_seconds" && k != "predicted_tokens_seconds" {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			line("  %s %s", k, fmtVal(ms[k]))
		}
	} else if s.metricsErr != nil {
		line("\n/metrics not available: %s", s.metricsErr)
	}

	if runs := u.bench.runs; len(runs) > 0 {
		run := runs[len(runs)-1]
		line("\nLast benchmark (%s, %d output tokens, prompt cache off):", run.at.Format("15:04"), cfg.Bench.OutputTokens)
		for _, r := range run.results {
			if r.err != nil {
				line("  %s x%d: error %s", r.name, r.parallel, r.err)
				continue
			}
			line("  %s prompt (%0.f tok) x%d: prefill %.0f t/s, gen %.1f t/s per request, total %.1f t/s",
				r.name, r.promptN, r.parallel, r.pp, r.gen, r.total)
		}
	}

	if act := m.activeAlerts(); len(act) > 0 {
		line("\nActive alerts:")
		for _, a := range act {
			name, _ := sevColorName(a.rule.sev)
			line("  %s %s (since %s)", name, a.text, a.since.Format("15:04:05"))
		}
	}
	if n := len(m.alertLog); n > 0 {
		line("\nRecent alert events:")
		for _, e := range m.alertLog[max(n-10, 0):] {
			what := "fired"
			if e.resolved {
				what = "resolved"
			}
			line("  %s %s %s", e.at.Format("15:04:05"), what, e.text)
		}
	}
	return b.String()
}

// Server for the analysis: -ai-url if given, else the monitored server. In
// router mode -ai-model picks the model, by default the monitored one; unlike
// monitoring, the analysis may load it.
func (u *ui) aiClient() *client {
	c := *u.c
	if u.ai != nil {
		c = *u.ai
	}
	switch {
	case cfg.AI.Model != "":
		c.model = cfg.AI.Model
	case u.ai == nil && u.router:
		c.model = u.model
	}
	c.autoload = true
	return &c
}

// Shown where the analysis is sent to.
func (u *ui) aiTarget() string {
	c := u.aiClient()
	if c.model != "" {
		return c.base + " (" + c.model + ")"
	}
	return c.base
}
