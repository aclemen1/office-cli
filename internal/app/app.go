// Package app holds the operations behind every action.
package app

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/aclemen1/dossier-cli/internal/acp"
	"github.com/aclemen1/dossier-cli/internal/connector"
	"github.com/aclemen1/dossier-cli/internal/dossier"
	"github.com/aclemen1/dossier-cli/internal/spec"
	"github.com/aclemen1/dossier-cli/internal/store"
)

type App struct{ S *store.Store }

func New(storeFlag string) (*App, error) {
	s, err := store.Resolve(storeFlag)
	if err != nil {
		return nil, err
	}
	return &App{S: s}, nil
}

// Load resolves an id; an empty id means the dossier of the current session.
func (a *App) Load(id string) (*dossier.Dossier, error) {
	if id == "" {
		id = os.Getenv("DOSSIER_ID")
	}
	if id == "" {
		return nil, spec.UserError("no dossier id given and DOSSIER_ID is not set. Pass one, for example `dossier show D-0042`")
	}
	if a.isDesk(id) {
		return nil, a.deskError()
	}
	if d := a.byAlias(id); d != nil {
		return d, nil
	}
	dir, err := a.S.FindDir(id)
	if err != nil {
		return nil, err
	}
	return dossier.Load(dir)
}

var aliasRe = regexp.MustCompile(`^[A-Z][A-Z0-9-]{0,15}$`)

// NormalizeAlias uppercases an alias and drops the store prefix: "u-rdir" → "RDIR".
func (a *App) NormalizeAlias(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	return strings.TrimPrefix(s, a.S.Prefix()+"-")
}

// byAlias finds the dossier named RDIR or U-RDIR. Ids that start with a digit
// after the prefix are numbers, never aliases.
func (a *App) byAlias(id string) *dossier.Dossier {
	name := a.NormalizeAlias(id)
	if !aliasRe.MatchString(name) {
		return nil
	}
	all, _ := a.All()
	for _, d := range all {
		if d.Alias == name {
			return d
		}
	}
	return nil
}

// SetAlias names a dossier. An alias is unique in the store; "" removes it.
func (a *App) SetAlias(d *dossier.Dossier, alias string) error {
	alias = a.NormalizeAlias(alias)
	if alias != "" {
		if !aliasRe.MatchString(alias) {
			return spec.UserError("alias %q must start with a letter and hold up to 16 letters, digits or dashes, e.g. RDIR", alias)
		}
		if alias == "DESK" {
			return spec.UserError("alias DESK names the store's desk; choose another")
		}
		if other := a.byAlias(alias); other != nil && other.ID != d.ID {
			return spec.UserError("alias %s is already %s (%s)", alias, other.ID, other.Title)
		}
	}
	old := d.Alias
	d.Alias = alias
	if err := d.Save(); err != nil {
		return err
	}
	_ = d.Log("alias %q → %q", old, alias)
	renameTab(d.Run.TabID, TabLabel(d))
	return nil
}

func (a *App) All() ([]*dossier.Dossier, error) {
	dirs, err := a.S.Dirs()
	if err != nil {
		return nil, err
	}
	var out []*dossier.Dossier
	for _, d := range dirs {
		if x, err := dossier.Load(d); err == nil {
			out = append(out, x)
		}
	}
	return out, nil
}

// follow resolves merged dossiers to the dossier they were merged into.
func (a *App) follow(d *dossier.Dossier) *dossier.Dossier {
	for i := 0; i < 10 && d.State == dossier.Merged && d.MergedInto != ""; i++ {
		next, err := a.Load(d.MergedInto)
		if err != nil {
			break
		}
		d = next
	}
	return d
}

func (a *App) FindBySource(ref string) *dossier.Dossier {
	all, _ := a.All()
	for _, d := range all {
		if d.HasSource(ref) {
			return a.follow(d)
		}
	}
	return nil
}

func (a *App) FindByThread(ref string) *dossier.Dossier {
	all, _ := a.All()
	for _, d := range all {
		if d.HasThread(ref) {
			return a.follow(d)
		}
	}
	return nil
}

