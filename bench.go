// Mini benchmark: fixed test series via /completion, measured with the
// server timings (prefill and generation separately).

package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type benchVariant struct {
	name      string
	promptTok int // approximate prompt length
	parallel  int // 0 = as many as there are slots
}

// The test series: short and long prompt, single and parallel.
func benchVariants() []benchVariant {
	b := cfg.Bench
	par := b.Parallel // 0 = all slots
	return []benchVariant{
		{"short", b.ShortPrompt, 1},
		{"long", b.LongPrompt, 1},
		{"short", b.ShortPrompt, par},
		{"long", b.LongPrompt, par},
	}
}

type benchResult struct {
	name     string
	parallel int
	promptN  float64 // avg prompt tokens reported by the server
	ppMs     float64 // avg prefill time per request
	pp       float64 // avg prefill t/s per request
	gen      float64 // avg gen t/s per request
	total    float64 // all generated tokens / wall time
	wall     time.Duration
	err      error
}

type benchRun struct {
	at      time.Time
	model   string // router mode: the model benchmarked
	busy    int    // slots busy at start, skews the result
	results []benchResult
	done    bool
	aborted bool
}

type benchState struct {
	runs   []benchRun // oldest first
	cancel context.CancelFunc
	ctx    context.Context
	client *client // fixed for the run, even if the monitored model changes
	slots  int
}

func (b *benchState) running() bool { return b.cancel != nil }

type benchMsg struct {
	step int
	res  benchResult
}

// Words that common tokenizers usually count as one token.
var benchWords = strings.Fields(`the of and to in is was for on that with as by at from
his her they this have had not are but which one were all their there been when who more
time will would about into than them can only other new some could these two may first
then do any like my now over such our man me even most made after also did many before
must through back years where much your way well down should because each just those
people how too little state good very make world still own see men work long get here`)

// Prompts of equal length but different content, so no request benefits
// from another one's cache.
func benchPrompt(tok, seed int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Run %d. ", seed)
	x := uint32(seed*2654435761 + 1)
	for range tok {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		b.WriteString(benchWords[x%uint32(len(benchWords))])
		b.WriteByte(' ')
	}
	return b.String()
}

type completionTimings struct {
	PromptN            int     `json:"prompt_n"`
	PromptMs           float64 `json:"prompt_ms"`
	PromptPerSecond    float64 `json:"prompt_per_second"`
	PredictedN         int     `json:"predicted_n"`
	PredictedPerSecond float64 `json:"predicted_per_second"`
}

func runBenchVariant(ctx context.Context, c *client, v benchVariant, slots, seed int) benchResult {
	n := v.parallel
	if n == 0 {
		n = max(slots, 1)
	}
	r := benchResult{name: v.name, parallel: n}
	res := make([]struct {
		Timings completionTimings `json:"timings"`
	}, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	t0 := time.Now()
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = c.post(ctx, "/completion", map[string]any{
				"prompt":       benchPrompt(v.promptTok, seed*100+i),
				"n_predict":    cfg.Bench.OutputTokens,
				"ignore_eos":   true, // always full length
				"cache_prompt": false,
				"temperature":  0,
			}, &res[i])
		}()
	}
	wg.Wait()
	r.wall = time.Since(t0)
	var gen int
	for i := range n {
		if errs[i] != nil {
			r.err = errs[i]
			return r
		}
		t := res[i].Timings
		r.promptN += float64(t.PromptN) / float64(n)
		r.ppMs += t.PromptMs / float64(n)
		r.pp += t.PromptPerSecond / float64(n)
		r.gen += t.PredictedPerSecond / float64(n)
		gen += t.PredictedN
	}
	r.total = float64(gen) / r.wall.Seconds()
	return r
}

// Starts a new test series; the variants run one after another.
func (u *ui) startBench() tea.Cmd {
	b := &u.bench
	if b.running() {
		return nil
	}
	if u.router && u.m.cur.slotsErr != nil && !errors.Is(u.m.cur.slotsErr, errSleeping) {
		u.flash, u.flashErr, u.flashAt = "Benchmark: "+u.m.cur.slotsErr.Error(), true, time.Now()
		return nil
	}
	busy := 0
	for _, sl := range u.m.cur.slots {
		if sl.IsProcessing {
			busy++
		}
	}
	b.slots = len(u.m.cur.slots)
	if p := u.m.props; p != nil && p.TotalSlots > 0 {
		b.slots = p.TotalSlots
	}
	b.ctx, b.cancel = context.WithCancel(u.ctx)
	b.client = u.modelClient()
	b.runs = append(b.runs, benchRun{at: time.Now(), model: b.client.model, busy: busy})
	u.benchOff = 0 // newest run is on top
	return u.benchStep(0)
}

func (u *ui) benchStep(i int) tea.Cmd {
	ctx, c, slots, seed := u.bench.ctx, u.bench.client, u.bench.slots, len(u.bench.runs)*10+i
	v := benchVariants()[i]
	return func() tea.Msg { return benchMsg{i, runBenchVariant(ctx, c, v, slots, seed)} }
}

func (u *ui) benchDone(msg benchMsg) tea.Cmd {
	b := &u.bench
	run := &b.runs[len(b.runs)-1]
	if b.ctx.Err() != nil {
		run.aborted = true
	} else {
		run.results = append(run.results, msg.res)
		if msg.res.err == nil && msg.step+1 < len(benchVariants()) {
			return u.benchStep(msg.step + 1)
		}
	}
	run.done = true
	b.cancel()
	b.cancel = nil
	return nil
}
