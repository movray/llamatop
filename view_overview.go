// View 1 (overview) and parts shared by all views.

package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func (u *ui) View() string {
	w := u.width
	if w <= 0 {
		w = 100
	}
	w = max(w, 40)
	switch u.view {
	case viewGraph:
		return u.graphView(w)
	case viewReqs:
		return u.reqsView(w)
	case viewTools:
		return u.toolsView(w)
	case viewAnalysis:
		return u.analysisView(w)
	}
	return u.overView(w)
}

// Joins body and footer lines. With a known terminal height the body is
// clipped or padded so the footer always sits on the bottom rows.
func (u *ui) frame(body string, bottom []string) string {
	if u.height <= 0 {
		return body + "\n" + strings.Join(bottom, "\n")
	}
	lines := strings.Split(body, "\n")
	room := max(u.height-len(bottom), 0)
	if len(lines) > room {
		lines = lines[:room]
	}
	for len(lines) < room {
		lines = append(lines, "")
	}
	return strings.Join(append(lines, bottom...), "\n")
}

// Footer lines: hints, confirmation prompt or status message, key help.
func (u *ui) bottom(w int, hints ...string) []string {
	var out []string
	for _, h := range hints {
		out = append(out, stDim.Render(fit(h, w)))
	}
	// Active alerts in every view, at most three lines.
	act := u.m.activeAlerts()
	for i, a := range act {
		if i == 2 && len(act) > 3 {
			out = append(out, fit(fg(cYellow).Render(fmt.Sprintf("▲ +%d more alerts – see 5 Analysis", len(act)-2)), w))
			break
		}
		out = append(out, fit(alertLine(a.rule.sev, a.text)+stDim.Render("  since "+a.since.Format("15:04:05")), w))
	}
	switch {
	case u.modal != nil:
		line := stBadge.Background(cYellow).Foreground(lipgloss.Color("#000000")).Render("?") + " " + u.modal.ask
		if u.modal.hasInput {
			line += " " + stBold.Render(string(u.modal.input)) + stSelect.Render("▏") + stDim.Render("  Enter ok · Esc cancel")
		} else {
			line += stDim.Render("  y/N")
		}
		out = append(out, fit(line, w))
	case u.flash != "" && time.Since(u.flashAt) < 15*time.Second:
		c := cGreen
		if u.flashErr {
			c = cRed
		}
		out = append(out, fit(fg(c).Render("● ")+u.flash, w))
	}
	return append(out, u.help(w))
}

func (u *ui) help(w int) string {
	keys := map[int]string{
		viewOverview: "↑/↓ slot · Enter details · e erase · s save · r restore",
		viewGraph:    "←/→ metric · z/Z time range",
		viewReqs:     "↑/↓ select · Enter details · End latest",
		viewTools:    "b benchmark · x abort · ↑/↓ adapter · ←/→ scale · Enter apply · PgUp/PgDn scroll",
		viewAnalysis: "a analyse · x abort · r data sent · ↑/↓ PgUp/PgDn scroll",
	}[u.view]
	// Arrow keys only steer the LoRA list; without adapters they do nothing.
	if u.view == viewTools && len(u.m.lora) == 0 {
		keys = "b benchmark · x abort · ↑/↓ PgUp/PgDn scroll"
	}
	tabs := make([]string, numViews)
	for i, n := range viewNames {
		t := fmt.Sprintf("%d %s", i+1, n)
		if i == u.view {
			tabs[i] = stSelect.Render(t)
		} else {
			tabs[i] = stDim.Render(t)
		}
	}
	return fit(strings.Join(tabs, "  ")+stDim.Render("  │  "+keys+" · p pause · +/- interval · q quit"), w)
}

// Rows for the log area given the height of the rest (border, title, header).
func (u *ui) rowsLeft(used int) int {
	if u.height <= 0 {
		return 10
	}
	return u.height - used - 4
}

