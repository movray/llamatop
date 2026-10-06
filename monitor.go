// Sample evaluation: time series, live rates and request detection.

package main

import (
	"time"

	"github.com/charmbracelet/lipgloss"
)

type point struct {
	t time.Time
	v float64
}

type series []point

func (s series) push(t time.Time, v float64) series {
	s = append(s, point{t, v})
	i := 0
	for i < len(s) && t.Sub(s[i].t) > cfg.UI.History.Duration {
		i++
	}
	return s[i:]
}

func (s series) last(n int) []float64 {
	if len(s) > n {
		s = s[len(s)-n:]
	}
	v := make([]float64, len(s))
	for i, p := range s {
		v[i] = p.v
	}
	return v
}

const (
	serGen = iota
	serPP
	serBusy
	serKV
	serQueue
	serPing
	numSeries
)

var seriesInfo = [numSeries]struct {
	name, unit string
	color      lipgloss.Color
}{
	serGen:   {"Gen", "t/s", cGreen},
	serPP:    {"Prefill", "t/s", cYellow},
	serBusy:  {"Active slots", "", cCyan},
	serKV:    {"KV usage", "%", cAccent},
	serQueue: {"Queued", "req", cRed},
	serPing:  {"Ping", "ms", cDim},
}

// Completed request. Times are accurate to the poll interval.
type reqRecord struct {
	end      time.Time
	slot     int
	task     int
	prompt   int
	cache    int
	gen      int
	nPredict int
	dur      time.Duration // 0 = started and finished between two polls, or partial
	ttft     time.Duration // <0 = first token already present at the first poll
	rate     float64
	params   map[string]any
	partial  bool // already running when first seen: start, TTFT and duration unknown
}

// Whether the number of generated tokens is known.
func (r reqRecord) genKnown() bool { return r.dur > 0 || r.partial }

type taskTrack struct {
	start    time.Time
	partial  bool // already running when first seen, start unknown
	prompt   int
	cache    int
	firstTok time.Time // estimated time of the first token
	obsAt    time.Time // first observation with generated tokens …
	obsDec   int       // … and their count
	lastAt   time.Time // latest observation with generated tokens …
	lastDec  int       // … and their count; both pairs give an exact t/s
}

// Totals since program start, independent of the log length.
type totals struct {
	reqs, prompt, cache, gen int
}

type monitor struct {
	props   *props
	model   *modelMeta
	lora    []loraAdapter
	loraErr error
	since   time.Time
	cur     sample
	prev    *sample
	genRate float64
	ppRate  float64
	slotGen map[int]float64
	slotPP  map[int]float64
	hist    [numSeries]series
	tracks  map[int]*taskTrack
	log     []reqRecord
	logSeq  int // number of requests ever logged, log[i] has number logSeq-len(log)+i
	tot     totals

	kvPct      float64 // KV usage of the current sample …
	kvOK       bool    // … valid only with slot data
	genSlots   int     // slots generating in both of the last two samples
	genPerSlot float64 // their mean gen rate
	genRefs    map[int]*genRef
	alerts     map[string]*alert
	alertLog   []alertEvent
}

func newMonitor() *monitor {
	return &monitor{tracks: map[int]*taskTrack{}}
}

// Live rates from slot deltas (the /metrics counters are only incremented
// when a task ends and are too jumpy for a live display).
func (m *monitor) update(s sample) {
	if s.props != nil {
		m.props = s.props
	}
	if s.model != nil {
		m.model = s.model
	}
	if s.loraPolled {
		m.lora, m.loraErr = s.lora, s.loraErr
	}
	if m.since.IsZero() {
		m.since = s.at
	}
	m.cur = s
	m.kvOK, m.genSlots, m.genPerSlot = false, 0, 0
	defer m.evalAlerts(s.at)
	m.slotGen = map[int]float64{}
	m.slotPP = map[int]float64{}
	m.genRate, m.ppRate = 0, 0
	if s.health != "down" && s.ping > 0 {
		m.hist[serPing] = m.hist[serPing].push(s.at, float64(s.ping.Microseconds())/1000)
	}
	if v, ok := s.metrics["requests_deferred"]; ok {
		m.hist[serQueue] = m.hist[serQueue].push(s.at, v)
	}
	// Without valid slots, touch neither history nor the comparison basis,
	// otherwise we get an artificial dip and the next rate is missing too.
	if s.slotsErr != nil {
		return
	}
	prev := map[int]slot{}
	if m.prev != nil {
		for _, ps := range m.prev.slots {
			prev[ps.ID] = ps
		}
		if dt := s.at.Sub(m.prev.at).Seconds(); dt > 0 {
			for _, cs := range s.slots {
				ps, ok := prev[cs.ID]
				if !ok || !cs.IsProcessing || cs.IDTask == nil || ps.IDTask == nil || *cs.IDTask != *ps.IDTask {
					continue
				}
				if ps.decoded() > 0 {
					m.genSlots++ // also counts a stalled slot (g == 0)
				}
				if g := float64(cs.decoded()-ps.decoded()) / dt; g > 0 {
					m.slotGen[cs.ID] = g
					m.genRate += g
				}
				if p := float64(cs.NPromptTokensProcessed-ps.NPromptTokensProcessed) / dt; p > 0 {
					m.slotPP[cs.ID] = p
					m.ppRate += p
				}
			}
		}
	}
	m.trackRequests(prev, s)

	busy := 0
	var used, total int
	for _, cs := range s.slots {
		if cs.IsProcessing {
			busy++
		}
		used += cs.used()
		total += cs.NCtx
	}
	m.hist[serGen] = m.hist[serGen].push(s.at, m.genRate)
	m.hist[serPP] = m.hist[serPP].push(s.at, m.ppRate)
	m.hist[serBusy] = m.hist[serBusy].push(s.at, float64(busy))
	m.hist[serKV] = m.hist[serKV].push(s.at, frac(used, total)*100)
	m.kvPct, m.kvOK = frac(used, total)*100, total > 0
	if m.genSlots > 0 {
		m.genPerSlot = m.genRate / float64(m.genSlots)
	}
	cp := s
	m.prev = &cp
}

