package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func intp(v int) *int { return &v }

// testMonitor feeds a fixed request pattern: every 10 s slot 0 runs a task
// with 1 s prefill, then 10 tokens/s for 5 s (50 tokens = n_predict).
func testMonitor(polls int) (*monitor, time.Time) {
	m := newMonitor()
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	params := map[string]any{"n_predict": 50.0, "temperature": 0.3, "generation_prompt": "<|im_start|>assistant\n"}
	task, dec, busy := 10, 0, false
	at := t0
	for i := range polls {
		at = t0.Add(time.Duration(i) * time.Second)
		switch phase := i % 10; {
		case phase == 0:
			task++
			busy, dec = true, 0
		case phase < 2:
		case phase < 7:
			dec += 10
		default:
			busy = false
		}
		sl := slot{ID: 0, NCtx: 1000, IsProcessing: busy, IDTask: intp(task), NPromptTokens: 800 + dec,
			NPromptTokensProcessed: 600, NPromptTokensCache: 200, Params: params, NextToken: []nextToken{{NDecoded: dec}}}
		if !busy {
			sl.NextToken[0].NDecoded = 0
		}
		m.update(sample{at: at, health: "ok", ping: 2 * time.Millisecond, slots: []slot{sl, {ID: 1, NCtx: 1000}},
			metrics: map[string]float64{"requests_processing": 1, "requests_deferred": 0, "prompt_tokens_total": 1e5,
				"prompt_seconds_total": 90, "tokens_predicted_total": 3e4, "tokens_predicted_seconds_total": 700},
			lora: []loraAdapter{{ID: 0, Path: "/x/a.gguf", Scale: 0.5}}, loraPolled: true})
	}
	return m, at
}

func testUI(w, h int) *ui {
	m, _ := testMonitor(300)
	u := &ui{ctx: context.Background(), c: &client{base: "http://test"}, m: m, interval: time.Second,
		width: w, height: h, zoom: 1, reqSel: -1}
	u.bench.runs = []benchRun{{at: time.Now(), done: true, results: []benchResult{
		{name: "short", parallel: 1, promptN: 70, pp: 110, gen: 31, total: 27, wall: 5 * time.Second},
		{name: "long", parallel: 4, err: errors.New("HTTP 500: test")}}}}
	u.analysis.text = strings.Repeat("- a long line of analysis text that has to be wrapped to the panel width. ", 12)
	return u
}

func TestRequestTracking(t *testing.T) {
	m, _ := testMonitor(100)
	if m.tot.reqs != 10 {
		t.Fatalf("requests: got %d, want 10", m.tot.reqs)
	}
	r := m.log[len(m.log)-1]
	if r.gen != 50 || r.prompt != 800 || r.cache != 200 || r.nPredict != 50 || r.rate != 10 || r.partial {
		t.Errorf("record: %+v", r)
	}
	if r.ttft != 2*time.Second || r.dur != 7*time.Second {
		t.Errorf("ttft %s dur %s, want 2s and 7s", r.ttft, r.dur)
	}
}

func TestViewsFit(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 40}, {200, 60}} {
		w, h := size[0], size[1]
		u := testUI(w, h)
		for v := range numViews {
			u.view, u.reqDetail, u.detail = v, true, true
			lines := strings.Split(u.View(), "\n")
			if len(lines) != h {
				t.Errorf("%dx%d view %s: %d lines, want %d (footer pinned to the bottom)", w, h, viewNames[v], len(lines), h)
			}
			for i, l := range lines {
				if lw := ansi.StringWidth(l); lw > w {
					t.Errorf("%dx%d view %s line %d: width %d", w, h, viewNames[v], i, lw)
				}
			}
			if !strings.Contains(lines[len(lines)-1], viewNames[v]) {
				t.Errorf("%dx%d view %s: help line missing", w, h, viewNames[v])
			}
		}
	}
}

func TestAlerts(t *testing.T) {
	m := newMonitor()
	t0 := time.Now()
	full := func(i int, used int) {
		m.update(sample{at: t0.Add(time.Duration(i) * time.Second), health: "ok",
			slots: []slot{{ID: 0, NCtx: 100, NPromptTokens: used}}})
	}
	for i := range 9 {
		full(i, 97)
	}
	if len(m.activeAlerts()) != 0 {
		t.Fatal("alert fired before the hold time")
	}
	full(10, 97)
	if a := m.activeAlerts(); len(a) != 1 || a[0].rule.id != "kv-crit" {
		t.Fatalf("want kv-crit, got %v", a)
	}
	full(11, 10)
	if len(m.activeAlerts()) != 0 || len(m.alertLog) != 2 || !m.alertLog[1].resolved {
		t.Fatalf("alert not resolved: %+v", m.alertLog)
	}
}

func TestJSON(t *testing.T) {
	u := testUI(100, 0)
	var b bytes.Buffer
	if err := u.writeJSON(&b); err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"health", "throughput", "slots", "alerts", "metrics"} {
		if _, ok := out[k]; !ok {
			t.Errorf("key %q missing", k)
		}
	}
}

// End to end against the simulator: poll, slot action, LoRA, completion, analysis.
func TestSim(t *testing.T) {
	url, err := startSim()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	c := &client{base: url, hc: &http.Client{Timeout: 3 * time.Second}, hcLong: &http.Client{Timeout: time.Minute}}
	s := c.poll(ctx, true)
	if s.health != "ok" || s.slotsErr != nil || s.metricsErr != nil || s.props == nil || s.model == nil || len(s.lora) != 1 {
		t.Fatalf("poll: %+v", s)
	}
	if msg := slotActionCmd(ctx, c, 0, "save", "x.bin")().(actionMsg); msg.err != nil {
		t.Errorf("save: %v", msg.err)
	}
	if msg := setLoraCmd(ctx, c, map[int]float64{0: 0.3})().(actionMsg); msg.err != nil {
		t.Errorf("lora: %v", msg.err)
	}
	if r := runBenchVariant(ctx, c, benchVariant{"tiny", 8, 1}, 4, 1); r.err != nil || r.promptN == 0 {
		t.Errorf("completion: %+v", r)
	}
	if msg := analyzeCmd(ctx, c, "report")().(analysisMsg); msg.err != nil || msg.text == "" {
		t.Errorf("analysis: %+v", msg)
	}
}

// llamatop starts while a request is generating: start, TTFT and duration
// are unknown, tokens and rate are measured, and the stats skip the times.
func TestPartialRequest(t *testing.T) {
	m := newMonitor()
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for i, dec := range []int{20, 30, 40, 50, -1} {
		sl := slot{ID: 0, NCtx: 1000, IsProcessing: dec >= 0, IDTask: intp(5), NPromptTokens: 800 + max(dec, 50),
			NextToken: []nextToken{{NDecoded: max(dec, 0)}}}
		m.update(sample{at: t0.Add(time.Duration(i) * time.Second), health: "ok", slots: []slot{sl}})
	}
	if len(m.log) != 1 {
		t.Fatalf("log: %d records", len(m.log))
	}
	r := m.log[0]
	if !r.partial || r.dur != 0 || r.ttft != 0 || r.gen != 50 || r.rate != 10 {
		t.Errorf("record: %+v", r)
	}
	st := m.stats(t0.Add(time.Minute))
	if st.dur.n != 0 || st.ttft.n != 0 || st.gen.n != 1 || st.rate.n != 1 {
		t.Errorf("stats: dur %d ttft %d gen %d rate %d", st.dur.n, st.ttft.n, st.gen.n, st.rate.n)
	}
}
