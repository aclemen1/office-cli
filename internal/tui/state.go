package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/aclemen1/office-cli/internal/app"
	"github.com/aclemen1/office-cli/internal/spec"
)

// The TUI's state lives in the user's config directory, one entry per root,
// so that a new start finds the same views, selection and docked agent.

type savedDock struct {
	Root string `json:"root,omitempty"`
	ID   string `json:"id,omitempty"`
}

type savedState struct {
	All             bool      `json:"all,omitempty"`
	Todo            bool      `json:"todo,omitempty"`
	ByPerson        bool      `json:"by_person,omitempty"`
	ByPriority      bool      `json:"by_priority"`
	NoDetail        bool      `json:"no_detail,omitempty"`
	Layout          string    `json:"layout,omitempty"`
	Filter          string    `json:"filter,omitempty"`
	Cursor          string    `json:"cursor,omitempty"`
	Side            bool      `json:"side"`
	Docked          savedDock `json:"docked,omitempty"`
	LastDocked      savedDock `json:"last_docked,omitempty"`
	SideRatio       float64   `json:"side_ratio,omitempty"` // the TUI's share of the width in side mode
	Pane            string    `json:"pane,omitempty"`       // the herdr pane of the running TUI, for office tui-key
	AgentsView      bool      `json:"agents_view,omitempty"`
	StarredView     bool      `json:"starred_view,omitempty"`
	LiveOldestFirst bool      `json:"live_oldest_first,omitempty"`
}

func statePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "office", "tui.json")
}

func readStates() map[string]savedState {
	all := map[string]savedState{}
	if b, err := os.ReadFile(statePath()); err == nil {
		_ = json.Unmarshal(b, &all)
	}
	return all
}

// loadState is the saved state for root; a first start is by priority, in side mode.
func loadState(root string) savedState {
	if s, ok := readStates()[root]; ok {
		return s
	}
	return defaultState
}

func (m *model) state() savedState {
	s := savedState{All: m.all, Todo: m.todo, ByPerson: m.byPerson, ByPriority: m.byPriority, NoDetail: m.noDetail, Layout: m.layout,
		Filter: m.filter, Side: m.side, Docked: savedDock{m.docked.root, m.docked.id}, LastDocked: savedDock{m.lastDocked.root, m.lastDocked.id}}
	s.SideRatio = m.sideRatio
	if !m.quitting {
		s.Pane = os.Getenv("HERDR_PANE_ID")
	}
	s.AgentsView = m.agentsView
	s.StarredView = m.starredView
	s.LiveOldestFirst = m.liveOldestFirst
	if m.cursor >= 0 && m.cursor < len(m.rows) {
		s.Cursor = m.rows[m.cursor].key()
	}
	return s
}

// save writes the state when it changed.
func (m *model) save() {
	if m.side {
		if r := measureRatio(os.Getenv("HERDR_PANE_ID")); r > 0 {
			m.sideRatio = r
		}
	}
	s := m.state()
	if s == m.saved {
		return
	}
	all := readStates()
	all[m.root] = s
	b, _ := json.MarshalIndent(all, "", "  ")
	p := statePath()
	if os.MkdirAll(filepath.Dir(p), 0o755) != nil {
		return
	}
	if os.WriteFile(p+".tmp", b, 0o644) == nil && os.Rename(p+".tmp", p) == nil {
		m.saved = s
	}
}

// restore applies a saved state; the command docks the agent docked before.
func (m *model) restore(s savedState, side bool) tea.Cmd {
	m.all, m.todo, m.byPerson, m.byPriority, m.noDetail, m.layout, m.filter = s.All, s.Todo, s.ByPerson, s.ByPriority, s.NoDetail, s.Layout, s.Filter
	m.lastDocked = docked{s.LastDocked.Root, s.LastDocked.ID}
	m.sideRatio = s.SideRatio
	m.agentsView = s.AgentsView
	m.starredView = s.StarredView
	m.liveOldestFirst = s.LiveOldestFirst
	m.reload()
	for i, r := range m.rows {
		if s.Cursor != "" && r.selectable() && r.key() == s.Cursor {
			m.cursor = i
		}
	}
	if !side || !s.Side {
		return nil
	}
	m.toggleSide()
	if !m.side || s.Docked.ID == "" {
		return nil
	}
	if i := m.rowOf(docked{s.Docked.Root, s.Docked.ID}); i >= 0 {
		return m.dock(&m.rows[i], false)
	}
	return nil
}

var defaultState = savedState{ByPriority: true, Side: true}

// measureRatio reads the share of the TUI in the split it makes with the pane
// at its right: the split that starts where the TUI starts, as tall as it.
func measureRatio(tui string) float64 {
	out, err := exec.Command("herdr", "pane", "layout", "--pane", tui).Output()
	if err != nil {
		return 0
	}
	type rect struct{ X, Y, Width, Height int }
	var r struct {
		Result struct {
			Layout struct {
				Panes []struct {
					PaneID string `json:"pane_id"`
					Rect   rect   `json:"rect"`
				} `json:"panes"`
				Splits []struct {
					Direction string  `json:"direction"`
					Ratio     float64 `json:"ratio"`
					Rect      rect    `json:"rect"`
				} `json:"splits"`
			} `json:"layout"`
		} `json:"result"`
	}
	if json.Unmarshal(out, &r) != nil {
		return 0
	}
	l := r.Result.Layout
	for _, p := range l.Panes {
		if p.PaneID != tui {
			continue
		}
		for _, s := range l.Splits {
			if s.Direction == "right" && s.Rect.X == p.Rect.X && s.Rect.Y == p.Rect.Y && s.Rect.Height == p.Rect.Height && s.Rect.Width > p.Rect.Width {
				return s.Ratio
			}
		}
	}
	return 0
}

// SendKeys types keys into the running TUI, as if the user had: herdr key
// bindings use it to drive the TUI while the focus is elsewhere.
// SendKeys types keys into the running TUI. With focus, the TUI's pane takes the
// focus first, and the pane that had it is noted for the TUI to give it back.
func SendKeys(keys []string, focus bool) error {
	for _, s := range readStates() {
		if s.Pane == "" {
			continue
		}
		if _, err := app.PaneTab(s.Pane); err != nil {
			continue
		}
		if focus {
			if prev := app.FocusedPane(); prev != "" && prev != s.Pane {
				_ = os.WriteFile(returnPath(), []byte(prev), 0o644)
			}
			if err := app.FocusPane(s.Pane); err != nil {
				return err
			}
		}
		for _, k := range keys {
			if out, err := exec.Command("herdr", "pane", "send-keys", s.Pane, k).CombinedOutput(); err != nil {
				return fmt.Errorf("herdr pane send-keys %s %s: %v: %s", s.Pane, k, err, out)
			}
		}
		return nil
	}
	return spec.UserError("no dossier TUI is running in a herdr pane")
}

// returnPath holds the pane that had the focus before `tui-key --focus`.
func returnPath() string { return filepath.Join(filepath.Dir(statePath()), "tui-return") }

// takeReturn reads and clears the pane to give the focus back to; a note older
// than a few seconds belongs to an earlier key.
func takeReturn() string {
	p := returnPath()
	fi, err := os.Stat(p)
	if err != nil {
		return ""
	}
	b, _ := os.ReadFile(p)
	_ = os.Remove(p)
	if time.Since(fi.ModTime()) > 10*time.Second {
		return ""
	}
	return strings.TrimSpace(string(b))
}
