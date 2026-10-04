package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aclemen1/office-cli/internal/connector"
	"github.com/aclemen1/office-cli/internal/dossier"
	"github.com/aclemen1/office-cli/internal/office"
	"github.com/aclemen1/office-cli/internal/testutil"
)

func TestMain(m *testing.M) {
	testutil.Dispatch()
	panes = func() map[string]string { return map[string]string{} }
	renameTab = func(string, string) {}
	os.Exit(m.Run())
}

type fixture struct {
	a       *App
	acpLog  string
	srcLog  string
	setMode func(string)
}

// newFixture builds an office whose ACP server and "fake" source are the fakes.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DOSSIER_ID", "")
	s, err := office.Init(filepath.Join(t.TempDir(), "office"), "test", false)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{a: &App{S: s}, acpLog: filepath.Join(t.TempDir(), "acp.jsonl"), srcLog: filepath.Join(t.TempDir(), "src.jsonl")}
	s.Config.ACP.Command = []string{os.Args[0]}
	s.Config.ACP.Env = map[string]string{testutil.EnvACP: f.acpLog}
	s.Config.Agent.RemoteControl = false
	s.Config.Lifecycle.CloseTabOn = []string{dossier.Done}
	src := office.SourceConfig{Name: "fake", Command: []string{os.Args[0]},
		Env: map[string]string{testutil.EnvConnector: "ok", testutil.EnvLog: f.srcLog}}
	s.Config.Sources = []office.SourceConfig{src}
	f.setMode = func(mode string) { s.Config.Sources[0].Env[testutil.EnvConnector] = mode }
	return f
}

func (f *fixture) calls(method string) []map[string]any {
	var out []map[string]any
	for _, c := range testutil.Calls(f.acpLog) {
		if c["method"] == method {
			out = append(out, c)
		}
	}
	return out
}

func (f *fixture) transitions() []string {
	var out []string
	for _, c := range testutil.Calls(f.srcLog) {
		if c["verb"] == "transition" {
			in := c["input"].(map[string]any)
			out = append(out, in["from"].(string)+"→"+in["to"].(string))
		}
	}
	return out
}

func promptText(c map[string]any) string {
	return c["params"].(map[string]any)["prompt"].([]any)[0].(map[string]any)["text"].(string)
}

func TestOpenCreatesTheDossierAndStartsItsSession(t *testing.T) {
	f := newFixture(t)
	r, err := f.a.Open(OpenParams{Title: "Armoire de pharmacie", Instruction: "Demander une date de passage"})
	if err != nil {
		t.Fatal(err)
	}
	if r.ID != "D-0001" || r.Outcome != "created" || r.Session != "sess-1" || r.TabID != "fake:t1" || !r.Started {
		t.Fatalf("open %+v", r)
	}
	d, _ := f.a.Load("1")
	if d.State != dossier.Open || d.Sources[0].ID != "manual:D-0001" || d.Description != "Demander une date de passage" {
		t.Fatalf("dossier %+v", d)
	}
	newCall := f.calls("session/new")[0]["params"].(map[string]any)
	if newCall["cwd"] != d.Dir {
		t.Fatalf("session cwd %v, want %s", newCall["cwd"], d.Dir)
	}
	text := promptText(f.calls("session/prompt")[0])
	if !strings.Contains(text, "D-0001") || !strings.Contains(text, "Demander une date de passage") {
		t.Fatalf("prompt %q", text)
	}
	if _, err := os.Stat(d.Path("prompts", "0001-open.md")); err != nil {
		t.Fatal("prompt not kept")
	}
	proc := f.calls("process")[0]["args"].([]any)
	joined := strings.Join(toStrings(proc), " ")
	if !strings.Contains(joined, "-- --name D-0001 --settings") {
		t.Fatalf("agent args %q", joined)
	}
}

func toStrings(v []any) []string {
	var out []string
	for _, x := range v {
		out = append(out, x.(string))
	}
	return out
}

func TestOpenWithoutInstructionUsesTheDefault(t *testing.T) {
	f := newFixture(t)
	f.a.Open(OpenParams{Title: "Sans consigne", NoStart: true})
	d, _ := f.a.Load("1")
	if d.Description != f.a.S.Config.Prompt.DefaultInstruction {
		t.Fatalf("description %q", d.Description)
	}
	if len(f.calls("session/new")) != 0 {
		t.Fatal("--no-start started a session")
	}
}

