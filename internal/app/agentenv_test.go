package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyncSkillsLinksGeneratesAndPrunes(t *testing.T) {
	f := newFixture(t)
	lib := t.TempDir()
	for _, n := range []string{"qmd", "gws"} {
		os.MkdirAll(filepath.Join(lib, n), 0o755)
		os.WriteFile(filepath.Join(lib, n, "SKILL.md"), []byte("---\nname: "+n+"\n---\n"), 0o644)
	}
	cfg := &f.a.S.Config.Agent
	cfg.Skills = []string{filepath.Join(lib, "qmd"), filepath.Join(lib, "gws"), filepath.Join(lib, "missing")}
	cfg.SkillCommands = map[string][]string{"macos": {"/usr/bin/printf", "%b", "---\\nname: macos\\n---\\n"}}
	rep := f.a.SyncSkills()
	if len(rep.Linked) != 2 || len(rep.Generated) != 1 || len(rep.Errors) != 1 || !strings.Contains(rep.Errors[0], "missing") {
		t.Fatalf("first sync %+v", rep)
	}
	if b, _ := os.ReadFile(filepath.Join(rep.Dir, "macos", "SKILL.md")); !strings.HasPrefix(string(b), "---\nname: macos") {
		t.Fatalf("generated skill %q", b)
	}
	hand := filepath.Join(rep.Dir, "hand-made")
	os.MkdirAll(hand, 0o755)
	cfg.Skills = []string{filepath.Join(lib, "qmd")}
	rep = f.a.SyncSkills()
	if len(rep.Removed) != 1 || rep.Removed[0] != "gws" {
		t.Fatalf("prune %+v", rep)
	}
	if _, err := os.Stat(hand); err != nil {
		t.Fatal("a hand-made directory was removed")
	}
}

// The protected directory is called "Private" here: the test strings must not
// trip the guard of the machine that runs the tests.
func TestGuardBlocksMutatingCommandsOnProtectedPaths(t *testing.T) {
	f := newFixture(t)
	private := filepath.Join(t.TempDir(), "vault", "Private")
	f.a.S.Config.Agent.Protect = []string{private}
	deny := []string{
		"mv '" + private + "/a.pdf' /tmp/",
		"rm Private/old.md",
		"echo hi > " + private + "/note.md",
		"sed -i 's/a/b/' Private/x.md",
		"cp Private/x /tmp/",
		"obsidian vault=X move Private/a 10-Staging/a",
	}
	allow := []string{
		"ls Private",
		"cat '" + private + "/a.md'",
		"rg budget " + private,
		"mv ~/Downloads/a.pdf ~/vault/10-Staging/",
	}
	for _, c := range deny {
		if f.a.Guard(c) == nil {
			t.Errorf("should deny: %s", c)
		}
	}
	for _, c := range allow {
		if v := f.a.Guard(c); v != nil {
			t.Errorf("should allow: %s → %v", c, v)
		}
	}
}

func TestAgentSettingsCarryDenyRulesAndHooks(t *testing.T) {
	f := newFixture(t)
	p, _ := f.a.agentSettings()
	var s map[string]any
	b, _ := os.ReadFile(p)
	json.Unmarshal(b, &s)
	perms := s["permissions"].(map[string]any)
	if len(perms["deny"].([]any)) != 2 || perms["allow"].([]any)[0] != "mcp__office" {
		t.Fatalf("base permissions %v", perms)
	}
	if _, ok := s["hooks"].(map[string]any)["PreToolUse"]; ok {
		t.Fatal("no protect: no guard hook expected")
	}
	f.a.S.Config.Agent.Protect = []string{"/v/Private"}
	p, _ = f.a.agentSettings()
	b, _ = os.ReadFile(p)
	s = nil
	json.Unmarshal(b, &s)
	deny := s["permissions"].(map[string]any)["deny"].([]any)
	if deny[0] != "Bash(dossier:*)" || deny[2] != "Edit(//v/Private/**)" || len(deny) != 3 {
		t.Fatalf("deny %v", deny)
	}
	if _, ok := s["hooks"].(map[string]any)["PreToolUse"]; !ok {
		t.Fatal("guard hook missing")
	}
}

func TestSessionGetsMemoryDirectories(t *testing.T) {
	f := newFixture(t)
	vault := t.TempDir()
	f.a.S.Config.Agent.AddDirs = []string{vault}
	f.a.Open(OpenParams{Title: "X"})
	proc := f.calls("process")[0]
	args := strings.Join(toStrings(proc["args"].([]any)), " ")
	env := proc["env"].(map[string]any)
	if !strings.Contains(args, "--add-dir "+vault) || env["ADDITIONAL_CLAUDE_MD"] != "1" || env["DOSSIER_ID"] != "D-0001" {
		t.Fatalf("args %q env %v", args, env)
	}
}

func TestAddAndRemoveSkillKeepTheConfigComments(t *testing.T) {
	f := newFixture(t)
	lib := t.TempDir()
	for _, n := range []string{"qmd", "compta"} {
		os.MkdirAll(filepath.Join(lib, n), 0o755)
		os.WriteFile(filepath.Join(lib, n, "SKILL.md"), []byte("---\nname: "+n+"\n---\n"), 0o644)
	}
	p := f.a.S.Meta("config.toml")
	b, _ := os.ReadFile(p)
	os.WriteFile(p, []byte(strings.Replace(string(b), "[agent]", "[agent]\n# skills partagés\nskills = [\n  \""+filepath.Join(lib, "qmd")+"\",\n]", 1)), 0o644)
	f.a.S.Config.Agent.Skills = []string{filepath.Join(lib, "qmd")}

	rep, err := f.a.AddSkill(filepath.Join(lib, "compta"))
	if err != nil || len(rep.Linked) != 2 {
		t.Fatalf("add %+v %v", rep, err)
	}
	b, _ = os.ReadFile(p)
	if !strings.Contains(string(b), "# skills partagés") || !strings.Contains(string(b), "compta") || len(f.a.S.Config.Agent.Skills) != 2 {
		t.Fatalf("config after add:\n%s", b)
	}
	if _, err := f.a.AddSkill(filepath.Join(lib, "compta")); err == nil {
		t.Fatal("a skill was added twice")
	}
	if _, err := f.a.AddSkill(lib); err == nil {
		t.Fatal("a directory without SKILL.md was added")
	}
	if r, _ := os.ReadFile(filepath.Join(rep.Dir, "README.md")); !strings.Contains(string(r), "Generated directory") {
		t.Fatalf("readme %q", r)
	}
	if rep, err = f.a.RemoveSkill("qmd"); err != nil || len(rep.Removed) != 1 {
		t.Fatalf("remove %+v %v", rep, err)
	}
	if _, err := f.a.RemoveSkill("qmd"); err == nil {
		t.Fatal("removed a skill that is not listed")
	}
}
