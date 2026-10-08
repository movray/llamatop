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

// testRouter puts u into router mode with three models, one of them monitored.
func testRouter(u *ui) {
	mk := func(id, state string, args ...string) routerModel {
		var r routerModel
		r.ID, r.Source = id, "preset"
		r.Status.Value, r.Status.Args = state, append([]string{"/usr/bin/llama-server", "--jinja", "--alias", id}, args...)
		return r
	}
	failed := mk("broken-model", "unloaded")
	failed.Status.Failed, failed.Status.ExitCode = true, 1
	loaded := mk("unsloth/Qwen3.6-35B-A3B-MTP-GGUF:Q4_K_XL-with-a-very-long-name", "loaded", "--ctx-size", "32768", "--parallel", "2")
	loaded.Meta = &modelMeta{Size: 22842671616, NCtx: 16384}
	u.router, u.maxInst, u.model = true, 1, loaded.ID
	u.models = []routerModel{mk("gpt-oss-20b", "unloaded", "--ctx-size", "65536"), loaded, failed, mk("sleepy", "sleeping")}
}

func TestViewsFit(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {120, 40}, {200, 60}, {80, -24}, {200, -60}} {
		w, h := size[0], size[1]
		u := testUI(w, abs(h))
		if h < 0 { // negative height: router mode
			h = -h
			testRouter(u)
		}
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

func abs(v int) int { return max(v, -v) }

// Following the loaded model, keeping a pinned one, and resuming a monitor.
func TestFollowModel(t *testing.T) {
	u := testUI(100, 0)
	testRouter(u)
	u.model = ""
	u.handleSample(sample{roleSeen: true, router: true, maxInst: 1, modelsSeen: true, models: u.models})
	if u.model != u.models[1].ID {
		t.Fatalf("follow: monitoring %q", u.model)
	}
	first := u.m
	u.models[1].Status.Value, u.models[0].Status.Value = "unloaded", "loaded"
	u.handleSample(sample{target: u.model, modelsSeen: true, models: u.models})
	if u.model != "gpt-oss-20b" || u.m == first {
		t.Fatalf("follow after switch: monitoring %q", u.model)
	}
	u.pinned = true
	u.switchModel(u.models[1].ID)
	if u.m != first || first.prev != nil {
		t.Error("monitor not resumed")
	}
	u.handleSample(sample{target: u.model, modelsSeen: true, models: u.models})
	if u.model != u.models[1].ID {
		t.Errorf("pinned model left: %q", u.model)
	}
	// A sample of another model must not reach the monitor.
	n := len(u.m.hist[serPing])
	u.handleSample(sample{target: "other", health: "ok", ping: time.Millisecond, at: time.Now()})
	if len(u.m.hist[serPing]) != n {
		t.Error("foreign sample was recorded")
	}
}

func TestModelPath(t *testing.T) {
	c := &client{model: "org/m:Q4"}
	for in, want := range map[string]string{
		"/slots":                "/slots?model=org%2Fm%3AQ4&autoload=false",
		"/slots/1?action=erase": "/slots/1?action=erase&model=org%2Fm%3AQ4&autoload=false",
	} {
		if got := c.modelPath(in); got != want {
			t.Errorf("%s: got %s", in, got)
		}
	}
	c.autoload = true
	if got := c.modelPath("/props"); got != "/props?model=org%2Fm%3AQ4" {
		t.Errorf("autoload: got %s", got)
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
	t.Parallel()
	url, err := startSim()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	c := &client{base: url, hc: &http.Client{Timeout: 3 * time.Second}, hcLong: &http.Client{Timeout: time.Minute}}
	s := c.poll(ctx, true, false)
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

// End to end against the router simulator: detection, per-model polling
// without loading, actions routed by model, load and unload.
func TestSimRouter(t *testing.T) {
	t.Parallel()
	url, err := startSimRouter()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	u := &ui{ctx: ctx, m: newMonitor(), interval: time.Second, reqSel: -1,
		c: &client{base: url, hc: &http.Client{Timeout: 3 * time.Second}, hcLong: &http.Client{Timeout: time.Minute}}}
	u.handleSample(u.modelClient().poll(ctx, true, u.router))
	if !u.router || u.maxInst != 2 {
		t.Fatalf("router not detected: %+v", u)
	}
	for i := 0; i < 2 && (u.model == "" || u.m.cur.at.IsZero()); i++ {
		u.handleSample(u.modelClient().poll(ctx, true, true))
	}
	s := u.m.cur
	if u.model != "sim-7b" || s.slotsErr != nil || s.metricsErr != nil || s.props == nil || s.model == nil || len(s.slots) != 4 {
		t.Fatalf("poll sim-7b (%q): %+v", u.model, s)
	}
	if msg := slotActionCmd(ctx, u.modelClient(), 0, "erase", "")().(actionMsg); msg.err != nil {
		t.Errorf("erase: %v", msg.err)
	}
	if r := runBenchVariant(ctx, u.modelClient(), benchVariant{"tiny", 8, 1}, 4, 1); r.err != nil {
		t.Errorf("completion: %+v", r)
	}
	if err := u.modelClient().post(ctx, "/lora-adapters", []int{1}, nil); err == nil {
		t.Error("a list body cannot be routed")
	}

	// Pinned to an unloaded model: polling must not load it.
	u.pinned = true
	u.switchModel("sim-32b")
	u.handleSample(u.modelClient().poll(ctx, true, true))
	if s := u.m.cur; s.slotsErr == nil || !strings.Contains(s.slotsErr.Error(), "unloaded") || u.findModel("sim-32b").Status.Value != "unloaded" {
		t.Fatalf("unloaded model: %v", s.slotsErr)
	}
	if msg := modelActionCmd(ctx, u.c, "load", "sim-32b")().(actionMsg); msg.err != nil {
		t.Fatalf("load: %v", msg.err)
	}
	u.handleSample(u.modelClient().poll(ctx, false, true))
	if st := u.findModel("sim-32b").Status.Value; st != "loading" {
		t.Errorf("after load: %s", st)
	}
	if msg := modelActionCmd(ctx, u.c, "unload", "sim-32b")().(actionMsg); msg.err != nil {
		t.Errorf("unload: %v", msg.err)
	}
	// The analysis goes to the monitored model by default, may load its
	// model (like -ai-model would) and waits for it.
	if ac := u.aiClient(); ac.model != "sim-32b" || !ac.autoload {
		t.Errorf("AI client: %+v", ac)
	}
	ac := *u.c
	ac.model, ac.autoload = "sim-coder-1.5b", true
	if msg := analyzeCmd(ctx, &ac, "report")().(analysisMsg); msg.err != nil || msg.text == "" {
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

func TestFmtDur(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0:                                     "–",
		9500 * time.Millisecond:               "9.5s",
		22 * time.Second:                      "22s",
		59*time.Second + 900*time.Millisecond: "59s",
		3*time.Minute + 5*time.Second:         "3m05s",
		time.Hour + 5*time.Minute + 40*time.Second: "1h05m",
		26 * time.Hour: "26h00m",
	} {
		if got := fmtDur(d); got != want {
			t.Errorf("fmtDur(%s) = %q, want %q", d, got, want)
		}
	}
}
