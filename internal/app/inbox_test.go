package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aclemen1/office-cli/internal/connector"
)

func TestAnInboxEntryGoesToTheDeskThenToTheTrash(t *testing.T) {
	f := newFixture(t)
	deskWith(t, f, "idle")
	f.a.S.Config.Inbox.Settle = "50ms"
	f.a.S.Config.Inbox.File = []string{"mnemo", "remember", "{path}", "--sphere", "{sphere}", "--note", "{note}"}
	f.a.S.Config.Inbox.Check = []string{"artefact", "which", "{file}"}
	f.a.S.Config.Inbox.Holder = "mnemo"
	in := f.a.InboxDir()
	os.MkdirAll(filepath.Join(in, "Rio"), 0o755)
	os.WriteFile(filepath.Join(in, "Rio", "booking.jpeg"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(in, "Rio", ".DS_Store"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(in, "big.crdownload"), []byte("x"), 0o644)

	var ran []string
	held := false
	old := inboxRun
	t.Cleanup(func() { inboxRun = old })
	inboxRun = func(argv []string) ([]byte, error) {
		ran = append(ran, strings.Join(argv, " "))
		if argv[0] == "mnemo" {
			return []byte(`{"ok":true,"result":[{"id":"e1","status":"ingested","concepts":["c1","c2"]}]}`), nil
		}
		if held {
			return []byte(`{"ok":true,"result":{"found":true,"holders":[{"by":"mnemo"}]}}`), nil
		}
		return []byte(`{"ok":true,"result":{"found":false}}`), nil
	}

	if reps, _ := f.a.Ingest([]string{"inbox"}, connector.PollOptions{}); len(reps) != 0 {
		t.Fatalf("a settling entry went to the desk: %+v", reps)
	}
	time.Sleep(120 * time.Millisecond)
	f.a.Ingest([]string{"inbox"}, connector.PollOptions{})
	items, _ := f.a.Inbox()
	if len(items) != 1 || items[0].Name != "Rio" || items[0].State != "sent" || items[0].Files != 1 {
		t.Fatalf("items %+v", items)
	}
	if d := f.a.FindByThread("inbox:item/Rio"); d == nil || !IsDesk(d) {
		t.Fatal("the desk did not get the entry")
	}
	if _, err := f.a.InboxRelease("Rio", false, ""); err == nil || !strings.Contains(err.Error(), "not filed") {
		t.Fatalf("released before filing: %v", err)
	}
	if it, err := f.a.InboxFile("Rio", "voyage"); err != nil || it.State != "filed" || len(it.Concepts) != 2 {
		t.Fatalf("file %+v %v", it, err)
	}
	if !strings.Contains(strings.Join(ran, "\n"), "mnemo remember "+filepath.Join(in, "Rio")+" --sphere test --note voyage") {
		t.Fatalf("file command %q", ran)
	}
	f.a.Open(OpenParams{Title: "Voyage Rio", NoStart: true})
	if it, err := f.a.InboxAttach("Rio", mustGet(t, f, "1")); err != nil || it.State != "attached" {
		t.Fatalf("attach %+v %v", it, err)
	}
	if _, err := f.a.InboxRelease("Rio", false, ""); err == nil || !strings.Contains(err.Error(), "not kept elsewhere") {
		t.Fatalf("released though the check failed: %v", err)
	}
	held = true
	if _, err := f.a.InboxRelease("Rio", true, ""); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(in, "Rio")); err != nil {
		t.Fatal("a dry run moved the entry")
	}
	if detail, err := f.a.InboxRelease("Rio", false, ""); err != nil || !strings.Contains(detail, ".Trash") {
		t.Fatalf("release %q %v", detail, err)
	}
	if _, err := os.Stat(filepath.Join(in, "Rio")); !os.IsNotExist(err) {
		t.Fatal("the entry is still in the inbox")
	}
}

func TestAnInboxEntryCanGoWithoutADossierWhenTheUserSaysWhy(t *testing.T) {
	f := newFixture(t)
	deskWith(t, f, "idle")
	f.a.S.Config.Inbox.Settle = "1ms"
	f.a.S.Config.Inbox.File = []string{"mnemo", "remember", "{path}"}
	f.a.S.Config.Inbox.Check = []string{"artefact", "which", "{file}"}
	f.a.S.Config.Inbox.Holder = "mnemo"
	os.WriteFile(filepath.Join(f.a.InboxDir(), "topo.md"), []byte("x"), 0o644)
	old := inboxRun
	t.Cleanup(func() { inboxRun = old })
	inboxRun = func(argv []string) ([]byte, error) {
		if argv[0] == "mnemo" {
			return []byte(`{"ok":true,"result":[{"status":"ingested","concepts":["c1"]}]}`), nil
		}
		return []byte(`{"ok":true,"result":{"found":true,"holders":[{"by":"mnemo"}]}}`), nil
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := f.a.InboxRelease("topo.md", false, "inutile"); err == nil {
		t.Fatal("an unfiled entry left without a dossier")
	}
	f.a.InboxFile("topo.md", "")
	if _, err := f.a.InboxRelease("topo.md", false, ""); err == nil {
		t.Fatal("left without a dossier and without a reason")
	}
	if detail, err := f.a.InboxRelease("topo.md", false, "Alain : on peut le jeter"); err != nil || !strings.Contains(detail, "no dossier: Alain") {
		t.Fatalf("release %q %v", detail, err)
	}
}