// ---------------------------------------------------------------- open

type OpenParams struct {
	Title       string
	SourceRef   string
	ThreadRef   string
	URL         string
	Instruction string
	Summary     map[string]any
	Files       []connector.File
	Alias       string
	In          []string // aliases or ids of the dossiers that include this one
	NoStart     bool
}

type OpenResult struct {
	ID       string   `json:"id"`
	Dir      string   `json:"dir"`
	Outcome  string   `json:"outcome"` // created | existing | reopened | routed
	Session  string   `json:"session,omitempty"`
	TabID    string   `json:"tab_id,omitempty"`
	Pending  int      `json:"pending_transitions,omitempty"`
	Started  bool     `json:"started"`
	Warnings []string `json:"warnings,omitempty"`
	Untitled bool     `json:"-"`
}

func (r OpenResult) PendingTransitions() int { return r.Pending }

var addressRe = regexp.MustCompile(`^\s*(\d+)\s*[:,.\-–]?\s*`)

// route returns the dossier an instruction addresses ("D-42: …"), if any.
func (a *App) route(instruction string) (*dossier.Dossier, string) {
	low := strings.ToLower(instruction)
	prefixes := append([]string{a.S.Prefix() + "-"}, a.S.Config.Routing.AddressPrefix...)
	for _, prefix := range prefixes {
		p := strings.ToLower(prefix)
		if !strings.HasPrefix(strings.TrimSpace(low), p) {
			continue
		}
		rest := strings.TrimSpace(instruction)[len(p):]
		m := addressRe.FindStringSubmatch(rest)
		if m == nil {
			continue
		}
		d, err := a.Load(m[1])
		if err != nil {
			continue
		}
		return a.follow(d), strings.TrimSpace(rest[len(m[0]):])
	}
	return nil, ""
}

// Open opens or finds the dossier, then makes every dossier of p.In include it.
func (a *App) Open(p OpenParams) (OpenResult, error) {
	res, err := a.open(p)
	if err != nil || res.ID == "" {
		return res, err
	}
	for _, holder := range p.In {
		if lerr := a.includeIn(holder, res.ID); lerr != nil {
			res.Warnings = append(res.Warnings, lerr.Error())
		}
	}
	return res, nil
}

// includeIn makes the holder dossier include the dossier, once.
func (a *App) includeIn(holder, id string) error {
	m, err := a.Load(holder)
	if err != nil {
		return fmt.Errorf("in %s: %w", holder, err)
	}
	if m.ID == id || m.HasLink(dossier.RelIncludes, id) {
		return nil
	}
	_, err = a.Link(m.ID, id, dossier.RelIncludes)
	return err
}

