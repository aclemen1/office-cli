package app

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aclemen1/office-cli/internal/dossier"
)

func TestParseUntil(t *testing.T) {
	loc := time.FixedZone("CEST", 2*3600)
	now := time.Date(2026, 10, 2, 9, 0, 0, 0, loc)
	cases := map[string]string{
		"7d":                        "2026-10-09T09:00:00+02:00",
		"48h":                       "2026-10-04T09:00:00+02:00",
		"2026-10-15":                "2026-10-15T23:59:59+02:00",
		"2026-10-15T08:00:00+02:00": "2026-10-15T08:00:00+02:00",
		"none":                      "",
		"aucun":                     "",
	}
	for in, want := range cases {
		got, err := ParseUntil(in, now)
		if err != nil || got != want {
			t.Errorf("ParseUntil(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"bientôt", "0d", "-3d", "15.10.2026"} {
		if _, err := ParseUntil(bad, now); err == nil {
			t.Errorf("ParseUntil(%q) accepted", bad)
		}
	}
}

func TestDefaultWait(t *testing.T) {
	f := newFixture(t)
	if got := f.a.DefaultWait(); got != "7d" {
		t.Fatalf("default %q", got)
	}
	f.a.S.Config.Lifecycle.DefaultWait = "3d"
	if got := f.a.DefaultWait(); got != "3d" {
		t.Fatalf("configured %q", got)
	}
}

func TestDeadlineWakesTheDossierAndAsksToChase(t *testing.T) {
	f := newFixture(t)
	f.a.Open(OpenParams{Title: "Armoire", SourceRef: "fake:thread/a"})
	f.a.Open(OpenParams{Title: "Pas encore", NoStart: true})
	f.a.Open(OpenParams{Title: "Sans échéance", NoStart: true})
	past, future := time.Now().Add(-time.Hour).Format(time.RFC3339), time.Now().Add(48*time.Hour).Format(time.RFC3339)
	for id, until := range map[string]string{"1": past, "2": future, "3": ""} {
		d, _ := f.a.Load(id)
		d.WaitUntil = until
		f.a.SetState(d, "wait", "", "Baer SA")
	}
	os.WriteFile(f.a.S.Meta("prompts", "deadline.md"), []byte("Relance {{id}} ? {{summary}}\n"), 0o644)

	rep := f.a.Deadlines(time.Now())
	if rep.Events != 1 || len(rep.Opened) != 1 || rep.Opened[0].ID != "D-0001" || len(rep.Errors) != 0 {
		t.Fatalf("deadlines %+v", rep)
	}
	d1, _ := f.a.Load("1")
	d2, _ := f.a.Load("2")
	d3, _ := f.a.Load("3")
	if d1.State != dossier.Open || d1.WaitUntil != "" || d2.State != dossier.Waiting || d3.State != dossier.Waiting {
		t.Fatalf("states %s %s %s", d1.State, d2.State, d3.State)
	}
	prompts := f.calls("session/prompt")
	if text := promptText(prompts[len(prompts)-1]); !strings.Contains(text, "Relance D-0001 ?") || !strings.Contains(text, "waiting_on: Baer SA") {
		t.Fatalf("chase prompt %q", text)
	}
	if got := strings.Join(f.transitions(), ","); got != "open→waiting,waiting→open" {
		t.Fatalf("transitions %s", got)
	}
	if again := f.a.Deadlines(time.Now()); again.Events != 0 {
		t.Fatal("a woken dossier fired twice")
	}
}

func TestRoutedSignalWakesAWaitingDossier(t *testing.T) {
	f := newFixture(t)
	f.a.Open(OpenParams{Title: "Armoire", NoStart: true})
	d, _ := f.a.Load("1")
	f.a.SetState(d, "wait", "", "Baer SA")
	if _, err := f.a.Open(OpenParams{Title: "Mémo", Instruction: "D-1: ils ont rappelé", NoStart: true}); err != nil {
		t.Fatal(err)
	}
	if d, _ := f.a.Load("1"); d.State != dossier.Open {
		t.Fatalf("routed signal left it %s", d.State)
	}
}
