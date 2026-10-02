package actions

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aclemen1/dossier-cli/internal/app"
	"github.com/aclemen1/dossier-cli/internal/spec"
	"github.com/aclemen1/dossier-cli/internal/store"
	"github.com/aclemen1/dossier-cli/internal/testutil"
)

func TestMain(m *testing.M) {
	testutil.Dispatch()
	runAction = runInProcess
	os.Exit(m.Run())
}

// storeWith creates a store with light dossiers (no session) and points the
// environment at it, as a dossier session would.
func storeWith(t *testing.T, titles ...string) *app.App {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	s, err := store.Init(filepath.Join(t.TempDir(), "s"), "test", false)
	if err != nil {
		t.Fatal(err)
	}
	a := &app.App{S: s}
	for _, title := range titles {
		if _, err := a.Open(app.OpenParams{Title: title, NoStart: true}); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("DOSSIER_STORE", s.Root)
	t.Setenv("DOSSIER_ID", "D-0001")
	return a
}

type mcpClient struct {
	in  io.WriteCloser
	out *bufio.Scanner
	id  int
}

func startMCP(t *testing.T) *mcpClient {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go func() { serveMCP(inR, outW); outW.Close() }()
	c := &mcpClient{in: inW, out: bufio.NewScanner(outR)}
	c.out.Buffer(make([]byte, 1<<20), 16<<20)
	t.Cleanup(func() { inW.Close() })
	return c
}

func (c *mcpClient) rpc(t *testing.T, method string, params any) map[string]any {
	t.Helper()
	c.id++
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": c.id, "method": method, "params": params})
	c.in.Write(append(b, '\n'))
	if !c.out.Scan() {
		t.Fatalf("%s: no answer", method)
	}
	var m map[string]any
	json.Unmarshal(c.out.Bytes(), &m)
	return m
}

// call runs a tool and returns the decoded envelope and the isError flag.
func (c *mcpClient) call(t *testing.T, name string, args map[string]any) (map[string]any, bool) {
	t.Helper()
	r := c.rpc(t, "tools/call", map[string]any{"name": name, "arguments": args})["result"].(map[string]any)
	text := r["content"].([]any)[0].(map[string]any)["text"].(string)
	var env map[string]any
	json.Unmarshal([]byte(text), &env)
	return env, r["isError"].(bool)
}

func TestMCPListsTools(t *testing.T) {
	storeWith(t, "Séance")
	c := startMCP(t)
	init := c.rpc(t, "initialize", map[string]any{"protocolVersion": "2025-06-18"})["result"].(map[string]any)
	if init["protocolVersion"] != "2025-06-18" || init["serverInfo"].(map[string]any)["name"] != "dossier" {
		t.Fatalf("initialize %v", init)
	}
	list := c.rpc(t, "tools/list", map[string]any{})["result"].(map[string]any)["tools"].([]any)
	byName := map[string]map[string]any{}
	for _, x := range list {
		m := x.(map[string]any)
		byName[m["name"].(string)] = m
	}
	for _, n := range []string{"show", "search", "grep", "wait", "park", "close", "open", "link", "merge", "notify"} {
		if byName[n] == nil {
			t.Errorf("tool %s missing", n)
		}
	}
	props := func(n string) map[string]any {
		return byName[n]["inputSchema"].(map[string]any)["properties"].(map[string]any)
	}
	if _, ok := props("close")["id"]; !ok {
		t.Fatal("close must take an optional id")
	}
	if req, _ := byName["close"]["inputSchema"].(map[string]any)["required"].([]any); len(req) > 0 {
		t.Fatalf("close requires %v: its id defaults to this dossier", req)
	}
	if _, ok := props("open")["parent"]; ok {
		t.Fatal("open must not expose parent")
	}
	if _, ok := props("grep")["dossier"]; !ok {
		t.Fatal("grep must expose dossier")
	}
}

func TestMCPToolsDefaultToTheCurrentDossier(t *testing.T) {
	a := storeWith(t, "Séance", "Armoire", "Autre")
	c := startMCP(t)
	c.rpc(t, "initialize", map[string]any{})

	if env, isErr := c.call(t, "wait", map[string]any{"on": "Baer SA"}); isErr || env["ok"] != true {
		t.Fatalf("wait %v", env)
	}
	if d, _ := a.Load("1"); d.State != "waiting" {
		t.Fatalf("D-0001 state %s", d.State)
	}
	if d, _ := a.Load("2"); d.State != "open" {
		t.Fatal("another dossier changed")
	}
	if env, isErr := c.call(t, "link", map[string]any{"to": "D-0002", "rel": "includes"}); isErr {
		t.Fatalf("link %v", env)
	}
	if env, isErr := c.call(t, "notify", map[string]any{"to": "D-0003", "text": "x"}); isErr {
		t.Fatalf("notify a dossier this one does not include: %v", env)
	}
	if log, _ := os.ReadFile(mustLoad(t, a, "3").Path("log.md")); !strings.Contains(string(log), "· by D-0001") {
		t.Fatalf("an action on another dossier is not signed:\n%s", log)
	}
	if env, isErr := c.call(t, "notify", map[string]any{"to": "D-0002", "text": "Décidé : on attend."}); isErr {
		t.Fatalf("notify %v", env)
	}
	log, _ := os.ReadFile(mustLoad(t, a, "2").Path("log.md"))
	if !strings.Contains(string(log), "from D-0001: Décidé : on attend.") {
		t.Fatalf("notify not logged:\n%s", log)
	}
	if env, isErr := c.call(t, "grep", map[string]any{"pattern": "x", "dossier": "D-0002"}); isErr {
		t.Fatalf("grep another dossier: %v", env)
	}
	if _, isErr := c.call(t, "grep", map[string]any{"pattern": "x"}); isErr {
		t.Fatal("grep on this dossier refused")
	}
	if env, isErr := c.call(t, "show", map[string]any{"id": "D-0003"}); isErr || env["result"].(map[string]any)["title"] != "Autre" {
		t.Fatalf("show another dossier %v", env)
	}
	if env, isErr := c.call(t, "open", map[string]any{"title": "Sous-affaire", "no-start": true}); isErr {
		t.Fatalf("open %v", env)
	} else if self, _ := a.Load("1"); !self.HasLink("includes", "D-0004") {
		t.Fatalf("this dossier should include the one it opened: %v", self.Links)
	}
	if env, isErr := c.call(t, "wait", map[string]any{}); !isErr || !strings.Contains(env["error"].(map[string]any)["message"].(string), "--on") {
		t.Fatalf("missing argument: %v", env)
	}
}

func mustLoad(t *testing.T, a *app.App, id string) *dossierView {
	t.Helper()
	d, err := a.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	return &dossierView{d.Path}
}

type dossierView struct {
	Path func(...string) string
}

func TestSkillInstall(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dossier")
	p, err := installSkill("claude", dir)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if !strings.HasPrefix(string(b), "---\nname: dossier") {
		t.Fatalf("skill %q", b)
	}
	if _, err := installSkill("cursor", ""); err == nil {
		t.Fatal("unsupported harness accepted")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	if p, _ := installSkill("claude-code", ""); p != filepath.Join(home, ".claude", "skills", "dossier", "SKILL.md") {
		t.Fatalf("default path %s", p)
	}
}

func TestEveryActionHasExamplesAndSummary(t *testing.T) {
	for _, a := range spec.All() {
		if a.Summary == "" || len(a.Examples) == 0 {
			t.Errorf("%s %s needs a summary and an example", a.Category, a.Name)
		}
	}
}

func TestMCPLinksFromAnyDossier(t *testing.T) {
	a := storeWith(t, "Limite de connexions", "Autre", "Accord")
	c := startMCP(t)
	c.rpc(t, "initialize", map[string]any{})
	if env, isErr := c.call(t, "open", map[string]any{"title": "Touch Base CI", "no-start": true}); isErr {
		t.Fatalf("open %v", env)
	}
	if env, isErr := c.call(t, "link", map[string]any{"from": "D-0004", "to": "D-0002", "rel": "includes"}); isErr {
		t.Fatalf("link from a dossier this one includes: %v", env)
	}
	child, _ := a.Load("4")
	if !child.HasLink("includes", "D-0002") {
		t.Fatalf("child links %v", child.Links)
	}
	if env, isErr := c.call(t, "link", map[string]any{"to": "D-0003", "rel": "depends_on"}); isErr {
		t.Fatalf("link from self: %v", env)
	}
	if env, isErr := c.call(t, "link", map[string]any{"from": "D-0002", "to": "D-0003", "rel": "includes"}); isErr {
		t.Fatalf("link from any dossier of the store: %v", env)
	}
}

func TestMCPWaitUsesTheDefaultDelayAndNotifyWakes(t *testing.T) {
	a := storeWith(t, "Séance", "Point")
	c := startMCP(t)
	c.rpc(t, "initialize", map[string]any{})
	if env, isErr := c.call(t, "wait", map[string]any{"on": "JMR"}); isErr {
		t.Fatalf("wait %v", env)
	}
	d, _ := a.Load("1")
	until, err := time.Parse(time.RFC3339, d.WaitUntil)
	if err != nil || until.Sub(time.Now()) < 6*24*time.Hour || until.Sub(time.Now()) > 8*24*time.Hour {
		t.Fatalf("wait_until %q", d.WaitUntil)
	}
	if env, isErr := c.call(t, "wait", map[string]any{"on": "x", "until": "bientôt"}); !isErr {
		t.Fatalf("bad until accepted: %v", env)
	}
	pt, _ := a.Load("2")
	a.SetState(pt, "wait", "", "Baer SA")
	c.call(t, "resume", map[string]any{})
	c.call(t, "link", map[string]any{"to": "D-0002", "rel": "includes"})
	if env, isErr := c.call(t, "notify", map[string]any{"to": "D-0002", "text": "Décidé en séance."}); isErr {
		t.Fatalf("notify %v", env)
	}
	if pt, _ := a.Load("2"); pt.State != "open" {
		t.Fatalf("notify left the point %s", pt.State)
	}
}

func TestMCPWaitsAndResumesAnotherDossier(t *testing.T) {
	a := storeWith(t, "Séance PSEC", "Audit des accès S3", "Autre")
	a.Link("1", "2", "includes")
	c := startMCP(t)
	c.rpc(t, "initialize", map[string]any{})
	if env, isErr := c.call(t, "wait", map[string]any{"id": "D-0002", "on": "Alain", "until": "2027-01-04"}); isErr {
		t.Fatalf("wait on an included dossier: %v", env)
	}
	if env, isErr := c.call(t, "wait", map[string]any{"id": "D-0002", "on": "Patricia", "until": "2026-10-09"}); isErr {
		t.Fatalf("correct the wait: %v", env)
	}
	d, _ := a.Load("2")
	if d.State != "waiting" || d.WaitingOn != "Patricia" || !strings.HasPrefix(d.WaitUntil, "2026-10-09") {
		t.Fatalf("corrected wait: %s on %q until %q", d.State, d.WaitingOn, d.WaitUntil)
	}
	if self, _ := a.Load("1"); self.State != "open" {
		t.Fatalf("the meeting itself moved to %s", self.State)
	}
	if env, isErr := c.call(t, "resume", map[string]any{"id": "D-0002"}); isErr {
		t.Fatalf("resume an included dossier: %v", env)
	}
	if env, isErr := c.call(t, "wait", map[string]any{"id": "D-0003", "on": "X"}); isErr {
		t.Fatalf("wait on any dossier of the store: %v", env)
	}
}

func TestMCPOnTheDeskNamesItsDossier(t *testing.T) {
	required := func(name string, desk bool) []string {
		for _, tl := range tools {
			if tl.name == name {
				r, _ := tl.schema(desk)["required"].([]string)
				return r
			}
		}
		t.Fatalf("no tool %s", name)
		return nil
	}
	if r := required("wait", true); !contains(r, "id") {
		t.Fatalf("on the desk, wait needs an id: %v", r)
	}
	if r := required("wait", false); contains(r, "id") {
		t.Fatalf("in a dossier, wait defaults to it: %v", r)
	}
	if r := required("notify", true); contains(r, "from") {
		t.Fatalf("the desk notifies as itself: %v", r)
	}
	if r := required("open", true); contains(r, "in") {
		t.Fatalf("a dossier opened from the desk stands alone: %v", r)
	}
}
