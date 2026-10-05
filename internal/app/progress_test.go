package app

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aclemen1/office-cli/internal/acp"
	"github.com/aclemen1/office-cli/internal/testutil"
)

func TestAPlaceholderShowsTheStepsThenCloses(t *testing.T) {
	f := newFixture(t)
	deskWith(t, f, "working")
	f.a.withProgress(func(ps []Progress) []Progress {
		return append(ps, Progress{Source: "fake", Placeholder: "fake:message/ph1", Dossier: f.a.DeskID(),
			Session: f.a.Desk().Run.Session, Started: time.Now().Format(time.RFC3339)})
	})
	var conn *acp.Client
	defer func() {
		if conn != nil {
			conn.Close()
		}
	}()
	for i := 0; i < 3; i++ {
		f.a.followProgress(&conn)
	}
	var ops []string
	for _, c := range testutil.Calls(f.srcLog) {
		if c["verb"] == "progress" {
			in := c["input"].(map[string]any)
			ops = append(ops, in["op"].(string)+":"+in["text"].(string))
		}
	}
	if strings.Join(ops, ",") != "update:⚙ office show,end:✓" {
		t.Fatalf("progress calls %v", ops)
	}
	var ps []Progress
	b, _ := os.ReadFile(f.a.S.Meta("run", progressFile))
	json.Unmarshal(b, &ps)
	if len(ps) != 0 {
		t.Fatalf("the placeholder stayed open: %+v", ps)
	}
}

func TestProgressStepsNameTheTools(t *testing.T) {
	got := progressSteps([]json.RawMessage{
		json.RawMessage(`{"sessionUpdate":"tool_call","title":"mcp__office__notify"}`),
		json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"text":"x"}}`),
		json.RawMessage(`{"sessionUpdate":"tool_call","title":"Read"}`),
	})
	if strings.Join(got, ",") != "office notify,Read" {
		t.Fatalf("steps %v", got)
	}
}

func TestReleaseAsksTheSourceAndNotesIt(t *testing.T) {
	f := newFixture(t)
	if detail, err := f.a.Release("fake:item/1", false); err != nil || detail != "released" {
		t.Fatalf("release %q %v", detail, err)
	}
	if b, _ := os.ReadFile(f.a.Desk().Path("log.md")); !strings.Contains(string(b), "released fake:item/1") {
		t.Fatalf("desk log:\n%s", b)
	}
	if _, err := f.a.Release("nope:item/1", false); err == nil {
		t.Fatal("an unknown source released")
	}
	if _, err := f.a.Release("noref", false); err == nil {
		t.Fatal("a reference without source was accepted")
	}
}
