package app

import (
	"strings"
	"testing"
)

// fakeHerdr answers pane get, tab get and pane move like herdr, and records
// every call.
func fakeHerdr(t *testing.T) *[]string {
	t.Helper()
	var calls []string
	oldCall, oldPanes := herdrCall, panes
	t.Cleanup(func() { herdrCall, panes = oldCall, oldPanes })
	panes = func() map[string]string {
		return map[string]string{"fake:p1": "idle", "tui-ws:p1": "idle", "ph": "unknown"}
	}
	herdrCall = func(args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		switch {
		case args[0] == "pane" && args[1] == "get" && args[2] == "ph":
			return []byte(`{"result":{"pane":{"tab_id":"tui-tab","workspace_id":"tui-ws"}}}`), nil
		case args[0] == "pane" && args[1] == "get" && args[2] == "ph2":
			return []byte(`{"result":{"pane":{"tab_id":"tui2-tab","workspace_id":"tui-ws"}}}`), nil
		case args[0] == "pane" && args[1] == "get":
			return []byte(`{"result":{"pane":{"tab_id":"own-tab","workspace_id":"dossiers-ws"}}}`), nil
		case args[0] == "pane" && args[1] == "split":
			return []byte(`{"result":{"pane":{"pane_id":"temp"}}}`), nil
		case args[0] == "tab" && args[1] == "get":
			return []byte(`{"result":{"tab":{"pane_count":2}}}`), nil
		case args[0] == "pane" && args[1] == "move" && contains(args, "--new-tab"):
			return []byte(`{"result":{"move_result":{"pane":{"pane_id":"` + args[2] + `"},"created_tab":{"tab_id":"new-tab"}}}}`), nil
		case args[0] == "pane" && args[1] == "move" && args[2] == "fake:p1":
			return []byte(`{"result":{"move_result":{"pane":{"pane_id":"tui-ws:p1"}}}}`), nil
		}
		return []byte(`{}`), nil
	}
	return &calls
}

