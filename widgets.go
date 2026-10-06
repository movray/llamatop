// Styles, drawing blocks (panel, bar, sparkline, chart) and formatting.

package main

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

var (
	cAccent = lipgloss.Color("#7D56F4")
	cGreen  = lipgloss.Color("#04B575")
	cYellow = lipgloss.Color("#E5C07B")
	cRed    = lipgloss.Color("#E06C75")
	cCyan   = lipgloss.Color("#56B6C2")
	cDim    = lipgloss.Color("244")
	cBorder = lipgloss.Color("238")

	stBold, stDim, stLabel, stLabelW, stTitle, stPanel, stBadge, stSelect lipgloss.Style
)

func init() { initStyles() }

// Builds the styles from the current colors; called again after the config is read.
func initStyles() {
	stBold = lipgloss.NewStyle().Bold(true)
	stDim = lipgloss.NewStyle().Foreground(cDim)
	stLabel = lipgloss.NewStyle().Foreground(cDim).Width(10)
	stLabelW = lipgloss.NewStyle().Foreground(cDim).Width(13)
	stTitle = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	stPanel = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cBorder).Padding(0, 1)
	stBadge = lipgloss.NewStyle().Bold(true).Padding(0, 1).Foreground(lipgloss.Color("#FFFFFF"))
	stSelect = lipgloss.NewStyle().Foreground(cAccent).Bold(true)
}

func fg(c lipgloss.Color) lipgloss.Style { return lipgloss.NewStyle().Foreground(c) }

func fit(s string, w int) string {
	if w < 1 {
		return ""
	}
	return ansi.Truncate(s, w, "…")
}

// Bordered box with title; w is the total width including the border.
func panel(title string, lines []string, w int) string {
	inner := w - 4
	out := make([]string, 0, len(lines)+1)
	out = append(out, fit(stTitle.Render(title), inner))
	for _, l := range lines {
		out = append(out, fit(l, inner))
	}
	return stPanel.Width(w - 2).Render(strings.Join(out, "\n"))
}

// Pads two line lists to equal length so panels placed side by side
// have the same height.
func padSame(a, b []string) ([]string, []string) {
	for len(a) < len(b) {
		a = append(a, "")
	}
	for len(b) < len(a) {
		b = append(b, "")
	}
	return a, b
}

func bar(f float64, w int, c lipgloss.Color) string {
	if w < 1 {
		return ""
	}
	if f < 0 {
		f = 0
	}
	if f > 1 {
		f = 1
	}
	p := progress.New(progress.WithSolidFill(string(c)), progress.WithWidth(w), progress.WithoutPercentage())
	p.EmptyColor = "238"
	return p.ViewAs(f)
}

func loadColor(f float64) lipgloss.Color {
	switch {
	case f >= 0.85:
		return cRed
	case f >= 0.6:
		return cYellow
	}
	return cGreen
}

func spark(h []float64, w int, c lipgloss.Color) string {
	if w < 1 {
		return ""
	}
	if len(h) > w {
		h = h[len(h)-w:]
	}
	mx := 0.0
	for _, v := range h {
		mx = max(mx, v)
	}
	ticks := []rune("▁▂▃▄▅▆▇█")
	var b strings.Builder
	for _, v := range h {
		i := 0
		if mx > 0 {
			i = int(v/mx*float64(len(ticks)-1) + 0.5)
		}
		b.WriteRune(ticks[i])
	}
	return fg(c).Render(b.String())
}

// Next "round" value ≥ v for the y axis.
func niceCeil(v float64) float64 {
	if v <= 0 {
		return 1
	}
	e := math.Pow(10, math.Floor(math.Log10(v)))
	for _, f := range []float64{1, 2, 2.5, 5} {
		if f*e >= v {
			return f * e
		}
	}
	return 10 * e
}

