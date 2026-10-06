// View 3: statistics, server metrics and request details.

package main

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (u *ui) statsLines(compact bool) []string {
	st := u.m.stats(u.now())
	t := u.m.tot
	if st.n == 0 {
		return []string{stDim.Render("no completed requests yet")}
	}
	row := func(k, v string) string { return stLabel.Render(k) + v }
	if !compact {
		row = func(k, v string) string { return stLabelW.Render(k) + v }
	}
	perMin := stDim.Render("rate pending")
	if st.window > 0 {
		perMin = fmt.Sprintf("%.1f/min", st.perMin)
	}
	cache := fmt.Sprintf("%.0f %% of prompt tokens", frac(t.cache, t.prompt)*100)
	if compact {
		return []string{
			row("Requests", fmt.Sprintf("%d · %s", t.reqs, perMin)),
			row("TTFT", fmt.Sprintf("avg %s · p95 %s", fmtSec(st.ttft.avg), fmtSec(st.ttft.p95))),
			row("Duration", fmt.Sprintf("avg %s · p95 %s", fmtSec(st.dur.avg), fmtSec(st.dur.p95))),
			row("Gen", fmt.Sprintf("avg %.1f · p50 %.1f t/s", st.rate.avg, st.rate.p50)),
			row("Tokens", fmt.Sprintf("%s Prompt · %s Gen", fmtSI(float64(t.prompt)), fmtSI(float64(t.gen)))),
			row("Cache", cache),
		}
	}

	lines := []string{
		row("Since", fmt.Sprintf("%s (%s) · %d requests · %s prompt · %s gen",
			u.m.since.Format("15:04:05"), fmtDur(u.now().Sub(u.m.since)), t.reqs,
			fmtSI(float64(t.prompt)), fmtSI(float64(t.gen)))),
	}
	if st.window > 0 {
		lines = append(lines, row(fmt.Sprintf("Last %dm", int(math.Ceil(st.window.Minutes()))),
			fmt.Sprintf("%.1f req/min · %s prompt/min · %s gen/min",
				st.perMin, fmtSI(st.promptMin), fmtSI(st.genMin))))
	}
	lines = append(lines, row("Cache", cache), "",
		stDim.Render(fmt.Sprintf("%-11s %5s %8s %8s %8s %8s", "", "n", "avg", "p50", "p95", "max")))
	tab := func(name string, d dist, f func(float64) string) string {
		if d.n == 0 {
			return stLabelW.Render(name) + stDim.Render(fmt.Sprintf(" %5d %8s", 0, "–"))
		}
		return stLabelW.Render(name) + fmt.Sprintf(" %5d %8s %8s %8s %8s", d.n, f(d.avg), f(d.p50), f(d.p95), f(d.max))
	}
	rate := func(v float64) string { return fmt.Sprintf("%.1f", v) }
	lines = append(lines,
		tab("TTFT", st.ttft, fmtSec),
		tab("Duration", st.dur, fmtSec),
		tab("Gen t/s", st.rate, rate),
		tab("Prompt tok", st.prompt, fmtSI),
		tab("Gen tok", st.gen, fmtSI),
		stDim.Render(fmt.Sprintf("Based on last %d requests · times accurate to ±%s", st.n, u.interval)))
	return lines
}

// Processed /metrics values (counters since server start).
var knownMetrics = map[string]bool{
	"requests_processing": true, "requests_deferred": true,
	"prompt_tokens_seconds": true, "predicted_tokens_seconds": true,
	"prompt_tokens_total": true, "prompt_seconds_total": true,
	"tokens_predicted_total": true, "tokens_predicted_seconds_total": true,
	"n_decode_total": true, "n_busy_slots_per_decode": true, "n_tokens_max": true,
	"prompt_tokens_cached_total": true, "spec_decode_num_drafts_total": true,
	"spec_decode_num_draft_tokens_total": true, "spec_decode_num_accepted_tokens_total": true,
}

