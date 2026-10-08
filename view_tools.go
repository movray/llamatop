// View 4: LoRA adapters and benchmark.

package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (u *ui) loraLines() []string {
	m := u.m
	if m.loraErr != nil {
		return []string{fg(cRed).Render(m.loraErr.Error())}
	}
	if len(m.lora) == 0 {
		return []string{stDim.Render("no adapters loaded – start llama-server with --lora <file.gguf>")}
	}
	u.loraSel = min(u.loraSel, len(m.lora)-1)
	lines := []string{stDim.Render(fmt.Sprintf("  %-3s %-40s %s", "ID", "File", "Scale"))}
	for i, a := range m.lora {
		cursor := "  "
		if i == u.loraSel {
			cursor = stSelect.Render("› ")
		}
		scale := fmtVal(a.Scale)
		if v, ok := u.loraEdit[a.ID]; ok && v != a.Scale {
			scale += " → " + fg(cYellow).Render(fmtVal(v))
		}
		lines = append(lines, fmt.Sprintf("%s%-3d %-40s %s", cursor, a.ID, fit(filepath.Base(a.Path), 40), scale))
	}
	if len(u.loraEdit) > 0 {
		lines = append(lines, fg(cYellow).Render("Enter applies the changed scales"))
	}
	lines = append(lines, stDim.Render("Applies globally; requests with their own \"lora\" field override the scale."))
	return lines
}

// Fixed head (description, warning, column header) and the scrollable result rows.
func (u *ui) benchLines() (head, rows []string) {
	b := &u.bench
	head = []string{
		stDim.Render(fmt.Sprintf("Series: short (≈64) and long (≈2048 token) prompt, 1 and %s in parallel, "+
			"%d output tokens each, prompt cache off.", parallelName(b, u), cfg.Bench.OutputTokens)),
		stDim.Render("Values from server timings; averaged per request, \"Total\" = all gen tokens / wall time."),
	}
	busy := 0
	for _, sl := range u.m.cur.slots {
		if sl.IsProcessing {
			busy++
		}
	}
	if busy > 0 && !b.running() {
		head = append(head, fg(cYellow).Render(fmt.Sprintf("Warning: %d slot(s) currently busy – this skews the results.", busy)))
	}
	head = append(head, "", stDim.Render(fmt.Sprintf("%-8s %-6s %4s %7s %9s %10s %8s %10s %7s",
		"Run", "Prompt", "Par", "Tokens", "Prefill", "Prefill/s", "Gen/s", "Total/s", "Time")))
	if len(b.runs) == 0 {
		rows = append(rows, stDim.Render("no runs yet – press b to start"))
	}
	for ri := len(b.runs) - 1; ri >= 0; ri-- {
		run := b.runs[ri]
		label := run.at.Format("15:04:05")
		for i, r := range run.results {
			if i > 0 {
				label = ""
			}
			if r.err != nil {
				rows = append(rows, fmt.Sprintf("%-8s %-6s %4d  ", label, r.name, r.parallel)+fg(cRed).Render(r.err.Error()))
				continue
			}
			rows = append(rows, fmt.Sprintf("%-8s %-6s %4d %7.0f %9s %s %s %s %7s",
				label, r.name, r.parallel, r.promptN, fmtDur(msDur(r.ppMs)),
				fg(cYellow).Render(fmt.Sprintf("%10.1f", r.pp)),
				fg(cGreen).Render(fmt.Sprintf("%8.1f", r.gen)),
				fg(cGreen).Bold(true).Render(fmt.Sprintf("%10.1f", r.total)),
				fmtDur(r.wall)))
		}
		if len(run.results) > 0 {
			label = ""
		}
		var state string
		switch {
		case !run.done:
			v := benchVariants()[len(run.results)]
			state = fg(cYellow).Render(fmt.Sprintf("running: %s ×%s … (x aborts)", v.name, parallelOf(v, b)))
		case run.aborted:
			state = fg(cYellow).Render("aborted")
		}
		if run.busy > 0 {
			state += stDim.Render(fmt.Sprintf("  (%d slot(s) busy at start)", run.busy))
		}
		if run.model != "" {
			state += stDim.Render("  model " + run.model)
		}
		if state != "" {
			rows = append(rows, fmt.Sprintf("%-8s %s", label, state))
		}
		if ri > 0 {
			rows = append(rows, stDim.Render(strings.Repeat("·", 20)))
		}
	}
	return head, rows
}

func parallelOf(v benchVariant, b *benchState) string {
	if v.parallel > 0 {
		return fmt.Sprint(v.parallel)
	}
	return fmt.Sprint(max(b.slots, 1))
}

// Number of parallel requests for the description, even before the first run.
func parallelName(b *benchState, u *ui) string {
	if b.slots > 0 {
		return fmt.Sprint(b.slots)
	}
	if p := u.m.props; p != nil && p.TotalSlots > 0 {
		return fmt.Sprint(p.TotalSlots)
	}
	return "N"
}

func (u *ui) toolsView(w int) string {
	top := u.header(w) + "\n" + panel("LoRA adapters", u.loraLines(), w)
	head, rows := u.benchLines()
	bottom := u.bottom(w)
	title := "Benchmark"
	if u.height > 0 {
		// Panel border (2) and title take 3 rows, the fixed head the rest.
		page := max(u.height-lipgloss.Height(top)-len(bottom)-3-len(head), 1)
		u.benchPage = page
		u.benchOff = min(max(u.benchOff, 0), max(len(rows)-page, 0))
		if len(rows) > page {
			title += stDim.Render(fmt.Sprintf(" · rows %d–%d of %d", u.benchOff+1, u.benchOff+page, len(rows)))
			rows = rows[u.benchOff : u.benchOff+page]
		}
	}
	body := top + "\n" + panel(title, append(head, rows...), w)
	return u.frame(body, bottom)
}
