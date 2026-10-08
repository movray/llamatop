// Data sources: query and decode llama-server endpoints.

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

type nextToken struct {
	HasNextToken bool `json:"has_next_token"`
	NRemain      int  `json:"n_remain"`
	NDecoded     int  `json:"n_decoded"`
}

type slot struct {
	ID                     int            `json:"id"`
	NCtx                   int            `json:"n_ctx"`
	Speculative            bool           `json:"speculative"`
	IsProcessing           bool           `json:"is_processing"`
	IDTask                 *int           `json:"id_task"`
	NPromptTokens          int            `json:"n_prompt_tokens"`
	NPromptTokensProcessed int            `json:"n_prompt_tokens_processed"`
	NPromptTokensCache     int            `json:"n_prompt_tokens_cache"`
	Params                 map[string]any `json:"params"`
	NextToken              []nextToken    `json:"next_token"`
}

func (s slot) decoded() int {
	if len(s.NextToken) == 0 {
		return 0
	}
	return s.NextToken[0].NDecoded
}

// n_prompt_tokens (as of b10540) already includes the generated tokens during
// generation and remains as the retained context after the task finishes.
func (s slot) used() int { return s.NPromptTokens }

// Prompt length only, without generated tokens.
func (s slot) promptLen() int {
	if p := s.NPromptTokens - s.decoded(); p > 0 {
		return p
	}
	return 0
}

func (s slot) nPredict() int {
	if v, ok := s.Params["n_predict"].(float64); ok {
		return int(v)
	}
	return -1
}

type props struct {
	Role       string          `json:"role"` // "router" in router mode
	MaxInst    int             `json:"max_instances"`
	ModelPath  string          `json:"model_path"`
	ModelAlias string          `json:"model_alias"`
	ModelFtype string          `json:"model_ftype"`
	TotalSlots int             `json:"total_slots"`
	BuildInfo  string          `json:"build_info"`
	IsSleeping bool            `json:"is_sleeping"`
	Modalities map[string]bool `json:"modalities"`
	DefaultGen struct {
		NCtx int `json:"n_ctx"`
	} `json:"default_generation_settings"`
}

// meta object from /v1/models.
type modelMeta struct {
	NParams   int64 `json:"n_params"`
	Size      int64 `json:"size"`
	NCtxTrain int   `json:"n_ctx_train"`
	NVocab    int   `json:"n_vocab"`
	NCtx      int   `json:"n_ctx"`
}

// Entry of the router's /models list.
type routerModel struct {
	ID      string   `json:"id"`
	Aliases []string `json:"aliases"`
	Source  string   `json:"source"` // cache, preset, models dir
	Status  struct {
		Value    string          `json:"value"` // unloaded, loading, loaded, sleeping, downloading
		Args     []string        `json:"args"`  // command line of the model instance
		Failed   bool            `json:"failed"`
		ExitCode int             `json:"exit_code"`
		Progress json.RawMessage `json:"progress"` // shape depends on the state
	} `json:"status"`
	Meta *modelMeta `json:"meta"` // only while loaded
}

func (r routerModel) is(name string) bool { return r.ID == name || slices.Contains(r.Aliases, name) }

// Status as shown; a failed start is reported as "unloaded" with failed set.
func (r routerModel) state() string {
	if r.Status.Failed && r.Status.Value == "unloaded" {
		return "failed"
	}
	return r.Status.Value
}

// The model instance answers requests without being started.
func (r routerModel) running() bool {
	return r.Status.Value == "loaded" || r.Status.Value == "sleeping"
}

// Value of a command-line argument of the model instance, "" if not set.
func (r routerModel) arg(names ...string) string {
	a := r.Status.Args
	for i := 0; i+1 < len(a); i++ {
		if slices.Contains(names, a[i]) {
			return a[i+1]
		}
	}
	return ""
}

// Download progress in percent, -1 if unknown.
func (r routerModel) downloadPct() float64 {
	var files map[string]struct{ Done, Total int64 }
	if json.Unmarshal(r.Status.Progress, &files) != nil {
		return -1
	}
	var done, total int64
	for _, f := range files {
		done, total = done+f.Done, total+f.Total
	}
	if total <= 0 {
		return -1
	}
	return float64(done) / float64(total) * 100
}

type client struct {
	base     string
	key      string
	model    string       // router mode: the model requests go to, "" = the router itself
	autoload bool         // router mode: requests may load the model (only the AI analysis)
	hc       *http.Client // polling, short timeout
	hcLong   *http.Client // actions and benchmark, cancelled via context
}

// Router mode: GET requests name the model in the query, POST requests in the
// body. Without autoload the router answers "model is not loaded" instead of
// loading it, so monitoring and actions never start a model.
func (c *client) modelPath(path string) string {
	if c.model == "" {
		return path
	}
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	path += sep + "model=" + url.QueryEscape(c.model)
	if !c.autoload {
		path += "&autoload=false"
	}
	return path
}

func (c *client) get(ctx context.Context, path string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, 0, err
	}
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	return b, resp.StatusCode, err
}