func TestOpenNeedsATitle(t *testing.T) {
	f := newFixture(t)
	if _, err := f.a.Open(OpenParams{Title: "  "}); err == nil || !strings.Contains(err.Error(), "--title") {
		t.Fatalf("got %v", err)
	}
}

func TestOpenIsIdempotentOnSourceAndReopensDone(t *testing.T) {
	f := newFixture(t)
	p := OpenParams{Title: "Armoire", SourceRef: "fake:task/1", ThreadRef: "fake:thread/t", NoStart: true}
	f.a.Open(p)
	again, _ := f.a.Open(p)
	if again.Outcome != "existing" || again.ID != "D-0001" {
		t.Fatalf("second open %+v", again)
	}
	d, _ := f.a.Load("1")
	if _, err := f.a.SetState(d, "close", "réglé", ""); err != nil {
		t.Fatal(err)
	}
	p.Instruction = "nouvelle relance"
	r, err := f.a.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	d, _ = f.a.Load("1")
	if r.Outcome != "reopened" || d.State != dossier.Open {
		t.Fatalf("reopen %+v state %s", r, d.State)
	}
	dirs, _ := f.a.S.Dirs()
	if len(dirs) != 1 {
		t.Fatalf("%d dossiers, want 1", len(dirs))
	}
	if got := strings.Join(f.transitions(), ","); got != "open→done,done→open" {
		t.Fatalf("transitions %s", got)
	}
}

func TestStateMoves(t *testing.T) {
	f := newFixture(t)
	f.a.Open(OpenParams{Title: "X", NoStart: true})
	d, _ := f.a.Load("1")
	steps := []struct{ move, want string }{{"wait", dossier.Waiting}, {"resume", dossier.Open}, {"close", dossier.Done}, {"reopen", dossier.Open}}
	for _, s := range steps {
		if _, err := f.a.SetState(d, s.move, "", "Baer SA"); err != nil {
			t.Fatalf("%s: %v", s.move, err)
		}
		if d.State != s.want {
			t.Fatalf("%s: state %s", s.move, d.State)
		}
		if s.move == "wait" && d.WaitingOn != "Baer SA" || s.move != "wait" && d.WaitingOn != "" {
			t.Fatalf("%s: waiting_on %q", s.move, d.WaitingOn)
		}
	}
	_, err := f.a.SetState(d, "resume", "", "")
	if err == nil || !strings.Contains(err.Error(), "office wait") {
		t.Fatalf("invalid move: %v", err)
	}
	log, _ := os.ReadFile(d.Path("log.md"))
	if strings.Count(string(log), "→") != 4 {
		t.Fatalf("log:\n%s", log)
	}
}

func TestCloseClosesTheTabWhenTheLifecycleSaysSo(t *testing.T) {
	f := newFixture(t)
	f.a.Open(OpenParams{Title: "X"})
	d, _ := f.a.Load("1")
	panes = func() map[string]string { return map[string]string{"fake:p1": "idle"} }
	defer func() { panes = func() map[string]string { return map[string]string{} } }()
	if _, err := f.a.SetState(d, "wait", "", "x"); err != nil {
		t.Fatal(err)
	}
	if len(f.calls("session/close")) != 0 {
		t.Fatal("waiting is not in close_tab_on, the tab should stay")
	}
	if _, err := f.a.SetState(d, "close", "", ""); err != nil {
		t.Fatal(err)
	}
	if len(f.calls("session/close")) != 1 {
		t.Fatal("done is in close_tab_on, the tab should close")
	}
	d, _ = f.a.Load("1")
	if d.Run.TabID != "" || d.Run.Session != "sess-1" {
		t.Fatalf("after close %+v", d.Run)
	}
}

