package app

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/aclemen1/office-cli/internal/dossier"
)

func TestSaveEditKeepsLinksAndRefusesAConflict(t *testing.T) {
	f := newFixture(t)
	f.a.Open(OpenParams{Title: "Séance", NoStart: true})
	f.a.Open(OpenParams{Title: "Point", NoStart: true})
	f.a.Link("1", "2", dossier.RelIncludes)
	d := mustGet(t, f, "1")
	before, _ := f.a.EditText(d)
	if strings.Contains(before, "dossier:links") {
		t.Fatalf("the links block is offered for editing:\n%s", before)
	}
	res, err := f.a.SaveEdit(d, before, before+"\n## À ne pas oublier\n\n- le budget\n")
	if err != nil || !res.Saved {
		t.Fatalf("save %+v %v", res, err)
	}
	b, _ := os.ReadFile(mustGet(t, f, "1").Path("dossier.md"))
	if !strings.Contains(string(b), "- le budget") || !strings.Contains(string(b), "dossier:links") {
		t.Fatalf("fiche after edit:\n%s", b)
	}
	stale := before
	res, err = f.a.SaveEdit(mustGet(t, f, "1"), stale, stale+"\nautre\n")
	if err != nil || res.Saved || res.Conflict == "" {
		t.Fatalf("a stale edit overwrote the fiche: %+v %v", res, err)
	}
	if b2, _ := os.ReadFile(mustGet(t, f, "1").Path("dossier.md")); strings.Contains(string(b2), "autre") {
		t.Fatal("the conflicting version reached the fiche")
	}
}

func TestTellEditQueuesTheChangeForTheAgent(t *testing.T) {
	f := newFixture(t)
	f.a.Open(OpenParams{Title: "Séance", NoStart: true})
	d := mustGet(t, f, "1")
	if err := f.a.TellEdit(d, "a\n", "a\nb\n"); err != nil || len(f.calls("session/prompt")) != 0 {
		t.Fatal("a dossier without session was prompted")
	}
	f.a.Prompt(d, "start")
	d = mustGet(t, f, "1")
	if err := f.a.TellEdit(d, "a\n", "a\n- le budget\n"); err != nil {
		t.Fatal(err)
	}
	ps := f.calls("session/prompt")
	last := ps[len(ps)-1]["params"].(map[string]any)
	if !strings.Contains(fmt.Sprint(last["prompt"]), "+ - le budget") || last["_meta"].(map[string]any)["delivery"] != "queue" {
		t.Fatalf("prompt %v", last)
	}
}
