package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aclemen1/dossier-cli/internal/connector"
	"github.com/aclemen1/dossier-cli/internal/dossier"
	"github.com/aclemen1/dossier-cli/internal/store"
)

func TestMoveTakesDossiersToAnotherStoreWithTheirLinksAmongThem(t *testing.T) {
	f := newFixture(t)
	pro, err := store.Init(filepath.Join(filepath.Dir(f.a.S.Root), "pro"), "pro", false)
	if err != nil {
		t.Fatal(err)
	}
	pro.Config.ACP, pro.Config.Agent, pro.Config.Sources = f.a.S.Config.ACP, f.a.S.Config.Agent, f.a.S.Config.Sources
	old, oldPanes := herdrCall, panes
	t.Cleanup(func() { herdrCall, panes = old, oldPanes })
	panes = func() map[string]string { return map[string]string{"fake:p1": "idle"} }
	herdrCall = func(args ...string) ([]byte, error) { return []byte(`{}`), nil }

	f.a.Open(OpenParams{Title: "K3S errata", SourceRef: "fake:item/1"})
	f.a.Open(OpenParams{Title: "K3S fenêtre", NoStart: true})
	f.a.Open(OpenParams{Title: "Séance", NoStart: true})
	f.a.Link("1", "2", dossier.RelIncludes)
	f.a.Link("3", "1", dossier.RelIncludes)
	oldDir := mustGet(t, f, "1").Dir
	sess := mustGet(t, f, "1").Run.Session
	os.MkdirAll(projectDir(oldDir), 0o755)
	os.WriteFile(filepath.Join(projectDir(oldDir), sess+".jsonl"), []byte("{}\n"), 0o644)

	res, err := f.a.Move([]string{"D-0001", "2"}, pro)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Moved) != 2 || res.Moved[0].From != "D-0001" || !res.Moved[0].Restarted {
		t.Fatalf("moved %+v", res)
	}
	to := &App{S: pro}
	a, err := to.Load(res.Moved[0].To)
	if err != nil {
		t.Fatal(err)
	}
	if !a.HasLink(dossier.RelIncludes, res.Moved[1].To) {
		t.Fatalf("the link between moved dossiers should follow: %v", a.Links)
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatal("the old directory is still there")
	}
	if _, err := os.Stat(filepath.Join(projectDir(a.Dir), sess+".jsonl")); err != nil {
		t.Fatal("the conversation did not follow: --resume would not find it")
	}
	if b, _ := os.ReadFile(a.Path("log.md")); !strings.Contains(string(b), "moved from D-0001") || !strings.Contains(string(b), "claimed: tagged") {
		t.Fatalf("history %s", b)
	}
	if s := mustGet(t, f, "3"); len(s.Links) != 0 || len(res.Unlinked) != 1 {
		t.Fatalf("the dossier that stays keeps a dangling link: %v", s.Links)
	}
	load := f.calls("session/load")
	if len(load) == 0 || load[len(load)-1]["params"].(map[string]any)["cwd"] != a.Dir {
		t.Fatalf("the session should restart in the new directory: %v", load)
	}
	if _, err := f.a.Move([]string{"D-0003"}, f.a.S); err == nil {
		t.Fatal("a move to the same store was accepted")
	}
}

func TestProjectDirFollowsClaudeCodesNaming(t *testing.T) {
	t.Setenv("HOME", "/Users/x")
	if got := filepath.Base(projectDir("/Users/x/dossiers/pro/0035-home-nfs")); got != "-Users-x-dossiers-pro-0035-home-nfs" {
		t.Fatalf("project dir %s", got)
	}
}

func TestIngestLeavesTheStoreFreeWhilePolling(t *testing.T) {
	f := newFixture(t)
	f.setMode("slow")
	old := store.LockWait
	store.LockWait = time.Second
	t.Cleanup(func() { store.LockWait = old })
	done := make(chan struct{})
	go func() { f.a.Ingest(nil, connector.PollOptions{Now: true}); close(done) }()
	time.Sleep(500 * time.Millisecond)
	other, _ := store.Open(f.a.S.Root)
	if err := other.Lock(); err != nil {
		t.Fatalf("a slow source keeps the store locked: %v", err)
	}
	other.Unlock()
	<-done
}
