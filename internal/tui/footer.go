package tui

import "github.com/aclemen1/dossier-cli/internal/dossier"

// keyGroup is one theme of the footer: its title and its keys.
type keyGroup struct {
	title string
	keys  [][2]string
}

// footer lists the keys that mean something now, by theme: the selected
// dossier, its agent, moving around, the view. After g, only where g goes.
func (m *model) footer() []keyGroup {
	if m.gPending {
		return []keyGroup{
			{"go to", [][2]string{{"g", "top"}, {"d", "desk"}, {"p", "placeholder"}, {"i", "active"}, {"t", "to do"}, {"w", "by person"}, {"a", "all states"}, {"s", "starred"}, {"n", "agents without dossier"}}},
			{"", [][2]string{{"esc", "cancel"}}},
		}
	}
	if m.linkMode {
		return []keyGroup{{"links", [][2]string{{"j k", "next, previous"}, {"enter", "open"}, {"esc", "back to the list"}}}}
	}
	var item, agent [][2]string
	r := m.selected()
	switch {
	case m.selectedAgent() != nil:
		item = [][2]string{{"c", "adopt as a dossier"}}
		agent = [][2]string{{"o", "go to its tab"}}
	case r != nil && r.desk:
		item = [][2]string{{"c", "new dossier"}}
		agent = [][2]string{{"o", "open the desk"}}
		if m.side {
			agent = append(agent, [2]string{"O", "show, keep focus"})
		}
		if r.d.Run.Session != "" {
			agent = append(agent, [2]string{"R", "restart"}, [2]string{"N", "new conversation"})
		}
	case r != nil:
		d := r.d
		if r.activity == "none" {
			agent = [][2]string{{"o", "start with its prompt"}, {"S", "start, no prompt"}}
		} else {
			agent = [][2]string{{"o", "open"}}
			if m.side {
				agent = append(agent, [2]string{"O", "show, keep focus"})
			}
			agent = append(agent, [2]string{"R", "restart"})
		}
		switch d.State {
		case dossier.Open:
			item = append(item, [2]string{"W", "wait"}, [2]string{"e", "close"})
			if d.NoAction {
				item = append(item, [2]string{"n", "needs action"})
			} else {
				item = append(item, [2]string{"n", "no action"})
			}
		case dossier.Waiting:
			item = append(item, [2]string{"u", "resume"}, [2]string{"W", "correct the wait"}, [2]string{"e", "close"})
		case dossier.Done:
			item = append(item, [2]string{"u", "reopen"})
		}
		if d.Starred {
			item = append(item, [2]string{"s", "unstar"})
		} else {
			item = append(item, [2]string{"s", "star"})
		}
		item = append(item, [2]string{"c", "new"}, [2]string{"#", "delete"})
	default:
		item = [][2]string{{"c", "new dossier"}}
	}
	if m.side && m.docked.id != "" {
		agent = append(agent, [2]string{"h", "send home"})
	}

	nav := [][2]string{{"j k", "move"}, {"g", "go to"}}
	if len(m.queue()) > 0 {
		nav = append(nav, [2]string{"]", "needs you"})
	}
	nav = append(nav, [2]string{"[", "latest"})
	nav = append(nav, [2]string{"< >", "previous, next dossier"})
	if m.lastDocked.id != "" {
		nav = append(nav, [2]string{"'", "back"})
	}
	nav = append(nav, [2]string{"/", "filter"})
	if len(m.links) > 0 {
		nav = append(nav, [2]string{"f", "open a link"})
	}
	if m.detailOverflows && (!m.noDetail || m.legend) {
		nav = append(nav, [2]string{"J K", "scroll detail"})
	}

	detailWord := "hide detail"
	if m.noDetail {
		detailWord = "show detail"
	}
	side := "side"
	if m.side {
		side = "side off"
	}
	view := [][2]string{{"v", side}, {"tab", detailWord}, {"L", "panel " + m.layoutName()}, {"?", "keys"}, {"q", "quit"}}

	return []keyGroup{{"dossier", item}, {"agent", agent}, {"move", nav}, {"view", view}}
}
