// Bubble Tea model: UI state and key bindings.

package main

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

var intervals = []time.Duration{
	250 * time.Millisecond, 500 * time.Millisecond, time.Second,
	2 * time.Second, 5 * time.Second, 10 * time.Second,
}

var zooms = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour}

const (
	viewOverview = iota
	viewGraph
	viewReqs
	viewTools
	viewAnalysis
	numViews
)

var viewNames = [numViews]string{"Overview", "History", "Requests", "Tools", "Analysis"}

// Confirmation before an action; with input, also a single-line text field.
type modal struct {
	ask      string
	input    []rune
	hasInput bool
	run      func(arg string) tea.Cmd
}

type sampleMsg sample
type tickMsg struct{}

type ui struct {
	ctx       context.Context
	cancel    context.CancelFunc
	c         *client
	ai        *client // server for the AI analysis, by default c
	m         *monitor
	interval  time.Duration
	paused    bool
	polling   bool
	lastProps time.Time
	width     int
	height    int // 0 = unlimited (-once)
	view      int
	sel       int  // slot selection (overview)
	detail    bool // show slot parameters
	ser       int  // time series (history)
	zoom      int  // Index in zooms
	reqSel    int  // number of the selected request, -1 = always the latest
	reqDetail bool
	modal     *modal
	flash     string // status line after an action
	flashErr  bool
	flashAt   time.Time
	slotFile  map[int]string  // last used file name per slot
	loraSel   int             // selection in the LoRA list
	loraEdit  map[int]float64 // scales not applied yet
	benchOff  int             // first visible benchmark row
	benchPage int             // visible benchmark rows, set while rendering
	bench     benchState
	analysis  analysisState
}

func (u *ui) poll() tea.Cmd {
	// /props is throttled per attempt, not per success: every 2 s until
	// data has arrived, every 10 s after that.
	every := 10 * time.Second
	if u.m.props == nil || u.m.model == nil {
		every = 2 * time.Second
	}
	withProps := time.Since(u.lastProps) >= every
	if withProps {
		u.lastProps = time.Now()
	}
	u.polling = true
	ctx, c := u.ctx, u.c
	return func() tea.Msg { return sampleMsg(c.poll(ctx, withProps)) }
}

func (u *ui) Init() tea.Cmd { return u.poll() }

func (u *ui) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		u.width, u.height = msg.Width, msg.Height
	case tea.KeyMsg:
		if u.modal != nil {
			return u, u.modalKey(msg)
		}
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			u.cancel()
			return u, tea.Quit
		case "p", " ":
			u.paused = !u.paused
			if !u.paused && !u.polling {
				return u, u.poll()
			}
		case "+", "=":
			u.interval = stepInterval(u.interval, 1)
		case "-":
			u.interval = stepInterval(u.interval, -1)
		case "1", "2", "3", "4", "5":
			u.view = int(msg.String()[0] - '1')
		case "tab":
			u.view = (u.view + 1) % numViews
		case "shift+tab":
			u.view = (u.view + numViews - 1) % numViews
		default:
			return u, u.viewKey(msg.String())
		}
	case actionMsg:
		u.flash, u.flashErr, u.flashAt = msg.text, msg.err != nil, time.Now()
		if msg.err != nil {
			u.flash = msg.err.Error()
		}
		if msg.lora {
			u.loraEdit = nil
			u.lastProps = time.Time{} // reload on the next poll
		}
	case benchMsg:
		return u, u.benchDone(msg)
	case analysisMsg:
		u.analysisDone(msg)
	case sampleMsg:
		u.polling = false
		if u.ctx.Err() != nil {
			return u, nil
		}
		u.m.update(sample(msg))
		if !u.paused {
			return u, tea.Tick(u.interval, func(time.Time) tea.Msg { return tickMsg{} })
		}
	case tickMsg:
		if !u.paused && !u.polling {
			return u, u.poll()
		}
	}
	return u, nil
}

func (u *ui) modalKey(msg tea.KeyMsg) tea.Cmd {
	md := u.modal
	k := msg.String()
	switch {
	case k == "ctrl+c":
		u.cancel()
		return tea.Quit
	// For yes/no prompts, No is the default, so Enter cancels.
	case k == "esc" || (!md.hasInput && (k == "n" || k == "enter")):
		u.modal = nil
		return nil
	case (md.hasInput && k == "enter") || (!md.hasInput && k == "y"):
		arg := strings.TrimSpace(string(md.input))
		if md.hasInput && arg == "" {
			return nil
		}
		u.modal = nil
		return md.run(arg)
	}
	if md.hasInput {
		switch msg.Type {
		case tea.KeyBackspace:
			if len(md.input) > 0 {
				md.input = md.input[:len(md.input)-1]
			}
		case tea.KeyCtrlU:
			md.input = nil
		case tea.KeyRunes, tea.KeySpace:
			md.input = append(md.input, msg.Runes...)
		}
	}
	return nil
}