func TestFailedTransitionsArePendingAndRetried(t *testing.T) {
	f := newFixture(t)
	f.a.Open(OpenParams{Title: "X", SourceRef: "fake:task/1", NoStart: true})
	d, _ := f.a.Load("1")
	f.setMode("fail-transition")
	pending, err := f.a.SetState(d, "wait", "", "Baer SA")
	if err != nil || pending != 1 || d.State != dossier.Waiting {
		t.Fatalf("pending %d err %v state %s", pending, err, d.State)
	}
	if !strings.Contains(d.Run.PendingTransitions[0].Error, "gmail unavailable") {
		t.Fatalf("pending %+v", d.Run.PendingTransitions)
	}
	if left := f.a.Retry(d); left != 1 {
		t.Fatalf("retry while failing left %d", left)
	}
	f.setMode("ok")
	if left := f.a.Retry(d); left != 0 {
		t.Fatalf("retry left %d", left)
	}
	d, _ = f.a.Load("1")
	if len(d.Run.PendingTransitions) != 0 {
		t.Fatal("pending not cleared on disk")
	}
}

func TestMissingSourceConfigIsPendingNotLost(t *testing.T) {
	f := newFixture(t)
	f.a.Open(OpenParams{Title: "X", SourceRef: "gmail:task/9", NoStart: true})
	d, _ := f.a.Load("1")
	pending, _ := f.a.SetState(d, "close", "", "")
	if pending != 1 || !strings.Contains(d.Run.PendingTransitions[0].Error, "no [[source]] named gmail") {
		t.Fatalf("pending %d %+v", pending, d.Run.PendingTransitions)
	}
}

