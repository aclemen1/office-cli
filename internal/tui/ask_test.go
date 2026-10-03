package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/aclemen1/dossier-cli/internal/dossier"
)

func typeIn(a *ask, s string) {
	for _, r := range s {
		a.key(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func TestWaitFormAsksWhomAndWhenThenRuns(t *testing.T) {
	m := &model{}
	r := &row{store: &storeView{root: "/s"}, d: &dossier.Dossier{ID: "D-0017", State: dossier.Waiting, WaitingOn: "Alain", WaitUntil: "2027-01-04T23:59:59+01:00"}}
	m.stateKey("W", r)
	if m.ask == nil || m.ask.fields[0].value != "Alain" {
		t.Fatalf("form %+v", m.ask)
	}
	m.ask.key(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	typeIn(m.ask, "Patricia")
	if done, _ := m.ask.key(tea.KeyPressMsg{Code: tea.KeyEnter}); done {
		t.Fatal("the form ended after the first field")
	}
	if done, cmd := m.ask.key(tea.KeyPressMsg{Code: tea.KeyEnter}); !done || cmd == nil {
		t.Fatal("the form did not run the wait")
	}
	m.stateKey("W", r)
	m.ask.key(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	m.ask.key(tea.KeyPressMsg{Code: tea.KeyEnter})
	if _, cmd := m.ask.key(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil || !m.statusErr {
		t.Fatal("a wait on nobody was accepted")
	}
	if done, cmd := (&ask{fields: []askField{{}}, done: func([]string) tea.Cmd { return tea.Quit }}).key(tea.KeyPressMsg{Code: tea.KeyEscape}); !done || cmd != nil {
		t.Fatal("esc should cancel")
	}
}

func TestNewDossierFormNeedsATitleAndKnowsTheStores(t *testing.T) {
	m := &model{stores: []*storeView{{name: "perso", root: "/p"}, {name: "pro", root: "/u"}}}
	seance := &row{store: m.stores[1], d: &dossier.Dossier{ID: "U-0006", Alias: "RDIR", State: dossier.Open}}
	m.newDossier(seance)
	labels := []string{}
	for _, f := range m.ask.fields {
		labels = append(labels, f.label+"="+f.value)
	}
	if got := strings.Join(labels, ","); got != "title=,instruction=,store=pro,in=U-RDIR,start session=yes" {
		t.Fatalf("fields %s", got)
	}
	for range m.ask.fields {
		m.ask.key(tea.KeyPressMsg{Code: tea.KeyEnter})
	}
	if !m.statusErr {
		t.Fatal("a dossier without a title was accepted")
	}
	m.newDossier(nil)
	typeIn(m.ask, "Armoire")
	var cmd tea.Cmd
	for range m.ask.fields {
		_, cmd = m.ask.key(tea.KeyPressMsg{Code: tea.KeyEnter})
	}
	if cmd == nil || m.statusErr {
		t.Fatalf("the form did not open the dossier: %s", m.status)
	}
}
