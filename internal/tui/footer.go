package tui

import "github.com/aclemen1/dossier-cli/internal/dossier"

// footer lists the keys that mean something now: first for the selected row,
// then to move around. After g, only where g can go.
func (m *model) footer() (row, nav [][2]string) {
	if m.gPending {
		return [][2]string{{"g", "top"}, {"d", "desk"}, {"i", "active"}, {"t", "to do"}, {"w", "by person"}, {"a", "all states"}},
			[][2]string{{"esc", "cancel"}}
	}
	r := m.selected()
	switch {
	case m.selectedAgent() != nil:
		row = [][2]string{{"c", "adopt as a dossier"}, {"o", "go to its tab"}}
	case r != nil && r.desk:
		row = [][2]string{{"o", "open the desk"}, {"c", "new dossier"}}
		if r.d.Run.Session != "" {
			row = append(row, [2]string{"R", "restart"}, [2]string{"N", "new conversation"})
		}
	case r != nil:
		d := r.d
		if r.activity == "none" {
			row = [][2]string{{"o", "start with its prompt"}, {"s", "start, no prompt"}}
		} else {
			row = [][2]string{{"o", "open"}, {"R", "restart"}}
		}
		if m.side {
			row = append(row[:1:1], append([][2]string{{"O", "show, keep focus"}}, row[1:]...)...)
		}
		switch d.State {
		case dossier.Open:
			row = append(row, [2]string{"W", "wait"}, [2]string{"e", "close"})
			if d.NoAction {
				row = append(row, [2]string{"n", "needs action"})
			} else {
				row = append(row, [2]string{"n", "no action"})
			}
		case dossier.Waiting:
			row = append(row, [2]string{"u", "resume"}, [2]string{"W", "correct the wait"}, [2]string{"e", "close"})
		case dossier.Done:
			row = append(row, [2]string{"u", "reopen"})
		}
		if !r.unread {
			row = append(row, [2]string{"U", "unread"})
		}
		row = append(row, [2]string{"c", "new"}, [2]string{"#", "delete"})
	default:
		row = [][2]string{{"c", "new dossier"}}
	}
	if m.side && m.docked.id != "" {
		row = append(row, [2]string{"h", "send home"})
	}
	nav = [][2]string{{"j k", "move"}, {"g", "go to"}}
	if len(m.queue()) > 0 {
		nav = append(nav, [2]string{"]", "needs you"})
	}
	nav = append(nav, [2]string{"[", "latest"})
	if m.lastDocked.id != "" {
		nav = append(nav, [2]string{"'", "back"})
	}
	side := "side"
	if m.side {
		side = "side off"
	}
	nav = append(nav, [2]string{"/", "filter"}, [2]string{"v", side}, [2]string{"tab", "detail"}, [2]string{"?", "keys"}, [2]string{"q", "quit"})
	return row, nav
}
