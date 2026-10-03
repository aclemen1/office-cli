// Package dossier reads and writes one dossier directory: dossier.md (an OKF
// concept), log.md (OKF's reserved history file) and .state.json (machine state).
package dossier

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/aclemen1/dossier-cli/internal/spec"
	"github.com/aclemen1/dossier-cli/internal/store"
)

const (
	Open    = "open"
	Waiting = "waiting"
	Done    = "done"
	Merged  = "merged"
	// Desk is the state of a store's desk: a session without an affair, never closed.
	Desk = "desk"
)

type Source struct {
	Resource string `yaml:"resource,omitempty" json:"resource,omitempty"`
	ID       string `yaml:"id" json:"id"`
	Title    string `yaml:"title,omitempty" json:"title,omitempty"`
}

// Name is the connector that owns the source ("gmail" in "gmail:task/abc").
func (s Source) Name() string {
	n, _, _ := strings.Cut(s.ID, ":")
	return n
}

type Transition struct {
	Source    string `json:"source"`
	SourceRef string `json:"source_ref"`
	ThreadRef string `json:"thread_ref,omitempty"`
	From      string `json:"from"`
	To        string `json:"to"`
	Note      string `json:"note,omitempty"`
	Error     string `json:"error"`
	At        string `json:"at"`
}

type RunState struct {
	Session            string       `json:"session,omitempty"`
	PaneID             string       `json:"pane_id,omitempty"`
	TabID              string       `json:"tab_id,omitempty"`
	Home               string       `json:"home,omitempty"`        // workspace to move the pane back to, while it is docked
	Placeholder        string       `json:"placeholder,omitempty"` // pane whose place it took
	Cwd                string       `json:"cwd,omitempty"`         // where an adopted session runs, instead of the dossier's directory
	Adopted            bool         `json:"adopted,omitempty"`     // runs as the user started it: no dossier tools until restart
	Prompts            int          `json:"prompts"`
	Contexts           int          `json:"contexts"`
	PendingTransitions []Transition `json:"pending_transitions"`
}

type Dossier struct {
	Dir         string   `json:"dir"`
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Alias       string   `json:"alias,omitempty"`
	Description string   `json:"description,omitempty"`
	Resource    string   `json:"resource,omitempty"`
	State       string   `json:"state"`
	WaitingOn   string   `json:"waiting_on,omitempty"`
	WaitUntil   string   `json:"wait_until,omitempty"`
	NoAction    bool     `json:"no_action,omitempty"`
	Starred     bool     `json:"starred,omitempty"`
	MergedInto  string   `json:"merged_into,omitempty"`
	Sources     []Source `json:"sources"`
	Threads     []string `json:"threads"`
	Links       []Link   `json:"links"`
	Created     string   `json:"created"`
	Updated     string   `json:"updated"`
	Run         RunState `json:"run"`

	doc  *yaml.Node
	body string
}

func Now() string { return time.Now().Format(time.RFC3339) }

func (d *Dossier) Path(parts ...string) string {
	return filepath.Join(append([]string{d.Dir}, parts...)...)
}

func (d *Dossier) Num() int { return store.NumberOf(d.Dir) }

// Label is how people name the dossier: U-RDIR when it has an alias, its id otherwise.
func (d *Dossier) Label() string {
	if d.Alias == "" {
		return d.ID
	}
	prefix, _, _ := strings.Cut(d.ID, "-")
	return prefix + "-" + d.Alias
}

func Create(dir, id, title string) *Dossier {
	now := Now()
	d := &Dossier{Dir: dir, ID: id, Title: title, State: Open, Created: now, Updated: now}
	d.doc = &yaml.Node{Kind: yaml.MappingNode}
	return d
}

