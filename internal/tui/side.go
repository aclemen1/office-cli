package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// Side mode keeps a placeholder pane at the TUI's right; enter puts the
// selected dossier's agent in its place, so the TUI stays in view.

type docked struct{ root, id string }

func dossierBin() string {
	exe, err := os.Executable()
	if err != nil {
		return "dossier"
	}
	return exe
}

// toggleSide opens or closes the placeholder pane.
func (m *model) toggleSide() tea.Cmd {
	if m.side {
		m.side = false
		cmd := m.leaveSide()
		m.status, m.statusErr = "side mode off", false
		return cmd
	}
	tui := os.Getenv("HERDR_PANE_ID")
	if tui == "" {
		m.status, m.statusErr = "side mode needs the TUI to run in a herdr pane", true
		return nil
	}
	out, err := exec.Command("herdr", "pane", "split", tui, "--direction", "right", "--ratio", "0.35").Output()
	var r struct {
		Result struct {
			Pane struct {
				PaneID string `json:"pane_id"`
			} `json:"pane"`
		} `json:"result"`
	}
	if err != nil || json.Unmarshal(out, &r) != nil || r.Result.Pane.PaneID == "" {
		m.status, m.statusErr = fmt.Sprintf("side mode: herdr pane split failed: %v", err), true
		return nil
	}
	m.placeholder, m.side = r.Result.Pane.PaneID, true
	_ = exec.Command("herdr", "pane", "run", m.placeholder, fmt.Sprintf("exec %q placeholder %q", dossierBin(), m.root)).Run()
	m.status, m.statusErr = "side mode on: enter shows a dossier's agent at the right; v turns it off", false
	return nil
}

// leaveSide sends the docked agent home and closes the placeholder.
func (m *model) leaveSide() tea.Cmd {
	prev, placeholder := m.docked, m.placeholder
	m.docked, m.placeholder = docked{}, ""
	return func() tea.Msg {
		if prev.id != "" {
			_ = exec.Command(dossierBin(), "undock", prev.id, "--placeholder", placeholder, "--store", prev.root, "--format", "text").Run()
		}
		if placeholder != "" {
			_ = exec.Command("herdr", "pane", "close", placeholder).Run()
		}
		return nil
	}
}

// dock shows the row's agent in place of the placeholder, after sending the
// one docked before back home.
func (m *model) dock(r *row) tea.Cmd {
	prev, next, placeholder := m.docked, docked{r.store.root, r.d.ID}, m.placeholder
	if prev == next {
		return func() tea.Msg {
			out, err := exec.Command(dossierBin(), "dock", next.id, "--placeholder", placeholder, "--store", next.root, "--format", "text").CombinedOutput()
			return doneMsg{id: next.id, verb: "dock", out: strings.TrimSpace(string(out)), err: err}
		}
	}
	m.docked = next
	m.status, m.statusErr = r.d.Label()+": docking its agent…", false
	return func() tea.Msg {
		if prev.id != "" {
			if out, err := exec.Command(dossierBin(), "undock", prev.id, "--placeholder", placeholder, "--store", prev.root, "--format", "text").CombinedOutput(); err != nil {
				return doneMsg{id: prev.id, verb: "undock", out: strings.TrimSpace(string(out)), err: err}
			}
		}
		out, err := exec.Command(dossierBin(), "dock", next.id, "--placeholder", placeholder, "--store", next.root, "--format", "text").CombinedOutput()
		return doneMsg{id: next.id, verb: "dock", out: strings.TrimSpace(string(out)), err: err}
	}
}

// listTop is the screen line of the list's first row: a blank line, the top
// bar, a blank line.
const listTop = 3

// mouse: a click selects a row and a click on the selected row opens it, as
// enter does; the wheel moves over the list, or scrolls the detail panel.
func (m *model) mouse(msg tea.MouseMsg) tea.Cmd {
	inList := m.wide() && msg.X < m.width*52/100 || !m.wide() && !m.detail && !m.legend
	switch msg.Button {
	case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
		step := 1
		if msg.Button == tea.MouseButtonWheelUp {
			step = -1
		}
		if inList {
			m.move(step)
		} else if m.scroll += step; m.scroll < 0 {
			m.scroll = 0
		}
		return nil
	case tea.MouseButtonLeft:
		if msg.Action != tea.MouseActionRelease || !inList {
			return nil
		}
	default:
		return nil
	}
	i := m.offset + msg.Y - listTop
	if msg.Y < listTop || msg.Y-listTop >= m.listHeight() || i >= len(m.rows) || !m.rows[i].selectable() {
		return nil
	}
	if i != m.cursor {
		m.cursor, m.scroll = i, 0
		return nil
	}
	return m.key("enter")
}
