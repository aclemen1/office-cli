package app

import (
	"os"
	"strings"
	"testing"
)

func TestTheDeskIsNoDossier(t *testing.T) {
	f := newFixture(t)
	f.a.Open(OpenParams{Title: "Affaire", NoStart: true})
	d, err := f.a.LoadAny("desk")
	if err != nil || d.ID != "D-DESK" || !IsDesk(d) {
		t.Fatalf("desk %+v %v", d, err)
	}
	if err := f.a.Attach(d, false); err != nil {
		t.Fatal(err)
	}
	if len(f.calls("session/new")) != 1 || len(f.calls("session/prompt")) != 0 {
		t.Fatal("the desk should start without a prompt")
	}
	if _, err := os.Stat(d.Path("CLAUDE.md")); err != nil {
		t.Fatal("the desk has no charter")
	}
	if all, _ := f.a.All(); len(all) != 1 {
		t.Fatalf("the desk counts as a dossier: %d", len(all))
	}
	if _, err := f.a.Load("D-DESK"); err == nil || !strings.Contains(err.Error(), "desk") {
		t.Fatalf("a dossier action reached the desk: %v", err)
	}
	if _, err := f.a.SetState(f.a.Desk(), "wait", "", "x"); err == nil {
		t.Fatal("the desk took a state")
	}
	if err := f.a.SetAlias(mustGet(t, f, "1"), "desk"); err == nil {
		t.Fatal("alias DESK accepted")
	}
}

func TestIngestResumesTheDeskOutsideTheCap(t *testing.T) {
	f := newFixture(t)
	f.a.S.Config.Lifecycle.MaxSessions = 1
	if _, err := f.a.Open(OpenParams{Title: "Affaire"}); err != nil {
		t.Fatal(err)
	}
	if err := f.a.Attach(f.a.Desk(), true); err != nil {
		t.Fatal(err)
	}
	rep := f.a.Reconcile(map[string]bool{})
	if len(rep.Opened) != 2 || rep.Opened[1].ID != "D-DESK" || len(f.calls("session/load")) != 2 {
		t.Fatalf("the desk should resume once the cap is reached: %+v", rep)
	}
}

func TestANewDeskConversationKeepsTheOldOneSearchable(t *testing.T) {
	f := newFixture(t)
	d := f.a.Desk()
	if err := f.a.Attach(d, true); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"user","message":{"role":"user","content":"Ouvre un dossier pour Diego"}}` + "\n" +
		`{"type":"system","subtype":"compact_boundary","timestamp":"2026-10-03T08:00:00Z"}` + "\n"
	os.WriteFile(d.Path("transcript.jsonl"), []byte(line), 0o644)
	if c := ConversationOf(d); c.Compactions != 1 || c.Started.IsZero() {
		t.Fatalf("conversation %+v", c)
	}
	if err := f.a.NewDeskConversation(d); err != nil {
		t.Fatal(err)
	}
	if d.Run.Session != "" {
		t.Fatal("the session stayed")
	}
	if _, err := os.Stat(d.Path("transcripts", "0001.jsonl")); err != nil {
		t.Fatal("the conversation was not archived")
	}
	if hits, _ := f.a.Grep(d, "diego", 10); len(hits) != 1 {
		t.Fatalf("grep %+v", hits)
	}
}

func TestStarMarksADossierButNotTheDesk(t *testing.T) {
	f := newFixture(t)
	f.a.Open(OpenParams{Title: "Affaire", NoStart: true})
	d := mustGet(t, f, "1")
	if err := f.a.Star(d, true); err != nil {
		t.Fatal(err)
	}
	if d = mustGet(t, f, "1"); !d.Starred {
		t.Fatal("the star is not saved")
	}
	f.a.Star(d, false)
	if d = mustGet(t, f, "1"); d.Starred {
		t.Fatal("unstar kept the star")
	}
	if err := f.a.Star(f.a.Desk(), true); err == nil {
		t.Fatal("the desk took a star")
	}
}
