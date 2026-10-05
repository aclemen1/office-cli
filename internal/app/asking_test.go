package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPendingQuestionReadsTheTranscript(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	ask := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"AskUserQuestion","input":{}}]}}` + "\n"
	other := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_2","name":"Bash","input":{}}]}}` + "\n"
	answer := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"oui"}]}}` + "\n"
	write := func(s string) { os.WriteFile(p, []byte(s), 0o644) }
	write(other)
	if scanPendingQuestion(p, int64(len(other))) {
		t.Fatal("a Bash call is not a question")
	}
	write(other + ask)
	if !scanPendingQuestion(p, int64(len(other+ask))) {
		t.Fatal("an unanswered question was missed")
	}
	write(other + ask + answer)
	if scanPendingQuestion(p, int64(len(other+ask+answer))) {
		t.Fatal("an answered question still counts")
	}
}
