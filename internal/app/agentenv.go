package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/aclemen1/office-cli/internal/office"
	"github.com/aclemen1/office-cli/internal/spec"
)

// SkillsReport says what the office's skills directory holds after a sync.
type SkillsReport struct {
	Dir       string   `json:"dir"`
	Linked    []string `json:"linked"`
	Generated []string `json:"generated"`
	Removed   []string `json:"removed,omitempty"`
	Errors    []string `json:"errors,omitempty"`
}

// SyncSkills makes <office>/.claude/skills match [agent] skills and
// skill_commands. Only symlinks are ever removed; a real directory placed there
// by hand stays.
func (a *App) SyncSkills() SkillsReport {
	dir := filepath.Join(a.S.Root, ".claude", "skills")
	rep := SkillsReport{Dir: dir, Linked: []string{}, Generated: []string{}}
	cfg := a.S.Config.Agent
	if len(cfg.Skills) == 0 && len(cfg.SkillCommands) == 0 {
		return rep
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		rep.Errors = append(rep.Errors, err.Error())
		return rep
	}
	if old, err := os.ReadFile(filepath.Join(dir, "README.md")); err != nil || string(old) != skillsReadme {
		_ = os.WriteFile(filepath.Join(dir, "README.md"), []byte(skillsReadme), 0o644)
	}
	want := map[string]bool{}
	for _, src := range cfg.Skills {
		src = office.ExpandHome(src)
		name := filepath.Base(src)
		want[name] = true
		if _, err := os.Stat(filepath.Join(src, "SKILL.md")); err != nil {
			rep.Errors = append(rep.Errors, fmt.Sprintf("skill %s: no SKILL.md in %s", name, src))
			continue
		}
		link := filepath.Join(dir, name)
		if cur, err := os.Readlink(link); err == nil && cur == src {
			rep.Linked = append(rep.Linked, name)
			continue
		}
		if fi, err := os.Lstat(link); err == nil && fi.Mode()&os.ModeSymlink == 0 {
			rep.Errors = append(rep.Errors, fmt.Sprintf("skill %s: %s exists and is not a symlink; left as is", name, link))
			continue
		}
		_ = os.Remove(link)
		if err := os.Symlink(src, link); err != nil {
			rep.Errors = append(rep.Errors, fmt.Sprintf("skill %s: %v", name, err))
			continue
		}
		rep.Linked = append(rep.Linked, name)
	}
	names := make([]string, 0, len(cfg.SkillCommands))
	for n := range cfg.SkillCommands {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		argv := cfg.SkillCommands[name]
		want[name] = true
		if len(argv) == 0 {
			continue
		}
		out, err := exec.Command(office.ExpandHome(argv[0]), argv[1:]...).Output()
		if err != nil || !bytes.HasPrefix(bytes.TrimSpace(out), []byte("---")) {
			rep.Errors = append(rep.Errors, fmt.Sprintf("skill %s: %s did not print a SKILL.md (%v)", name, strings.Join(argv, " "), err))
			continue
		}
		target := filepath.Join(dir, name)
		if fi, err := os.Lstat(target); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			_ = os.Remove(target)
		}
		if err := os.MkdirAll(target, 0o755); err != nil {
			rep.Errors = append(rep.Errors, err.Error())
			continue
		}
		p := filepath.Join(target, "SKILL.md")
		if old, err := os.ReadFile(p); err != nil || !bytes.Equal(old, out) {
			if err := os.WriteFile(p, out, 0o644); err != nil {
				rep.Errors = append(rep.Errors, err.Error())
				continue
			}
		}
		rep.Generated = append(rep.Generated, name)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if want[e.Name()] {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if fi, err := os.Lstat(p); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			if os.Remove(p) == nil {
				rep.Removed = append(rep.Removed, e.Name())
			}
		}
	}
	return rep
}

func (a *App) protected() []string {
	var out []string
	for _, p := range a.S.Config.Agent.Protect {
		out = append(out, filepath.Clean(office.ExpandHome(p)))
	}
	return out
}

// AgentSettings writes the settings file passed to every session. See agentSettings.
func (a *App) AgentSettings() (string, error) { return a.agentSettings() }

