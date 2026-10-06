// Alert rules with a hold time: a condition must persist before it fires,
// so short spikes do not cause false alarms.

package main

import (
	"fmt"
	"sort"
	"time"
)

const (
	sevWarn = iota
	sevCrit
)

const (
	alertLogLen = 100
	genRefAlpha = 0.02 // smoothing of the per-slot gen rate baseline
	genRefMinN  = 20   // samples needed before the baseline is trusted
)

type alertRule struct {
	id   string
	sev  int
	hold *duration // points into cfg.Alerts.Hold
	// check reports whether the condition holds and describes it.
	check func(m *monitor) (bool, string)
}

type alert struct {
	rule   *alertRule
	since  time.Time // condition true since
	firing bool
	text   string
}

type alertEvent struct {
	at       time.Time
	sev      int
	text     string
	resolved bool
}

// Baseline of the gen rate per slot for one level of concurrency, because
// each slot gets slower when several slots generate at once.
type genRef struct {
	avg float64
	n   int
}

var alertRules = []*alertRule{
	{id: "down", sev: sevCrit, hold: &cfg.Alerts.Hold.Down, check: func(m *monitor) (bool, string) {
		return m.cur.health == "down", "server unreachable"
	}},
	{id: "health", sev: sevWarn, hold: &cfg.Alerts.Hold.Health, check: func(m *monitor) (bool, string) {
		h := m.cur.health
		return h != "" && h != "ok" && h != "down", "server health: " + h
	}},
	{id: "kv-crit", sev: sevCrit, hold: &cfg.Alerts.Hold.KV, check: func(m *monitor) (bool, string) {
		return m.kvOK && m.kvPct >= cfg.Alerts.KVCrit, fmt.Sprintf("KV cache %.0f%% full", m.kvPct)
	}},
	{id: "kv-warn", sev: sevWarn, hold: &cfg.Alerts.Hold.KV, check: func(m *monitor) (bool, string) {
		return m.kvOK && m.kvPct >= cfg.Alerts.KVWarn && m.kvPct < cfg.Alerts.KVCrit, fmt.Sprintf("KV cache %.0f%% full", m.kvPct)
	}},
	{id: "queue", sev: sevWarn, hold: &cfg.Alerts.Hold.Queue, check: func(m *monitor) (bool, string) {
		q := m.cur.metrics["requests_deferred"]
		return q > 0, fmt.Sprintf("%s request(s) queued – all slots busy", fmtVal(q))
	}},
	{id: "ping", sev: sevWarn, hold: &cfg.Alerts.Hold.Ping, check: func(m *monitor) (bool, string) {
		s := m.cur
		return s.health != "down" && s.ping > cfg.Alerts.Ping.Duration, fmt.Sprintf("slow response: ping %d ms", s.ping.Milliseconds())
	}},
	{id: "slow-gen", sev: sevWarn, hold: &cfg.Alerts.Hold.Gen, check: func(m *monitor) (bool, string) {
		ref, ok := m.genRefs[m.genSlots]
		if m.genSlots == 0 || !ok || ref.n < genRefMinN {
			return false, ""
		}
		return m.genPerSlot < cfg.Alerts.GenDrop*ref.avg, fmt.Sprintf("gen speed dropped: %.1f t/s per slot, usually %.1f (%d slot(s) generating)",
			m.genPerSlot, ref.avg, m.genSlots)
	}},
}

func (m *monitor) evalAlerts(now time.Time) {
	if m.alerts == nil {
		m.alerts = map[string]*alert{}
		m.genRefs = map[int]*genRef{}
	}
	if !cfg.Alerts.Enabled {
		return
	}
	for _, r := range alertRules {
		ok, text := r.check(m)
		a := m.alerts[r.id]
		switch {
		case ok:
			if a == nil {
				a = &alert{rule: r, since: now}
				m.alerts[r.id] = a
			}
			a.text = text
			if !a.firing && now.Sub(a.since) >= r.hold.Duration {
				a.firing = true
				m.logAlert(alertEvent{at: now, sev: r.sev, text: text})
			}
		case a != nil:
			if a.firing {
				m.logAlert(alertEvent{at: now, sev: r.sev, text: a.text, resolved: true})
			}
			delete(m.alerts, r.id)
		}
	}
	// Learn the baseline only while no drop is pending, otherwise a lasting
	// drop would become the new normal before the alert fires.
	if m.genSlots > 0 && m.alerts["slow-gen"] == nil {
		ref := m.genRefs[m.genSlots]
		if ref == nil {
			ref = &genRef{avg: m.genPerSlot}
			m.genRefs[m.genSlots] = ref
		}
		ref.avg += genRefAlpha * (m.genPerSlot - ref.avg)
		ref.n++
	}
}

func (m *monitor) logAlert(e alertEvent) {
	m.alertLog = append(m.alertLog, e)
	if len(m.alertLog) > alertLogLen {
		m.alertLog = m.alertLog[len(m.alertLog)-alertLogLen:]
	}
}

// Firing alerts, critical first, then oldest first.
func (m *monitor) activeAlerts() []*alert {
	var out []*alert
	for _, a := range m.alerts {
		if a.firing {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].rule.sev != out[j].rule.sev {
			return out[i].rule.sev > out[j].rule.sev
		}
		return out[i].since.Before(out[j].since)
	})
	return out
}

func sevColorName(sev int) (string, bool) {
	if sev == sevCrit {
		return "CRIT", true
	}
	return "WARN", false
}