func (u *ui) overView(w int) string {
	s := u.m.cur
	top := []string{u.header(w)}
	if w >= 100 {
		lw := w * 45 / 100
		ml, rl := padSame(u.modelLines(), u.rateLines(w-lw))
		top = append(top, lipgloss.JoinHorizontal(lipgloss.Top, panel("Model", ml, lw), panel("Throughput", rl, w-lw)))
	} else {
		top = append(top, panel("Model", u.modelLines(), w), panel("Throughput", u.rateLines(w), w))
	}
	top = append(top, u.slotPanel(w))

	var hints []string
	if s.metricsErr != nil {
		hints = append(hints, "Server metrics: "+s.metricsErr.Error())
	}
	bottom := u.bottom(w, hints...)

	body := strings.Join(top, "\n")
	logRows := u.rowsLeft(lipgloss.Height(body) + len(bottom))
	var lower string
	switch {
	case logRows < 1:
	case w >= 130:
		sw := 48
		st := u.statsLines(true)
		// Match the statistics panel to the log height (header + rows).
		for len(st) < logRows+1 {
			st = append(st, "")
		}
		lower = lipgloss.JoinHorizontal(lipgloss.Top, u.logPanel(w-sw, logRows, false), panel("Statistics", st[:logRows+1], sw))
	default:
		lower = u.logPanel(w, logRows, false)
		if u.height == 0 {
			lower += "\n" + panel("Statistics", u.statsLines(true), w)
		}
	}
	if lower != "" {
		body += "\n" + lower
	}
	return u.frame(body, bottom)
}

func (u *ui) header(w int) string {
	s := u.m.cur
	hc := cRed
	switch s.health {
	case "ok":
		hc = cGreen
	case "loading":
		hc = cYellow
	}
	parts := []string{
		stBadge.Background(cAccent).Render("llamatop"),
		stBadge.Background(hc).Render(s.health),
	}
	if p := u.m.props; p != nil && p.IsSleeping {
		parts = append(parts, stBadge.Background(cYellow).Render("sleeping"))
	}
	if q := s.metrics["requests_deferred"]; q > 0 {
		parts = append(parts, stBadge.Background(cRed).Render(fmt.Sprintf("%s queued", fmtVal(q))))
	}
	if act := u.m.activeAlerts(); len(act) > 0 {
		c := cYellow
		if act[0].rule.sev == sevCrit {
			c = cRed
		}
		parts = append(parts, stBadge.Background(c).Render(fmt.Sprintf("▲ %d alert(s)", len(act))))
	}
	if u.paused {
		parts = append(parts, stBadge.Background(lipgloss.Color("240")).Render("⏸ paused"))
	}
	info := fmt.Sprintf(" %s  %s  every %s", u.c.base, s.at.Format("15:04:05"), u.interval)
	if s.health != "down" && s.ping > 0 {
		info += fmt.Sprintf("  ping %d ms", s.ping.Milliseconds())
	}
	parts = append(parts, stDim.Render(info))
	return fit(strings.Join(parts, " "), w)
}

func (u *ui) modelLines() []string {
	p, mm := u.m.props, u.m.model
	row := func(k, v string) string { return stLabel.Render(k) + v }
	if p == nil {
		return []string{stDim.Render("no data from /props yet")}
	}
	var lines []string
	lines = append(lines, row("File", stBold.Render(filepath.Base(p.ModelPath))))
	name := p.ModelAlias
	if p.ModelFtype != "" {
		name += stDim.Render(" · ") + p.ModelFtype
	}
	lines = append(lines, row("Alias", name))
	if mm != nil {
		lines = append(lines, row("Size", fmt.Sprintf("%s params · %s",
			fmtBig(mm.NParams, []string{"", "K", "M", "B", "T"}, 1000),
			fmtBig(mm.Size, []string{"B", "KB", "MB", "GB", "TB"}, 1000))))
	}
	ctx := fmt.Sprintf("%d / slot", p.DefaultGen.NCtx)
	if mm != nil && mm.NCtxTrain > 0 {
		ctx += stDim.Render(fmt.Sprintf(" · trained %d", mm.NCtxTrain))
	}
	lines = append(lines, row("Context", ctx))
	var mods []string
	for _, k := range []string{"vision", "video", "audio"} {
		if p.Modalities[k] {
			mods = append(mods, k)
		}
	}
	slots := fmt.Sprintf("%d", p.TotalSlots)
	if len(mods) > 0 {
		slots += stDim.Render(" · ") + strings.Join(mods, ", ")
	}
	lines = append(lines, row("Slots", slots))
	if len(u.m.lora) > 0 {
		var ls []string
		for _, a := range u.m.lora {
			ls = append(ls, fmt.Sprintf("%s %s", strings.TrimSuffix(filepath.Base(a.Path), ".gguf"), stDim.Render(fmtVal(a.Scale))))
		}
		lines = append(lines, row("LoRA", strings.Join(ls, ", ")))
	}
	lines = append(lines, row("Build", stDim.Render(p.BuildInfo)))
	return lines
}

