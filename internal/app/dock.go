package app

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/aclemen1/office-cli/internal/dossier"
	"github.com/aclemen1/office-cli/internal/spec"
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

// exchange puts each pane in the other's place, in each other's tab; the
// rest of both tabs keeps its geometry. herdr swaps panes only within a tab,
// so a temporary pane holds a's place while a and b cross: a joins b's tab and
// swaps with b, b joins a's tab and swaps with the temporary pane, which
// closes. A pane that changes workspace gets a new id: exchange returns both.
func exchange(a, b string) (aNow, bNow string, err error) {
	wa, err := paneOf(a)
	if err != nil {
		return "", "", err
	}
	wb, err := paneOf(b)
	if err != nil {
		return "", "", err
	}
	a, b = orID(wa.PaneID, a), orID(wb.PaneID, b)
	out, err := herdrCall("pane", "split", a, "--direction", "down")
	if err != nil {
		return "", "", err
	}
	var r struct {
		Result struct {
			Pane struct {
				PaneID string `json:"pane_id"`
			} `json:"pane"`
		} `json:"result"`
	}
	_ = json.Unmarshal(out, &r)
	temp := r.Result.Pane.PaneID
	if aNow, _, err = move(a, "--tab", wb.TabID, "--split", "down", "--target-pane", b, "--no-focus"); err != nil {
		return "", "", err
	}
	if _, err = herdrCall("pane", "swap", "--source-pane", aNow, "--target-pane", b); err != nil {
		return "", "", err
	}
	if bNow, _, err = move(b, "--tab", wa.TabID, "--split", "down", "--target-pane", temp, "--no-focus"); err != nil {
		return "", "", err
	}
	if _, err = herdrCall("pane", "swap", "--source-pane", bNow, "--target-pane", temp); err != nil {
		return "", "", err
	}
	_, _ = herdrCall("pane", "close", temp)
	resize(aNow)
	resize(bNow)
	return aNow, bNow, nil
}

func orID(now, was string) string {
	if now != "" {
		return now
	}
	return was
}

