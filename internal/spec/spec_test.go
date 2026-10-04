package spec

import (
	"os"
	"strings"
	"testing"
)

func testAction() *Action {
	return &Action{
		Category: "test", Name: "move", Summary: "Move a thing.",
		Params: []Param{
			{Name: "id", Kind: String, Positional: true, Aliases: []string{"dossier"}},
			{Name: "on", Kind: String, Required: true},
			{Name: "status", Kind: String, Default: "active", Enum: []string{"active", "all"}},
			{Name: "file", Kind: StringList},
			{Name: "dry-run", Kind: Bool},
		},
		Examples: []string{`office move D-0042 --on "Baer SA"`},
	}
}

func TestParseFlagsAndPositionals(t *testing.T) {
	got, err := Parse(testAction(), []string{"D-0042", "--on", "Baer SA", "--file", "a", "--file=b", "--dry-run"})
	if err != nil {
		t.Fatal(err)
	}
	if got["id"] != "D-0042" || got["on"] != "Baer SA" || got["status"] != "active" || got["dry-run"] != true {
		t.Fatalf("unexpected parse: %#v", got)
	}
	if f := got["file"].([]string); len(f) != 2 || f[0] != "a" || f[1] != "b" {
		t.Fatalf("file list = %#v", got["file"])
	}
}

func TestParseHiddenAlias(t *testing.T) {
	got, err := Parse(testAction(), []string{"--dossier", "D-7", "--on", "x"})
	if err != nil {
		t.Fatal(err)
	}
	if got["id"] != "D-7" {
		t.Fatalf("alias not absorbed: %#v", got)
	}
}

func TestParseListPositionalLeavesFlags(t *testing.T) {
	a := &Action{Name: "ingest", Params: []Param{
		{Name: "source", Kind: StringList, Positional: true},
		{Name: "dry-run", Kind: Bool},
	}}
	got, err := Parse(a, []string{"gmail", "memos", "--dry-run"})
	if err != nil {
		t.Fatal(err)
	}
	if s := got["source"].([]string); len(s) != 2 || got["dry-run"] != true {
		t.Fatalf("got %#v", got)
	}
}

func TestParseErrorsNameTheCanonicalCall(t *testing.T) {
	cases := map[string][]string{
		"missing --on":                {"D-1"},
		"unknown option --onn":        {"D-1", "--onn", "x"},
		`got "nope"`:                  {"D-1", "--on", "x", "--status", "nope"},
		"option --on needs a value":   {"D-1", "--on"},
		`unexpected argument "extra"`: {"D-1", "extra", "--on", "x"},
	}
	for want, argv := range cases {
		_, err := Parse(testAction(), argv)
		if err == nil {
			t.Fatalf("%v: expected an error", argv)
		}
		e := err.(*Error)
		if e.Kind != "user_error" || e.Code != ExitUsage || !strings.Contains(e.Message, want) {
			t.Fatalf("%v: got %+v, want message containing %q", argv, e, want)
		}
		if !strings.Contains(e.Message, "office move") {
			t.Fatalf("%v: message does not show the canonical call: %s", argv, e.Message)
		}
	}
}

type pendingResult struct{ n int }

func (p pendingResult) PendingTransitions() int { return p.n }

func TestEmitExitCodes(t *testing.T) {
	devnull, _ := os.Open(os.DevNull)
	stdout, stderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = devnull, devnull
	defer func() { os.Stdout, os.Stderr = stdout, stderr }()
	a := testAction()
	if code := Emit(a, "json", map[string]any{"x": 1}, nil); code != 0 {
		t.Fatalf("ok result exit %d", code)
	}
	if code := Emit(a, "json", pendingResult{2}, nil); code != ExitPending {
		t.Fatalf("pending result exit %d", code)
	}
	if code := Emit(a, "json", nil, NotFound("x")); code != ExitNotFound {
		t.Fatalf("not found exit %d", code)
	}
	if code := Emit(a, "json", nil, Locked("x")); code != ExitLocked {
		t.Fatalf("locked exit %d", code)
	}
}

func TestSchemaDrillDown(t *testing.T) {
	saved := registry
	defer func() { registry = saved }()
	registry = nil
	Register(&Action{Category: "state", Name: "close", Summary: "Close it."})
	Register(&Action{Category: "state", Name: "wait", Summary: "Wait for someone."})
	Register(&Action{Category: "graph", Name: "link", Summary: "Link two."})
	cat := Catalog()
	if len(cat) != 2 || cat[0].Category != "graph" || cat[1].ActionCount != 2 {
		t.Fatalf("catalog %#v", cat)
	}
	if l := ActionsIn("state"); len(l) != 2 || l[0].Name != "close" {
		t.Fatalf("actions %#v", l)
	}
	if Find("state", "wait") == nil || FindVerb("link") == nil {
		t.Fatal("find failed")
	}
	if s := Search("someone"); len(s) != 1 || s[0].Name != "state wait" {
		t.Fatalf("search %#v", s)
	}
}
