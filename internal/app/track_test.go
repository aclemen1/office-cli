package app

import (
	"os"
	"strings"
	"testing"
)

func TestTrackAttachesAThreadThatTransitionsFollow(t *testing.T) {
	f := newFixture(t)
	f.a.Open(OpenParams{Title: "PostgreSQL", SourceRef: "fake:thread/a", ThreadRef: "fake:thread/a", NoStart: true})
	f.a.Open(OpenParams{Title: "Autre", SourceRef: "fake:thread/b", NoStart: true})
	if _, err := f.a.Track("1", "fake:thread/draft"); err != nil {
		t.Fatal(err)
	}
	d, _ := f.a.Load("1")
	if !d.HasSource("fake:thread/draft") || !d.HasThread("fake:thread/draft") {
		t.Fatalf("not tracked %+v", d)
	}
	f.a.SetState(d, "wait", "", "Camptocamp")
	if got := strings.Join(f.transitions(), ","); got != "open→waiting,open→waiting" {
		t.Fatalf("both threads should get the waiting transition: %s", got)
	}
	if _, err := f.a.Track("1", "fake:thread/b"); err == nil || !strings.Contains(err.Error(), "already belongs to D-0002") {
		t.Fatalf("thread of another dossier: %v", err)
	}
	if _, err := f.a.Track("1", "gmail:thread/x"); err == nil || !strings.Contains(err.Error(), "no [[source]] named") {
		t.Fatalf("unknown source: %v", err)
	}
	if _, err := f.a.Track("1", "nonsense"); err == nil {
		t.Fatal("bad reference accepted")
	}
}

func TestRestartClosesAndResumesTheSameSession(t *testing.T) {
	f := newFixture(t)
	f.a.Open(OpenParams{Title: "X"})
	d, _ := f.a.Load("1")
	panes = func() map[string]string { return map[string]string{"fake:p1": "idle"} }
	defer func() { panes = func() map[string]string { return map[string]string{} } }()
	if err := f.a.Restart(d); err != nil {
		t.Fatal(err)
	}
	if len(f.calls("session/close")) != 1 {
		t.Fatal("restart did not close the session")
	}
	loads := f.calls("session/load")
	if len(loads) < 2 || loads[len(loads)-1]["params"].(map[string]any)["sessionId"] != "sess-1" {
		t.Fatalf("restart did not resume the same session: %v", loads)
	}
	if d.Run.TabID != "fake:t1" {
		t.Fatalf("tab %q", d.Run.TabID)
	}
	light, _ := f.a.Open(OpenParams{Title: "Léger", NoStart: true})
	ld, _ := f.a.Load(light.ID)
	if err := f.a.Restart(ld); err == nil || !strings.Contains(err.Error(), "office attach") {
		t.Fatalf("restart without session: %v", err)
	}
}

func TestReconcileResumesOpenDossiersUpToTheCap(t *testing.T) {
	f := newFixture(t)
	f.a.Open(OpenParams{Title: "A"})
	f.a.Open(OpenParams{Title: "B"})
	f.a.Open(OpenParams{Title: "C"})
	f.a.Open(OpenParams{Title: "Sans session", NoStart: true})
	c, _ := f.a.Load("3")
	f.a.SetState(c, "wait", "", "Livit")
	f.a.S.Config.Lifecycle.MaxSessions = 1
	before := len(f.calls("session/load"))
	rep := f.a.Reconcile(nil)
	if len(rep.Opened) != 1 || rep.Opened[0].Outcome != "resumed" || len(rep.Skipped) != 1 || !strings.Contains(rep.Skipped[0], "max_sessions") {
		t.Fatalf("cap 1: %+v", rep)
	}
	if got := len(f.calls("session/load")) - before; got != 1 {
		t.Fatalf("%d session/load, want 1", got)
	}
	f.a.S.Config.Lifecycle.MaxSessions = 0
	if rep := f.a.Reconcile(nil); len(rep.Opened) != 2 {
		t.Fatalf("default cap: waiting and session-less dossiers stay as they are, got %+v", rep)
	}
	if s := f.a.Sessions(); s.Cap != 20 || len(s.Stopped) != 2 {
		t.Fatalf("sessions %+v", s)
	}
}

func TestDisabledPluginsReachTheAgentSettings(t *testing.T) {
	f := newFixture(t)
	f.a.S.Config.Agent.DisablePlugins = []string{"playwright@claude-plugins-official"}
	p, err := f.a.AgentSettings()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), `"playwright@claude-plugins-official": false`) {
		t.Fatalf("settings %s", b)
	}
}

func TestParkHoldsUntilSomethingNewArrives(t *testing.T) {
	f := newFixture(t)
	f.a.Open(OpenParams{Title: "Point", NoStart: true})
	d, _ := f.a.Load("1")
	if err := f.a.Park(d, "à évoquer en séance"); err != nil {
		t.Fatal(err)
	}
	if d, _ = f.a.Load("1"); !d.NoAction {
		t.Fatal("park did not stick")
	}
	if _, err := f.a.event(d, "Nouvelle réponse", nil, nil, true); err != nil {
		t.Fatal(err)
	}
	if d, _ = f.a.Load("1"); d.NoAction {
		t.Fatal("an event should clear no_action")
	}
	f.a.Park(d, "")
	f.a.SetState(d, "wait", "", "Patricia")
	if d, _ = f.a.Load("1"); d.NoAction {
		t.Fatal("a state change should clear no_action")
	}
	if err := f.a.Park(d, ""); err == nil {
		t.Fatal("park accepted a waiting dossier")
	}
}

func TestTodoListsOpenDossiersThatNeedAction(t *testing.T) {
	f := newFixture(t)
	for _, title := range []string{"À faire", "Mis de côté", "En attente"} {
		f.a.Open(OpenParams{Title: title, NoStart: true})
	}
	d, _ := f.a.Load("2")
	f.a.Park(d, "")
	d, _ = f.a.Load("3")
	f.a.SetState(d, "wait", "", "Patricia")
	rows, err := f.a.List("todo", "")
	if err != nil || len(rows) != 1 || rows[0].Title != "À faire" {
		t.Fatalf("todo %+v %v", rows, err)
	}
}
