package dossier

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newSaved(t *testing.T) *Dossier {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "0042-armoire")
	os.MkdirAll(dir, 0o755)
	d := Create(dir, "D-0042", "Armoire de pharmacie")
	d.Description = "Demander une date"
	d.AddSource(Source{ID: "gmail:task/abc", Resource: "https://mail.example/1"})
	d.AddThread("gmail:thread/t1")
	d.SetBody("\n## Instruction\n\nDemander une date\n")
	if err := d.Save(); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestRoundTripPreservesUnknownKeysAndBody(t *testing.T) {
	d := newSaved(t)
	p := d.Path("dossier.md")
	raw, _ := os.ReadFile(p)
	edited := strings.Replace(string(raw), "state: open\n", "state: open\ntags: [logement]\nma_note: \"à la main\"\n", 1) + "\n## Notes\n\nLa concierge a la clé.\n"
	os.WriteFile(p, []byte(edited), 0o644)

	d2, err := Load(d.Dir)
	if err != nil {
		t.Fatal(err)
	}
	d2.State, d2.WaitingOn = Waiting, "Baer SA"
	if err := d2.Save(); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(p)
	s := string(out)
	for _, want := range []string{"type: Dossier", "state: waiting", "waiting_on: Baer SA", "tags: [logement]", `ma_note: "à la main"`, "La concierge a la clé.", "## Instruction"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	d3, _ := Load(d.Dir)
	if d3.State != Waiting || d3.Sources[0].Name() != "gmail" || !d3.HasThread("gmail:thread/t1") {
		t.Fatalf("typed view lost: %+v", d3)
	}
}

func TestEmptyFieldsAreRemoved(t *testing.T) {
	d := newSaved(t)
	d.WaitingOn = "x"
	d.Save()
	d.WaitingOn = ""
	d.Save()
	raw, _ := os.ReadFile(d.Path("dossier.md"))
	if strings.Contains(string(raw), "waiting_on") {
		t.Fatalf("waiting_on should disappear:\n%s", raw)
	}
}

func TestStateFileAndLog(t *testing.T) {
	d := newSaved(t)
	d.Run.Session = "sess-1"
	d.Run.PendingTransitions = append(d.Run.PendingTransitions, Transition{Source: "gmail", To: Done, Error: "boom"})
	d.Save()
	d.Log("open → %s", Done)
	d2, _ := Load(d.Dir)
	if d2.Run.Session != "sess-1" || len(d2.Run.PendingTransitions) != 1 {
		t.Fatalf("state lost: %+v", d2.Run)
	}
	log, _ := os.ReadFile(d.Path("log.md"))
	if !strings.HasPrefix(string(log), "# D-0042 history") || !strings.Contains(string(log), "open → done") {
		t.Fatalf("log: %s", log)
	}
}

func TestLinksBlockIsManagedInPlace(t *testing.T) {
	d := newSaved(t)
	d.SetBody(d.body + "\n## Notes\n\nkeep me\n")
	d.SetLinksBlock("- [D-0051](../0051-x/dossier.md)\n")
	d.SetLinksBlock("- [D-0052](../0052-y/dossier.md)\n")
	if strings.Count(d.body, linksBegin) != 1 || strings.Contains(d.body, "D-0051") || !strings.Contains(d.body, "D-0052") {
		t.Fatalf("block not replaced in place:\n%s", d.body)
	}
	if !strings.Contains(d.body, "keep me") {
		t.Fatal("body outside the block was lost")
	}
	d.SetLinksBlock("")
	if strings.Contains(d.body, linksBegin) || !strings.Contains(d.body, "keep me") {
		t.Fatalf("block not removed cleanly:\n%s", d.body)
	}
}

func TestLinkHelpers(t *testing.T) {
	d := newSaved(t)
	d.Links = []Link{{RelIncludes, "D-0001"}, {RelDependsOn, "D-0002"}, {RelIncludes, "D-0003"}}
	if !d.HasLink(RelDependsOn, "D-0002") || d.HasLink(RelIncludes, "D-0002") {
		t.Fatal("HasLink")
	}
	if got := d.Targets(RelIncludes); len(got) != 2 || got[1] != "D-0003" {
		t.Fatalf("Targets %v", got)
	}
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Armoire de pharmacie, Chemin de Vassin 20": "armoire-de-pharmacie-chemin-de",
		"Tête-à-tête JMR":                           "tete-a-tete-jmr",
		"Œuvre — été 2026 !":                        "oeuvre-ete-2026",
		"???":                                       "dossier",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUnterminatedFrontmatterIsAnError(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "dossier.md"), []byte("---\ntype: Dossier\n"), 0o644)
	if _, err := Load(dir); err == nil {
		t.Fatal("expected an error")
	}
}

func TestSavingMachineStateKeepsTheTimestamp(t *testing.T) {
	d := Create(t.TempDir(), "D-0001", "Affaire")
	if err := d.Save(); err != nil {
		t.Fatal(err)
	}
	before := d.Updated
	time.Sleep(1100 * time.Millisecond)
	d.Run.PaneID, d.Run.Home = "w5:p1", "w61"
	if err := d.Save(); err != nil {
		t.Fatal(err)
	}
	if again, _ := Load(d.Dir); again.Updated != before || again.Run.PaneID != "w5:p1" {
		t.Fatalf("a dock saved: timestamp %s → %s, pane %q", before, again.Updated, again.Run.PaneID)
	}
	d.Title = "Autre affaire"
	d.Save()
	if again, _ := Load(d.Dir); again.Updated == before {
		t.Fatal("a real change should move the timestamp")
	}
}
