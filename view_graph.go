// View 2: history as a large chart.

package main

import (
	"fmt"
	"strings"
)

func (u *ui) graphView(w int) string {
	si := seriesInfo[u.ser]
	pts := u.m.hist[u.ser]
	span := zooms[u.zoom]
	end := u.now()

	tabs := make([]string, numSeries)
	for i, info := range seriesInfo {
		if i == u.ser {
			tabs[i] = stBadge.Background(info.color).Render(info.name)
		} else {
			tabs[i] = stDim.Render(" " + info.name + " ")
		}
	}

	// Summary over the raw values in the window.
	var n int
	var sum, lo, hi float64
	for _, p := range pts {
		if end.Sub(p.t) > span {
			continue
		}
		if n == 0 || p.v < lo {
			lo = p.v
		}
		hi = max(hi, p.v)
		sum += p.v
		n++
	}
	summary := stDim.Render("no data in this time range")
	switch {
	case u.ser == serQueue && u.m.cur.metricsErr != nil:
		summary = fg(cYellow).Render("Queue: " + u.m.cur.metricsErr.Error())
	case n > 0:
		unit := si.unit
		if unit != "" {
			unit = " " + unit
		}
		summary = fmt.Sprintf("%s %s%s   %s %s   %s %s   %s %s",
			stDim.Render("current"), fmtSI(pts[len(pts)-1].v), unit,
			stDim.Render("avg"), fmtSI(sum/float64(n)),
			stDim.Render("min"), fmtSI(lo),
			stDim.Render("max"), fmtSI(hi))
		summary += stDim.Render(fmt.Sprintf("   · %d samples, mean per column", n))
	}

	bottom := u.bottom(w)
	// Header, panel border (2), title, selector, blank line, axis (2), summary.
	h := 14
	if u.height > 0 {
		h = max(u.height-len(bottom)-9, 3)
	}
	fixed := 0.0
	switch u.ser {
	case serKV:
		fixed = 100
	case serBusy:
		fixed = float64(len(u.m.cur.slots))
	}
	lines := []string{strings.Join(tabs, " "), ""}
	lines = append(lines, chart(pts, end, span, w-4, h, si.color, fixed)...)
	lines = append(lines, summary)
	title := fmt.Sprintf("History · %s", si.name)
	if si.unit != "" {
		title += " (" + si.unit + ")"
	}
	title += fmt.Sprintf(" · last %d min", int(span.Minutes()))
	body := u.header(w) + "\n" + panel(title, lines, w)
	return u.frame(body, bottom)
}
