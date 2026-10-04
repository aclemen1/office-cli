package app

import (
	"os"
	"strings"
	"testing"

	"github.com/aclemen1/office-cli/internal/connector"
	"github.com/aclemen1/office-cli/internal/dossier"
)

func TestMergeMovesEverythingAndReopensTheTarget(t *testing.T) {
	f := newFixture(t)
	f.a.Open(OpenParams{Title: "Armoire", SourceRef: "fake:task/1", ThreadRef: "fake:thread/a", NoStart: true})
	f.a.Open(OpenParams{Title: "Mémo armoire", SourceRef: "fake:memo/9", NoStart: true,
		Files: []connector.File{{Name: "memo.md", Content: "absent le 12"}}})
	f.a.Open(OpenParams{Title: "Gérance", NoStart: true})
	f.a.Link("2", "3", dossier.RelDependsOn)
	memo, _ := f.a.Load("2")
	os.MkdirAll(memo.Path("files"), 0o755)
	os.WriteFile(memo.Path("files", "brouillon.md"), []byte("brouillon"), 0o644)
	target, _ := f.a.Load("1")
	f.a.SetState(target, "close", "", "")

	r, err := f.a.Merge("2", "1")
	if err != nil {
		t.Fatal(err)
	}
	if r.Into != "D-0001" || len(r.Files) != 2 || len(r.Sources) != 1 {
		t.Fatalf("merge %+v", r)
	}
	into, _ := f.a.Load("1")
	from, _ := f.a.Load("2")
	if into.State != dossier.Open || !into.HasSource("fake:memo/9") || !into.HasLink(dossier.RelDependsOn, "D-0003") {
		t.Fatalf("target %+v", into)
	}
	if from.State != dossier.Merged || from.MergedInto != "D-0001" || len(from.Sources) != 0 {
		t.Fatalf("source dossier %+v", from)
	}
	if _, err := os.Stat(into.Path("files", "D-0002-brouillon.md")); err != nil {
		t.Fatal("files not merged")
	}
	if got := f.a.FindBySource("fake:memo/9"); got == nil || got.ID != "D-0001" {
		t.Fatal("merged source does not resolve to the target")
	}
	if got := strings.Join(f.transitions(), ","); got != "open→done,done→open" {
		t.Fatalf("transitions %s", got)
	}
	if _, err := f.a.Merge("2", "1"); err == nil || !strings.Contains(err.Error(), "already merged") {
		t.Fatalf("second merge: %v", err)
	}
	if _, err := f.a.Merge("1", "1"); err == nil {
		t.Fatal("self merge accepted")
	}
	if m := f.a.MergedInto(into); len(m) != 1 || m[0].ID != "D-0002" {
		t.Fatalf("MergedInto %v", m)
	}
}

func TestGrepReadsMessageTextAcrossTranscripts(t *testing.T) {
	f := newFixture(t)
	f.a.Open(OpenParams{Title: "X", NoStart: true})
	d, _ := f.a.Load("1")
	lines := []string{
		`{"type":"user","message":{"role":"user","content":"Demande une date de passage à Baer SA"}}`,
		`{"type":"system","subtype":"compact_boundary"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Brouillon prêt pour Baer."}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"baer.md written"}]}}`,
		`not json`,
	}
	os.WriteFile(d.Path("transcript.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	hits, err := f.a.Grep(d, "baer", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 3 || hits[0].Role != "user" || hits[1].Role != "assistant" || hits[0].Source != "archived" {
		t.Fatalf("hits %+v", hits)
	}
	if hits, _ := f.a.Grep(d, "baer", 1); len(hits) != 1 {
		t.Fatal("limit ignored")
	}
	if _, err := f.a.Grep(d, "(", 10); err == nil {
		t.Fatal("bad regexp accepted")
	}
}

func TestMergeRedirectsIncomingLinks(t *testing.T) {
	f := newFixture(t)
	for _, title := range []string{"Séance", "Relais SMTP", "Relais SMTP dans GCP"} {
		f.a.Open(OpenParams{Title: title, NoStart: true})
	}
	f.a.Link("1", "2", dossier.RelIncludes)
	f.a.Link("1", "3", dossier.RelIncludes)
	if _, err := f.a.Merge("3", "2"); err != nil {
		t.Fatal(err)
	}
	meeting, _ := f.a.Load("1")
	if len(meeting.Links) != 1 || meeting.Links[0].To != "D-0002" {
		t.Fatalf("links after merge %v", meeting.Links)
	}
}
