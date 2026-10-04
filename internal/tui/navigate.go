package tui

import (
	"encoding/json"
	"os"
	"os/exec"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/aclemen1/office-cli/internal/app"
)

// Navigation between agents: ' the one docked before, ] the next that needs
// you, [ the one whose conversation moved last. In side mode the agent comes
// to the TUI's right; otherwise its tab is focused.

// open selects row i and shows its agent. The focus follows when it was not
// in the TUI: a key relayed by herdr from the docked agent keeps you there.
func (m *model) open(i int) tea.Cmd {
	m.cursor, m.scroll = i, 0
	if m.side {
		return m.dock(&m.rows[i], !tuiFocused())
	}
	return m.key("enter")
}

func (m *model) rowOf(k docked) int {
	for i, r := range m.rows {
		if r.d != nil && r.office.root == k.root && r.d.ID == k.id {
			return i
		}
	}
	return -1
}

// current is the agent being looked at: the docked one, else the selected row.
func (m *model) current() docked {
	if m.side && m.docked.id != "" {
		return m.docked
	}
	if r := m.selected(); r != nil {
		return docked{r.office.root, r.d.ID}
	}
	return docked{}
}

func (m *model) toggleLast() tea.Cmd {
	if m.lastDocked.id == "" {
		m.status, m.statusErr = "no agent was docked before this one", true
		return nil
	}
	i := m.rowOf(m.lastDocked)
	if i < 0 {
		m.status, m.statusErr = m.lastDocked.id+" is not in this view: g a shows every dossier", true
		return nil
	}
	return m.open(i)
}

// attention ranks what an agent asks of you: a permission, then your turn,
// then something unread; -1 for nothing.
func attention(r row) int {
	switch {
	case r.d == nil || r.d.Run.Session == "":
		return -1
	case r.activity == "blocked":
		return 0
	case r.activity == "ready":
		return 1
	case r.unread:
		return 2
	}
	return -1
}

// queue lists the rows that need you, most urgent first, each dossier once.
func (m *model) queue() []int {
	seen := map[string]bool{}
	var out []int
	for rank := 0; rank <= 2; rank++ {
		for i, r := range m.rows {
			if attention(r) == rank && !seen[r.key()] {
				seen[r.key()] = true
				out = append(out, i)
			}
		}
	}
	return out
}

// forYou counts the agents that ask a permission or wait for your turn.
func (m *model) forYou() int {
	n := 0
	for _, i := range m.queue() {
		if attention(m.rows[i]) <= 1 {
			n++
		}
	}
	return n
}

func (m *model) nextAttention() tea.Cmd {
	q := m.queue()
	if len(q) == 0 {
		m.status, m.statusErr = "no agent needs you", false
		return nil
	}
	cur, at := m.current(), -1
	for j, i := range q {
		if m.rows[i].key() == cur.root+"|"+cur.id {
			at = j
		}
	}
	return m.open(q[(at+1)%len(q)])
}

func (m *model) lastManifested() tea.Cmd {
	cur, best := m.current(), -1
	var when time.Time
	for i, r := range m.rows {
		if r.d == nil || r.d.Run.Session == "" || r.key() == cur.root+"|"+cur.id {
			continue
		}
		if t := app.LastActivity(r.d); t.After(when) {
			best, when = i, t
		}
	}
	if best < 0 {
		m.status, m.statusErr = "no other agent has spoken", false
		return nil
	}
	return m.open(best)
}

// tuiFocused tells whether the TUI's pane has the focus. Tests replace it.
var tuiFocused = func() bool {
	pane := os.Getenv("HERDR_PANE_ID")
	if pane == "" {
		return true
	}
	out, err := exec.Command("herdr", "pane", "get", pane).Output()
	if err != nil {
		return true
	}
	var r struct {
		Result struct {
			Pane struct {
				Focused bool `json:"focused"`
			} `json:"pane"`
		} `json:"result"`
	}
	if json.Unmarshal(out, &r) != nil {
		return true
	}
	return r.Result.Pane.Focused
}

// step shows the dossier above (-1) or below (+1) the current one: the docked
// one in side mode, the selected one otherwise.
func (m *model) step(dir int) tea.Cmd {
	from := m.cursor
	if i := m.rowOf(m.current()); i >= 0 {
		from = i
	}
	for i := from + dir; i >= 0 && i < len(m.rows); i += dir {
		if m.rows[i].d != nil {
			return m.open(i)
		}
	}
	m.status, m.statusErr = "no dossier further", false
	return nil
}
