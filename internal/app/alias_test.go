package app

import (
	"strings"
	"testing"

	"github.com/aclemen1/office-cli/internal/dossier"
)

func TestAliasesNameLastingDossiers(t *testing.T) {
	f := newFixture(t)
	f.a.S.Config.Office.IDPrefix = "U"
	r, err := f.a.Open(OpenParams{Title: "Séance RDIR", Alias: "rdir", NoStart: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"RDIR", "rdir", "U-RDIR", "u-rdir", "U-0001", "1"} {
		if d, err := f.a.Load(id); err != nil || d.ID != r.ID {
			t.Errorf("Load(%q): %v", id, err)
		}
	}
	d, _ := f.a.Load("RDIR")
	if d.Label() != "U-RDIR" || TabLabel(d) != "U-RDIR · Séance RDIR" {
		t.Fatalf("label %q / %q", d.Label(), TabLabel(d))
	}
	f.a.Open(OpenParams{Title: "Autre", NoStart: true})
	other, _ := f.a.Load("2")
	if err := f.a.SetAlias(other, "RDIR"); err == nil || !strings.Contains(err.Error(), "already U-0001") {
		t.Fatalf("duplicate alias: %v", err)
	}
	if err := f.a.SetAlias(other, "4ever"); err == nil {
		t.Fatal("alias starting with a digit accepted")
	}
	if err := f.a.SetAlias(d, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.Load("RDIR"); err == nil {
		t.Fatal("cleared alias still resolves")
	}
}

func TestInMakesTheHolderIncludeTheDossier(t *testing.T) {
	f := newFixture(t)
	f.a.Open(OpenParams{Title: "Séance RDIR", Alias: "RDIR", NoStart: true})
	r, err := f.a.Open(OpenParams{Title: "Chiffres étudiants FBM", SourceRef: "fake:thread/fbm", In: []string{"RDIR"}, NoStart: true})
	if err != nil || len(r.Warnings) != 0 {
		t.Fatalf("open %+v %v", r, err)
	}
	meeting, _ := f.a.Load("RDIR")
	if !meeting.HasLink(dossier.RelIncludes, r.ID) {
		t.Fatalf("meeting links %v", meeting.Links)
	}
	again, _ := f.a.Open(OpenParams{Title: "Chiffres étudiants FBM", SourceRef: "fake:thread/fbm", In: []string{"RDIR"}, NoStart: true})
	meeting, _ = f.a.Load("RDIR")
	if again.Outcome != "existing" || len(meeting.Links) != 1 {
		t.Fatalf("second signal %+v, links %v", again, meeting.Links)
	}
	missing, _ := f.a.Open(OpenParams{Title: "X", In: []string{"PSEC"}, NoStart: true})
	if len(missing.Warnings) != 1 || !strings.Contains(missing.Warnings[0], "in PSEC") {
		t.Fatalf("unknown meeting should warn: %+v", missing)
	}
}
