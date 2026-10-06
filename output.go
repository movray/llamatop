// Machine-readable snapshot for -once -output json.

package main

import (
	"encoding/json"
	"io"
	"path/filepath"
	"time"
)

type jsonSnapshot struct {
	Time         string             `json:"time"`
	URL          string             `json:"url"`
	Health       string             `json:"health"`
	PingMs       float64            `json:"ping_ms"`
	Model        *jsonModel         `json:"model,omitempty"`
	Throughput   jsonThroughput     `json:"throughput"`
	Slots        []jsonSlot         `json:"slots"`
	SlotsError   string             `json:"slots_error,omitempty"`
	Metrics      map[string]float64 `json:"metrics,omitempty"`
	MetricsError string             `json:"metrics_error,omitempty"`
	LoRA         []loraAdapter      `json:"lora,omitempty"`
	Alerts       []jsonAlert        `json:"alerts"`
}

type jsonModel struct {
	File       string `json:"file"`
	Alias      string `json:"alias"`
	Ftype      string `json:"ftype"`
	Params     int64  `json:"n_params,omitempty"`
	SizeBytes  int64  `json:"size_bytes,omitempty"`
	CtxPerSlot int    `json:"n_ctx_slot"`
	CtxTrain   int    `json:"n_ctx_train,omitempty"`
	Slots      int    `json:"total_slots"`
	Build      string `json:"build"`
}

type jsonThroughput struct {
	GenTPS      float64 `json:"gen_tps"`
	PrefillTPS  float64 `json:"prefill_tps"`
	ActiveSlots int     `json:"active_slots"`
	KVUsed      int     `json:"kv_used"`
	KVTotal     int     `json:"kv_total"`
	KVPct       float64 `json:"kv_pct"`
}

type jsonSlot struct {
	ID         int     `json:"id"`
	State      string  `json:"state"`
	Task       *int    `json:"task"`
	Used       int     `json:"used"`
	NCtx       int     `json:"n_ctx"`
	Decoded    int     `json:"decoded"`
	GenTPS     float64 `json:"gen_tps"`
	PrefillTPS float64 `json:"prefill_tps"`
}

type jsonAlert struct {
	Severity string `json:"severity"`
	Text     string `json:"text"`
	Since    string `json:"since"`
}

func slotState(sl slot) string {
	switch {
	case sl.IsProcessing && sl.decoded() == 0:
		return "prefill"
	case sl.IsProcessing:
		return "gen"
	case sl.NPromptTokens > 0:
		return "warm"
	}
	return "idle"
}

func (u *ui) writeJSON(w io.Writer) error {
	m, s := u.m, u.m.cur
	out := jsonSnapshot{
		Time: s.at.Format(time.RFC3339), URL: u.c.base, Health: s.health,
		PingMs: float64(s.ping.Microseconds()) / 1000, Metrics: s.metrics, LoRA: m.lora,
		Slots: []jsonSlot{}, Alerts: []jsonAlert{},
	}
	if p := m.props; p != nil {
		out.Model = &jsonModel{File: filepath.Base(p.ModelPath), Alias: p.ModelAlias, Ftype: p.ModelFtype,
			CtxPerSlot: p.DefaultGen.NCtx, Slots: p.TotalSlots, Build: p.BuildInfo}
		if mm := m.model; mm != nil {
			out.Model.Params, out.Model.SizeBytes, out.Model.CtxTrain = mm.NParams, mm.Size, mm.NCtxTrain
		}
	}
	if s.slotsErr != nil {
		out.SlotsError = s.slotsErr.Error()
	}
	if s.metricsErr != nil {
		out.MetricsError = s.metricsErr.Error()
	}
	t := &out.Throughput
	t.GenTPS, t.PrefillTPS = m.genRate, m.ppRate
	for _, sl := range s.slots {
		if sl.IsProcessing {
			t.ActiveSlots++
		}
		t.KVUsed += sl.used()
		t.KVTotal += sl.NCtx
		out.Slots = append(out.Slots, jsonSlot{ID: sl.ID, State: slotState(sl), Task: sl.IDTask, Used: sl.used(),
			NCtx: sl.NCtx, Decoded: sl.decoded(), GenTPS: m.slotGen[sl.ID], PrefillTPS: m.slotPP[sl.ID]})
	}
	t.KVPct = frac(t.KVUsed, t.KVTotal) * 100
	for _, a := range m.activeAlerts() {
		name, _ := sevColorName(a.rule.sev)
		out.Alerts = append(out.Alerts, jsonAlert{Severity: name, Text: a.text, Since: a.since.Format(time.RFC3339)})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