// Detects task start and end from id_task changes.
func (m *monitor) trackRequests(prev map[int]slot, s sample) {
	// Estimate events between two polls at the middle of the interval.
	mid := s.at
	if m.prev != nil {
		mid = m.prev.at.Add(s.at.Sub(m.prev.at) / 2)
	}
	cur := map[int]slot{}
	for _, cs := range s.slots {
		cur[cs.ID] = cs
	}

	for id, ps := range prev {
		if !ps.IsProcessing || ps.IDTask == nil {
			continue
		}
		cs, ok := cur[id]
		same := ok && cs.IDTask != nil && *cs.IDTask == *ps.IDTask
		if same && cs.IsProcessing {
			continue
		}
		gen := ps.decoded()
		// After the task ends n_decoded is 0, but n_prompt_tokens still holds
		// prompt + generated – derive the final count from that.
		if same {
			if g := cs.NPromptTokens - ps.promptLen(); g > gen {
				gen = g
			}
		}
		m.finish(reqRecord{end: mid, slot: id, task: *ps.IDTask, prompt: ps.promptLen(),
			cache: ps.NPromptTokensCache, gen: gen, nPredict: ps.nPredict(), params: ps.Params})
	}

	if m.prev != nil {
		for id, cs := range cur {
			if cs.IsProcessing || cs.IDTask == nil || cs.NPromptTokens == 0 {
				continue
			}
			if ps, ok := prev[id]; ok && ps.IDTask != nil && *ps.IDTask == *cs.IDTask {
				continue
			}
			// Whole request between two polls: only the retained context is known.
			m.finish(reqRecord{end: mid, slot: id, task: *cs.IDTask, prompt: cs.NPromptTokens,
				cache: cs.NPromptTokensCache, nPredict: cs.nPredict(), params: cs.Params})
		}
	}

	active := map[int]bool{}
	for _, cs := range s.slots {
		if !cs.IsProcessing || cs.IDTask == nil {
			continue
		}
		active[*cs.IDTask] = true
		t := m.tracks[*cs.IDTask]
		if t == nil {
			// Without a previous sample, or if the task was already running in
			// it, the real start lies before what we have seen.
			ps, ok := prev[cs.ID]
			partial := m.prev == nil || (ok && ps.IsProcessing && ps.IDTask != nil && *ps.IDTask == *cs.IDTask)
			t = &taskTrack{start: mid, partial: partial}
			m.tracks[*cs.IDTask] = t
		}
		t.prompt, t.cache = cs.promptLen(), cs.NPromptTokensCache
		if cs.decoded() > 0 {
			if t.obsAt.IsZero() {
				t.obsAt, t.obsDec = s.at, cs.decoded()
				if !t.partial {
					t.firstTok = mid
				}
			}
			t.lastAt, t.lastDec = s.at, cs.decoded()
		}
	}
	for id := range m.tracks {
		if !active[id] {
			delete(m.tracks, id)
		}
	}
}

// Adds times from tracking and appends the request to the log.
func (m *monitor) finish(r reqRecord) {
	if t := m.tracks[r.task]; t != nil {
		r.partial = t.partial
		if !t.partial {
			r.dur = r.end.Sub(t.start)
			if !t.firstTok.IsZero() {
				r.ttft = t.firstTok.Sub(t.start)
				if r.ttft == 0 {
					r.ttft = -1
				}
			}
		}
		// Rate between two real observations is exact. With only one, fall back
		// to the estimated end, but not for partial tasks, where the window
		// can be tiny and the estimate far off.
		switch d := t.lastAt.Sub(t.obsAt).Seconds(); {
		case d > 0:
			r.rate = float64(t.lastDec-t.obsDec) / d
		case !t.partial && !t.obsAt.IsZero():
			if d := r.end.Sub(t.obsAt).Seconds(); d > 0 && r.gen > t.obsDec {
				r.rate = float64(r.gen-t.obsDec) / d
			}
		}
		delete(m.tracks, r.task)
	}
	m.tot.reqs++
	m.tot.prompt += r.prompt
	m.tot.cache += r.cache
	m.tot.gen += r.gen
	m.logSeq++
	m.log = append(m.log, r)
	if n := cfg.UI.LogSize; len(m.log) > n {
		m.log = m.log[len(m.log)-n:]
	}
}