func (a *App) open(p OpenParams) (OpenResult, error) {
	if strings.TrimSpace(p.Title) == "" {
		return OpenResult{}, spec.UserError("a dossier needs a title. Example: dossier open --title \"Armoire de pharmacie\" --instruction \"Demander une date de passage\"")
	}
	if p.SourceRef != "" {
		if d := a.FindBySource(p.SourceRef); d != nil {
			if d.State != dossier.Done {
				return OpenResult{ID: d.ID, Dir: d.Dir, Outcome: "existing", Session: d.Run.Session, TabID: d.Run.TabID}, nil
			}
			pending, err := a.SetState(d, "reopen", "signal "+p.SourceRef, "")
			if err != nil {
				return OpenResult{}, err
			}
			res, err := a.event(d, p.Instruction, p.Summary, p.Files, p.NoStart)
			res.Outcome, res.Pending = "reopened", pending
			return res, err
		}
	}
	if target, rest := a.route(p.Instruction); target != nil {
		if p.SourceRef != "" {
			target.AddSource(dossier.Source{Resource: p.URL, ID: p.SourceRef, Title: p.Title})
		}
		target.AddThread(p.ThreadRef)
		if err := target.Save(); err != nil {
			return OpenResult{}, err
		}
		pending, err := a.Wake(target, "addressed by "+orManual(p.SourceRef))
		if err != nil {
			return OpenResult{}, err
		}
		res, err := a.event(target, rest, p.Summary, p.Files, p.NoStart)
		res.Outcome, res.Pending = "routed", pending
		return res, err
	}

	alias := a.NormalizeAlias(p.Alias)
	if alias != "" {
		if !aliasRe.MatchString(alias) {
			return OpenResult{}, spec.UserError("alias %q must start with a letter and hold up to 16 letters, digits or dashes, e.g. RDIR", p.Alias)
		}
		if alias == "DESK" {
			return OpenResult{}, spec.UserError("alias DESK names the store's desk; choose another")
		}
		if other := a.byAlias(alias); other != nil {
			return OpenResult{}, spec.UserError("alias %s is already %s (%s)", alias, other.ID, other.Title)
		}
	}
	num, dir, err := a.S.NewDir(dossier.Slug(p.Title))
	if err != nil {
		return OpenResult{}, err
	}
	id := a.S.FormatID(num)
	d := dossier.Create(dir, id, p.Title)
	instruction := strings.TrimSpace(p.Instruction)
	if instruction == "" {
		instruction = a.S.Config.Prompt.DefaultInstruction
	}
	d.Alias = alias
	d.Description = firstLine(instruction)
	d.Resource = p.URL
	ref := p.SourceRef
	if ref == "" {
		ref = "manual:" + id
	}
	d.AddSource(dossier.Source{Resource: p.URL, ID: ref, Title: p.Title})
	d.AddThread(p.ThreadRef)
	d.SetBody("\n## Instruction\n\n" + instruction + "\n")
	paths, err := a.writeContext(d, p.Files)
	if err != nil {
		return OpenResult{}, err
	}
	if err := d.Save(); err != nil {
		return OpenResult{}, err
	}
	_ = d.Log("opened from %s", ref)
	text, err := a.renderPrompt(d, "open", instruction, p.Summary, paths)
	if err != nil {
		return OpenResult{}, err
	}
	res := OpenResult{ID: id, Dir: dir, Outcome: "created"}
	if p.NoStart {
		return res, d.Save()
	}
	if err := a.sendPrompt(d, text); err != nil {
		return res, fmt.Errorf("dossier %s created in %s, but its session did not start: %w. Retry with `dossier attach %s`", id, dir, err, id)
	}
	res.Session, res.TabID, res.Started = d.Run.Session, d.Run.TabID, true
	return res, nil
}

func orManual(s string) string {
	if s == "" {
		return "manual"
	}
	return s
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len([]rune(s)) > 160 {
		s = string([]rune(s)[:157]) + "…"
	}
	return s
}

// event drops new content into a dossier and prompts its session.
func (a *App) event(d *dossier.Dossier, note string, summary map[string]any, files []connector.File, noStart bool) (OpenResult, error) {
	if err := a.requireAction(d, "something new arrived"); err != nil {
		return OpenResult{}, err
	}
	paths, err := a.writeContext(d, files)
	if err != nil {
		return OpenResult{}, err
	}
	if note != "" {
		if summary == nil {
			summary = map[string]any{}
		}
		summary["note"] = note
	}
	if err := d.Save(); err != nil {
		return OpenResult{}, err
	}
	_ = d.Log("event: %s", firstLine(fmt.Sprint(summaryLine(summary))))
	text, err := a.renderPrompt(d, "event", note, summary, paths)
	if err != nil {
		return OpenResult{}, err
	}
	res := OpenResult{ID: d.ID, Dir: d.Dir}
	if noStart {
		return res, nil
	}
	if err := a.sendPrompt(d, text); err != nil {
		return res, err
	}
	res.Session, res.TabID, res.Started = d.Run.Session, d.Run.TabID, true
	return res, nil
}

var unsafeName = regexp.MustCompile(`[^\w.\-]+`)

