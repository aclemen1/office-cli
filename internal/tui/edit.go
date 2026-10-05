package tui

import (
	"os"
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/aclemen1/office-cli/internal/app"
)

// editedMsg comes back when the editor closes.
type editedMsg struct {
	root, id, tmp, before string
	err                   error
}

// editFiche suspends the TUI and opens the fiche (the desk's charter) in
// $EDITOR, in this very pane.
func (m *model) editFiche(r *row) tea.Cmd {
	text, err := r.office.a.EditText(r.d)
	if err != nil {
		m.status, m.statusErr = r.d.Label()+": "+err.Error(), true
		return nil
	}
	f, err := os.CreateTemp("", "office-"+strings.ToLower(r.d.ID)+"-*.md")
	if err != nil {
		m.status, m.statusErr = err.Error(), true
		return nil
	}
	_, _ = f.WriteString(text)
	f.Close()
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}
	// $EDITOR may carry arguments, e.g. "code -w".
	cmd := exec.Command("/bin/sh", "-c", editor+` "$1"`, "sh", f.Name())
	root, id, tmp := r.office.root, r.d.ID, f.Name()
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return editedMsg{root: root, id: id, tmp: tmp, before: text, err: err}
	})
}

func (m *model) edited(msg editedMsg) {
	keep := false
	defer func() {
		if !keep {
			os.Remove(msg.tmp)
		}
	}()
	if msg.err != nil {
		m.status, m.statusErr = "editor: "+msg.err.Error(), true
		return
	}
	b, err := os.ReadFile(msg.tmp)
	if err != nil {
		m.status, m.statusErr = err.Error(), true
		return
	}
	var a *app.App
	for _, sv := range m.offices {
		if sv.root == msg.root {
			a = sv.a
		}
	}
	if a == nil {
		return
	}
	if err := a.S.Lock(); err != nil {
		keep = true
		m.status, m.statusErr = err.Error()+" · your text: "+msg.tmp, true
		return
	}
	defer a.S.Unlock()
	d, err := a.LoadAny(msg.id)
	if err != nil {
		m.status, m.statusErr = err.Error(), true
		return
	}
	res, err := a.SaveEdit(d, msg.before, string(b))
	switch {
	case err != nil:
		keep = true
		m.status, m.statusErr = err.Error()+" · your text: "+msg.tmp, true
	case res.Conflict != "":
		m.status, m.statusErr = d.Label()+" changed while you edited: your version waits in "+res.Conflict, true
	case !res.Saved:
		m.status, m.statusErr = d.Label()+": unchanged", false
	default:
		m.status, m.statusErr = d.Label()+": saved", false
		if !app.IsDesk(d) && d.Run.Session != "" {
			before, after, label := msg.before, string(b), d.Label()
			m.ask = &ask{title: label + " · fiche saved", fields: []askField{
				{label: "tell its agent? (y/n)", value: "y", hint: "it reads the fiche again after its turn"},
			}, done: func(v []string) tea.Cmd {
				if !strings.HasPrefix(strings.ToLower(v[0]), "y") && !strings.HasPrefix(strings.ToLower(v[0]), "o") {
					m.status, m.statusErr = label+": saved, its agent not told", false
					return nil
				}
				m.status, m.statusErr = label+": its agent will read the fiche after its turn", false
				return func() tea.Msg {
					if err := a.S.Lock(); err != nil {
						return doneMsg{id: label, verb: "tell", err: err}
					}
					defer a.S.Unlock()
					fresh, err := a.LoadAny(msg.id)
					if err == nil {
						err = a.TellEdit(fresh, before, after)
					}
					return doneMsg{id: label, verb: "tell", err: err}
				}
			}}
		}
	}
	m.reload()
}