// focusAfter gives the focus to the agent, or back to the pane at the left of
// the one in the placeholder's place, e.g. the TUI: swaps move the focus.
func focusAfter(agent, inSlot string, focus bool) {
	if focus {
		_, _ = herdrCall("agent", "focus", agent)
		return
	}
	_, _ = herdrCall("pane", "focus", "--pane", inSlot, "--direction", "left")
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

// Dock runs the session and exchanges its pane with the placeholder pane,
// e.g. the one the TUI keeps at its right: the agent comes there, and the
// placeholder waits in the agent's own tab, in its place, until Undock.
// The placeholder keeps the id it was given: herdr still resolves it.
func (a *App) Dock(d *dossier.Dossier, placeholder string, focus bool) error {
	if placeholder == "" {
		return spec.UserError("dock needs the placeholder pane the agent takes the place of, e.g. --placeholder w5:p8")
	}
	// Already in this placeholder's place: only the focus may move.
	if d.Run.Home != "" && d.Run.Placeholder == placeholder && paneAlive(d.Run.PaneID) {
		if focus {
			_, _ = herdrCall("agent", "focus", d.Run.PaneID)
		}
		return nil
	}
	// Docked in another placeholder's place, e.g. by another TUI: give that place back first.
	if d.Run.Home != "" && d.Run.Placeholder != placeholder {
		if err := a.Undock(d); err != nil {
			return err
		}
	}
	// Another dossier holds the place, e.g. after keys pressed faster than docks ran: it goes home.
	if other := a.holderOf(placeholder); other != nil && other.ID != d.ID {
		if err := a.Undock(other); err != nil {
			return err
		}
	}
	if err := a.ensureRunning(d, false); err != nil {
		return err
	}
	own, err := paneOf(d.Run.PaneID)
	if err != nil {
		return err
	}
	_, agentNow, err := exchange(placeholder, d.Run.PaneID)
	if err != nil {
		return err
	}
	focusAfter(agentNow, agentNow, focus)
	d.Run.PaneID = agentNow
	_, _ = herdrCall("pane", "rename", agentNow, TabLabel(d))
	d.Run.Home, d.Run.Placeholder, d.Run.TabID = own.WorkspaceID, placeholder, ""
	_ = d.Log("pane docked in place of %s; the placeholder waits in tab %s", placeholder, own.TabID)
	if err := d.Save(); err != nil {
		return err
	}
	a.touchDock()
	return nil
}

// holderOf is the dossier or desk of this office docked in the placeholder's place.
func (a *App) holderOf(placeholder string) *dossier.Dossier {
	all, _ := a.All()
	for _, d := range append(all, a.Desk()) {
		if d.Run.Home != "" && d.Run.Placeholder == placeholder {
			return d
		}
	}
	return nil
}

// Undock exchanges the pane with the placeholder again: the agent goes back
// to its own tab, the placeholder to its place. Without a placeholder left,
// the pane moves to a new tab of its home workspace.
func (a *App) Undock(d *dossier.Dossier) error {
	if d.Run.Home == "" {
		return nil
	}
	home, placeholder := d.Run.Home, d.Run.Placeholder
	// herdr still answers pane get for an id a move replaced.
	if w, err := paneOf(d.Run.PaneID); err == nil {
		d.Run.PaneID = orID(w.PaneID, d.Run.PaneID)
		var paneNow, tab string
		if pw, perr := paneOf(placeholder); placeholder != "" && perr == nil {
			var placeholderNow string
			if placeholderNow, paneNow, err = exchange(placeholder, d.Run.PaneID); err == nil {
				focusAfter(paneNow, placeholderNow, false)
				tab = pw.TabID
			}
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
	if err := d.Save(); err != nil {
		return err
	}
	a.touchDock()
	return nil
}

// resize makes herdr give the pane's terminal the size of its place: after a
// swap, the pane keeps the terminal of the half it passed through. Moving its
// split there and back is enough.
func resize(pane string) {
	for _, dirs := range [][2]string{{"left", "right"}, {"right", "left"}} {
		if _, err := herdrCall("pane", "resize", "--pane", pane, "--direction", dirs[0], "--amount", "0.01"); err == nil {
			_, _ = herdrCall("pane", "resize", "--pane", pane, "--direction", dirs[1], "--amount", "0.01")
			return
		}
	}
}

// DockStamp is touched at each dock and undock: placeholders watch it.
func (a *App) DockStamp() string { return a.S.Meta("run", "dock.stamp") }

func (a *App) touchDock() {
	p := a.DockStamp()
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	_ = os.WriteFile(p, []byte(time.Now().Format(time.RFC3339Nano)+"\n"), 0o644)
}

// focusedIn is the focused pane of the tab the pane sits in.
func focusedIn(pane string) string {
	out, err := herdrCall("pane", "layout", "--pane", pane)
	if err != nil {
		return ""
	}
	var r struct {
		Result struct {
			Layout struct {
				Focused string `json:"focused_pane_id"`
			} `json:"layout"`
		} `json:"result"`
	}
	_ = json.Unmarshal(out, &r)
	return r.Result.Layout.Focused
}

type paneListed struct {
	PaneID  string `json:"pane_id"`
	TabID   string `json:"tab_id"`
	Focused bool   `json:"focused"`
}

func listPanes() []paneListed {
	out, err := herdrCall("pane", "list")
	if err != nil {
		return nil
	}
	var r struct {
		Result struct {
			Panes []paneListed `json:"panes"`
		} `json:"result"`
	}
	_ = json.Unmarshal(out, &r)
	return r.Result.Panes
}

// FocusedPane is the herdr pane that has the focus now.
func FocusedPane() string {
	for _, p := range listPanes() {
		if p.Focused {
			return p.PaneID
		}
	}
	return ""
}

// FocusPane gives the focus to a pane, agent or not. herdr focuses agents by
// id, other panes only from a neighbour: each pane of the tab is tried.
func FocusPane(id string) error {
	if _, err := herdrCall("agent", "focus", id); err == nil {
		return nil
	}
	tab, err := PaneTab(id)
	if err != nil {
		return err
	}
	_, _ = herdrCall("tab", "focus", tab)
	focused := func() bool { return FocusedPane() == id }
	if focused() {
		return nil
	}
	for _, p := range listPanes() {
		if p.TabID != tab || p.PaneID == id {
			continue
		}
		for _, dir := range []string{"left", "right", "up", "down"} {
			_, _ = herdrCall("pane", "focus", "--pane", p.PaneID, "--direction", dir)
			if focused() {
				return nil
			}
		}
	}
	return fmt.Errorf("herdr: could not focus pane %s", id)
}