// writeContext stores connector files under context/, one number per batch.
// Markdown files of the batch that point at a sibling ("./name") are rewritten
// to the numbered name.
func (a *App) writeContext(d *dossier.Dossier, files []connector.File) ([]string, error) {
	if len(files) == 0 {
		return nil, nil
	}
	if err := os.MkdirAll(d.Path("context"), 0o755); err != nil {
		return nil, err
	}
	d.Run.Contexts++
	prefix := fmt.Sprintf("%04d-", d.Run.Contexts)
	names := map[string]string{}
	for _, f := range files {
		base := unsafeName.ReplaceAllString(filepath.Base(f.Name), "-")
		names[f.Name] = prefix + base
		names[base] = prefix + base
	}
	var out []string
	for _, f := range files {
		target := d.Path("context", names[f.Name])
		var data []byte
		if f.Path != "" {
			b, err := os.ReadFile(f.Path)
			if err != nil {
				return nil, fmt.Errorf("connector file %s: %w", f.Path, err)
			}
			data = b
		} else {
			data = []byte(f.Content)
		}
		if strings.HasSuffix(target, ".md") {
			s := string(data)
			for from, to := range names {
				s = strings.ReplaceAll(s, "./"+from, "./"+to)
			}
			data = []byte(s)
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return nil, err
		}
		out = append(out, filepath.Join("context", names[f.Name]))
	}
	return out, nil
}

func summaryLine(summary map[string]any) string {
	if len(summary) == 0 {
		return ""
	}
	keys := make([]string, 0, len(summary))
	for k := range summary {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s: %v", k, summary[k]))
	}
	return strings.Join(parts, "; ")
}

func (a *App) renderPrompt(d *dossier.Dossier, kind, instruction string, summary map[string]any, files []string) (string, error) {
	tpl, err := a.S.PromptTemplate(kind)
	if err != nil {
		return "", err
	}
	fileList := "none"
	if len(files) > 0 {
		fileList = strings.Join(files, ", ")
	}
	sum := summaryLine(summary)
	if sum == "" {
		sum = "—"
	}
	r := strings.NewReplacer(
		"{{id}}", d.ID, "{{title}}", d.Title, "{{instruction}}", instruction,
		"{{summary}}", sum, "{{files}}", fileList, "{{url}}", d.Resource,
	)
	text := strings.TrimSpace(r.Replace(tpl)) + "\n"
	if err := os.MkdirAll(d.Path("prompts"), 0o755); err != nil {
		return "", err
	}
	d.Run.Prompts++
	name := fmt.Sprintf("%04d-%s.md", d.Run.Prompts, kind)
	fm := fmt.Sprintf("---\ntype: Prompt\ntitle: %s prompt %d of %s\ngenerated: { by: \"process:dossier\", at: %q }\n---\n", kind, d.Run.Prompts, d.ID, dossier.Now())
	if err := os.WriteFile(d.Path("prompts", name), []byte(fm+text), 0o644); err != nil {
		return "", err
	}
	return text, d.Save()
}

// ---------------------------------------------------------------- sessions

