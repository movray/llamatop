// View 6: the models of a llama-server in router mode.

package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func modelStateColor(state string) lipgloss.Color {
	switch state {
	case "loaded":
		return cGreen
	case "loading", "sleeping", "downloading":
		return cYellow
	case "failed":
		return cRed
	}
	return lipgloss.Color("240")
}

func (u *ui) modelTable(w int) []string {
	if u.modelsErr != nil {
		return []string{fg(cRed).Render("/models: " + u.modelsErr.Error())}
	}
	if len(u.models) == 0 {
		return []string{stDim.Render("the router lists no models")}
	}
	u.modelSel = min(max(u.modelSel, 0), len(u.models)-1)
	// Fixed columns: cursor(2) mark(2) state(12) ctx(8) par(4) size(9) source(8), the name gets the rest.
	nw := max(w-4-2-2-12-8-4-9-8-6, 12)
	lines := []string{stDim.Render(fmt.Sprintf("    %-*s %-12s %8s %4s %9s %-8s", nw, "Model", "Status", "Ctx", "Par", "Size", "Source"))}
	for i, r := range u.models {
		cursor, mark := "  ", "  "
		if i == u.modelSel {
			cursor = stSelect.Render("› ")
		}
		if r.is(u.model) {
			mark = fg(cAccent).Render("● ")
		}
		st := r.state()
		switch {
		case st == "failed":
			st = fmt.Sprintf("failed (%d)", r.Status.ExitCode)
		case st == "downloading" && r.downloadPct() >= 0:
			st = fmt.Sprintf("dl %.0f%%", r.downloadPct())
		}
		ctx, par, size := r.arg("--ctx-size", "-c"), r.arg("--parallel", "-np"), "–"
		if ctx == "" && r.Meta != nil && r.Meta.NCtx > 0 {
			ctx = fmt.Sprint(r.Meta.NCtx)
		}
		if r.Meta != nil && r.Meta.Size > 0 {
			size = fmtBig(r.Meta.Size, []string{"B", "KB", "MB", "GB", "TB"}, 1000)
		}
		lines = append(lines, fmt.Sprintf("%s%s%-*s %s %8s %4s %9s %-8s", cursor, mark,
			nw, ansi.Truncate(r.ID, nw, "…"), fg(modelStateColor(r.state())).Render(fmt.Sprintf("%-12s", st)),
			orDash(ctx), orDash(par), size, r.Source))
	}
	return lines
}

func orDash(s string) string {
	if s == "" {
		return "–"
	}
	return s
}

// Launch arguments of the selected model, wrapped at argument boundaries.
func (u *ui) modelArgLines(w int) []string {
	if len(u.models) == 0 {
		return nil
	}
	r := u.models[min(u.modelSel, len(u.models)-1)]
	args := r.Status.Args
	if len(args) == 0 {
		return []string{stDim.Render("no launch arguments reported")}
	}
	// Pair flags with their value; the binary path is left out.
	var items []string
	for i := 1; i < len(args); i++ {
		it := args[i]
		if strings.HasPrefix(it, "-") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			it += " " + args[i+1]
			i++
		}
		items = append(items, it)
	}
	var lines []string
	cur := ""
	for _, it := range items {
		if cur != "" && ansi.StringWidth(cur)+2+ansi.StringWidth(it) > w {
			lines = append(lines, cur)
			cur = ""
		}
		if cur != "" {
			cur += "  "
		}
		cur += it
	}
	return append(lines, cur)
}

func (u *ui) modelsView(w int) string {
	top := u.header(w)
	bottom := u.bottom(w)
	if !u.router {
		hint := "llama-server does not run in router mode – it serves a single model."
		if cfg.Server.Model != "" {
			hint += " -m / server.model is ignored."
		}
		return u.frame(top+"\n"+panel("Models", []string{stDim.Render(hint),
			stDim.Render("Router mode: start llama-server without -m, with --models-dir or --models-preset.")}, w), bottom)
	}
	title := "Models"
	if u.maxInst > 0 {
		title += stDim.Render(fmt.Sprintf(" · at most %d loaded at once", u.maxInst))
	}
	follow := "Monitoring follows the loaded model; Enter pins one."
	if u.pinned {
		follow = "Monitoring is pinned to the model marked ●; f follows the loaded model again."
	}
	table := append(u.modelTable(w), "", stDim.Render(follow),
		stDim.Render("Monitoring never loads a model; l and u load and unload it on the router."))
	body := top + "\n" + panel(title, table, w)
	if len(u.models) > 0 {
		r := u.models[min(u.modelSel, len(u.models)-1)]
		body += "\n" + panel("Launch arguments · "+r.ID, u.modelArgLines(w-4), w)
	}
	return u.frame(body, bottom)
}
