package tui

import (
	"testing"

	"github.com/aclemen1/office-cli/internal/dossier"
)

func TestJumpRanksAliasThenTitleThenContentAndClosedLast(t *testing.T) {
	mk := func(id, alias, title, state, body string) *dossier.Dossier {
		d := &dossier.Dossier{ID: id, Alias: alias, Title: title, State: state}
		d.SetBody(body)
		return d
	}
	sv := &officeView{name: "perso", root: "/o/perso", all: []*dossier.Dossier{
		mk("P-0001", "", "Notes sur l'office", dossier.Open, "rien"),
		mk("P-0002", "OFFICE-DEV", "Développement", dossier.Open, ""),
		mk("P-0003", "", "Facture", dossier.Open, "le relevé de l'Office des poursuites"),
		mk("P-0004", "", "Office fermé", dossier.Done, ""),
		mk("P-0005", "", "Sans rapport", dossier.Open, "rien"),
	}}
	var got []string
	for _, h := range rankJump([]*officeView{sv}, "office") {
		got = append(got, h.d.ID+":"+h.field)
	}
	want := []string{"P-0002:alias", "P-0001:title", "P-0003:content", "P-0004:title"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	if h := rankJump([]*officeView{sv}, "developpements"); len(h) != 0 {
		t.Fatalf("a letter that is not there matched: %v", h[0].d.ID)
	}
	if h := rankJump([]*officeView{sv}, "dvlpmt"); len(h) != 1 || h[0].d.ID != "P-0002" {
		t.Fatalf("fuzzy title %v", h)
	}
	if h := rankJump([]*officeView{sv}, "releve poursuites"); len(h) != 1 || h[0].d.ID != "P-0003" {
		t.Fatalf("content without accents %v", h)
	}
}

func TestJumpKeepsTheView(t *testing.T) {
	sv := &officeView{name: "perso", root: "/o/perso"}
	open := &dossier.Dossier{ID: "P-0001", Title: "Ouvert", State: dossier.Open}
	done := &dossier.Dossier{ID: "P-0002", Title: "Clos", State: dossier.Done}
	sv.all = []*dossier.Dossier{open, done}
	m := &model{todo: true, filter: "x", rows: []row{{header: "perso", office: sv}, {office: sv, d: open}}}
	m.jumpTo(jumpHit{sv: sv, d: open})
	if m.cursor != 1 || !m.todo || m.filter != "x" {
		t.Fatalf("cursor %d todo %v filter %q", m.cursor, m.todo, m.filter)
	}
	m.jumpTo(jumpHit{sv: sv, d: done, closed: true})
	if m.all || !m.todo || m.cursor != 1 || !m.statusErr {
		t.Fatalf("a hidden dossier changed the view: all %v todo %v cursor %d", m.all, m.todo, m.cursor)
	}
}