func Load(dir string) (*Dossier, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "dossier.md"))
	if err != nil {
		return nil, spec.NotFound("%s has no dossier.md: %v", dir, err)
	}
	fm, body, err := splitFrontmatter(string(raw))
	if err != nil {
		return nil, fmt.Errorf("%s/dossier.md: %w", dir, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(fm), &doc); err != nil {
		return nil, fmt.Errorf("%s/dossier.md frontmatter: %w", dir, err)
	}
	mapping := &yaml.Node{Kind: yaml.MappingNode}
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 && doc.Content[0].Kind == yaml.MappingNode {
		mapping = doc.Content[0]
	}
	var f struct {
		ID          string   `yaml:"id"`
		Title       string   `yaml:"title"`
		Alias       string   `yaml:"alias"`
		Description string   `yaml:"description"`
		Resource    string   `yaml:"resource"`
		State       string   `yaml:"state"`
		WaitingOn   string   `yaml:"waiting_on"`
		WaitUntil   string   `yaml:"wait_until"`
		NoAction    bool     `yaml:"no_action"`
		Starred     bool     `yaml:"starred"`
		MergedInto  string   `yaml:"merged_into"`
		Sources     []Source `yaml:"sources"`
		Threads     []string `yaml:"threads"`
		Links       []Link   `yaml:"links"`
		Created     string   `yaml:"created"`
		Timestamp   string   `yaml:"timestamp"`
	}
	if err := mapping.Decode(&f); err != nil {
		return nil, fmt.Errorf("%s/dossier.md frontmatter: %w", dir, err)
	}
	d := &Dossier{
		Dir: dir, ID: f.ID, Title: f.Title, Alias: f.Alias, Description: f.Description, Resource: f.Resource,
		State: f.State, WaitingOn: f.WaitingOn, WaitUntil: f.WaitUntil, NoAction: f.NoAction, Starred: f.Starred, MergedInto: f.MergedInto,
		Sources: f.Sources, Threads: f.Threads, Links: f.Links, Created: f.Created, Updated: f.Timestamp,
		doc: mapping, body: body,
	}
	if d.ID == "" {
		d.ID = fmt.Sprintf("D-%04d", store.NumberOf(dir))
	}
	if b, err := os.ReadFile(filepath.Join(dir, ".state.json")); err == nil {
		_ = json.Unmarshal(b, &d.Run)
	}
	return d, nil
}

func splitFrontmatter(s string) (fm, body string, err error) {
	if !strings.HasPrefix(s, "---\n") {
		return "", s, nil
	}
	end := strings.Index(s[4:], "\n---")
	if end < 0 {
		return "", "", fmt.Errorf("unterminated frontmatter")
	}
	fm = s[4 : 4+end]
	rest := s[4+end+4:]
	rest = strings.TrimPrefix(rest, "\n")
	return fm, rest, nil
}

// Body is the Markdown body of dossier.md: the instruction and every section a
// person or an agent added, the managed links block included.
func (d *Dossier) Body() string { return d.body }

// Notes is the body without the managed links block.
func (d *Dossier) Notes() string {
	b := d.body
	if i := strings.Index(b, linksBegin); i >= 0 {
		if j := strings.Index(b, linksEnd); j > i {
			b = b[:i] + b[j+len(linksEnd):]
		}
	}
	return strings.TrimSpace(b)
}

// SetBody replaces the Markdown body. Only used at creation.
func (d *Dossier) SetBody(b string) { d.body = b }

// Save rewrites dossier.md and .state.json. Keys dossier does not know and the
// body are preserved as they are.
func (d *Dossier) Save() error {
	d.Updated = Now()
	set := func(key string, v any) { setKey(d.doc, key, v) }
	set("type", "Dossier")
	set("id", d.ID)
	set("title", d.Title)
	set("alias", d.Alias)
	set("description", d.Description)
	set("resource", d.Resource)
	set("state", d.State)
	set("waiting_on", d.WaitingOn)
	set("wait_until", d.WaitUntil)
	set("no_action", d.NoAction)
	set("starred", d.Starred)
	set("merged_into", d.MergedInto)
	set("sources", d.Sources)
	set("threads", d.Threads)
	set("links", d.Links)
	set("created", d.Created)
	set("timestamp", d.Updated)

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(d.doc); err != nil {
		return err
	}
	content := "---\n" + buf.String() + "---\n" + d.body
	if err := writeAtomic(d.Path("dossier.md"), []byte(content)); err != nil {
		return err
	}
	if d.Run.PendingTransitions == nil {
		d.Run.PendingTransitions = []Transition{}
	}
	b, _ := json.MarshalIndent(d.Run, "", "  ")
	return writeAtomic(d.Path(".state.json"), append(b, '\n'))
}

