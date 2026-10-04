package app

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aclemen1/office-cli/internal/office"
)

func TestTheAgentViewHidesTheOfficesWorkspaces(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, _ := office.Init(filepath.Join(t.TempDir(), "pro"), "pro", false)
	oldCall, oldReq := herdrCall, herdrRequest
	t.Cleanup(func() { herdrCall, herdrRequest = oldCall, oldReq })
	herdrCall = func(args ...string) ([]byte, error) {
		return []byte(`{"result":{"workspaces":[{"workspace_id":"w5R","label":"offices-pro"},{"workspace_id":"w5","label":"appdir26"}]}}`), nil
	}
	var sent string
	herdrRequest = func(method string, params any) error {
		b, _ := json.Marshal(params)
		sent = method + " " + string(b)
		return nil
	}
	if err := SetAgentView([]string{s.Root}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`agent.view.set`, `"values":["w5R"]`, `"values":["blocked","done"]`, `"current_workspace_id"`} {
		if !strings.Contains(sent, want) {
			t.Fatalf("missing %s in %s", want, sent)
		}
	}
	if strings.Contains(sent, `"w5"`) {
		t.Fatalf("hides a workspace that is not an office's: %s", sent)
	}
}

func TestHerdrReadyNeedsTheBinaryAndAServer(t *testing.T) {
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "herdr"), []byte("#!/bin/sh\n"), 0o755)
	t.Setenv("PATH", bin)
	t.Setenv("HERDR_SOCKET_PATH", filepath.Join(t.TempDir(), "none.sock"))
	if err := HerdrReady(); err == nil || !strings.Contains(err.Error(), "running herdr server") {
		t.Fatalf("no server: %v", err)
	}
	dir, _ := os.MkdirTemp("/tmp", "herdr")
	defer os.RemoveAll(dir)
	l, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	t.Setenv("HERDR_SOCKET_PATH", filepath.Join(dir, "s"))
	if err := HerdrReady(); err != nil {
		t.Fatalf("with a server: %v", err)
	}
	t.Setenv("PATH", t.TempDir())
	if err := HerdrReady(); err == nil || !strings.Contains(err.Error(), "not on the PATH") {
		t.Fatalf("no binary: %v", err)
	}
}