// Asks before a slot action; save/restore also ask for a file name.
func (u *ui) askSlotAction(action string) tea.Cmd {
	if u.sel >= len(u.m.cur.slots) {
		return nil
	}
	sl := u.m.cur.slots[u.sel]
	id := sl.ID
	ask := fmt.Sprintf("%s slot %d?", actionNames[action], id)
	if sl.IsProcessing {
		ask = fmt.Sprintf("Slot %d is busy – %s anyway?", id, strings.ToLower(actionNames[action]))
	}
	md := &modal{ask: ask, run: func(arg string) tea.Cmd {
		if action != "erase" {
			if u.slotFile == nil {
				u.slotFile = map[int]string{}
			}
			u.slotFile[id] = arg
		}
		return slotActionCmd(u.ctx, u.c, id, action, arg)
	}}
	if action != "erase" {
		name := fmt.Sprintf("slot%d.bin", id)
		if f := u.slotFile[id]; f != "" {
			name = f
		}
		md.input, md.hasInput = []rune(name), true
		md.ask = fmt.Sprintf("%s slot %d – file relative to --slot-save-path:", actionNames[action], id)
	}
	u.modal = md
	return nil
}

// Keys whose meaning depends on the view.
func (u *ui) viewKey(k string) tea.Cmd {
	switch u.view {
	case viewOverview:
		switch k {
		case "up", "k":
			u.sel = max(u.sel-1, 0)
		case "down", "j":
			u.sel = min(u.sel+1, max(len(u.m.cur.slots)-1, 0))
		case "enter", "d":
			u.detail = !u.detail
		case "e":
			return u.askSlotAction("erase")
		case "s":
			return u.askSlotAction("save")
		case "r":
			return u.askSlotAction("restore")
		}
	case viewGraph:
		switch k {
		case "left", "h":
			u.ser = (u.ser + numSeries - 1) % numSeries
		case "right", "l":
			u.ser = (u.ser + 1) % numSeries
		case "z":
			u.zoom = min(u.zoom+1, len(zooms)-1)
		case "Z":
			u.zoom = max(u.zoom-1, 0)
		}
	case viewTools:
		return u.toolsKey(k)
	case viewAnalysis:
		a := &u.analysis
		switch k {
		case "a":
			return u.startAnalysis()
		case "x":
			if a.running() {
				a.cancel()
				a.cancel, a.err = nil, fmt.Errorf("aborted")
			}
		case "r":
			a.raw, a.off = !a.raw, 0
		case "up", "k":
			a.off = max(a.off-1, 0)
		case "down", "j":
			a.off++ // the view clamps the offset
		case "pgup":
			a.off = max(a.off-max(a.page, 1), 0)
		case "pgdown":
			a.off += max(a.page, 1)
		}
	case viewReqs:
		n := len(u.m.log)
		if n == 0 {
			return nil
		}
		first, i := u.m.logSeq-n, u.reqIndex()
		switch k {
		case "up", "k": // newer; at the top, follow the latest again
			if i+1 >= n-1 {
				u.reqSel = -1
			} else {
				u.reqSel = first + i + 1
			}
		case "down", "j": // older
			u.reqSel = first + max(i-1, 0)
		case "end", "G":
			u.reqSel = -1
		case "enter", "d":
			u.reqDetail = !u.reqDetail
		}
	}
	return nil
}

func (u *ui) toolsKey(k string) tea.Cmd {
	ls := u.m.lora
	switch k {
	case "b":
		return u.startBench()
	case "x":
		if u.bench.running() {
			u.bench.cancel()
		}
	// Without adapters ↑/↓ scroll the benchmark results; the view clamps the offset.
	case "up", "k":
		if len(ls) == 0 {
			u.benchOff = max(u.benchOff-1, 0)
		} else {
			u.loraSel = max(u.loraSel-1, 0)
		}
	case "down", "j":
		if len(ls) == 0 {
			u.benchOff++
		} else {
			u.loraSel = min(u.loraSel+1, len(ls)-1)
		}
	case "pgup":
		u.benchOff = max(u.benchOff-max(u.benchPage, 1), 0)
	case "pgdown":
		u.benchOff += max(u.benchPage, 1)
	case "left", "h", "right", "l":
		if u.loraSel >= len(ls) {
			return nil
		}
		a := ls[u.loraSel]
		if u.loraEdit == nil {
			u.loraEdit = map[int]float64{}
		}
		v, ok := u.loraEdit[a.ID]
		if !ok {
			v = a.Scale
		}
		d := 0.1
		if k == "left" || k == "h" {
			d = -0.1
		}
		u.loraEdit[a.ID] = math.Round(min(max(v+d, -2), 2)*10) / 10
	case "enter":
		if len(u.loraEdit) == 0 {
			return nil
		}
		scales := map[int]float64{}
		for _, a := range ls {
			scales[a.ID] = a.Scale
		}
		for id, v := range u.loraEdit {
			scales[id] = v
		}
		return setLoraCmd(u.ctx, u.c, scales)
	}
	return nil
}

// Index of the selected request in m.log, -1 if the log is empty.
func (u *ui) reqIndex() int {
	n := len(u.m.log)
	if n == 0 {
		return -1
	}
	if u.reqSel < 0 {
		return n - 1
	}
	return min(max(u.reqSel-(u.m.logSeq-n), 0), n-1)
}

func stepInterval(cur time.Duration, dir int) time.Duration {
	i := sort.Search(len(intervals), func(i int) bool { return intervals[i] >= cur })
	i = min(max(i+dir, 0), len(intervals)-1)
	return intervals[i]
}

// Time shown: the last poll, so pausing freezes the display.
func (u *ui) now() time.Time {
	if t := u.m.cur.at; !t.IsZero() {
		return t
	}
	return time.Now()
}