func isEmpty(v any) bool {
	switch x := v.(type) {
	case bool:
		return !x
	case string:
		return x == ""
	case []Source:
		return len(x) == 0
	case []string:
		return len(x) == 0
	case []Link:
		return len(x) == 0
	}
	return v == nil
}

func setKey(m *yaml.Node, key string, v any) {
	idx := -1
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			idx = i
			break
		}
	}
	if isEmpty(v) {
		if idx >= 0 {
			m.Content = append(m.Content[:idx], m.Content[idx+2:]...)
		}
		return
	}
	var val yaml.Node
	if err := val.Encode(v); err != nil {
		return
	}
	if idx >= 0 {
		m.Content[idx+1] = &val
		return
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, &val)
}

func writeAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Log appends one line to log.md, OKF's reserved history file.
func (d *Dossier) Log(format string, a ...any) error {
	p := d.Path("log.md")
	if _, err := os.Stat(p); err != nil {
		if err := os.WriteFile(p, []byte("# "+d.ID+" history\n\n"), 0o644); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	line := fmt.Sprintf(format, a...)
	// A session acting on another dossier signs what it did there.
	if actor := os.Getenv("DOSSIER_ID"); actor != "" && actor != d.ID {
		line += " · by " + actor
	}
	_, err = fmt.Fprintf(f, "- %s · %s\n", Now(), line)
	return err
}

func (d *Dossier) HasSource(ref string) bool {
	for _, s := range d.Sources {
		if s.ID == ref {
			return true
		}
	}
	return false
}

func (d *Dossier) HasThread(ref string) bool {
	for _, t := range d.Threads {
		if t == ref {
			return true
		}
	}
	return false
}

func (d *Dossier) AddSource(s Source) {
	if s.ID != "" && !d.HasSource(s.ID) {
		d.Sources = append(d.Sources, s)
	}
}

func (d *Dossier) AddThread(t string) {
	if t != "" && !d.HasThread(t) {
		d.Threads = append(d.Threads, t)
	}
}

// Slug turns a title into a short directory-safe name.
func Slug(title string) string {
	repl := strings.NewReplacer("à", "a", "â", "a", "ä", "a", "ç", "c", "é", "e", "è", "e", "ê", "e", "ë", "e",
		"î", "i", "ï", "i", "ô", "o", "ö", "o", "ù", "u", "û", "u", "ü", "u", "ÿ", "y", "œ", "oe", "æ", "ae", "ß", "ss")
	s := repl.Replace(strings.ToLower(title))
	var b strings.Builder
	dash := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	words := strings.Split(out, "-")
	if len(words) > 5 {
		words = words[:5]
	}
	out = strings.Join(words, "-")
	if out == "" {
		out = "dossier"
	}
	return out
}

// Link is an outgoing edge of the dossier graph.
type Link struct {
	Rel string `yaml:"rel" json:"rel"`
	To  string `yaml:"to" json:"to"`
}

const (
	RelIncludes  = "includes"
	RelDependsOn = "depends_on"
)

func (d *Dossier) HasLink(rel, to string) bool {
	for _, l := range d.Links {
		if l.Rel == rel && l.To == to {
			return true
		}
	}
	return false
}

func (d *Dossier) Targets(rel string) []string {
	var out []string
	for _, l := range d.Links {
		if l.Rel == rel {
			out = append(out, l.To)
		}
	}
	return out
}

const (
	linksBegin = "<!-- dossier:links (managed by dossier, edits here are replaced) -->"
	linksEnd   = "<!-- /dossier:links -->"
)

// SetLinksBlock replaces the managed links block of the body, or appends it.
// An empty block removes it. The rest of the body is left untouched.
func (d *Dossier) SetLinksBlock(markdown string) {
	block := ""
	if markdown != "" {
		block = linksBegin + "\n" + markdown + linksEnd + "\n"
	}
	start := strings.Index(d.body, linksBegin)
	end := strings.Index(d.body, linksEnd)
	if start >= 0 && end > start {
		rest := d.body[end+len(linksEnd):]
		rest = strings.TrimPrefix(rest, "\n")
		d.body = d.body[:start] + block + rest
		return
	}
	if block == "" {
		return
	}
	if !strings.HasSuffix(d.body, "\n") {
		d.body += "\n"
	}
	d.body += "\n" + block
}