// agentSettings is the settings file passed to every session: the archive hook,
// the Bash guard and the deny rules of protected paths.
func (a *App) agentSettings() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	q := strconv.Quote(exe)
	hooks := map[string]any{
		"SessionEnd": []any{map[string]any{"hooks": []any{map[string]any{
			"type": "command", "command": q + " hook session-end", "timeout": 30,
		}}}},
	}
	settings := map[string]any{"hooks": hooks}
	// The agent acts on dossiers through its scoped MCP tools, not the CLI.
	deny := []string{"Bash(dossier:*)", "Bash(" + exe + ":*)"}
	if prot := a.protected(); len(prot) > 0 {
		hooks["PreToolUse"] = []any{map[string]any{"matcher": "Bash", "hooks": []any{map[string]any{
			"type": "command", "command": q + " hook guard", "timeout": 10,
		}}}}
		for _, p := range prot {
			// An Edit rule covers every file-editing tool; Write(path) is not matched.
			deny = append(deny, "Edit(/"+p+"/**)")
		}
	}
	settings["permissions"] = map[string]any{"allow": []string{"mcp__office"}, "deny": deny}
	if off := a.S.Config.Agent.DisablePlugins; len(off) > 0 {
		plugins := map[string]bool{}
		for _, p := range off {
			plugins[p] = false
		}
		settings["enabledPlugins"] = plugins
	}
	p := a.S.Meta("run", "agent-settings.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	b, _ := json.MarshalIndent(settings, "", "  ")
	return p, os.WriteFile(p, b, 0o644)
}

var (
	wordStart    = `(^|[^[:alnum:]_.-])`
	mutatingBash = regexp.MustCompile(wordStart + `(mv|rm|cp|touch|mkdir|rmdir|ln|dd|tee|truncate|chmod|chown|shred|unlink|rsync)\s` +
		`|` + wordStart + `sed\s+-i|` + wordStart + `perl\s+-[a-z]*i` +
		`|obsidian(\s+\S+=("[^"]*"|\S+))*\s+(move|rename|delete|remove|write|append|create|trash)`)
)

// Guard refuses a mutating Bash command that names a protected path. It
// over-blocks on purpose: citing the path in a mutating command is enough.
// It returns the hook output, or nil to let the command run.
func (a *App) Guard(command string) map[string]any {
	for _, p := range a.protected() {
		names := []string{p, filepath.Base(p)}
		hit := ""
		for _, n := range names {
			if strings.Contains(command, n) {
				hit = n
				break
			}
		}
		if hit == "" {
			continue
		}
		redirect := regexp.MustCompile(`>>?\s*[^|;&]*` + regexp.QuoteMeta(hit))
		if mutatingBash.MatchString(command) || redirect.MatchString(command) {
			return map[string]any{"hookSpecificOutput": map[string]any{
				"hookEventName":            "PreToolUse",
				"permissionDecision":       "deny",
				"permissionDecisionReason": fmt.Sprintf("%s is protected by the office (agent.protect): read it, never change it with a shell command. Ask the user to move or edit what is there.", p),
			}}
		}
	}
	return nil
}

const skillsReadme = `# Generated directory

office rebuilds this directory from [agent] skills and skill_commands in
.office/config.toml, before every session start. A link made here by hand is
removed. Add a skill with ` + "`office skills add <path>`" + `, remove one with
` + "`office skills remove <name>`" + `.
`

// AddSkill lists a skill directory in [agent] skills and links it.
func (a *App) AddSkill(path string) (SkillsReport, error) {
	abs, err := filepath.Abs(office.ExpandHome(path))
	if err != nil {
		return SkillsReport{}, err
	}
	if _, err := os.Stat(filepath.Join(abs, "SKILL.md")); err != nil {
		return SkillsReport{}, spec.UserError("%s has no SKILL.md: a skill is a directory holding one", abs)
	}
	name := filepath.Base(abs)
	if _, ok := a.S.Config.Agent.SkillCommands[name]; ok {
		return SkillsReport{}, spec.UserError("skill_commands already provides a skill named %s", name)
	}
	list := a.S.Config.Agent.Skills
	for _, x := range list {
		if filepath.Base(office.ExpandHome(x)) == name {
			return SkillsReport{}, spec.UserError("[agent] skills already has a skill named %s: %s", name, x)
		}
	}
	if err := a.S.SetSkills(append(append([]string{}, list...), office.TildePath(abs))); err != nil {
		return SkillsReport{}, err
	}
	return a.SyncSkills(), nil
}

// RemoveSkill drops a skill, named by its directory name, from [agent] skills.
func (a *App) RemoveSkill(name string) (SkillsReport, error) {
	var kept []string
	found := false
	for _, x := range a.S.Config.Agent.Skills {
		if filepath.Base(office.ExpandHome(x)) == name {
			found = true
			continue
		}
		kept = append(kept, x)
	}
	if !found {
		return SkillsReport{}, spec.UserError("no skill named %s in [agent] skills", name)
	}
	if err := a.S.SetSkills(kept); err != nil {
		return SkillsReport{}, err
	}
	return a.SyncSkills(), nil
}