func TestDockExchangesTheAgentWithThePlaceholderAndBack(t *testing.T) {
	f := newFixture(t)
	f.a.Open(OpenParams{Title: "Affaire"})
	calls := fakeHerdr(t)
	d := mustGet(t, f, "1")
	if err := f.a.Dock(d, "ph", true); err != nil {
		t.Fatal(err)
	}
	if d.Run.Home != "dossiers-ws" || d.Run.Placeholder != "ph" || d.Run.TabID != "" || d.Run.PaneID != "tui-ws:p1" {
		t.Fatalf("run after dock %+v", d.Run)
	}
	got := strings.Join(*calls, "\n")
	for _, want := range []string{
		"pane split ph --direction down",
		"pane move ph --tab own-tab --split down --target-pane fake:p1 --no-focus",
		"pane swap --source-pane ph --target-pane fake:p1",
		"pane move fake:p1 --tab tui-tab --split down --target-pane temp --no-focus",
		"pane swap --source-pane tui-ws:p1 --target-pane temp",
		"pane close temp",
		"agent focus tui-ws:p1",
		"pane resize --pane tui-ws:p1 --direction left --amount 0.01",
		"pane rename tui-ws:p1 D-0001 · Affaire",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}
	*calls = nil
	// A prompt, e.g. a new email, leaves the agent where it is.
	if err := f.a.Prompt(d, "Du nouveau"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(*calls, "\n"); strings.Contains(got, "pane swap") || d.Run.Home == "" || d.Run.TabID != "" {
		t.Fatalf("the prompt moved the agent: %+v\n%s", d.Run, got)
	}
	// Closing it in place would leave a hole: it goes home first.
	if err := f.a.CloseTab(d); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(*calls, "\n"); !strings.Contains(got, "pane swap --source-pane ph --target-pane fake:p1") {
		t.Fatalf("not undocked before closing:\n%s", got)
	}
	if d.Run.Home != "" || d.Run.Placeholder != "" {
		t.Fatalf("run after undock %+v", d.Run)
	}
}

func TestAdoptMakesTheAgentsConversationADossier(t *testing.T) {
	f := newFixture(t)
	repo := t.TempDir()
	old, oldPanes := herdrCall, panes
	t.Cleanup(func() { herdrCall, panes = old, oldPanes })
	panes = func() map[string]string { return map[string]string{"w9:p3": "idle"} }
	herdrCall = func(args ...string) ([]byte, error) {
		return []byte(`{"result":{"pane":{"pane_id":"w9:p3","tab_id":"w9:t1","agent":"claude","agent_status":"idle","cwd":"` + repo +
			`","terminal_title_stripped":"Relais SMTP","agent_session":{"value":"sess-user"}}}}`), nil
	}
	res, err := f.a.Adopt(AdoptParams{Pane: "w9:p3"})
	if err != nil || res.Outcome != "adopted" {
		t.Fatalf("%+v %v", res, err)
	}
	d := mustGet(t, f, "1")
	if d.Title != "Relais SMTP" || d.Run.Session != "sess-user" || d.Run.Cwd != repo || !d.Run.Adopted || !d.HasSource("herdr:session/sess-user") {
		t.Fatalf("dossier %+v", d)
	}
	if _, err := f.a.Adopt(AdoptParams{Pane: "w9:p3"}); err == nil || !strings.Contains(err.Error(), "already") {
		t.Fatalf("adopted twice: %v", err)
	}
	if err := f.a.Prompt(d, "Du nouveau"); err != nil {
		t.Fatal(err)
	}
	load := f.calls("session/load")
	if len(load) != 1 || load[0]["params"].(map[string]any)["cwd"] != repo {
		t.Fatalf("the session should load in its own directory: %v", load)
	}
}

func TestASecondTUIGivesTheFirstPlaceholderItsPlaceBack(t *testing.T) {
	f := newFixture(t)
	f.a.Open(OpenParams{Title: "Affaire"})
	calls := fakeHerdr(t)
	d := mustGet(t, f, "1")
	if err := f.a.Dock(d, "ph", true); err != nil {
		t.Fatal(err)
	}
	*calls = nil
	if err := f.a.Dock(d, "ph2", true); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(*calls, "\n")
	back := strings.Index(got, "pane swap --source-pane ph --target-pane tui-ws:p1")
	in := strings.Index(got, "pane split ph2")
	if back < 0 || in < back || d.Run.Placeholder != "ph2" || d.Run.Home != "dossiers-ws" {
		t.Fatalf("run %+v\n%s", d.Run, got)
	}
}

func TestDockCanLeaveTheFocusInTheTUI(t *testing.T) {
	f := newFixture(t)
	f.a.Open(OpenParams{Title: "Affaire"})
	calls := fakeHerdr(t)
	if err := f.a.Dock(mustGet(t, f, "1"), "ph", false); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(*calls, "\n"); !strings.Contains(got, "pane focus --pane tui-ws:p1 --direction left") || strings.Contains(got, "agent focus") {
		t.Fatalf("dock moved the focus:\n%s", got)
	}
}

func TestOpeningTheDockedDossierOnlyFocusesIt(t *testing.T) {
	f := newFixture(t)
	f.a.Open(OpenParams{Title: "Affaire"})
	calls := fakeHerdr(t)
	d := mustGet(t, f, "1")
	if err := f.a.Dock(d, "ph", false); err != nil {
		t.Fatal(err)
	}
	*calls = nil
	if err := f.a.Dock(d, "ph", true); err != nil {
		t.Fatalf("a second open of the docked dossier failed: %v", err)
	}
	if got := strings.Join(*calls, "\n"); got != "agent focus tui-ws:p1" {
		t.Fatalf("calls %q", got)
	}
}

func TestDockingInAnOccupiedPlaceSendsTheHolderHome(t *testing.T) {
	f := newFixture(t)
	f.a.Open(OpenParams{Title: "Un"})
	f.a.Open(OpenParams{Title: "Deux"})
	fakeHerdr(t)
	one, two := mustGet(t, f, "1"), mustGet(t, f, "2")
	if err := f.a.Dock(one, "ph", false); err != nil {
		t.Fatal(err)
	}
	if err := f.a.Dock(two, "ph", false); err != nil {
		t.Fatalf("the place is taken: %v", err)
	}
	if one = mustGet(t, f, "1"); one.Run.Home != "" {
		t.Fatalf("the first holder should be home: %+v", one.Run)
	}
	if two = mustGet(t, f, "2"); two.Run.Placeholder != "ph" {
		t.Fatalf("the second should hold the place: %+v", two.Run)
	}
}
