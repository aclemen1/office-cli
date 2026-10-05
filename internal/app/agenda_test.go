package app

import (
	"strings"
	"testing"

	"github.com/aclemen1/office-cli/internal/dossier"
)

func TestIncludingADossierInAMeetingProposesItOnTheAgenda(t *testing.T) {
	f := newFixture(t)
	var calls []string
	old := agendaCall
	t.Cleanup(func() { agendaCall = old })
	agendaCall = func(argv []string) ([]byte, error) {
		calls = append(calls, strings.Join(argv, " "))
		return []byte(`{"ok":true,"result":{"id":"RDIR-7"}}`), nil
	}
	f.a.Open(OpenParams{Title: "Séance RDIR", NoStart: true, Alias: "RDIR"})
	f.a.Open(OpenParams{Title: "Audit des accès S3", NoStart: true})
	item := mustGet(t, f, "2")
	item.Sources = append(item.Sources, dossier.Source{ID: "gmail:task/T42"})
	item.Save()

	f.a.Link("1", "2", dossier.RelIncludes)
	if len(calls) != 0 {
		t.Fatal("an office without [agenda] called the agenda")
	}
	f.a.Unlink("1", "2", dossier.RelIncludes)
	f.a.S.Config.Agenda.Command, f.a.S.Config.Agenda.Sphere = []string{"ordo"}, "pro"
	if _, err := f.a.Link("1", "2", dossier.RelIncludes); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || !strings.Contains(calls[0], `ordo item add RDIR Audit des accès S3 --ref office:D-0002 --ref gtasks:T42 --sphere pro --format json`) {
		t.Fatalf("calls %v", calls)
	}
	f.a.Unlink("1", "2", dossier.RelIncludes)
	f.a.Link("1", "2", dossier.RelIncludes)
	if len(calls) != 1 {
		t.Fatal("the same dossier was proposed twice")
	}
	f.a.Open(OpenParams{Title: "Sans alias", NoStart: true})
	f.a.Link("3", "2", dossier.RelIncludes)
	if len(calls) != 1 {
		t.Fatal("a holder without alias is no meeting")
	}
}
