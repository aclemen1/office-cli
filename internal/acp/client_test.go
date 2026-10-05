package acp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aclemen1/office-cli/internal/testutil"
)

func TestMain(m *testing.M) {
	testutil.Dispatch()
	os.Exit(m.Run())
}

func start(t *testing.T) (*Client, string) {
	t.Helper()
	log := filepath.Join(t.TempDir(), "acp.jsonl")
	c, err := Start(Options{
		Command:   []string{os.Args[0], "--workspace", "w"},
		AgentArgs: []string{"--name", "D-0001"},
		Env:       map[string]string{testutil.EnvACP: log, "DOSSIER_ID": "D-0001"},
		Meta:      map[string]any{"interaction": "native"},
		Cwd:       t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return c, log
}

func find(calls []map[string]any, method string) map[string]any {
	for _, c := range calls {
		if c["method"] == method {
			return c
		}
	}
	return nil
}

func TestSessionLifecycle(t *testing.T) {
	c, log := start(t)
	sid, pl, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	if sid != "sess-1" || pl.PaneID != "fake:p1" || pl.TabID != "fake:t1" {
		t.Fatalf("new session %s %+v", sid, pl)
	}
	if err := c.Prompt(sid, "hello", "queue"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.LoadSession(sid); err != nil {
		t.Fatal(err)
	}
	if err := c.CloseSession(sid); err != nil {
		t.Fatal(err)
	}
	c.Close()

	calls := testutil.Calls(log)
	proc := find(calls, "process")["args"].([]any)
	if len(proc) != 5 || proc[2] != "--" || proc[3] != "--name" {
		t.Fatalf("agent args not passed after --: %v", proc)
	}
	newParams := find(calls, "session/new")["params"].(map[string]any)
	meta := newParams["_meta"].(map[string]any)["herdr"].(map[string]any)
	if meta["interaction"] != "native" {
		t.Fatalf("meta not sent: %v", newParams)
	}
	if _, ok := newParams["mcpServers"].([]any); !ok {
		t.Fatalf("mcpServers must be an array: %v", newParams)
	}
	prompt := find(calls, "session/prompt")["params"].(map[string]any)
	if prompt["prompt"].([]any)[0].(map[string]any)["text"] != "hello" {
		t.Fatalf("prompt %v", prompt)
	}
	// The fake asks for a permission during the prompt: dossier declines it.
	resp := find(calls, "client-response")["message"].(map[string]any)
	outcome := resp["result"].(map[string]any)["outcome"].(map[string]any)["outcome"]
	if outcome != "cancelled" {
		t.Fatalf("permission request answered with %v", resp)
	}
}

func TestStartFailsClearly(t *testing.T) {
	if _, err := Start(Options{Command: []string{"/nonexistent/acp"}, Cwd: t.TempDir()}); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := Start(Options{Cwd: t.TempDir()}); err == nil {
		t.Fatal("empty command should fail")
	}
}
