package office

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Office    OfficeSection  `toml:"office"`
	ACP       ACPSection     `toml:"acp"`
	Agent     AgentSection   `toml:"agent"`
	Prompt    PromptSection  `toml:"prompt"`
	Lifecycle Lifecycle      `toml:"lifecycle"`
	Routing   Routing        `toml:"routing"`
	Sources   []SourceConfig `toml:"source"`
	Agenda    AgendaSection  `toml:"agenda"`
	Inbox     InboxSection   `toml:"inbox"`
}

// InboxSection: the office's drop folder. Each top-level entry (a file or a
// whole folder) goes to the desk once it stops changing; the desk files it
// (File), attaches it to a dossier, then releases it to the Trash, after Check
// found every file kept elsewhere. Placeholders: {path}, {file}, {sphere}, {note}.
type InboxSection struct {
	Dir    string   `toml:"dir"`    // default inbox
	Settle string   `toml:"settle"` // default 30s
	Off    bool     `toml:"off"`
	File   []string `toml:"file"`   // files an entry, e.g. mnemo remember {path} --sphere {sphere} --note {note} --wait --format json
	Check  []string `toml:"check"`  // checks one file is kept, e.g. artefact which {file} --sphere {sphere} --format json
	Holder string   `toml:"holder"` // the holder Check must report, e.g. mnemo
}

// AgendaSection names the CLI that keeps the meetings' agendas (ordo): a
// dossier included in a meeting's dossier is proposed as an item there.
type AgendaSection struct {
	Command []string `toml:"command"`
	Sphere  string   `toml:"sphere"`
}

// MCPConfig is an MCP server added to every session; {id} in an env value
// stands for the dossier's id.
type MCPConfig struct {
	Name    string            `toml:"name"`
	Command []string          `toml:"command"`
	Env     map[string]string `toml:"env"`
}

type OfficeSection struct {
	Sphere   string `toml:"sphere"`
	IDPrefix string `toml:"id_prefix"`
}

type ACPSection struct {
	Command []string          `toml:"command"`
	Meta    map[string]any    `toml:"meta"`
	Env     map[string]string `toml:"env"`
}

type AgentSection struct {
	Args          []string            `toml:"args"`
	RemoteControl bool                `toml:"remote_control"`
	AddDirs       []string            `toml:"add_dirs"`
	Skills        []string            `toml:"skills"`
	SkillCommands map[string][]string `toml:"skill_commands"`
	Protect       []string            `toml:"protect"`
	// Plugins turned off in every session, e.g. "playwright@claude-plugins-official".
	DisablePlugins []string `toml:"disable_plugins"`
	// Model of the dossiers' sessions, unless a dossier names its own; DeskModel
	// is the desk's. Empty: the agent's own default.
	Model     string      `toml:"model"`
	DeskModel string      `toml:"desk_model"`
	MCP       []MCPConfig `toml:"mcp"`
}

type PromptSection struct {
	Open               string `toml:"open"`
	Event              string `toml:"event"`
	Deadline           string `toml:"deadline"`
	Locale             string `toml:"locale"`
	DefaultInstruction string `toml:"default_instruction"`
}

type Lifecycle struct {
	CloseTabOn  []string `toml:"close_tab_on"`
	DefaultWait string   `toml:"default_wait"`
	MaxSessions int      `toml:"max_sessions"`
}

const defaultMaxSessions = 20

// SessionCap is the number of sessions ingest keeps running in tabs.
func (c Config) SessionCap() int {
	if c.Lifecycle.MaxSessions > 0 {
		return c.Lifecycle.MaxSessions
	}
	return defaultMaxSessions
}

type Routing struct {
	AddressPrefix []string `toml:"address_prefix"`
}

type SourceConfig struct {
	Name    string            `toml:"name"`
	Command []string          `toml:"command"`
	Env     map[string]string `toml:"env"`
	Config  map[string]any    `toml:"config"`
	Timeout string            `toml:"timeout"`
	// Signals "desk" hands every new item of the source to the desk instead of
	// opening a dossier; replies in a dossier's thread still reach the dossier.
	Signals string `toml:"signals"`
}

func (s SourceConfig) TimeoutDuration() time.Duration {
	if d, err := time.ParseDuration(s.Timeout); err == nil && d > 0 {
		return d
	}
	return 60 * time.Second
}

func (c Config) Source(name string) (SourceConfig, bool) {
	for _, s := range c.Sources {
		if s.Name == name {
			return s, true
		}
	}
	return SourceConfig{}, false
}

func (c Config) ClosesTabOn(state string) bool {
	for _, s := range c.Lifecycle.CloseTabOn {
		if s == state {
			return true
		}
	}
	return false
}

func loadConfig(path string) (Config, error) {
	var c Config
	_, err := toml.DecodeFile(path, &c)
	return c, err
}

// ExpandHome turns a leading ~ into the home directory.
func ExpandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	return p
}
