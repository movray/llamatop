// Built-in llama-server simulator (--sim): serves the same endpoints as a real
// server, with random load, queueing and an occasional slowdown, so every view
// and action can be tried without a real server.

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	simSlots    = 4
	simCtx      = 16384
	simPrefill  = 1200.0 // t/s for one prefilling slot
	simGen      = 45.0   // t/s for one generating slot
	simTick     = 20 * time.Millisecond
	simSlowdown = 30 * time.Second // every 5 min gen runs at half speed this long
)

type simTimings struct {
	prompt, predicted   int
	promptMs, predictMs float64
}

type simReq struct {
	prompt, cached, nPredict int
	params                   map[string]any
	answer                   string
	done                     chan simTimings // nil for background load
}

type simSlot struct {
	id        int
	task      int
	req       *simReq
	nPrompt   int     // n_prompt_tokens: retained context, during gen incl. generated
	processed float64 // prefilled tokens (without cache)
	decoded   float64
	tPrompt   time.Duration
	tGen      time.Duration
}

type simServer struct {
	mu       sync.Mutex
	start    time.Time
	slots    []*simSlot
	queue    []*simReq
	nextTask int
	m        map[string]float64
	lora     []loraAdapter
	rnd      *rand.Rand
}

// startSim runs the simulator on a free local port and returns its URL.
func startSim() (string, error) {
	s := &simServer{
		start:    time.Now(),
		nextTask: 1,
		m:        map[string]float64{},
		lora:     []loraAdapter{{ID: 0, Path: "/models/lora/sim-style.gguf", Scale: 1}},
		rnd:      rand.New(rand.NewPCG(1, 2)),
	}
	for i := range simSlots {
		s.slots = append(s.slots, &simSlot{id: i})
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	// Pre-run a minute of load so the first view already shows activity.
	for range int(time.Minute / simTick) {
		s.step(simTick.Seconds())
	}
	go http.Serve(ln, s.mux())
	go s.run()
	return "http://" + ln.Addr().String(), nil
}

func (s *simServer) run() {
	t := time.NewTicker(simTick)
	defer t.Stop()
	for range t.C {
		s.mu.Lock()
		s.step(simTick.Seconds())
		s.mu.Unlock()
	}
}

// Load varies slowly, so phases of idle, normal and overload alternate.
func (s *simServer) arrivalRate() float64 {
	t := time.Since(s.start).Seconds()
	return 0.25 + 0.35*math.Max(0, math.Sin(t/40))
}

func (s *simServer) slow() bool {
	return time.Since(s.start)%(5*time.Minute) > 5*time.Minute-simSlowdown
}

func (s *simServer) step(dt float64) {
	if s.rnd.Float64() < s.arrivalRate()*dt {
		prompt := int(math.Exp(math.Log(80) + s.rnd.Float64()*math.Log(6000.0/80)))
		r := &simReq{prompt: prompt, nPredict: 64 + s.rnd.IntN(700)}
		if s.rnd.Float64() < 0.4 { // follow-up of a conversation: prefix cached
			r.cached = prompt * 7 / 10
		}
		s.queue = append(s.queue, r)
	}
	for _, sl := range s.slots {
		if sl.req == nil && len(s.queue) > 0 {
			s.assign(sl, s.queue[0])
			s.queue = s.queue[1:]
		}
	}

	var pre, gen int
	for _, sl := range s.slots {
		if sl.req == nil {
			continue
		}
		if sl.processed < float64(sl.req.prompt-sl.req.cached) {
			pre++
		} else {
			gen++
		}
	}
	genRate := simGen / (1 + 0.15*float64(max(gen-1, 0)))
	if s.slow() {
		genRate /= 2
	}
	if pre+gen > 0 {
		s.m["n_decode_total"]++
		s.m["n_busy_slots_per_decode"] = float64(pre + gen)
	}
	for _, sl := range s.slots {
		r := sl.req
		if r == nil {
			continue
		}
		todo := float64(r.prompt - r.cached)
		if sl.processed < todo {
			sl.processed = math.Min(todo, sl.processed+simPrefill/float64(pre)*dt)
			sl.tPrompt += simTick
			continue
		}
		sl.decoded = math.Min(float64(r.nPredict), sl.decoded+genRate*dt)
		sl.tGen += simTick
		sl.nPrompt = r.prompt + int(sl.decoded)
		if int(sl.decoded) >= r.nPredict {
			s.finish(sl)
		}
	}
}

func (s *simServer) assign(sl *simSlot, r *simReq) {
	sl.req, sl.task = r, s.nextTask
	s.nextTask++
	sl.nPrompt, sl.processed, sl.decoded, sl.tPrompt, sl.tGen = r.prompt, 0, 0, 0, 0
	if r.params == nil {
		r.params = map[string]any{"n_predict": float64(r.nPredict), "temperature": 0.7, "top_k": 40.0, "top_p": 0.95}
	}
}

func (s *simServer) finish(sl *simSlot) {
	r := sl.req
	t := simTimings{prompt: r.prompt - r.cached, predicted: int(sl.decoded),
		promptMs: float64(sl.tPrompt.Milliseconds()), predictMs: float64(sl.tGen.Milliseconds())}
	s.m["prompt_tokens_total"] += float64(t.prompt)
	s.m["prompt_tokens_cached_total"] += float64(r.cached)
	s.m["prompt_seconds_total"] += t.promptMs / 1000
	s.m["tokens_predicted_total"] += float64(t.predicted)
	s.m["tokens_predicted_seconds_total"] += t.predictMs / 1000
	s.m["n_tokens_max"] = math.Max(s.m["n_tokens_max"], float64(sl.nPrompt))
	sl.req, sl.decoded = nil, 0 // like llama-server: n_decoded drops to 0, the context stays
	if r.done != nil {
		r.done <- t
	}
}

func (s *simServer) mux() *http.ServeMux {
	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /props", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"model_path": "/models/sim-7B-Instruct-Q4_K_M.gguf", "model_alias": "sim-7b", "model_ftype": "Q4_K - Medium",
			"total_slots": simSlots, "build_info": "sim", "is_sleeping": false,
			"modalities":                  map[string]bool{"vision": false},
			"default_generation_settings": map[string]any{"n_ctx": simCtx},
		})
	})
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"data": []any{map[string]any{"id": "sim-7b",
			"meta": map[string]any{"n_params": 7.24e9, "size": 4.37e9, "n_ctx_train": 32768, "n_vocab": 32000}}}})
	})
	mux.HandleFunc("GET /slots", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		out := make([]map[string]any, 0, len(s.slots))
		for _, sl := range s.slots {
			o := map[string]any{"id": sl.id, "n_ctx": simCtx, "speculative": false, "is_processing": sl.req != nil,
				"n_prompt_tokens": sl.nPrompt, "n_prompt_tokens_processed": int(sl.processed),
				"next_token": []any{map[string]any{"has_next_token": sl.req != nil, "n_remain": -1, "n_decoded": int(sl.decoded)}}}
			if sl.task > 0 {
				o["id_task"] = sl.task
			}
			if r := sl.req; r != nil {
				o["n_prompt_tokens_cache"], o["params"] = r.cached, r.params
			}
			out = append(out, o)
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		busy := 0
		for _, sl := range s.slots {
			if sl.req != nil {
				busy++
			}
		}
		s.m["requests_processing"], s.m["requests_deferred"] = float64(busy), float64(len(s.queue))
		var b strings.Builder
		for k, v := range s.m {
			fmt.Fprintf(&b, "llamacpp:%s %g\n", k, v)
		}
		w.Write([]byte(b.String()))
	})
	mux.HandleFunc("GET /lora-adapters", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		writeJSON(w, s.lora)
	})
	mux.HandleFunc("POST /lora-adapters", func(w http.ResponseWriter, r *http.Request) {
		var in []loraAdapter
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			http.Error(w, `{"error":{"message":"bad json"}}`, 400)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, a := range in {
			for i := range s.lora {
				if s.lora[i].ID == a.ID {
					s.lora[i].Scale = a.Scale
				}
			}
		}
		writeJSON(w, map[string]bool{"success": true})
	})
	mux.HandleFunc("POST /slots/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("id"))
		s.mu.Lock()
		defer s.mu.Unlock()
		if err != nil || id < 0 || id >= len(s.slots) {
			w.WriteHeader(400)
			writeJSON(w, map[string]any{"error": map[string]string{"message": "invalid slot id"}})
			return
		}
		sl := s.slots[id]
		n := sl.nPrompt
		switch r.URL.Query().Get("action") {
		case "erase":
			if sl.req == nil {
				sl.nPrompt = 0
			}
			writeJSON(w, map[string]any{"id_slot": id, "n_erased": n})
		case "save":
			writeJSON(w, map[string]any{"id_slot": id, "n_saved": n, "n_written": n * 1100, "timings": map[string]float64{"save_ms": 12}})
		case "restore":
			writeJSON(w, map[string]any{"id_slot": id, "n_restored": n, "n_read": n * 1100, "timings": map[string]float64{"restore_ms": 9}})
		default:
			w.WriteHeader(400)
			writeJSON(w, map[string]any{"error": map[string]string{"message": "invalid action"}})
		}
	})
	// Both completion endpoints queue a request and block until it is done.
	complete := func(r *simReq) simTimings {
		r.done = make(chan simTimings, 1)
		s.mu.Lock()
		s.queue = append(s.queue, r)
		s.mu.Unlock()
		return <-r.done
	}
	mux.HandleFunc("POST /completion", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Prompt   string `json:"prompt"`
			NPredict int    `json:"n_predict"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		t := complete(&simReq{prompt: len(strings.Fields(in.Prompt)) + 2, nPredict: max(in.NPredict, 1)})
		writeJSON(w, map[string]any{"content": "…", "timings": map[string]any{
			"prompt_n": t.prompt, "prompt_ms": t.promptMs, "prompt_per_second": float64(t.prompt) / math.Max(t.promptMs/1000, 1e-3),
			"predicted_n": t.predicted, "predicted_ms": t.predictMs, "predicted_per_second": float64(t.predicted) / math.Max(t.predictMs/1000, 1e-3)}})
	})
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		answer := "- This is the simulator, so the analysis is a fixed example text.\n" +
			"- Load alternates between idle and overload; requests queue when all 4 slots are busy.\n" +
			"- Every 5 minutes generation runs at half speed for 30 s, which triggers the gen-speed alert.\n" +
			"Overall: connect to a real llama-server for a real analysis."
		complete(&simReq{prompt: 400 + len(b)/4, nPredict: 120})
		writeJSON(w, map[string]any{"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": answer}}}})
	})
	return mux
}
