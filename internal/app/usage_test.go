package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUsageCountsFrictionWithoutThirdPartyText(t *testing.T) {
	f := newFixture(t)
	f.a.Open(OpenParams{Title: "Briefing", NoStart: true})
	d := mustGet(t, f, "1")
	d.Run.Session = "sess-u"
	d.Save()
	dir := filepath.Join(os.Getenv("HOME"), ".claude", "projects", "p")
	os.MkdirAll(dir, 0o755)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	lines := []string{
		`{"type":"assistant","timestamp":"` + now + `","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"mcp__office__notify","input":{}}]}}`,
		`{"type":"user","timestamp":"` + now + `","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","is_error":true,"content":"{\"ok\":false,\"error\":{\"message\":\"\\\"Facture Baer SA\\\" is not a dossier id\"}}"}]}}`,
		`{"type":"assistant","timestamp":"` + now + `","message":{"role":"assistant","content":[{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"office stores --format text"}}]}}`,
		`{"type":"user","timestamp":"` + now + `","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","is_error":true,"content":"Permission to use Bash has been denied."}]}}`,
		`{"type":"user","timestamp":"` + now + `","origin":{"kind":"human"},"message":{"role":"user","content":"Non, je préfère que le desk le fasse"}}`,
	}
	os.WriteFile(filepath.Join(dir, "sess-u.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)

	u := f.a.Usage(time.Now().Add(-time.Hour), false)
	if u.ToolCalls["notify"] != 1 || len(u.ToolErrors) != 1 || strings.Contains(u.ToolErrors[0].What, "Baer") {
		t.Fatalf("tool errors %+v", u.ToolErrors)
	}
	if len(u.PermissionDenied) != 1 || u.PermissionDenied[0].What != "Bash office stores (unknown verb)" {
		t.Fatalf("denied %+v", u.PermissionDenied)
	}
	if len(u.Corrections) != 1 || !strings.Contains(u.Corrections[0].What, "desk") {
		t.Fatalf("corrections %+v", u.Corrections)
	}
	if facts := f.a.Usage(time.Now().Add(-time.Hour), true); facts.Corrections[0].What != "correction" {
		t.Fatalf("facts-only kept the user's words: %+v", facts.Corrections)
	}
}
