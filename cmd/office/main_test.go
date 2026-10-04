package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// End-to-end tests: they go through run, the CLI's entry point, with a fake
// herdr on the PATH and a fake herdr server socket.

// fakeHerdr puts a herdr script on the PATH and serves its socket; it answers
// every listing with nothing.
func fakeHerdr(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := "#!/bin/sh\ncase \"$1 $2\" in\n\"pane list\") echo '{\"result\":{\"panes\":[]}}' ;;\n" +
		"\"workspace list\") echo '{\"result\":{\"workspaces\":[]}}' ;;\n" +
		"\"pane get\") echo '{\"result\":{\"pane\":{\"pane_id\":\"'\"$3\"'\",\"tab_id\":\"t\"}}}' ;;\n" +
		"\"pane send-keys\") echo \"$@\" >> \"$HERDR_LOG\"; echo '{}' ;;\n*) echo '{}' ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	// A unix socket path is short: macOS caps it near 104 bytes.
	dir, err := os.MkdirTemp("/tmp", "herdr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	t.Setenv("HERDR_SOCKET_PATH", sock)
}

// call runs the CLI and returns its exit code and its JSON envelope.
func call(t *testing.T, args ...string) (int, map[string]any) {
	t.Helper()
	r, w, _ := os.Pipe()
	stdout := os.Stdout
	os.Stdout = w
	code := run(args)
	w.Close()
	os.Stdout = stdout
	var buf bytes.Buffer
	io.Copy(&buf, r)
	var env map[string]any
	_ = json.Unmarshal(buf.Bytes(), &env)
	return code, env
}

func setup(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DOSSIER_ID", "")
	t.Setenv("OFFICE_DIR", "")
	fakeHerdr(t)
	root := filepath.Join(t.TempDir(), "pro")
	if code, env := call(t, "init", root, "--sphere", "pro", "--format", "json"); code != 0 {
		t.Fatalf("init: %d %v", code, env)
	}
	return root
}

func TestWithoutHerdrOnlyMetaMCPAndDoctorRun(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HERDR_SOCKET_PATH", filepath.Join(t.TempDir(), "none.sock"))
	code, env := call(t, "ls", "--office", t.TempDir(), "--format", "json")
	if code == 0 || !strings.Contains(env["error"].(map[string]any)["message"].(string), "herdr") {
		t.Fatalf("ls without herdr: %d %v", code, env)
	}
	if code, _ := call(t, "version"); code != 0 {
		t.Fatal("version needs no herdr")
	}
	if code, _ := call(t, "schema", "dossier", "open"); code != 0 {
		t.Fatal("schema needs no herdr")
	}
}

func TestADossierLivesThroughTheCLI(t *testing.T) {
	root := setup(t)
	s := func(args ...string) map[string]any {
		t.Helper()
		code, env := call(t, append(args, "--office", root, "--format", "json")...)
		if code != 0 || env["ok"] != true {
			t.Fatalf("%v: %d %v", args, code, env)
		}
		return env
	}
	open := s("open", "--title", "Armoire de pharmacie", "--instruction", "Demander une date", "--no-start")
	id := open["result"].(map[string]any)["id"].(string)
	if id != "D-0001" {
		t.Fatalf("id %s", id)
	}
	s("wait", id, "--on", "Livit", "--until", "7d")
	if rows := s("ls", "--status", "waiting")["result"].([]any); len(rows) != 1 {
		t.Fatalf("waiting %v", rows)
	}
	s("resume", id)
	s("park", id, "--note", "plus tard")
	if rows := s("ls", "--status", "todo")["result"].([]any); len(rows) != 0 {
		t.Fatalf("a parked dossier is not to do: %v", rows)
	}
	s("star", id)
	if rows := s("ls", "--status", "all")["result"].([]any); rows[0].(map[string]any)["id"] != id {
		t.Fatalf("starred %v", rows)
	}
	s("unstar", id)
	s("close", id, "--note", "Passage fixé")
	if hits := s("search", "armoire")["result"].([]any); len(hits) == 0 {
		t.Fatal("a closed dossier stays searchable")
	}
	if show := s("show", "desk")["result"].(map[string]any); show["id"] != "D-DESK" {
		t.Fatalf("desk %v", show)
	}
	if code, env := call(t, "wait", "desk", "--on", "x", "--office", root, "--format", "json"); code == 0 {
		t.Fatalf("the desk took a state: %v", env)
	}
}

func TestUnknownActionsAndBadFormatsFailCleanly(t *testing.T) {
	setup(t)
	if code, env := call(t, "frobnicate"); code == 0 || env["ok"] != false {
		t.Fatalf("unknown action: %d %v", code, env)
	}
	if code, _ := call(t, "ls", "--format", "yaml"); code == 0 {
		t.Fatal("--format yaml accepted")
	}
}

func TestMoveFromTheCLIByPrefixAndSphere(t *testing.T) {
	pro := setup(t)
	perso := filepath.Join(filepath.Dir(pro), "perso")
	if code, env := call(t, "init", perso, "--sphere", "perso", "--format", "json"); code != 0 {
		t.Fatalf("init perso: %v", env)
	}
	s := func(office string, args ...string) map[string]any {
		t.Helper()
		code, env := call(t, append(args, "--office", office, "--format", "json")...)
		if code != 0 || env["ok"] != true {
			t.Fatalf("%v: %d %v", args, code, env)
		}
		return env
	}
	a := s(perso, "open", "--title", "K3S errata", "--no-start")["result"].(map[string]any)["id"].(string)
	b := s(perso, "open", "--title", "K3S fenêtre", "--no-start")["result"].(map[string]any)["id"].(string)
	s(perso, "link", a, b, "--rel", "includes")
	moved := s(perso, "move", a, b, "--to", "pro")["result"].(map[string]any)["moved"].([]any)
	if len(moved) != 2 {
		t.Fatalf("moved %v", moved)
	}
	newA := moved[0].(map[string]any)["to"].(string)
	if dir := moved[0].(map[string]any)["dir"].(string); !strings.Contains(dir, filepath.Base(filepath.Dir(pro))+"/pro/") {
		t.Fatalf("moved to %s, not into %s", dir, pro)
	}
	if tree := s(pro, "tree", newA)["result"].(map[string]any); len(tree["children"].([]any)) != 1 {
		t.Fatalf("the link should follow: %v", tree)
	}
	if rows := s(perso, "ls", "--status", "all")["result"].([]any); len(rows) != 0 {
		t.Fatalf("perso still holds %v", rows)
	}
}

func TestTuiKeyTypesIntoTheRunningTUI(t *testing.T) {
	setup(t)
	log := filepath.Join(t.TempDir(), "herdr.log")
	t.Setenv("HERDR_LOG", log)
	if code, _ := call(t, "tui-key", "]", "--format", "json"); code == 0 {
		t.Fatal("no TUI runs: tui-key should say so")
	}
	cfg, _ := os.UserConfigDir()
	os.MkdirAll(filepath.Join(cfg, "office"), 0o755)
	os.WriteFile(filepath.Join(cfg, "office", "tui.json"), []byte(`{"/s":{"by_priority":true,"side":true,"pane":"w5T:p9"}}`), 0o644)
	if code, env := call(t, "tui-key", "g", "p", "--format", "json"); code != 0 {
		t.Fatalf("tui-key: %v", env)
	}
	b, _ := os.ReadFile(log)
	if got := strings.TrimSpace(string(b)); got != "pane send-keys w5T:p9 g\npane send-keys w5T:p9 p" {
		t.Fatalf("keys sent %q", got)
	}
}