func (u *ui) serverLines() []string {
	s := u.m.cur
	if s.metricsErr != nil {
		c := cRed
		if errors.Is(s.metricsErr, errMetricsOff) {
			c = cYellow
		}
		return []string{fg(c).Render(s.metricsErr.Error())}
	}
	ms := s.metrics
	if len(ms) == 0 {
		return []string{stDim.Render("no data from /metrics yet")}
	}
	row := func(k, v string) string { return stLabelW.Render(k) + v }
	var lines []string
	if p, ok := ms["requests_processing"]; ok {
		q := ms["requests_deferred"]
		qs := stDim.Render(fmtVal(q) + " queued")
		if q > 0 {
			qs = fg(cRed).Bold(true).Render(fmtVal(q) + " queued")
		}
		lines = append(lines, row("Requests", fmtVal(p)+" active · "+qs))
	}
	// The server resets prompt_tokens_seconds and predicted_tokens_seconds on
	// every /metrics request – so compute the averages from the totals.
	if t := ms["prompt_seconds_total"]; t > 0 {
		lines = append(lines, row("Avg prefill", fg(cYellow).Render(fmt.Sprintf("%.1f t/s", ms["prompt_tokens_total"]/t))))
	}
	if t := ms["tokens_predicted_seconds_total"]; t > 0 {
		lines = append(lines, row("Avg gen", fg(cGreen).Render(fmt.Sprintf("%.1f t/s", ms["tokens_predicted_total"]/t))+
			stDim.Render(" per request")))
	}
	if v, ok := ms["prompt_tokens_total"]; ok {
		lines = append(lines, row("Prompt", fmt.Sprintf("%s tokens · %s", fmtSI(v), fmtSec(ms["prompt_seconds_total"]))))
	}
	if v, ok := ms["tokens_predicted_total"]; ok {
		lines = append(lines, row("Generated", fmt.Sprintf("%s tokens · %s", fmtSI(v), fmtSec(ms["tokens_predicted_seconds_total"]))))
	}
	if v, ok := ms["prompt_tokens_cached_total"]; ok {
		lines = append(lines, row("Cache", fmt.Sprintf("%s prompt tokens from cache", fmtSI(v))))
	}
	if d := ms["spec_decode_num_drafts_total"]; d > 0 {
		dt, acc := ms["spec_decode_num_draft_tokens_total"], ms["spec_decode_num_accepted_tokens_total"]
		lines = append(lines, row("Speculative", fmt.Sprintf("%s drafts · %s/%s tokens accepted (%.0f %%)",
			fmtSI(d), fmtSI(acc), fmtSI(dt), acc/max(dt, 1)*100)))
	}
	if v, ok := ms["n_decode_total"]; ok {
		d := fmtSI(v) + " calls"
		if b, ok := ms["n_busy_slots_per_decode"]; ok {
			d += fmt.Sprintf(" · avg %.2f slots/decode", b)
		}
		lines = append(lines, row("Decodes", d))
	}
	if v, ok := ms["n_tokens_max"]; ok {
		lines = append(lines, row("Max tokens", fmtSI(v)))
	}
	var rest []string
	for k := range ms {
		if !knownMetrics[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range rest {
		lines = append(lines, stDim.Render(k+" ")+fmtVal(ms[k]))
	}
	return lines
}

func (u *ui) reqDetailPanel(r reqRecord, w, maxLines int) string {
	row := func(k, v string) string { return stLabelW.Render(k) + v }
	start := "?"
	if r.dur > 0 {
		start = r.end.Add(-r.dur).Format("15:04:05")
	}
	prompt := fmt.Sprintf("%d tokens", r.prompt)
	if r.prompt > 0 {
		prompt += stDim.Render(fmt.Sprintf(" · %d from cache (%.0f %%) · %d new",
			r.cache, frac(r.cache, r.prompt)*100, max(r.prompt-r.cache, 0)))
	}
	gen := stDim.Render("unknown (finished between two polls)")
	if r.genKnown() {
		gen = strconv.Itoa(r.gen)
		if r.nPredict > 0 {
			gen += stDim.Render(fmt.Sprintf(" / limit %d", r.nPredict))
			if r.gen >= r.nPredict {
				gen += fg(cYellow).Render("  limit reached")
			}
		}
	}
	ttft := fmtDur(r.ttft)
	if r.ttft < 0 {
		ttft = "<" + fmtDur(u.interval)
	}
	dur := fmtDur(r.dur)
	if r.partial {
		ttft, dur = "?", "?"
	}
	if r.dur > 0 && r.ttft > 0 {
		dur += stDim.Render(" · of which gen " + fmtDur(r.dur-r.ttft))
	}
	rate := "–"
	if r.rate > 0 {
		rate = fg(cGreen).Render(fmt.Sprintf("%.1f t/s", r.rate))
	}
	pp := "–"
	if r.ttft > 0 && r.prompt > r.cache {
		pp = fg(cYellow).Render(fmt.Sprintf("≈ %.0f t/s", float64(r.prompt-r.cache)/r.ttft.Seconds())) +
			stDim.Render(" (new tokens / TTFT)")
	}
	lines := []string{
		row("Slot", strconv.Itoa(r.slot)),
		row("Time", start+" → "+r.end.Format("15:04:05")),
		row("Prompt", prompt),
		row("Generated", gen),
		row("TTFT", ttft),
		row("Duration", dur),
		row("Gen rate", rate),
		row("Prefill", pp),
	}
	if r.partial {
		lines = append(lines, fg(cYellow).Render("Already running when llamatop started: start, TTFT and duration unknown."))
	}
	lines = append(lines,
		"",
		stTitle.Render("Sampler parameters"))
	if len(r.params) == 0 {
		lines = append(lines, stDim.Render("none"))
	} else {
		lines = append(lines, paramLines(r.params, w-4, "")...)
	}
	if maxLines > 0 && len(lines) > maxLines {
		lines = append(lines[:maxLines-1], stDim.Render("…"))
	}
	return panel(fmt.Sprintf("Request · Task %d", r.task), lines, w)
}

func (u *ui) reqsView(w int) string {
	top := []string{u.header(w)}
	sl, srv := u.statsLines(false), u.serverLines()
	if w >= 110 {
		lw := w / 2
		sl, srv = padSame(sl, srv)
		top = append(top, lipgloss.JoinHorizontal(lipgloss.Top, panel("Statistics", sl, lw), panel("Server metrics", srv, w-lw)))
	} else {
		top = append(top, panel("Statistics", sl, w), panel("Server metrics", srv, w))
	}
	bottom := u.bottom(w)
	body := strings.Join(top, "\n")
	rows := u.rowsLeft(lipgloss.Height(body) + len(bottom))
	if rows < 1 {
		return u.frame(body, bottom)
	}

	var lower string
	i := u.reqIndex()
	switch {
	case !u.reqDetail || i < 0:
		lower = u.logPanel(w, rows, true)
	case w >= 120:
		dw := max(w*2/5, 50)
		lower = lipgloss.JoinHorizontal(lipgloss.Top,
			u.logPanel(w-dw, rows, true), u.reqDetailPanel(u.m.log[i], dw, rows+1))
	default:
		lower = u.reqDetailPanel(u.m.log[i], w, rows+1)
	}
	return u.frame(body+"\n"+lower, bottom)
}
