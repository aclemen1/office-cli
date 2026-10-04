package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/aclemen1/office-cli/internal/dossier"
)

// ask is a short form on the bottom line: one field after the other, enter to
// go on, esc to cancel.
type ask struct {
	title  string
	fields []askField
	i      int
	done   func(values []string) tea.Cmd
}

type askField struct {
	label, value, hint string
}

func (a *ask) key(k tea.KeyPressMsg) (finished bool, cmd tea.Cmd) {
	f := &a.fields[a.i]
	switch k.Keystroke() {
	case "esc":
		return true, nil
	case "enter":
		if a.i < len(a.fields)-1 {
			a.i++
			return false, nil
		}
		values := make([]string, len(a.fields))
		for i, f := range a.fields {
			values[i] = strings.TrimSpace(f.value)
		}
		return true, a.done(values)
	case "backspace":
		if r := []rune(f.value); len(r) > 0 {
			f.value = string(r[:len(r)-1])
		}
	case "ctrl+u":
		f.value = ""
	case "ctrl+c":
		return true, tea.Quit
	default:
		f.value += k.Text
	}
	return false, nil
}

func (a *ask) view() string {
	f := a.fields[a.i]
	head := lipgloss.NewStyle().Bold(true).Foreground(cAccent).Render(a.title)
	line := head + sMuted.Render("  ·  "+f.label+"  ") + sBold.Render(f.value+"▏")
	if f.hint != "" {
		line += sFaint.Render("   " + f.hint)
	}
	return line + "\n" + sMuted.Render("enter ") + sFaint.Render("confirm") + sMuted.Render("  ·  esc ") + sFaint.Render("cancel") + sMuted.Render("  ·  ctrl+u ") + sFaint.Render("clear")
}

// stateKey opens the form or runs the command of a state key: W wait, u
// resume or reopen, x close.
func (m *model) stateKey(k string, r *row) tea.Cmd {
	d, root := r.d, r.office.root
	switch k {
	case "W":
		if d.State != dossier.Open && d.State != dossier.Waiting {
			m.status, m.statusErr = d.Label()+" is "+d.State+": only an open or waiting dossier can wait", true
			return nil
		}
		title, hint := "Wait "+d.Label(), "a date (2026-10-15), a delay (7d, 48h) or none; empty: the office's default"
		if d.State == dossier.Waiting {
			title, hint = "Correct the wait of "+d.Label(), "a date, a delay or none; empty: keep "+dayMonthYear(d.WaitUntil)
		}
		m.ask = &ask{title: title, fields: []askField{
			{label: "waiting on", value: d.WaitingOn, hint: "who must answer"},
			{label: "chase", hint: hint},
		}, done: func(v []string) tea.Cmd {
			if v[0] == "" {
				m.status, m.statusErr = d.Label()+": a wait needs someone to wait on", true
				return nil
			}
			args := []string{"--on", v[0]}
			if v[1] != "" {
				args = append(args, "--until", v[1])
			}
			return run(root, d.ID, "wait", args...)
		}}
	case "u":
		switch d.State {
		case dossier.Waiting:
			return run(root, d.ID, "resume")
		case dossier.Done:
			return run(root, d.ID, "reopen")
		}
		m.status, m.statusErr = d.Label()+" is "+d.State+": u resumes a waiting dossier or reopens a closed one", true
	case "D":
		m.ask = &ask{title: "Delete " + d.Label() + " · " + truncate(d.Title, 40), fields: []askField{
			{label: "type " + d.Label() + " to confirm", hint: "its signal is withdrawn, its directory goes to the Trash"},
		}, done: func(v []string) tea.Cmd {
			if !strings.EqualFold(v[0], d.Label()) && !strings.EqualFold(v[0], d.ID) {
				m.status, m.statusErr = d.Label()+" kept", false
				return nil
			}
			return run(root, d.ID, "delete")
		}}
	case "x":
		if d.State == dossier.Done || d.State == dossier.Merged {
			m.status, m.statusErr = d.Label()+" is already "+d.State, true
			return nil
		}
		m.ask = &ask{title: "Close " + d.Label() + " · " + truncate(d.Title, 40), fields: []askField{
			{label: "outcome", hint: "optional, kept in the history; its sources are closed too"},
		}, done: func(v []string) tea.Cmd {
			var args []string
			if v[0] != "" {
				args = []string{"--note", v[0]}
			}
			return run(root, d.ID, "close", args...)
		}}
	}
	return nil
}

// newDossier opens the form of the + key: title, instruction, office, the
// dossier that includes it, and whether to start its session.
func (m *model) newDossier(r *row) {
	offices := make([]string, 0, len(m.offices))
	for _, sv := range m.offices {
		offices = append(offices, sv.name)
	}
	office, in := "", ""
	if r != nil {
		office = r.office.name
		if r.d.Alias != "" || len(r.d.Targets(dossier.RelIncludes)) > 0 {
			in = r.d.Label()
		}
	} else if len(m.offices) > 0 {
		office = m.offices[0].name
	}
	fields := []askField{
		{label: "title", hint: "short name of the affair"},
		{label: "instruction", hint: "what the agent should do; empty: the office's default"},
	}
	if len(offices) > 1 {
		fields = append(fields, askField{label: "office", value: office, hint: strings.Join(offices, " or ")})
	}
	fields = append(fields,
		askField{label: "in", value: in, hint: "dossier that includes it, e.g. a meeting; empty: on its own"},
		askField{label: "start session", value: "yes", hint: "yes: its agent starts on the instruction; no: later, with s or o"})
	m.ask = &ask{title: "New dossier", fields: fields, done: func(v []string) tea.Cmd {
		get := func(label string) string {
			for i, f := range fields {
				if f.label == label {
					return v[i]
				}
			}
			return ""
		}
		title := get("title")
		if title == "" {
			m.status, m.statusErr = "a dossier needs a title", true
			return nil
		}
		root := ""
		want := get("office")
		if want == "" {
			want = office
		}
		for _, sv := range m.offices {
			if strings.EqualFold(sv.name, want) {
				root = sv.root
			}
		}
		if root == "" {
			m.status, m.statusErr = "no office named "+want+": "+strings.Join(offices, ", "), true
			return nil
		}
		args := []string{"open", "--title", title}
		if i := get("instruction"); i != "" {
			args = append(args, "--instruction", i)
		}
		if in := get("in"); in != "" {
			args = append(args, "--in", in)
		}
		if a := strings.ToLower(get("start session")); a == "no" || a == "n" || a == "non" {
			args = append(args, "--no-start")
		}
		m.status, m.statusErr = "opening "+title+"…", false
		return runArgs(root, "open", args...)
	}}
}
