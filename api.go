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
}

type client struct {
	base   string
	key    string
	hc     *http.Client // polling, short timeout
	hcLong *http.Client // actions and benchmark, cancelled via context
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(b))
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
}

type loraAdapter struct {
	ID    int     `json:"id"`
	Path  string  `json:"path"`
	Scale float64 `json:"scale"`
}

func (c *client) poll(ctx context.Context, withProps bool) sample {
	s := sample{at: time.Now()}
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
	run(func() {
		b, code, err := c.get(ctx, "/slots")
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
	run(func() {
		b, code, err := c.get(ctx, "/metrics")
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
	if withProps {
		run(func() {
			b, code, err := c.get(ctx, "/props")
			if err != nil || code != 200 {
				return
			}
			var p props
			if json.Unmarshal(b, &p) == nil {
				s.props = &p
			}
		})
		run(func() {
			b, code, err := c.get(ctx, "/v1/models")
			if err != nil || code != 200 {
				return
			}
			var r struct {
				Data []struct {
					Meta *modelMeta `json:"meta"`
				} `json:"data"`
			}
			if json.Unmarshal(b, &r) == nil && len(r.Data) > 0 {
				s.model = r.Data[0].Meta
			}
		})
		run(func() {
			s.loraPolled = true
			b, code, err := c.get(ctx, "/lora-adapters")
			switch {
			case err != nil:
				s.loraErr = err
			case code != 200:
				s.loraErr = apiError(b, code)
			default:
				s.loraErr = json.Unmarshal(b, &s.lora)
			}
		})
	}
	wg.Wait()
	return s
}