func TestRoutingByAddress(t *testing.T) {
	f := newFixture(t)
	f.a.Open(OpenParams{Title: "Armoire"})
	r, err := f.a.Open(OpenParams{Title: "Mémo", SourceRef: "fake:memo/7", Instruction: "D-1: dis-leur que je suis absent le 12"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Outcome != "routed" || r.ID != "D-0001" {
		t.Fatalf("route %+v", r)
	}
	dirs, _ := f.a.S.Dirs()
	if len(dirs) != 1 {
		t.Fatal("routing created a dossier")
	}
	prompts := f.calls("session/prompt")
	if text := promptText(prompts[len(prompts)-1]); !strings.Contains(text, "dis-leur que je suis absent le 12") || strings.Contains(text, "D-1:") {
		t.Fatalf("event prompt %q", text)
	}
	d, _ := f.a.Load("1")
	if !d.HasSource("fake:memo/7") {
		t.Fatal("routed signal source not attached")
	}
	if f.a.FindBySource("fake:memo/7").ID != "D-0001" {
		t.Fatal("routed source does not resolve back")
	}
}

func TestContextFilesAreNumberedAndCompanionsFollow(t *testing.T) {
	f := newFixture(t)
	files := []connector.File{
		{Name: "thread.md", Content: "---\ntype: Email Thread\n---\n"},
		{Name: "devis.pdf", Content: "%PDF"},
		{Name: "devis.pdf.md", Content: "---\ntype: Attachment\nresource: ./devis.pdf\n---\n"},
	}
	f.a.Open(OpenParams{Title: "X", Files: files, NoStart: true})
	d, _ := f.a.Load("1")
	f.a.event(d, "", nil, []connector.File{{Name: "reply.md", Content: "oui"}}, true)
	comp, err := os.ReadFile(d.Path("context", "0001-devis.pdf.md"))
	if err != nil || !strings.Contains(string(comp), "resource: ./0001-devis.pdf") {
		t.Fatalf("companion %q %v", comp, err)
	}
	for _, name := range []string{"0001-thread.md", "0001-devis.pdf", "0002-reply.md"} {
		if _, err := os.Stat(d.Path("context", name)); err != nil {
			t.Errorf("missing context/%s", name)
		}
	}
	text, _ := os.ReadFile(d.Path("prompts", "0001-open.md"))
	if !strings.Contains(string(text), "context/0001-thread.md") {
		t.Fatalf("open prompt does not list files:\n%s", text)
	}
}

func TestIngestOpensSignalsThenDeliversEvents(t *testing.T) {
	f := newFixture(t)
	reps, err := f.a.Ingest(nil, connector.PollOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(reps) != 1 || len(reps[0].Opened) != 1 || reps[0].Opened[0].Outcome != "created" || len(reps[0].Errors) != 0 {
		t.Fatalf("first ingest %+v", reps)
	}
	if f.a.cursors()["fake"] != "cursor-2" {
		t.Fatalf("cursor %v", f.a.cursors())
	}
	d, _ := f.a.Load("1")
	f.a.SetState(d, "wait", "", "Baer SA")

	reps, _ = f.a.Ingest([]string{"fake"}, connector.PollOptions{})
	rep := reps[0]
	if len(rep.Skipped) != 1 || !strings.Contains(rep.Skipped[0], "already in D-0001") {
		t.Fatalf("second ingest skipped %+v", rep)
	}
	if len(rep.Opened) != 1 || rep.Opened[0].Outcome != "event" {
		t.Fatalf("event not delivered %+v", rep)
	}
	d, _ = f.a.Load("1")
	if d.State != dossier.Open {
		t.Fatalf("event did not resume the waiting dossier: %s", d.State)
	}
	if _, err := os.Stat(d.Path("context", "0002-reply.md")); err != nil {
		t.Fatal("event file not stored")
	}
	if got := strings.Join(f.transitions(), ","); got != "open→waiting,waiting→open" {
		t.Fatalf("transitions %s", got)
	}
}

func TestIngestDryRunChangesNothing(t *testing.T) {
	f := newFixture(t)
	reps, _ := f.a.Ingest(nil, connector.PollOptions{DryRun: true})
	dirs, _ := f.a.S.Dirs()
	if len(dirs) != 0 || len(reps[0].Skipped) != 1 || f.a.cursors()["fake"] != "" {
		t.Fatalf("dry run wrote something: %+v %d", reps, len(dirs))
	}
	for _, c := range testutil.Calls(f.srcLog) {
		if c["verb"] == "poll" && c["input"].(map[string]any)["dry_run"] != true {
			t.Fatalf("dry_run not passed to the connector: %v", c["input"])
		}
	}
}

func TestIngestKeepsTheCursorWhenTheSourceFails(t *testing.T) {
	f := newFixture(t)
	f.setMode("error")
	reps, _ := f.a.Ingest(nil, connector.PollOptions{})
	if len(reps[0].Errors) != 1 || f.a.cursors()["fake"] != "" {
		t.Fatalf("%+v", reps)
	}
	if _, err := f.a.Ingest([]string{"nope"}, connector.PollOptions{}); err == nil || !strings.Contains(err.Error(), "Declared: fake") {
		t.Fatalf("unknown source: %v", err)
	}
}

func TestGraph(t *testing.T) {
	f := newFixture(t)
	for _, title := range []string{"Séance JMR", "Armoire", "Budget 2027", "Accord gérance"} {
		f.a.Open(OpenParams{Title: title, NoStart: true})
	}
	mustLink := func(from, to, rel string) {
		t.Helper()
		if _, err := f.a.Link(from, to, rel); err != nil {
			t.Fatal(err)
		}
	}
	mustLink("1", "2", dossier.RelIncludes)
	mustLink("1", "3", dossier.RelIncludes)
	mustLink("2", "4", dossier.RelDependsOn)
	mustLink("2", "4", dossier.RelDependsOn) // idempotent

	if _, err := f.a.Link("4", "2", dossier.RelDependsOn); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("depends_on cycle: %v", err)
	}
	if _, err := f.a.Link("2", "1", dossier.RelIncludes); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("includes cycle: %v", err)
	}
	if _, err := f.a.Link("1", "1", dossier.RelIncludes); err == nil {
		t.Fatal("self link accepted")
	}
	if _, err := f.a.Link("1", "2", "related"); err == nil || !strings.Contains(err.Error(), "includes") {
		t.Fatalf("unknown rel: %v", err)
	}

	d1, _ := f.a.Load("1")
	tree := f.a.Tree(d1, "all", 2)
	if len(tree.Children) != 2 || tree.Children[0].ID != "D-0002" || tree.Children[0].BlockedBy[0] != "D-0004" ||
		tree.Children[0].Children[0].Rel != dossier.RelDependsOn {
		t.Fatalf("tree %+v", tree)
	}
	if flat := f.a.Tree(d1, dossier.RelIncludes, 1); len(flat.Children[0].Children) != 0 {
		t.Fatal("depth 1 walked too far")
	}
	d2, _ := f.a.Load("2")
	if in := f.a.Incoming(d2); len(in) != 1 || in[0].ID != "D-0001" || in[0].Rel != dossier.RelIncludes {
		t.Fatalf("incoming %+v", in)
	}
	body, _ := os.ReadFile(d1.Path("dossier.md"))
	if !strings.Contains(string(body), "(../0002-armoire/dossier.md)") {
		t.Fatalf("managed links block:\n%s", body)
	}

	d4, _ := f.a.Load("4")
	f.a.SetState(d4, "close", "", "")
	d2, _ = f.a.Load("2")
	log, _ := os.ReadFile(d2.Path("log.md"))
	if !strings.Contains(string(log), "dependency D-0004 (Accord gérance) is done") {
		t.Fatalf("dependent not told:\n%s", log)
	}
	if len(f.calls("session/new")) != 0 {
		t.Fatal("a light dependent must not get a session")
	}
	rows, _ := f.a.List("all", "")
	for _, r := range rows {
		if r.ID == "D-0002" && len(r.BlockedBy) != 0 {
			t.Fatalf("D-0002 still blocked: %+v", r)
		}
	}
	if _, err := f.a.Unlink("1", "3", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.Unlink("1", "3", ""); err == nil || !strings.Contains(err.Error(), "includes D-0002") {
		t.Fatalf("second unlink: %v", err)
	}
}

func TestDependentWithASessionIsPrompted(t *testing.T) {
	f := newFixture(t)
	f.a.Open(OpenParams{Title: "Armoire"})
	f.a.Open(OpenParams{Title: "Accord", NoStart: true})
	f.a.Link("1", "2", dossier.RelDependsOn)
	d2, _ := f.a.Load("2")
	f.a.SetState(d2, "close", "", "")
	prompts := f.calls("session/prompt")
	if text := promptText(prompts[len(prompts)-1]); !strings.Contains(text, "D-0002 (Accord)") {
		t.Fatalf("dependent prompt %q", text)
	}
}

func TestSearchFindsEveryStateAndNeedsAllWords(t *testing.T) {
	f := newFixture(t)
	f.a.Open(OpenParams{Title: "Armoire de pharmacie Vassin", NoStart: true})
	f.a.Open(OpenParams{Title: "Chauffage Vassin", NoStart: true})
	d, _ := f.a.Load("1")
	f.a.SetState(d, "close", "", "")
	hits, _ := f.a.Search("vassin armoire", []string{"all"}, false)
	if len(hits) != 1 || hits[0].ID != "D-0001" || hits[0].State != dossier.Done {
		t.Fatalf("hits %+v", hits)
	}
	if hits, _ := f.a.Search("vassin", []string{"open"}, false); len(hits) != 1 || hits[0].ID != "D-0002" {
		t.Fatalf("state filter %+v", hits)
	}
	if _, err := f.a.Search("  ", nil, false); err == nil {
		t.Fatal("empty query accepted")
	}
}

func TestLoadDirFindsTheDossierThroughSymlinks(t *testing.T) {
	f := newFixture(t)
	f.a.Open(OpenParams{Title: "X", NoStart: true})
	d, _ := f.a.Load("1")
	link := filepath.Join(t.TempDir(), "alias")
	os.Symlink(d.Dir, link)
	got, err := f.a.LoadDir(filepath.Join(link))
	if err != nil || got.ID != "D-0001" {
		t.Fatalf("LoadDir through symlink: %v %v", got, err)
	}
	if _, err := f.a.LoadDir(t.TempDir()); err == nil {
		t.Fatal("a directory outside the office resolved")
	}
}

func TestDossierIDFromEnvironment(t *testing.T) {
	f := newFixture(t)
	f.a.Open(OpenParams{Title: "X", NoStart: true})
	if _, err := f.a.Load(""); err == nil || !strings.Contains(err.Error(), "DOSSIER_ID") {
		t.Fatalf("got %v", err)
	}
	t.Setenv("DOSSIER_ID", "D-0001")
	if d, err := f.a.Load(""); err != nil || d.ID != "D-0001" {
		t.Fatalf("got %v %v", d, err)
	}
}

func TestShowReturnsTheBody(t *testing.T) {
	f := newFixture(t)
	f.a.Open(OpenParams{Title: "Séance", Instruction: "Tenir l'ordre du jour", NoStart: true})
	d, _ := f.a.Load("1")
	if body := f.a.Show(d).Body; !strings.Contains(body, "Tenir l'ordre du jour") {
		t.Fatalf("body %q", body)
	}
}
