package tui

import (
	"os/exec"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/aclemen1/office-cli/internal/app"
)

func (m *model) selectedAgent() *app.AgentPane {
	if m.cursor >= 0 && m.cursor < len(m.rows) {
		return m.rows[m.cursor].agent
	}
	return nil
}

// agentKey: enter jumps to the agent's tab, + adopts it as a new dossier.
func (m *model) agentKey(k string, ag *app.AgentPane) (tea.Cmd, bool) {
	switch k {
	case "enter":
		_ = exec.Command("herdr", "tab", "focus", ag.TabID).Run()
		m.status, m.statusErr = "agent in "+filepath.Base(ag.Cwd)+": tab focused", false
		return nil, true
	case "+":
		m.adopt(ag)
		return nil, true
	case "W", "u", "x", "D", "n", "m", "s", "o", "R", "N":
		m.status, m.statusErr = "this agent has no dossier yet: + adopts it", true
		return nil, true
	}
	return nil, false
}

func (m *model) adopt(ag *app.AgentPane) {
	offices := make([]string, 0, len(m.offices))
	for _, sv := range m.offices {
		offices = append(offices, sv.name)
	}
	fields := []askField{{label: "title", value: ag.Title, hint: "short name of the affair"}}
	if len(offices) > 1 {
		fields = append(fields, askField{label: "office", hint: strings.Join(offices, " or ")})
	}
	fields = append(fields, askField{label: "in", hint: "dossier that includes it; empty: on its own"})
	m.ask = &ask{title: "Adopt the agent in " + filepath.Base(ag.Cwd), fields: fields, done: func(v []string) tea.Cmd {
		get := func(label string) string {
			for i, f := range fields {
				if f.label == label {
					return v[i]
				}
			}
			return ""
		}
		root := m.offices[0].root
		if want := get("office"); want != "" {
			root = ""
			for _, sv := range m.offices {
				if strings.EqualFold(sv.name, want) {
					root = sv.root
				}
			}
		} else if len(m.offices) > 1 {
			m.status, m.statusErr = "say which office: "+strings.Join(offices, " or "), true
			return nil
		}
		if root == "" {
			m.status, m.statusErr = "no office named "+get("office"), true
			return nil
		}
		args := []string{"adopt", "--pane", ag.PaneID, "--title", get("title")}
		if in := get("in"); in != "" {
			args = append(args, "--in", in)
		}
		m.status, m.statusErr = "adopting the agent…", false
		return runArgs(root, "adopt", args...)
	}}
}

func (m *model) agentRowView(r row, sel bool, w int) string {
	ag := r.agent
	line := "   " + activityMark(r.activity) + "    " + sBold.Render(truncate(filepath.Base(ag.Cwd), 18)) + "  " +
		sText.Render(truncate(ag.Title, w-30))
	st := lipgloss.NewStyle().Width(w).MaxWidth(w)
	if sel {
		st = st.Background(cSelBg)
	}
	return st.Render(line)
}

func agentView(r *row, w int, add func(...string)) {
	ag := r.agent
	label := lipgloss.NewStyle().Foreground(cMuted).Width(12)
	field := func(name, value string) { add(label.Render(name) + value) }
	add(sTitle.Render("Agent without dossier"), "", lipgloss.NewStyle().Width(w).Inherit(sBold).Render(ag.Title), "")
	field("agent", activityMark(r.activity)+" "+sText.Render(activityWord(r.activity)))
	field("directory", sText.Render(truncate(ag.Cwd, w-12)))
	field("session", sText.Render(truncate(ag.Session, w-12)))
	field("pane", sText.Render(ag.PaneID+"  ·  tab "+ag.TabID))
	add("", sSection.Render("Keys"), "",
		sText.Render("+")+sMuted.Render(" adopt it as a new dossier: its conversation becomes the dossier's session"),
		sText.Render("enter")+sMuted.Render(" go to its tab"),
		"", sFaint.Render("Once adopted, R restarts it with the dossier's tools, after its turn ends."))
}
