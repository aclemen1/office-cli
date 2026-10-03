package app

import (
	"os"
	"strings"
	"testing"

	"github.com/aclemen1/dossier-cli/internal/dossier"
)

func TestDeleteWithdrawsTheSignalAndDropsTheLinks(t *testing.T) {
	f := newFixture(t)
	f.a.Open(OpenParams{Title: "Essai", SourceRef: "fake:thread/t", NoStart: true})
	f.a.Open(OpenParams{Title: "Séance", NoStart: true})
	f.a.Link("2", "1", dossier.RelIncludes)
	d, _ := f.a.Load("1")
	dir := d.Dir
	f.setMode("fail-transition")
	if _, err := f.a.Delete(d, ""); err == nil || !strings.Contains(err.Error(), "nothing deleted") {
		t.Fatalf("a failed withdrawal should keep the dossier: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("the dossier is gone although its signal stays")
	}
	f.setMode("ok")
	d, _ = f.a.Load("1")
	res, err := f.a.Delete(d, "essai")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("the directory is still there")
	}
	if got := strings.Join(f.transitions(), ","); !strings.HasSuffix(got, "open→done") {
		t.Fatalf("transitions %s", got)
	}
	if s, _ := f.a.Load("2"); s.HasLink(dossier.RelIncludes, "D-0001") || len(res.Unlinked) != 1 {
		t.Fatalf("link kept: %v, unlinked %v", s.Links, res.Unlinked)
	}
	if _, err := f.a.Load("1"); err == nil {
		t.Fatal("the deleted dossier still loads")
	}
}

func mustGet(t *testing.T, f *fixture, id string) *dossier.Dossier {
	t.Helper()
	d, err := f.a.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