func (a *App) client(d *dossier.Dossier) (*acp.Client, error) {
	// herdr-acp takes the pane's current tab as the session's own: it renames
	// it and closes it with the session. A docked pane goes home first.
	if err := a.Undock(d); err != nil {
		return nil, err
	}
	if IsDesk(d) {
		if err := a.ensureDesk(d); err != nil {
			return nil, err
		}
	}
	cfg := a.S.Config
	settings, err := a.agentSettings()
	if err != nil {
		return nil, err
	}
	if rep := a.SyncSkills(); len(rep.Errors) > 0 {
		_ = d.Log("skills: %s", strings.Join(rep.Errors, "; "))
	}
	args := append([]string{}, cfg.Agent.Args...)
	args = append(args, "--name", d.ID, "--settings", settings)
	for _, dir := range cfg.Agent.AddDirs {
		args = append(args, "--add-dir", store.ExpandHome(dir))
	}
	if cfg.Agent.RemoteControl {
		args = append(args, "--remote-control", d.Label()+" · "+d.Title)
	}
	env := map[string]string{}
	for k, v := range cfg.ACP.Env {
		env[k] = store.ExpandHome(v)
	}
	if len(cfg.Agent.AddDirs) > 0 {
		env["CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD"] = "1"
	}
	env["DOSSIER_ID"] = d.ID
	env["DOSSIER_STORE"] = a.S.Root
	cmd := append([]string{}, cfg.ACP.Command...)
	if len(cmd) > 0 {
		cmd[0] = store.ExpandHome(cmd[0])
		for i := 1; i < len(cmd); i++ {
			cmd[i] = store.ExpandHome(cmd[i])
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	mcp := []acp.MCPServer{{Name: "dossier", Command: exe, Args: []string{"mcp"},
		Env: []acp.EnvEntry{{Name: "DOSSIER_ID", Value: d.ID}, {Name: "DOSSIER_STORE", Value: a.S.Root}}}}
	meta := map[string]any{}
	for k, v := range cfg.ACP.Meta {
		meta[k] = v
	}
	meta["tabLabel"] = TabLabel(d)
	return acp.Start(acp.Options{Command: cmd, AgentArgs: args, Env: env, Meta: meta, Cwd: d.Dir, MCP: mcp})
}

var (
	weekdaysFR = []string{"dimanche", "lundi", "mardi", "mercredi", "jeudi", "vendredi", "samedi"}
	monthsFR   = []string{"janvier", "février", "mars", "avril", "mai", "juin", "juillet", "août", "septembre", "octobre", "novembre", "décembre"}
)

// Stamp is the first line of every prompt: when dossier sent it, weekday
// included, so that a resumed session knows how much time has passed.
func Stamp(t time.Time, locale string) string {
	zone := t.Format("-07:00")
	if strings.HasPrefix(strings.ToLower(locale), "fr") {
		return fmt.Sprintf("[dossier · %s %d %s %d, %s (%s)]", weekdaysFR[t.Weekday()], t.Day(), monthsFR[t.Month()-1], t.Year(), t.Format("15:04"), zone)
	}
	return fmt.Sprintf("[dossier · %s %s (%s)]", t.Format("Monday 2 January 2006,"), t.Format("15:04"), zone)
}

// now is the clock of prompt stamps. Tests replace it.
var now = time.Now

func (a *App) sendPrompt(d *dossier.Dossier, text string) error {
	text = Stamp(now(), a.S.Config.Prompt.Locale) + "\n\n" + text
	c, err := a.client(d)
	if err != nil {
		return err
	}
	defer c.Close()
	if d.Run.Session == "" {
		sid, pl, err := c.NewSession()
		if err != nil {
			return err
		}
		d.Run.Session, d.Run.PaneID, d.Run.TabID = sid, pl.PaneID, pl.TabID
		_ = d.Log("session %s started in tab %s", sid, pl.TabID)
	} else {
		pl, err := c.LoadSession(d.Run.Session)
		if err != nil {
			return err
		}
		d.Run.PaneID, d.Run.TabID = pl.PaneID, pl.TabID
	}
	if err := d.Save(); err != nil {
		return err
	}
	renameTab(d.Run.TabID, TabLabel(d))
	if err := c.Prompt(d.Run.Session, text); err != nil {
		return err
	}
	_ = a.Archive(d)
	return nil
}

// Attach makes sure the session runs in a tab and focuses it. A dossier
// without a session gets one, with its open prompt unless noPrompt. The desk
// starts without a prompt: its CLAUDE.md says what it does.
func (a *App) Attach(d *dossier.Dossier, noPrompt bool) error {
	if err := a.ensureRunning(d, noPrompt); err != nil {
		return err
	}
	if d.Run.TabID != "" {
		_ = exec.Command("herdr", "tab", "focus", d.Run.TabID).Run()
	}
	return a.MarkSeen(d)
}

// ensureRunning starts or resumes the session so that its pane is alive.
func (a *App) ensureRunning(d *dossier.Dossier, noPrompt bool) error {
	if d.Run.Session == "" && (noPrompt || IsDesk(d)) {
		if err := a.startSilent(d); err != nil {
			return err
		}
	} else if d.Run.Session == "" {
		text, err := a.renderPrompt(d, "open", bodyInstruction(d), nil, contextFiles(d))
		if err != nil {
			return err
		}
		if err := a.sendPrompt(d, text); err != nil {
			return err
		}
	} else if !paneAlive(d.Run.PaneID) {
		if err := a.resume(d); err != nil {
			return err
		}
	}
	return nil
}

// startSilent starts a new session in a tab and sends it nothing.
func (a *App) startSilent(d *dossier.Dossier) error {
	c, err := a.client(d)
	if err != nil {
		return err
	}
	defer c.Close()
	sid, pl, err := c.NewSession()
	if err != nil {
		return err
	}
	d.Run.Session, d.Run.PaneID, d.Run.TabID = sid, pl.PaneID, pl.TabID
	_ = d.Log("session %s started in tab %s, without a prompt", sid, pl.TabID)
	if err := d.Save(); err != nil {
		return err
	}
	renameTab(d.Run.TabID, TabLabel(d))
	return nil
}

// resume loads the dossier's session into a new tab, without a prompt.
func (a *App) resume(d *dossier.Dossier) error {
	c, err := a.client(d)
	if err != nil {
		return err
	}
	pl, err := c.LoadSession(d.Run.Session)
	c.Close()
	if err != nil {
		return err
	}
	d.Run.PaneID, d.Run.TabID = pl.PaneID, pl.TabID
	if err := d.Save(); err != nil {
		return err
	}
	renameTab(d.Run.TabID, TabLabel(d))
	return nil
}

// SessionsReport counts the store's sessions running in a tab and names the
// open dossiers whose session has none.
type SessionsReport struct {
	Running int      `json:"running"`
	Cap     int      `json:"cap"`
	Stopped []string `json:"stopped"`
}

// Sessions reports without changing anything. See Reconcile.
func (a *App) Sessions() SessionsReport {
	_, rep := a.sessions()
	return rep
}

func (a *App) sessions() ([]*dossier.Dossier, SessionsReport) {
	rep := SessionsReport{Cap: a.S.Config.SessionCap(), Stopped: []string{}}
	all, _ := a.All()
	live := panes()
	var stopped []*dossier.Dossier
	for _, d := range all {
		if d.Run.Session == "" {
			continue
		}
		if _, ok := live[d.Run.PaneID]; ok && d.Run.PaneID != "" {
			rep.Running++
		} else if d.State == dossier.Open {
			stopped = append(stopped, d)
		}
	}
	sort.SliceStable(stopped, func(i, j int) bool { return stopped[i].Updated > stopped[j].Updated })
	for _, d := range stopped {
		rep.Stopped = append(rep.Stopped, d.ID)
	}
	return stopped, rep
}

// Reconcile resumes, in a tab, every open dossier whose session lost its tab,
// most recently updated first, while fewer than the cap run. Dossiers in
// handled were just started or prompted: herdr may not list their pane yet.
func (a *App) Reconcile(handled map[string]bool) IngestReport {
	rep := IngestReport{Source: "sessions"}
	stopped, s := a.sessions()
	for _, d := range stopped {
		if handled[d.ID] {
			continue
		}
		if s.Running >= s.Cap {
			rep.Skipped = append(rep.Skipped, fmt.Sprintf("%s has no tab: %d sessions already run (lifecycle.max_sessions)", d.ID, s.Running))
			continue
		}
		rep.Events++
		if err := a.resume(d); err != nil {
			rep.Errors = append(rep.Errors, d.ID+": "+err.Error())
			continue
		}
		s.Running++
		_ = d.Log("session resumed in tab %s: the dossier is open", d.Run.TabID)
		rep.Opened = append(rep.Opened, OpenResult{ID: d.ID, Dir: d.Dir, Outcome: "resumed", Session: d.Run.Session, TabID: d.Run.TabID})
	}
	// A desk once started stays available, outside the cap.
	if d := a.Desk(); d.Run.Session != "" && !handled[d.ID] && !paneAlive(d.Run.PaneID) {
		rep.Events++
		if err := a.resume(d); err != nil {
			rep.Errors = append(rep.Errors, d.ID+": "+err.Error())
		} else {
			_ = d.Log("session resumed in tab %s: the desk stays available", d.Run.TabID)
			rep.Opened = append(rep.Opened, OpenResult{ID: d.ID, Dir: d.Dir, Outcome: "resumed", Session: d.Run.Session, TabID: d.Run.TabID})
		}
	}
	return rep
}

func bodyInstruction(d *dossier.Dossier) string {
	if d.Description != "" {
		return d.Description
	}
	return d.Title
}

func contextFiles(d *dossier.Dossier) []string {
	entries, _ := os.ReadDir(d.Path("context"))
	var out []string
	for _, e := range entries {
		out = append(out, filepath.Join("context", e.Name()))
	}
	return out
}

func (a *App) Prompt(d *dossier.Dossier, text string) error {
	if IsDesk(d) {
		if err := a.ensureDesk(d); err != nil {
			return err
		}
	}
	if err := a.requireAction(d, "prompted"); err != nil {
		return err
	}
	if err := d.Save(); err != nil {
		return err
	}
	_ = d.Log("prompt: %s", firstLine(text))
	return a.sendPrompt(d, text)
}

// closeTabLater starts a detached `dossier internal-closetab` that waits, then
// closes the session's tab.
func (a *App) closeTabLater(d *dossier.Dossier, delay time.Duration) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "closetab", d.ID, "--store", a.S.Root, "--delay", delay.String())
	cmd.Env = os.Environ()
	for i, kv := range cmd.Env {
		if strings.HasPrefix(kv, "DOSSIER_ID=") {
			cmd.Env[i] = "DOSSIER_ID="
		}
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// Restart closes the session's tab and resumes it in a new one, so that the
// agent gets a fresh process: current binary, MCP server and environment.
func (a *App) Restart(d *dossier.Dossier) error {
	if d.Run.Session == "" {
		return spec.UserError("%s has no session yet. Start one with `dossier attach %s`", d.ID, d.ID)
	}
	if a.S.Config.ClosesTabOn(d.State) && !paneAlive(d.Run.PaneID) {
		// Its tab stays closed in this state; the next wake starts the current binary anyway.
		return nil
	}
	_ = a.Archive(d)
	if err := a.closeSession(d); err != nil {
		return err
	}
	d.Run.PaneID, d.Run.TabID = "", ""
	// herdr-acp may still be closing the old tab: retry the resume briefly.
	var pl acp.Placement
	for attempt := 0; ; attempt++ {
		c, err := a.client(d)
		if err != nil {
			return err
		}
		pl, err = c.LoadSession(d.Run.Session)
		c.Close()
		if err == nil {
			break
		}
		if attempt == 2 {
			_ = d.Save()
			return fmt.Errorf("%w. The session is closed but intact: resume it with `dossier attach %s`", err, d.ID)
		}
		time.Sleep(3 * time.Second)
	}
	d.Run.PaneID, d.Run.TabID = pl.PaneID, pl.TabID
	renameTab(d.Run.TabID, TabLabel(d))
	_ = d.Log("session restarted in tab %s", pl.TabID)
	return d.Save()
}

// Track attaches a source reference (a thread) to a dossier.
func (a *App) Track(id, ref string) (map[string]any, error) {
	d, err := a.Load(id)
	if err != nil {
		return nil, err
	}
	name, rest, ok := strings.Cut(ref, ":")
	if !ok || name == "" || rest == "" {
		return nil, spec.UserError("%q is not a source reference. Use <source>:<kind>/<id>, e.g. gmail:thread/1a0d7b2f4c0fff93", ref)
	}
	if _, ok := a.S.Config.Source(name); !ok {
		return nil, spec.UserError("no [[source]] named %q in this store; declared: %s", name, sourceNames(a.S.Config.Sources))
	}
	if other := a.FindBySource(ref); other != nil && other.ID != d.ID {
		return nil, spec.UserError("%s already belongs to %s. Merge the dossiers instead: merge %s into %s", ref, other.ID, d.ID, other.ID)
	}
	d.AddSource(dossier.Source{ID: ref})
	d.AddThread(ref)
	if err := d.Save(); err != nil {
		return nil, err
	}
	_ = d.Log("tracking %s", ref)
	return map[string]any{"id": d.ID, "sources": d.Sources}, nil
}

// CloseTab closes the dossier's tab, keeping the session resumable.
func (a *App) CloseTab(d *dossier.Dossier) error {
	if err := a.closeSession(d); err != nil {
		return err
	}
	return d.Save()
}

func (a *App) closeSession(d *dossier.Dossier) error {
	if d.Run.Session == "" || !paneAlive(d.Run.PaneID) {
		return nil
	}
	c, err := a.client(d)
	if err != nil {
		return err
	}
	defer c.Close()
	if _, err := c.LoadSession(d.Run.Session); err != nil {
		return err
	}
	if err := c.CloseSession(d.Run.Session); err != nil {
		return err
	}
	d.Run.PaneID, d.Run.TabID = "", ""
	return nil
}

type paneInfo struct {
	PaneID      string `json:"pane_id"`
	AgentStatus string `json:"agent_status"`
}

const tabTitleLength = 30

// TabLabel names a dossier's tab: its id and the start of its title.
func TabLabel(d *dossier.Dossier) string {
	t := []rune(strings.TrimSpace(d.Title))
	if len(t) > tabTitleLength {
		t = append([]rune(strings.TrimSpace(string(t[:tabTitleLength-1]))), '…')
	}
	return d.Label() + " · " + string(t)
}

// renameTab labels a herdr tab. Tests replace it.
var renameTab = func(tabID, label string) {
	if tabID != "" {
		_ = exec.Command("herdr", "tab", "rename", tabID, label).Run()
	}
}

// panes lists herdr panes and their agent status. Tests replace it.
var panes = func() map[string]string {
	out := map[string]string{}
	b, err := exec.Command("herdr", "pane", "list").Output()
	if err != nil {
		return out
	}
	var r struct {
		Result struct {
			Panes []paneInfo `json:"panes"`
		} `json:"result"`
	}
	if json.Unmarshal(b, &r) == nil {
		for _, p := range r.Result.Panes {
			out[p.PaneID] = p.AgentStatus
		}
	}
	return out
}

func paneAlive(id string) bool {
	if id == "" {
		return false
	}
	_, ok := panes()[id]
	return ok
}

// Activity says what the agent does now: working, idle or stopped.
func Activity(d *dossier.Dossier, all map[string]string) string {
	if d.Run.Session == "" {
		return "none"
	}
	st, ok := all[d.Run.PaneID]
	if !ok {
		return "stopped"
	}
	switch st {
	case "", "unknown":
		return "idle"
	case "done":
		// herdr: the turn ended and the agent waits for the user.
		return "ready"
	}
	return st
}

func Panes() map[string]string { return panes() }

// ---------------------------------------------------------------- transcripts

func transcriptOf(session string) string {
	if session == "" {
		return ""
	}
	matches, _ := filepath.Glob(filepath.Join(store.ExpandHome("~/.claude/projects"), "*", session+".jsonl"))
	if len(matches) == 0 {
		return ""
	}
	return matches[0]
}

// Archive copies the live transcript into the dossier when it is newer.
func (a *App) Archive(d *dossier.Dossier) error {
	src := transcriptOf(d.Run.Session)
	if src == "" {
		return nil
	}
	return copyIfNewer(src, d.Path("transcript.jsonl"))
}

func ArchiveFrom(src string, d *dossier.Dossier) error {
	return copyIfNewer(src, d.Path("transcript.jsonl"))
}

func copyIfNewer(src, dst string) error {
	si, err := os.Stat(src)
	if err != nil {
		return err
	}
	if di, err := os.Stat(dst); err == nil && di.Size() >= si.Size() && !di.ModTime().Before(si.ModTime()) {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// TranscriptPaths returns the archived and the live transcripts, when present.
func TranscriptPaths(d *dossier.Dossier) []string {
	out := archivedTranscripts(d)
	if _, err := os.Stat(d.Path("transcript.jsonl")); err == nil {
		out = append(out, d.Path("transcript.jsonl"))
	}
	if live := transcriptOf(d.Run.Session); live != "" {
		out = append(out, live)
	}
	return out
}
