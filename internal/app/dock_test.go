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
	panes = func() map[string]string { return map[string]string{"fake:p1": "idle", "ph": "unknown"} }
	herdrCall = func(args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		switch {
		case args[0] == "pane" && args[1] == "get" && args[2] == "ph":
			return []byte(`{"result":{"pane":{"tab_id":"tui-tab","workspace_id":"tui-ws"}}}`), nil
		case args[0] == "pane" && args[1] == "get":
			return []byte(`{"result":{"pane":{"tab_id":"own-tab","workspace_id":"dossiers-ws"}}}`), nil
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

func TestDockTradesPlacesWithThePlaceholderAndBack(t *testing.T) {
	f := newFixture(t)
	f.a.Open(OpenParams{Title: "Affaire"})
	calls := fakeHerdr(t)
	d := mustGet(t, f, "1")
	if err := f.a.Dock(d, "ph"); err != nil {
		t.Fatal(err)
	}
	if d.Run.Home != "dossiers-ws" || d.Run.Placeholder != "ph" || d.Run.TabID != "" || d.Run.PaneID != "tui-ws:p1" {
		t.Fatalf("run after dock %+v", d.Run)
	}
	got := strings.Join(*calls, "\n")
	for _, want := range []string{
		"pane move fake:p1 --tab tui-tab --split down --target-pane ph --focus",
		"pane swap --source-pane tui-ws:p1 --target-pane ph",
		"pane move ph --new-tab --workspace tui-ws --label dock placeholder --no-focus",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}
	*calls = nil
	// Any session call sends the pane home first: herdr-acp would take the TUI's tab for its own.
	if err := f.a.Prompt(d, "Du nouveau"); err != nil {
		t.Fatal(err)
	}
	got = strings.Join(*calls, "\n")
	if !strings.Contains(got, "pane swap --source-pane ph --target-pane tui-ws:p1") ||
		!strings.Contains(got, "pane move tui-ws:p1 --new-tab --workspace dossiers-ws") {
		t.Fatalf("not undocked before the prompt:\n%s", got)
	}
	if d.Run.Home != "" || d.Run.Placeholder != "" {
		t.Fatalf("run after undock %+v", d.Run)
	}
}
