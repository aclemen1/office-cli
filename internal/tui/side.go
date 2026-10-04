package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/aclemen1/office-cli/internal/app"
	"github.com/aclemen1/office-cli/internal/dossier"
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
	out, err := exec.Command("herdr", "pane", "split", tui, "--direction", "right", "--ratio", m.ratio()).Output()
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
	_ = exec.Command("herdr", "pane", "run", m.placeholder, fmt.Sprintf("exec %q placeholder %q --tui %q", dossierBin(), m.root, tui)).Run()
	m.status, m.statusErr = "side mode on: enter shows a dossier's agent at the right; v turns it off", false
	return nil
}

// leaveSide sends the docked agent home and closes the placeholder.
func (m *model) leaveSide() tea.Cmd {
	prev, placeholder := m.docked, m.placeholder
	m.docked, m.placeholder = docked{}, ""
	return func() tea.Msg {
		if prev.id != "" {
			_ = exec.Command(dossierBin(), "undock", prev.id, "--placeholder", placeholder, "--office", prev.root, "--format", "text").Run()
		}
		if placeholder != "" {
			_ = exec.Command("herdr", "pane", "close", placeholder).Run()
		}
		return nil
	}
}

// dock shows the row's agent in place of the placeholder, after sending the
// one docked before back home.
func (m *model) dock(r *row, focus bool) tea.Cmd {
	// One dock at a time: keys pressed meanwhile come down to the last one,
	// which runs when the current one is done.
	if m.docking {
		m.wantDock, m.wantFocus = &docked{r.office.root, r.d.ID}, focus
		m.status, m.statusErr = r.d.Label()+": next, once the current dock is done", false
		return nil
	}
	m.docking = true
	args := []string{"--placeholder", m.placeholder}
	if !focus {
		args = append(args, "--no-focus")
	}
	prev, next, placeholder := m.docked, docked{r.office.root, r.d.ID}, m.placeholder
	if prev == next {
		return func() tea.Msg {
			out, err := exec.Command(dossierBin(), append(append([]string{"dock", next.id}, args...), "--office", next.root, "--format", "text")...).CombinedOutput()
			return doneMsg{id: next.id, verb: "dock", out: strings.TrimSpace(string(out)), err: err}
		}
	}
	if prev.id != "" {
		m.lastDocked = prev
	}
	m.docked = next
	m.dockedReady = r.activity == "ready" || r.activity == "blocked"
	m.status, m.statusErr = r.d.Label()+": docking its agent…", false
	return func() tea.Msg {
		if prev.id != "" {
			if out, err := exec.Command(dossierBin(), "undock", prev.id, "--placeholder", placeholder, "--office", prev.root, "--format", "text").CombinedOutput(); err != nil {
				return doneMsg{id: prev.id, verb: "undock", out: strings.TrimSpace(string(out)), err: err}
			}
		}
		out, err := exec.Command(dossierBin(), append(append([]string{"dock", next.id}, args...), "--office", next.root, "--format", "text")...).CombinedOutput()
		return doneMsg{id: next.id, verb: "dock", out: strings.TrimSpace(string(out)), err: err}
	}
}

// mouse: a click selects a row and a click on the selected row opens it, as
// enter does; the wheel moves over the list, or scrolls the detail panel.
func (m *model) mouse(ev tea.MouseMsg) tea.Cmd {
	msg := ev.Mouse()
	// render records where the list is: listY, listW and listH.
	inList := msg.X < m.listW && msg.Y >= m.listY && msg.Y < m.listY+m.listH
	switch {
	case isWheel(ev):
		step := 1
		if msg.Button == tea.MouseWheelUp {
			step = -1
		}
		if inList {
			m.move(step)
		} else if m.scroll += step; m.scroll < 0 {
			m.scroll = 0
		}
		return nil
	case isLeftRelease(ev):
		if !inList {
			if l, ok := m.linkAt(msg.Y); ok {
				return m.openLink(l)
			}
			return nil
		}
	default:
		return nil
	}
	i := m.offset + msg.Y - m.listY
	if i >= len(m.rows) || !m.rows[i].selectable() {
		return nil
	}
	if i != m.cursor {
		m.cursor, m.scroll = i, 0
		return nil
	}
	return m.key("enter")
}

// heal handles a docked agent whose pane is gone, e.g. after /exit: its
// placeholder waits in the gone agent's tab, so it comes back to the TUI's right.
func (m *model) heal() tea.Cmd {
	if !m.side || m.docked.id == "" {
		return nil
	}
	for _, r := range m.rows {
		if r.d == nil || r.office.root != m.docked.root || r.d.ID != m.docked.id {
			continue
		}
		if r.activity != "stopped" && r.activity != "none" {
			return nil
		}
		prev, placeholder := m.docked, m.placeholder
		m.docked = docked{}
		tui, tab := os.Getenv("HERDR_PANE_ID"), os.Getenv("HERDR_TAB_ID")
		ratio := m.ratio()
		return func() tea.Msg {
			_ = exec.Command(dossierBin(), "undock", prev.id, "--placeholder", placeholder, "--office", prev.root, "--format", "text").Run()
			_ = exec.Command("herdr", "pane", "move", placeholder, "--tab", tab, "--split", "right", "--target-pane", tui, "--ratio", ratio, "--no-focus").Run()
			return nil
		}
	}
	return nil
}

