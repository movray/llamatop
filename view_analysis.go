// View 5: alerts and AI analysis.

package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func alertLine(sev int, text string) string {
	name, crit := sevColorName(sev)
	c := cYellow
	if crit {
		c = cRed
	}
	return fg(c).Bold(true).Render("▲ "+name) + " " + text
}

func (u *ui) alertLines() []string {
	m := u.m
	var lines []string
	act := m.activeAlerts()
	if len(act) == 0 {
		lines = append(lines, fg(cGreen).Render("● no active alerts"))
	}
	for _, a := range act {
		lines = append(lines, alertLine(a.rule.sev, a.text)+stDim.Render("  since "+a.since.Format("15:04:05")))
	}
	if n := len(m.alertLog); n > 0 {
		lines = append(lines, "", stDim.Render("Recent events"))
		for i := n - 1; i >= max(n-6, 0); i-- {
			e := m.alertLog[i]
			what := fg(cRed).Render("fired   ")
			if e.resolved {
				what = fg(cGreen).Render("resolved")
			}
			name, _ := sevColorName(e.sev)
			lines = append(lines, fmt.Sprintf("%s %s %s %s", stDim.Render(e.at.Format("15:04:05")), what, stDim.Render(name), e.text))
		}
	}
	return lines
}

func (u *ui) analysisView(w int) string {
	a := &u.analysis
	top := u.header(w) + "\n" + panel("Alerts", u.alertLines(), w)
	bottom := u.bottom(w)

	var head, body []string
	title := "AI analysis"
	switch {
	case a.running():
		head = []string{fg(cYellow).Render(fmt.Sprintf("analysing with %s … %s (x aborts)", u.aiClient().base, fmtDur(time.Since(a.at))))}
	case a.err != nil:
		head = []string{fg(cRed).Render("analysis failed: " + a.err.Error())}
	case a.text == "":
		head = []string{
			stDim.Render("a sends a summary of the monitoring data to " + u.aiClient().base + " and shows its assessment."),
			stDim.Render("The request occupies a slot for a moment and appears in the request log."),
		}
	default:
		head = []string{stDim.Render(fmt.Sprintf("%s · answer took %s · r toggles the data sent",
			a.at.Format("15:04:05"), fmtDur(a.took)))}
	}
	text := a.text
	if a.raw {
		text, title = a.report, "Data sent to the model"
	}
	if text != "" {
		head = append(head, "")
		body = strings.Split(ansi.Wrap(text, w-4, ""), "\n")
	}
	if u.height > 0 {
		page := max(u.height-lipgloss.Height(top)-len(bottom)-3-len(head), 1)
		a.page = page
		a.off = min(max(a.off, 0), max(len(body)-page, 0))
		if len(body) > page {
			title += stDim.Render(fmt.Sprintf(" · lines %d–%d of %d", a.off+1, a.off+page, len(body)))
			body = body[a.off : a.off+page]
		}
	}
	return u.frame(top+"\n"+panel(title, append(head, body...), w), bottom)
}
