// Package testutil provides a fake ACP server and a fake source connector.
// Test binaries run them by re-executing themselves: call Dispatch from TestMain.
package testutil

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const (
	EnvACP       = "OFFICE_FAKE_ACP"       // log file of the fake ACP server
	EnvConnector = "OFFICE_FAKE_CONNECTOR" // behaviour of the fake connector
	EnvLog       = "OFFICE_FAKE_LOG"       // log file of the fake connector
)

// Dispatch runs a fake when the environment asks for one, and exits.
func Dispatch() {
	if p := os.Getenv(EnvACP); p != "" {
		fakeACP(p)
		os.Exit(0)
	}
	if mode := os.Getenv(EnvConnector); mode != "" {
		fakeConnector(mode, os.Getenv(EnvLog))
		os.Exit(0)
	}
}

func appendLine(path string, v any) {
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	b, _ := json.Marshal(v)
	f.Write(append(b, '\n'))
}

// fakeACP answers the methods dossier uses. A prompt gets one session/update and
// one permission request, but no response: the turn stays open, as with herdr-acp.
func fakeACP(logPath string) {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 16<<20)
	out := json.NewEncoder(os.Stdout)
	sessions := 0
	appendLine(logPath, map[string]any{"method": "process", "args": os.Args[1:],
		"env": map[string]string{"DOSSIER_ID": os.Getenv("DOSSIER_ID"), "ADDITIONAL_CLAUDE_MD": os.Getenv("CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD")}})
	for in.Scan() {
		var m map[string]any
		if json.Unmarshal(in.Bytes(), &m) != nil {
			continue
		}
		method, _ := m["method"].(string)
		id := m["id"]
		if method == "" {
			appendLine(logPath, map[string]any{"method": "client-response", "message": m})
			continue
		}
		appendLine(logPath, map[string]any{"method": method, "params": m["params"]})
		placement := map[string]any{"herdr": map[string]any{"paneId": "fake:p1", "tabId": "fake:t1"}}
		switch method {
		case "initialize":
			out.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"protocolVersion": 1, "agentInfo": map[string]any{"name": "fake"}}})
		case "session/new":
			sessions++
			out.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"sessionId": fmt.Sprintf("sess-%d", sessions), "_meta": placement}})
		case "session/load":
			out.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"_meta": placement}})
		case "session/prompt":
			params, _ := m["params"].(map[string]any)
			out.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{
				"sessionId": params["sessionId"], "update": map[string]any{"sessionUpdate": "user_message_chunk"}}})
			out.Encode(map[string]any{"jsonrpc": "2.0", "id": "perm-1", "method": "session/request_permission", "params": map[string]any{
				"sessionId": params["sessionId"], "options": []any{map[string]any{"optionId": "allow", "name": "Allow"}}}})
		case "session/close":
			out.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{}})
		default:
			out.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32601, "message": "unknown"}})
		}
	}
}

// fakeConnector implements protocol 1 with canned answers.
// Modes: ok, empty, error, slow, fail-transition.
func fakeConnector(mode, logPath string) {
	verb := os.Args[len(os.Args)-1]
	input, _ := io.ReadAll(os.Stdin)
	var in map[string]any
	_ = json.Unmarshal(input, &in)
	appendLine(logPath, map[string]any{"verb": verb, "input": in, "env_home": os.Getenv("HOME"), "env_secret": os.Getenv("OFFICE_TEST_SECRET")})
	switch mode {
	case "empty":
		return
	case "error":
		fmt.Print(`{"error":{"message":"token expired"}}`)
		os.Exit(1)
	case "slow":
		time.Sleep(5 * time.Second)
	}
	switch verb {
	case "describe":
		fmt.Print(`{"name":"fake","protocol":1,"verbs":["describe","poll","transition","claim"]}`)
	case "poll":
		watch, _ := in["watch"].([]any)
		events := []any{}
		for _, w := range watch {
			if s, _ := w.(string); strings.HasSuffix(s, "/thread-reply") {
				events = append(events, map[string]any{"thread_ref": s, "kind": "reply",
					"summary": map[string]any{"from": "Baer SA"}, "files": []any{map[string]any{"name": "reply.md", "content": "---\ntype: Email Message\n---\nOui le 12.\n"}}})
			}
		}
		b, _ := json.Marshal(map[string]any{
			"signals": []any{map[string]any{
				"source_ref": "fake:task/1", "thread_ref": "fake:thread/thread-reply", "title": "Armoire de pharmacie",
				"instruction": "Demander une date", "summary": map[string]any{"from": "Natacha"}, "url": "https://example.test/1",
				"files": []any{
					map[string]any{"name": "thread.md", "content": "---\ntype: Email Thread\n---\nBonjour\n"},
					map[string]any{"name": "devis.pdf", "content": "%PDF-fake"},
					map[string]any{"name": "devis.pdf.md", "content": "---\ntype: Attachment\nresource: ./devis.pdf\n---\n"},
				},
			}},
			"events": events, "cursor": "cursor-2",
		})
		fmt.Print(string(b))
	case "claim":
		fmt.Print(`{"ok":true,"detail":"tagged"}`)
	case "transition":
		if mode == "fail-transition" {
			fmt.Print(`{"error":{"message":"gmail unavailable"}}`)
			os.Exit(1)
		}
		fmt.Print(`{"ok":true,"detail":"done"}`)
	}
}

// Calls reads a JSON-lines log written by a fake.
func Calls(path string) []map[string]any {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}