// Line chart with axes over the time window [end-span, end], drawn with
// braille characters: each cell holds 2×4 dots. Each dot column shows the
// mean of its samples; gaps between samples are interpolated linearly.
// The area under the line is filled with faint dots.
// fixedMax > 0 fixes the y scale. Returns h+2 lines of width w.
func chart(pts series, end time.Time, span time.Duration, w, h int, c lipgloss.Color, fixedMax float64) []string {
	const yw = 8 // label + axis
	pw := max(w-yw, 10)
	dw, dh := pw*2, h*4 // size in dots
	start := end.Add(-span)
	sum := make([]float64, dw)
	cnt := make([]int, dw)
	for _, p := range pts {
		if p.t.Before(start) || p.t.After(end) {
			continue
		}
		i := int(float64(p.t.Sub(start)) / float64(span) * float64(dw))
		i = min(max(i, 0), dw-1)
		sum[i] += p.v
		cnt[i]++
	}
	vals := make([]float64, dw)
	has := make([]bool, dw)
	mx, prev := 0.0, -1
	for i := range dw {
		if cnt[i] == 0 {
			continue
		}
		vals[i], has[i] = sum[i]/float64(cnt[i]), true
		mx = max(mx, vals[i])
		if prev >= 0 {
			for k := prev + 1; k < i; k++ {
				f := float64(k-prev) / float64(i-prev)
				vals[k], has[k] = vals[prev]+f*(vals[i]-vals[prev]), true
			}
		}
		prev = i
	}
	ymax := fixedMax
	if ymax <= 0 {
		ymax = niceCeil(mx)
	}

	// Dot grid, row 0 at the top; vertical steps are joined so the line stays connected.
	grid := make([][]bool, dh)
	fill := make([][]bool, dh)
	for y := range grid {
		grid[y] = make([]bool, dw)
		fill[y] = make([]bool, dw)
	}
	dotY := func(v float64) int {
		y := int(math.Round(v / ymax * float64(dh-1)))
		return dh - 1 - min(max(y, 0), dh-1)
	}
	lastY := -1
	for x := range dw {
		if !has[x] {
			lastY = -1
			continue
		}
		y := dotY(vals[x])
		for yy := y + 1; yy < dh && cfg.UI.Fill; yy++ {
			fill[yy][x] = true
		}
		lo, hi := y, y
		if lastY >= 0 {
			// Split the step: half in the previous column, half in this one.
			mid := (lastY + y) / 2
			for yy := min(lastY, mid); yy <= max(lastY, mid); yy++ {
				grid[yy][x-1] = true
			}
			lo, hi = min(mid, y), max(mid, y)
		}
		for yy := lo; yy <= hi; yy++ {
			grid[yy][x] = true
		}
		lastY = y
	}

	// Labelled rows at full quarters of the scale.
	marks := map[int]float64{}
	for _, f := range []float64{1, 0.75, 0.5, 0.25} {
		if r := h - int(math.Round(f*float64(h))); r < h && (h >= 8 || f == 1 || f == 0.5) {
			marks[r] = f * ymax
		}
	}
	// Braille bit for dot (column, row) inside a cell.
	bits := [4][2]rune{{0x01, 0x08}, {0x02, 0x10}, {0x04, 0x20}, {0x40, 0x80}}
	stLine, stFill := fg(c), fg(c).Faint(true)
	lines := make([]string, 0, h+2)
	for r := range h {
		// Cells are collected in runs of the same style to keep rendering cheap.
		var b, run strings.Builder
		var runSt *lipgloss.Style
		flush := func() {
			if run.Len() == 0 {
				return
			}
			if runSt == nil {
				b.WriteString(run.String())
			} else {
				b.WriteString(runSt.Render(run.String()))
			}
			run.Reset()
		}
		for cx := range pw {
			var line, area rune
			for dy := range 4 {
				for dx := range 2 {
					switch {
					case grid[r*4+dy][cx*2+dx]:
						line |= bits[dy][dx]
					case fill[r*4+dy][cx*2+dx]:
						area |= bits[dy][dx]
					}
				}
			}
			st := &stFill
			switch {
			case line != 0:
				st = &stLine
			case area == 0:
				st = nil
			}
			if st != runSt {
				flush()
				runSt = st
			}
			if line|area == 0 {
				run.WriteByte(' ')
			} else {
				run.WriteRune(0x2800 + (line | area))
			}
		}
		flush()
		label, axis := "", "│"
		if v, ok := marks[r]; ok {
			label, axis = fmtSI(v), "┤"
		}
		lines = append(lines, stDim.Render(fmt.Sprintf("%*s", yw-1, label)+axis)+b.String())
	}

	// X axis with clock times about every 24 columns.
	k := max(pw/24, 1)
	axis := []rune(strings.Repeat("─", pw))
	labels := []rune(strings.Repeat(" ", pw))
	for i := 0; i <= k; i++ {
		pos := i * (pw - 1) / k
		axis[pos] = '┴'
		txt := []rune(start.Add(span * time.Duration(i) / time.Duration(k)).Format("15:04:05"))
		at := pos - len(txt)/2
		at = min(max(at, 0), pw-len(txt))
		if at >= 0 {
			copy(labels[at:], txt)
		}
	}
	lines = append(lines,
		stDim.Render(fmt.Sprintf("%*s└", yw-1, "0")+string(axis)),
		stDim.Render(strings.Repeat(" ", yw)+string(labels)))
	return lines
}

func frac(a, b int) float64 {
	if b <= 0 {
		return 0
	}
	return float64(a) / float64(b)
}

func fmtVal(v float64) string {
	if v == float64(int64(v)) {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'f', 2, 64)
}

// Compact with SI suffix: 950, 12.3k, 4.5M.
func fmtSI(v float64) string {
	a := math.Abs(v)
	switch {
	case a >= 1e9:
		return fmt.Sprintf("%.1fG", v/1e9)
	case a >= 1e6:
		return fmt.Sprintf("%.1fM", v/1e6)
	case a >= 1e4:
		return fmt.Sprintf("%.1fk", v/1e3)
	case a >= 100 || v == math.Trunc(v):
		return fmt.Sprintf("%.0f", v)
	case a >= 10:
		return fmt.Sprintf("%.1f", v)
	}
	return strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64)
}

func fmtBig(v int64, unit []string, base float64) string {
	f := float64(v)
	i := 0
	for f >= base && i < len(unit)-1 {
		f /= base
		i++
	}
	return fmt.Sprintf("%.1f %s", f, unit[i])
}

func fmtDur(d time.Duration) string {
	switch {
	case d <= 0:
		return "–"
	case d < 10*time.Second:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return d.Round(time.Minute).String()
}

func fmtSec(s float64) string { return fmtDur(time.Duration(s * float64(time.Second))) }

func fmtParam(v any) string {
	switch x := v.(type) {
	case float64:
		return fmtVal(x)
	case string:
		// Line breaks (e.g. generation_prompt) would break the layout.
		if strings.ContainsAny(x, "\n\r\t") {
			return strconv.Quote(x)
		}
		return x
	case bool:
		return strconv.FormatBool(x)
	case nil:
		return "–"
	}
	b, _ := json.Marshal(v)
	return string(b)
}