// sendHome undocks the agent at the TUI's right: it goes back to its own
// tab, and the placeholder takes its place again. Side mode stays on.
func (m *model) sendHome() tea.Cmd {
	if !m.side || m.docked.id == "" {
		m.status, m.statusErr = "no agent docked at the right", true
		return nil
	}
	prev, placeholder := m.docked, m.placeholder
	m.docked, m.lastDocked = docked{}, prev
	m.docking = true
	m.status, m.statusErr = prev.id+": sending its agent home…", false
	return func() tea.Msg {
		out, err := exec.Command(dossierBin(), "undock", prev.id, "--placeholder", placeholder, "--office", prev.root, "--format", "text").CombinedOutput()
		return doneMsg{id: prev.id, verb: "undock", out: strings.TrimSpace(string(out)), err: err}
	}
}

// ratio is the TUI's share of the width when it opens the placeholder: the
// one saved at the last exit, or 35%.
func (m *model) ratio() string {
	r := m.sideRatio
	if r <= 0.05 || r >= 0.95 {
		r = 0.35
	}
	return strconv.FormatFloat(r, 'f', 4, 64)
}

func isWheel(ev tea.MouseMsg) bool {
	_, ok := ev.(tea.MouseWheelMsg)
	return ok
}

func isLeftRelease(ev tea.MouseMsg) bool {
	r, ok := ev.(tea.MouseReleaseMsg)
	return ok && r.Button == tea.MouseLeft
}

// screen wraps a rendered frame: full screen, with mouse clicks and the wheel.
func screen(s string) tea.View {
	v := tea.NewView(s)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.ReportFocus = true
	return v
}

// toPlaceholder focuses the placeholder: at the TUI's right, or in its own
// tab while an agent holds its place.
func (m *model) toPlaceholder() {
	if !m.side || m.placeholder == "" {
		m.status, m.statusErr = "no placeholder: v turns side mode on", true
		return
	}
	tab, err := app.PaneTab(m.placeholder)
	if err != nil {
		m.status, m.statusErr = "the placeholder pane is gone: v twice opens a new one", true
		return
	}
	if tab == os.Getenv("HERDR_TAB_ID") {
		_ = exec.Command("herdr", "pane", "focus", "--pane", os.Getenv("HERDR_PANE_ID"), "--direction", "right").Run()
		return
	}
	_ = exec.Command("herdr", "tab", "focus", tab).Run()
}

// dockDone ends a dock or undock and runs the one asked for meanwhile.
func (m *model) dockDone() tea.Cmd {
	m.docking = false
	want := m.wantDock
	m.wantDock = nil
	if want == nil {
		return nil
	}
	if i := m.rowOf(*want); i >= 0 {
		return m.dock(&m.rows[i], m.wantFocus)
	}
	return nil
}

// syncDocked reads which agent holds the placeholder's place from the offices:
// the TUI's own idea can lag behind docks run by others or that failed.
func (m *model) syncDocked() {
	if !m.side || m.placeholder == "" || m.docking {
		return
	}
	m.docked = docked{}
	for _, sv := range m.offices {
		for _, d := range append(append([]*dossier.Dossier{}, sv.all...), sv.a.Desk()) {
			if d.Run.Home != "" && d.Run.Placeholder == m.placeholder {
				m.docked = docked{sv.root, d.ID}
			}
		}
	}
}

func (m *model) dockedKey() string {
	if !m.side || m.docked.id == "" || !m.dockedReady {
		return ""
	}
	return m.docked.root + "|" + m.docked.id
}

// titleSeg names the TUI in the top bar: bright when it has the focus, dimmed
// when another pane has it, with the docked agent named in side mode.
func (m *model) titleSeg() string {
	if paneFocused {
		return sTitle.Render("office")
	}
	s := sFaint.Render("office")
	if m.side && m.docked.id != "" {
		s += sMuted.Render("  focus → ") + lipgloss.NewStyle().Foreground(cAccent).Bold(true).Render(m.docked.id)
	}
	return s
}

// isDocked says whether the row's agent is the one shown at the right.
func (m *model) isDocked(r row) bool {
	return m.side && r.d != nil && r.office != nil && r.office.root == m.docked.root && r.d.ID == m.docked.id
}

// markDocked puts ▶ in the row's first column: its agent is at the right.
func markDocked(line string, w int) string {
	return lipgloss.NewStyle().Foreground(cAccent).Bold(true).Render("▶") + ansi.Cut(line, 1, w)
}

// labelCell shows a dossier's label; an alias keeps its id beside it, dimmed,
// since agents name dossiers by their id.
func labelCell(d *dossier.Dossier, w int) string {
	s := sBold.Render(d.Label())
	if d.Alias != "" {
		s += " " + sMuted.Render(d.ID)
	}
	if pad := w - labelWidth(d); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}

func labelWidth(d *dossier.Dossier) int {
	if d.Alias != "" {
		return lipgloss.Width(d.Label()) + 1 + lipgloss.Width(d.ID)
	}
	return lipgloss.Width(d.Label())
}