func (u *ui) rateLines(w int) []string {
	m, s := u.m, u.m.cur
	sw := max(w-4-9-14, 1)
	row := func(k, v, sp string) string { return stLabel.Render(k) + v + "  " + sp }
	nslots := len(s.slots)
	busy := 0
	var used, total int
	for _, sl := range s.slots {
		used += sl.used()
		total += sl.NCtx
		if sl.IsProcessing {
			busy++
		}
	}
	f := frac(used, total)
	lines := []string{
		row("Gen", fg(cGreen).Render(fmt.Sprintf("%8.1f t/s", m.genRate)), spark(m.hist[serGen].last(sw), sw, cGreen)),
		row("Prefill", fg(cYellow).Render(fmt.Sprintf("%8.1f t/s", m.ppRate)), spark(m.hist[serPP].last(sw), sw, cYellow)),
		row("Active", fmt.Sprintf("%6s slots", fmt.Sprintf("%d/%d", busy, nslots)), spark(m.hist[serBusy].last(sw), sw, cCyan)),
	}
	if q, ok := s.metrics["requests_deferred"]; ok {
		c := cDim
		if q > 0 {
			c = cRed
		}
		lines = append(lines, row("Queued", fg(c).Render(fmt.Sprintf("%8s req", fmtVal(q))), spark(m.hist[serQueue].last(sw), sw, cRed)))
	}
	lines = append(lines, "",
		stLabel.Render("KV")+bar(f, max(w-4-9-26, 6), loadColor(f))+fmt.Sprintf(" %5.1f%%  %d/%d", f*100, used, total))
	if s.slotsErr != nil {
		c := cRed
		if errors.Is(s.slotsErr, errSlotsOff) {
			c = cYellow
		}
		lines = append(lines, fg(c).Render("Slots: "+s.slotsErr.Error()))
	}
	return lines
}

func (u *ui) slotPanel(w int) string {
	s := u.m.cur
	inner := w - 4
	if len(s.slots) == 0 {
		msg := "no slot data"
		if s.slotsErr != nil {
			msg = s.slotsErr.Error()
		}
		return panel("Slots", []string{stDim.Render(msg)}, w)
	}
	u.sel = min(u.sel, len(s.slots)-1)
	// Fixed columns: cursor(2) #id(4) state(9) … percent(7) tokens(15) task(11) rate(12) cache(10)
	bw := min(max(inner-78, 6), 50)
	var lines []string
	for i, sl := range s.slots {
		state, col := "idle", cDim
		switch {
		case sl.IsProcessing && sl.decoded() == 0:
			state, col = "prefill", cYellow
		case sl.IsProcessing:
			state, col = "gen", cGreen
		case sl.NPromptTokens > 0:
			state, col = "warm", cCyan
		}
		cursor := "  "
		if i == u.sel {
			cursor = stSelect.Render("› ")
		}
		f := frac(sl.used(), sl.NCtx)
		task := "-"
		if sl.IDTask != nil {
			task = strconv.Itoa(*sl.IDTask)
		}
		rate := ""
		if r, ok := u.m.slotGen[sl.ID]; ok {
			rate = fg(cGreen).Render(fmt.Sprintf("%7.1f t/s", r))
		} else if r, ok := u.m.slotPP[sl.ID]; ok {
			rate = fg(cYellow).Render(fmt.Sprintf("%7.1f pp/s", r))
		}
		cache := ""
		if sl.IsProcessing && sl.promptLen() > 0 {
			cache = stDim.Render(fmt.Sprintf("  cache %3.0f%%", frac(sl.NPromptTokensCache, sl.promptLen())*100))
		}
		lines = append(lines, fmt.Sprintf("%s%s %s %s %5.1f%%  %6d/%-7d task %-6s %-11s%s",
			cursor, stBold.Render(fmt.Sprintf("#%-2d", sl.ID)),
			fg(col).Render(fmt.Sprintf("%-7s", state)),
			bar(f, bw, loadColor(f)), f*100, sl.used(), sl.NCtx, task, rate, cache))

		if sl.IsProcessing {
			pl := sl.promptLen()
			pw := max(min(bw, 30), 6)
			done := min(sl.NPromptTokensCache+sl.NPromptTokensProcessed, pl)
			pre := fmt.Sprintf("prefill %s %d/%d", bar(frac(done, pl), pw, cYellow), done, pl)
			gen := fmt.Sprintf("gen %d", sl.decoded())
			if np := sl.nPredict(); np > 0 {
				gen = fmt.Sprintf("gen %s %d/%d", bar(frac(sl.decoded(), np), pw, cGreen), sl.decoded(), np)
			} else {
				gen += stDim.Render(" (no limit)")
			}
			lines = append(lines, "              "+pre+"   "+gen)
		}
		if u.detail && i == u.sel {
			const pad = "              "
			if len(sl.Params) == 0 {
				lines = append(lines, pad+stDim.Render("no parameters (slot not used yet)"))
			} else {
				lines = append(lines, paramLines(sl.Params, inner-len(pad), pad)...)
			}
		}
	}
	return panel("Slots", lines, w)
}

