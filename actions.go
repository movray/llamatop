// Server actions: erase, save and restore slots, set LoRA scales.

package main

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Result of an action, shown as the status line at the bottom.
type actionMsg struct {
	text string
	err  error
	lora bool // LoRA changed, reload the list
}

// Response of POST /slots/{id}?action=…
type slotActionResult struct {
	NErased   int   `json:"n_erased"`
	NSaved    int   `json:"n_saved"`
	NWritten  int64 `json:"n_written"`
	NRestored int   `json:"n_restored"`
	NRead     int64 `json:"n_read"`
	Timings   struct {
		SaveMs    float64 `json:"save_ms"`
		RestoreMs float64 `json:"restore_ms"`
	} `json:"timings"`
}

// The server needs --slot-save-path for slot actions; filename is relative
// to it. Without the flag the server returns an error.
func slotActionCmd(ctx context.Context, c *client, id int, action, filename string) tea.Cmd {
	return func() tea.Msg {
		in := map[string]string{}
		if action != "erase" {
			in["filename"] = filename
		}
		var r slotActionResult
		err := c.post(ctx, fmt.Sprintf("/slots/%d?action=%s", id, action), in, &r)
		if err != nil {
			return actionMsg{err: fmt.Errorf("%s slot %d: %w", actionNames[action], id, err)}
		}
		bytes := []string{"B", "KB", "MB", "GB"}
		switch action {
		case "erase":
			return actionMsg{text: fmt.Sprintf("Slot %d erased, %d tokens discarded", id, r.NErased)}
		case "save":
			return actionMsg{text: fmt.Sprintf("Slot %d saved: %d tokens, %s in %s → %s",
				id, r.NSaved, fmtBig(r.NWritten, bytes, 1024), fmtDur(msDur(r.Timings.SaveMs)), filename)}
		}
		return actionMsg{text: fmt.Sprintf("Slot %d restored: %d tokens, %s in %s ← %s",
			id, r.NRestored, fmtBig(r.NRead, bytes, 1024), fmtDur(msDur(r.Timings.RestoreMs)), filename)}
	}
}

var actionNames = map[string]string{"erase": "Erase", "save": "Save", "restore": "Restore"}

func msDur(ms float64) time.Duration { return time.Duration(ms * float64(time.Millisecond)) }

// Sets the global scales. Requests with their own "lora" field override them.
func setLoraCmd(ctx context.Context, c *client, scales map[int]float64) tea.Cmd {
	return func() tea.Msg {
		type entry struct {
			ID    int     `json:"id"`
			Scale float64 `json:"scale"`
		}
		in := make([]entry, 0, len(scales))
		for id, s := range scales {
			in = append(in, entry{id, s})
		}
		if err := c.post(ctx, "/lora-adapters", in, nil); err != nil {
			return actionMsg{err: fmt.Errorf("set LoRA: %w", err), lora: true}
		}
		return actionMsg{text: fmt.Sprintf("LoRA scales applied to %d adapter(s)", len(in)), lora: true}
	}
}
