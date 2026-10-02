package app

import (
	"encoding/json"
	"fmt"
	"os/exec"

	"github.com/aclemen1/dossier-cli/internal/dossier"
	"github.com/aclemen1/dossier-cli/internal/spec"
)

// herdrCall runs a herdr command that answers JSON. Tests replace it.
var herdrCall = func(args ...string) ([]byte, error) {
	out, err := exec.Command("herdr", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("herdr %v: %w", args, err)
	}
	return out, nil
}

type paneWhere struct {
	PaneID      string `json:"pane_id"`
	TabID       string `json:"tab_id"`
	WorkspaceID string `json:"workspace_id"`
}

func paneOf(id string) (paneWhere, error) {
	out, err := herdrCall("pane", "get", id)
	if err != nil {
		return paneWhere{}, err
	}
	var r struct {
		Result struct {
			Pane paneWhere `json:"pane"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &r); err != nil || r.Result.Pane.TabID == "" {
		return paneWhere{}, fmt.Errorf("herdr pane get %s: no pane in the answer", id)
	}
	return r.Result.Pane, nil
}

// PaneTab is the tab a herdr pane sits in now.
func PaneTab(id string) (string, error) {
	w, err := paneOf(id)
	return w.TabID, err
}

// trade puts pane in place of other, which leaves for a tab of its own in
// workspace: herdr swaps panes only within a tab, so pane first joins
// other's tab under it. The rest of the tab keeps its geometry.
// A pane that changes workspace gets a new id: trade returns both panes' ids
// after the moves, and the tab other ends in.
func trade(pane, other, workspace, label, focus string) (paneNow, otherNow, tab string, err error) {
	where, err := paneOf(other)
	if err != nil {
		return "", "", "", err
	}
	paneNow, _, err = move(pane, "--tab", where.TabID, "--split", "down", "--target-pane", other, focus)
	if err != nil {
		return "", "", "", err
	}
	if _, err := herdrCall("pane", "swap", "--source-pane", paneNow, "--target-pane", other); err != nil {
		return "", "", "", err
	}
	otherNow, tab, err = move(other, "--new-tab", "--workspace", workspace, "--label", label, "--no-focus")
	return paneNow, otherNow, tab, err
}

// move runs herdr pane move and returns the pane's id after it, and the tab
// it created, if any.
func move(pane string, args ...string) (string, string, error) {
	out, err := herdrCall(append([]string{"pane", "move", pane}, args...)...)
	if err != nil {
		return "", "", err
	}
	var r struct {
		Result struct {
			MoveResult struct {
				Pane struct {
					PaneID string `json:"pane_id"`
				} `json:"pane"`
				CreatedTab struct {
					TabID string `json:"tab_id"`
				} `json:"created_tab"`
			} `json:"move_result"`
		} `json:"result"`
	}
	_ = json.Unmarshal(out, &r)
	now := r.Result.MoveResult.Pane.PaneID
	if now == "" {
		now = pane
	}
	return now, r.Result.MoveResult.CreatedTab.TabID, nil
}

// Dock runs the session and puts its pane in place of the placeholder pane,
// e.g. the one the TUI keeps at its right. The placeholder waits in a tab of
// its own until Undock brings it back.
func (a *App) Dock(d *dossier.Dossier, placeholder string) error {
	if placeholder == "" {
		return spec.UserError("dock needs the placeholder pane the agent takes the place of, e.g. --placeholder w5:p8")
	}
	if err := a.ensureRunning(d, false); err != nil {
		return err
	}
	slot, err := paneOf(placeholder)
	if err != nil {
		return err
	}
	own, err := paneOf(d.Run.PaneID)
	if err != nil {
		return err
	}
	if own.TabID != slot.TabID && paneCount(slot.TabID) < 2 {
		return spec.UserError("placeholder %s waits alone in its tab: another agent holds its place. Undock that one first", placeholder)
	}
	if own.TabID != slot.TabID {
		paneNow, placeholderNow, _, err := trade(d.Run.PaneID, placeholder, slot.WorkspaceID, "dock placeholder", "--focus")
		if err != nil {
			return err
		}
		d.Run.PaneID = paneNow
		d.Run.Home, d.Run.Placeholder, d.Run.TabID = own.WorkspaceID, placeholderNow, ""
		_ = d.Log("pane docked in place of %s", placeholder)
	}
	if err := d.Save(); err != nil {
		return err
	}
	return a.MarkSeen(d)
}

// Undock gives the placeholder its place back and moves the pane to a tab of
// its own in its home workspace. Without a placeholder left, the pane just leaves.
func (a *App) Undock(d *dossier.Dossier) error {
	if d.Run.Home == "" {
		return nil
	}
	home, placeholder := d.Run.Home, d.Run.Placeholder
	// herdr still answers pane get for an id a move replaced.
	if w, err := paneOf(d.Run.PaneID); err == nil {
		if w.PaneID != "" {
			d.Run.PaneID = w.PaneID
		}
		var paneNow, tab string
		if _, perr := paneOf(placeholder); placeholder != "" && perr == nil {
			_, paneNow, tab, err = trade(placeholder, d.Run.PaneID, home, TabLabel(d), "--no-focus")
		} else {
			paneNow, tab, err = move(d.Run.PaneID, "--new-tab", "--workspace", home, "--label", TabLabel(d), "--no-focus")
		}
		if err != nil {
			return err
		}
		d.Run.PaneID, d.Run.TabID = paneNow, tab
		_ = d.Log("pane undocked to tab %s", tab)
	}
	d.Run.Home, d.Run.Placeholder = "", ""
	return d.Save()
}

func paneCount(tab string) int {
	out, err := herdrCall("tab", "get", tab)
	if err != nil {
		return 0
	}
	var r struct {
		Result struct {
			Tab struct {
				PaneCount int `json:"pane_count"`
			} `json:"tab"`
		} `json:"result"`
	}
	_ = json.Unmarshal(out, &r)
	return r.Result.Tab.PaneCount
}