// Sampler parameters, compactly wrapped.
func paramLines(params map[string]any, w int, pad string) []string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var lines []string
	var cur strings.Builder
	n := 0
	for _, k := range keys {
		v := fmtParam(params[k])
		iw := ansi.StringWidth(k) + 1 + ansi.StringWidth(v)
		if n > 0 && n+2+iw > w {
			lines = append(lines, pad+cur.String())
			cur.Reset()
			n = 0
		}
		if n > 0 {
			cur.WriteString("  ")
			n += 2
		}
		cur.WriteString(stDim.Render(k+"=") + v)
		n += iw
	}
	if n > 0 {
		lines = append(lines, pad+cur.String())
	}
	return lines
}

// Request log, newest on top. With pick the selection is marked and the
// list is scrolled so it stays visible.
func (u *ui) logPanel(w, rows int, pick bool) string {
	log := u.m.log
	cur := ""
	if pick {
		cur = "  "
	}
	hdr := stDim.Render(cur + fmt.Sprintf("%-8s %4s %7s %7s %6s %6s %6s %7s %9s",
		"End", "Slot", "Task", "Prompt", "Cache", "Gen", "TTFT", "Time", "t/s"))
	lines := []string{hdr}
	if len(log) == 0 {
		lines = append(lines, stDim.Render("no completed requests yet"))
	}
	selIdx, off := -1, 0
	if pick && len(log) > 0 {
		selIdx = u.reqIndex()
		if pos := len(log) - 1 - selIdx; pos >= rows {
			off = pos - rows + 1
		}
	}
	for k := off; k < len(log) && k-off < rows; k++ {
		i := len(log) - 1 - k
		r := log[i]
		gen, rate, ttft, dur := "?", "–", fmtDur(r.ttft), fmtDur(r.dur)
		if r.genKnown() {
			gen = strconv.Itoa(r.gen)
		}
		if r.partial {
			ttft, dur = "?", "?"
		}
		if r.rate > 0 {
			rate = fmt.Sprintf("%.1f", r.rate)
		}
		if r.ttft < 0 {
			ttft = "<" + fmtDur(u.interval)
		}
		prefix := ""
		if pick {
			prefix = "  "
			if i == selIdx {
				prefix = stSelect.Render("› ")
			}
		}
		lines = append(lines, prefix+fmt.Sprintf("%-8s %4d %7d %7d %6d %6s %6s %7s %s",
			r.end.Format("15:04:05"), r.slot, r.task, r.prompt, r.cache, gen,
			ttft, dur, fg(cGreen).Render(fmt.Sprintf("%9s", rate))))
	}
	title := fmt.Sprintf("Requests (%d)", u.m.tot.reqs)
	if pick && u.reqSel >= 0 {
		title += stDim.Render(" · selection pinned, End = latest")
	}
	return panel(title, lines, w)
}