// POST with a JSON body. out may be nil.
func (c *client) post(ctx context.Context, path string, in, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	if c.model != "" {
		// The router reads the model from the body; only objects can carry it.
		var obj map[string]any
		if json.Unmarshal(b, &obj) != nil {
			return errors.New("not possible in router mode")
		}
		obj["model"] = c.model
		if b, err = json.Marshal(obj); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+c.modelPath(path), bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	resp, err := c.hcLong.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err = io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return apiError(b, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(b, out)
}

// llama-server answers 501 when an endpoint is disabled by a startup flag.
var (
	errSlotsOff   = errors.New("disabled – start llama-server with --slots")
	errMetricsOff = errors.New("disabled – start llama-server with --metrics")
)

func apiError(b []byte, code int) error {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(b, &e) == nil && e.Error.Message != "" {
		return fmt.Errorf("HTTP %d: %s", code, e.Error.Message)
	}
	return fmt.Errorf("HTTP %d", code)
}

// Prometheus text format; llama.cpp uses no labels.
func parseMetrics(b []byte) map[string]float64 {
	m := map[string]float64{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		name := f[0]
		if i := strings.IndexByte(name, '{'); i >= 0 {
			name = name[:i]
		}
		v, err := strconv.ParseFloat(f[1], 64)
		if err != nil {
			continue
		}
		m[strings.TrimPrefix(name, "llamacpp:")] = v
	}
	return m
}

type sample struct {
	at         time.Time
	health     string
	ping       time.Duration
	slots      []slot
	slotsErr   error
	metrics    map[string]float64
	metricsErr error
	props      *props
	model      *modelMeta
	lora       []loraAdapter
	loraErr    error
	loraPolled bool // /lora-adapters was queried in this poll

	// Router mode
	target     string // model the per-model data belongs to, "" = none
	roleSeen   bool   // the router's own /props was read in this poll …
	router     bool   // … and it says router mode
	maxInst    int    // models the router keeps loaded at once, 0 = unlimited
	models     []routerModel
	modelsErr  error
	modelsSeen bool // /models was queried in this poll
}

type loraAdapter struct {
	ID    int     `json:"id"`
	Path  string  `json:"path"`
	Scale float64 `json:"scale"`
}

var (
	errNoModel  = errors.New("router mode – no model loaded, see 6 Models")
	errSleeping = errors.New("model is sleeping – slots not polled, that would wake it up")
)

func (c *client) getJSON(ctx context.Context, path string, out any) error {
	b, code, err := c.get(ctx, path)
	switch {
	case err != nil:
		return err
	case code != 200:
		return apiError(b, code)
	}
	return json.Unmarshal(b, out)
}

// One poll. In router mode (known from an earlier poll) the model list comes
// first: per-model endpoints are only asked for c.model if it is running,
// and /slots and /lora-adapters not while it sleeps, since they would wake it.
func (c *client) poll(ctx context.Context, withProps, router bool) sample {
	s := sample{at: time.Now(), target: c.model}
	var wg sync.WaitGroup
	run := func(f func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f()
		}()
	}
	run(func() {
		t0 := time.Now()
		_, code, err := c.get(ctx, "/health")
		s.ping = time.Since(t0)
		switch {
		case err != nil:
			s.health = "down"
		case code == 200:
			s.health = "ok"
		case code == 503:
			s.health = "loading"
		default:
			s.health = fmt.Sprintf("HTTP %d", code)
		}
	})
	slots, metrics := true, true
	if router {
		var r struct {
			Data []routerModel `json:"data"`
		}
		s.modelsSeen = true
		s.modelsErr = c.getJSON(ctx, "/models", &r)
		s.models = r.Data
		var err error
		switch i := slices.IndexFunc(s.models, func(m routerModel) bool { return m.is(c.model) }); {
		case s.modelsErr != nil:
			err = s.modelsErr
		case c.model == "":
			err = errNoModel
		case i < 0:
			err = fmt.Errorf("model %s not found on the server", c.model)
		case s.models[i].Status.Value == "sleeping":
			slots = false
			s.slotsErr = errSleeping
		case s.models[i].Status.Value != "loaded":
			err = fmt.Errorf("model %s is %s – see 6 Models", c.model, s.models[i].state())
		default:
			s.model = s.models[i].Meta
		}
		if err != nil {
			slots, metrics = false, false
			s.slotsErr, s.metricsErr = err, err
		}
	}
	if slots {
		run(func() {
			b, code, err := c.get(ctx, c.modelPath("/slots"))
			switch {
			case err != nil:
				s.slotsErr = err
			case code == http.StatusNotImplemented:
				s.slotsErr = errSlotsOff
			case code != 200:
				s.slotsErr = apiError(b, code)
			default:
				s.slotsErr = json.Unmarshal(b, &s.slots)
			}
		})
	}
	if metrics {
		run(func() {
			b, code, err := c.get(ctx, c.modelPath("/metrics"))
			switch {
			case err != nil:
				s.metricsErr = err
			case code == http.StatusNotImplemented:
				s.metricsErr = errMetricsOff
			case code != 200:
				s.metricsErr = apiError(b, code)
			default:
				s.metrics = parseMetrics(b)
			}
		})
	}
	if withProps {
		// The router's own /props (role "router"), or the only model's.
		run(func() {
			var p props
			if c.getJSON(ctx, "/props", &p) != nil {
				return
			}
			s.roleSeen, s.router, s.maxInst = true, p.Role == "router", p.MaxInst
			if !s.router {
				s.props = &p
			}
		})
		if router && metrics { // running, also while sleeping
			run(func() {
				var p props
				if c.getJSON(ctx, c.modelPath("/props"), &p) == nil {
					s.props = &p
				}
			})
		}
		if !router {
			run(func() {
				var r struct {
					Data []struct {
						Meta   *modelMeta      `json:"meta"`
						Status json.RawMessage `json:"status"` // only in router mode
					} `json:"data"`
				}
				if c.getJSON(ctx, "/v1/models", &r) == nil && len(r.Data) > 0 && r.Data[0].Status == nil {
					s.model = r.Data[0].Meta
				}
			})
		}
		if slots {
			run(func() {
				s.loraPolled = true
				s.loraErr = c.getJSON(ctx, c.modelPath("/lora-adapters"), &s.lora)
			})
		}
	}
	wg.Wait()
	return s
}
