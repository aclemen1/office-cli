package connector

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aclemen1/office-cli/internal/office"
	"github.com/aclemen1/office-cli/internal/testutil"
)

func TestMain(m *testing.M) {
	testutil.Dispatch()
	os.Exit(m.Run())
}

func runner(t *testing.T, mode, timeout string) (Runner, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	s, err := office.Init(filepath.Join(t.TempDir(), "s"), "s", false)
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "calls.jsonl")
	src := office.SourceConfig{
		Name: "fake", Command: []string{os.Args[0]}, Timeout: timeout,
		Env:    map[string]string{testutil.EnvConnector: mode, testutil.EnvLog: log},
		Config: map[string]any{"tasklist": "L1"},
	}
	return Runner{Office: s, Source: src}, log
}

func TestDescribeAndPoll(t *testing.T) {
	r, log := runner(t, "ok", "")
	d, err := r.Describe()
	if err != nil || d.Protocol != Protocol || d.Name != "fake" {
		t.Fatalf("describe %+v %v", d, err)
	}
	p, err := r.Poll("cursor-1", []string{"fake:thread/thread-reply"}, PollOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Signals) != 1 || len(p.Events) != 1 || p.Cursor != "cursor-2" || len(p.Signals[0].Files) != 3 {
		t.Fatalf("poll %+v", p)
	}
	calls := testutil.Calls(log)
	in := calls[len(calls)-1]["input"].(map[string]any)
	if in["cursor"] != "cursor-1" || in["config"].(map[string]any)["tasklist"] != "L1" {
		t.Fatalf("poll input %+v", in)
	}
}

func TestEnvironmentIsLimited(t *testing.T) {
	t.Setenv("OFFICE_TEST_SECRET", "leak")
	r, log := runner(t, "ok", "")
	if _, err := r.Describe(); err != nil {
		t.Fatal(err)
	}
	c := testutil.Calls(log)[0]
	if c["env_secret"] != "" || c["env_home"] == "" {
		t.Fatalf("environment not limited: %+v", c)
	}
}

func TestFailuresAreNeverSilent(t *testing.T) {
	for mode, want := range map[string]string{
		"empty": "returned nothing",
		"error": "token expired",
	} {
		r, _ := runner(t, mode, "")
		_, err := r.Poll("", nil, PollOptions{})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", mode, err, want)
		}
	}
	r, _ := runner(t, "slow", "300ms")
	if _, err := r.Describe(); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("slow: %v", err)
	}
}

func TestTransition(t *testing.T) {
	r, log := runner(t, "ok", "")
	if err := r.Transition("fake:task/1", "fake:thread/t", "open", "done", "note", Dossier{ID: "D-0001"}); err != nil {
		t.Fatal(err)
	}
	in := testutil.Calls(log)[0]["input"].(map[string]any)
	if in["source_ref"] != "fake:task/1" || in["to"] != "done" || in["from"] != "open" {
		t.Fatalf("transition input %+v", in)
	}
	r, _ = runner(t, "fail-transition", "")
	if err := r.Transition("fake:task/1", "", "open", "done", "", Dossier{ID: "D-0001"}); err == nil || !strings.Contains(err.Error(), "gmail unavailable") {
		t.Fatalf("got %v", err)
	}
}

func TestAServerTakesTheCallsAndFallsBackWhenItEnds(t *testing.T) {
	r, log := runner(t, "ok", "")
	s, err := Serve(r.Office, r.Source)
	if err != nil {
		t.Fatal(err)
	}
	ph, err := r.Progress("start", "fake:message/1", "", "")
	if err != nil || ph != "fake:message/ph1" {
		t.Fatalf("progress through the server: %q %v", ph, err)
	}
	ready, err := r.Wait("", 1)
	if err != nil || ready {
		t.Fatalf("wait through the server: %v %v", ready, err)
	}
	served := 0
	for _, c := range testutil.Calls(log) {
		if c["served"] == true {
			served++
		}
	}
	if served != 2 {
		t.Fatalf("calls served: %d", served)
	}
	var out any
	_ = r.run("boom", map[string]any{}, &out)
	deadline := time.Now().Add(5 * time.Second)
	for !s.Gone() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !s.Gone() || serverFor(r.Office, r.Source.Name) != nil {
		t.Fatal("a server that ended is still in use")
	}
	if d, err := r.Describe(); err != nil || d.Name != "fake" {
		t.Fatalf("the one-shot fallback: %+v %v", d, err)
	}
}
