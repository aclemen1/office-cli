package app

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aclemen1/office-cli/internal/dossier"
	"github.com/aclemen1/office-cli/internal/office"
	"github.com/aclemen1/office-cli/internal/spec"
)

// The desk lives in <office>/desk/. Office.Dirs skips it: its name has no number.
const deskDir = "desk"

func (a *App) DeskID() string { return a.S.Prefix() + "-DESK" }

func (a *App) isDesk(id string) bool { return id != "" && a.NormalizeAlias(id) == "DESK" }

func IsDesk(d *dossier.Dossier) bool { return d.State == dossier.Desk }

// Desk loads the office's desk. A desk never started exists only in memory.
func (a *App) Desk() *dossier.Dossier {
	dir := filepath.Join(a.S.Root, deskDir)
	if d, err := dossier.Load(dir); err == nil {
		return d
	}
	d := dossier.Create(dir, a.DeskID(), "desk · "+a.S.Config.Office.Sphere)
	d.State = dossier.Desk
	return d
}

// ensureDesk writes the desk's directory, charter and dossier.md once.
func (a *App) ensureDesk(d *dossier.Dossier) error {
	if err := os.MkdirAll(d.Dir, 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(d.Path("CLAUDE.md")); os.IsNotExist(err) {
		if err := os.WriteFile(d.Path("CLAUDE.md"), []byte(office.DeskCharter), 0o644); err != nil {
			return err
		}
	}
	if _, err := os.Stat(d.Path("dossier.md")); os.IsNotExist(err) {
		if err := d.Save(); err != nil {
			return err
		}
		_ = d.Log("desk created")
	}
	return nil
}

// LoadAny resolves an id like Load, and also "desk" or U-DESK to the desk.
func (a *App) LoadAny(id string) (*dossier.Dossier, error) {
	if id == "" {
		id = os.Getenv("DOSSIER_ID")
	}
	if a.isDesk(id) {
		return a.Desk(), nil
	}
	return a.Load(id)
}

func (a *App) deskError() error {
	return spec.UserError("%s is the office's desk: it has no state, links or sources. Name a dossier, or use `office desk`", a.DeskID())
}

// ActingDesk is the desk when the current session is this office's desk.
func (a *App) ActingDesk() *dossier.Dossier {
	if os.Getenv("DOSSIER_ID") != a.DeskID() {
		return nil
	}
	return a.Desk()
}

// NewDeskConversation archives the desk's conversation and starts a fresh one.
func (a *App) NewDeskConversation(d *dossier.Dossier) error {
	if d.Run.Session == "" {
		return nil
	}
	if err := a.closeSession(d); err != nil {
		return err
	}
	_ = a.Archive(d)
	if err := os.MkdirAll(d.Path("transcripts"), 0o755); err != nil {
		return err
	}
	n := len(archivedTranscripts(d)) + 1
	if _, err := os.Stat(d.Path("transcript.jsonl")); err == nil {
		if err := os.Rename(d.Path("transcript.jsonl"), d.Path("transcripts", fmt.Sprintf("%04d.jsonl", n))); err != nil {
			return err
		}
	}
	_ = d.Log("conversation %d archived (session %s)", n, d.Run.Session)
	d.Run.Session, d.Run.PaneID, d.Run.TabID = "", "", ""
	return d.Save()
}

func archivedTranscripts(d *dossier.Dossier) []string {
	m, _ := filepath.Glob(d.Path("transcripts", "*.jsonl"))
	sort.Strings(m)
	return m
}

// Escalations wait in <desk>/escalations/<stamp>_<from>.md until the desk's
// session is idle; delivered ones move to escalations/delivered/.
const escalationsDir = "escalations"

type Escalation struct {
	File   string `json:"file"`
	From   string `json:"from"`
	Text   string `json:"text"`
	Status string `json:"status"` // pending (not yet shown to the desk), delivered, resolved
}

const (
	escalationPending   = "pending"
	escalationDelivered = "delivered"
	escalationResolved  = "resolved"
)

type EscalateResult struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Delivered bool   `json:"delivered"`
	Pending   int    `json:"pending"`
}

// Escalate queues a dossier's request for the desk, then delivers the queue
// if the desk is idle.
func (a *App) Escalate(from *dossier.Dossier, text string) (EscalateResult, error) {
	d := a.Desk()
	res := EscalateResult{From: from.ID, To: d.ID}
	if IsDesk(from) {
		return res, spec.UserError("the desk cannot escalate to itself")
	}
	if err := a.ensureDesk(d); err != nil {
		return res, err
	}
	if err := os.MkdirAll(d.Path(escalationsDir), 0o755); err != nil {
		return res, err
	}
	name := now().Format("20060102-150405.000000000") + "_" + from.ID + ".md"
	body := fmt.Sprintf("From dossier %s (%s): %s\n", from.ID, from.Title, text)
	if err := os.WriteFile(d.Path(escalationsDir, name), []byte(body), 0o644); err != nil {
		return res, err
	}
	_ = d.Log("escalation from %s: %s", from.ID, text)
	_ = from.Log("escalated to %s · %s", d.ID, text)
	n, err := a.DeliverEscalations()
	if err != nil {
		return res, err
	}
	res.Delivered = n > 0
	res.Pending = len(Escalations(d))
	return res, nil
}

// Escalations lists the desk's pending escalations, oldest first.
func Escalations(d *dossier.Dossier) []Escalation {
	return escalationsIn(d, escalationPending)
}

// OpenEscalations lists the escalations the desk has not resolved yet,
// delivered or not, oldest first.
func OpenEscalations(d *dossier.Dossier) []Escalation {
	out := append(escalationsIn(d, escalationDelivered), escalationsIn(d, escalationPending)...)
	sort.Slice(out, func(i, j int) bool { return out[i].File < out[j].File })
	return out
}

func escalationsIn(d *dossier.Dossier, status string) []Escalation {
	dir := d.Path(escalationsDir)
	if status != escalationPending {
		dir = d.Path(escalationsDir, status)
	}
	m, _ := filepath.Glob(filepath.Join(dir, "*.md"))
	sort.Strings(m)
	out := []Escalation{}
	for _, p := range m {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		base := strings.TrimSuffix(filepath.Base(p), ".md")
		_, from, _ := strings.Cut(base, "_")
		out = append(out, Escalation{File: filepath.Base(p), From: from, Text: strings.TrimSpace(string(b)), Status: status})
	}
	return out
}

func (e Escalation) path(d *dossier.Dossier) string {
	if e.Status == escalationPending {
		return d.Path(escalationsDir, e.File)
	}
	return d.Path(escalationsDir, e.Status, e.File)
}

type ResolveResult struct {
	File     string `json:"file"`
	From     string `json:"from"`
	Prompted bool   `json:"prompted"`
	Open     int    `json:"open"`
}

// Resolve records the desk's decision on an open escalation, named by its
// file or by the dossier it came from, tells that dossier, and files the
// escalation under escalations/resolved/.
func (a *App) Resolve(ref, decision string) (ResolveResult, error) {
	d := a.Desk()
	var res ResolveResult
	if strings.TrimSpace(decision) == "" {
		return res, spec.UserError("a resolution needs --decision")
	}
	refID := ""
	if x, err := a.Load(ref); err == nil {
		refID = x.ID
	}
	var hits []Escalation
	for _, e := range OpenEscalations(d) {
		if e.File == ref || strings.TrimSuffix(e.File, ".md") == ref || (refID != "" && e.From == refID) {
			hits = append(hits, e)
		}
	}
	switch {
	case len(hits) == 0:
		return res, spec.UserError("no open escalation matches %q. List them with `office escalations`", ref)
	case len(hits) > 1:
		files := make([]string, len(hits))
		for i, e := range hits {
			files[i] = e.File
		}
		return res, spec.UserError("%d open escalations match %q; name one by its file: %s", len(hits), ref, strings.Join(files, ", "))
	}
	e := hits[0]
	res.File, res.From = e.File, e.From
	if err := os.MkdirAll(d.Path(escalationsDir, escalationResolved), 0o755); err != nil {
		return res, err
	}
	body := e.Text + "\n\nDecision (" + now().Format("2006-01-02 15:04") + "): " + decision + "\n"
	if err := os.WriteFile(d.Path(escalationsDir, escalationResolved, e.File), []byte(body), 0o644); err != nil {
		return res, err
	}
	if err := os.Remove(e.path(d)); err != nil {
		return res, err
	}
	_ = d.Log("escalation from %s resolved · %s", e.From, decision)
	if to, err := a.Load(e.From); err == nil {
		res.Prompted, err = a.Tell(d, to, "Decision on your escalation: "+decision)
		if err != nil {
			return res, err
		}
	}
	res.Open = len(OpenEscalations(d))
	return res, nil
}

// Tell logs an event from one dossier in another, wakes it and prompts its
// session; it reports whether a session got the prompt.
func (a *App) Tell(from, to *dossier.Dossier, text string) (bool, error) {
	_ = to.Log("from %s: %s", from.ID, text)
	if IsDesk(from) {
		_ = from.Log("notified %s · %s", to.ID, text)
	}
	if _, err := a.Wake(to, "notified by "+from.ID); err != nil {
		return false, err
	}
	if to.Run.Session == "" {
		return false, to.Save()
	}
	return true, a.Prompt(to, fmt.Sprintf("From dossier %s (%s): %s", from.ID, from.Title, text))
}

// DeliverEscalations sends the pending escalations to the desk in one prompt
// when its session is idle or waits for the user. A busy, stopped or not yet
// started desk keeps them for the next ingest.
func (a *App) DeliverEscalations() (int, error) {
	d := a.Desk()
	pending := Escalations(d)
	if len(pending) == 0 || d.Run.Session == "" {
		return 0, nil
	}
	if act := Activity(d, Panes()); act != "idle" && act != "ready" {
		return 0, nil
	}
	var b strings.Builder
	b.WriteString("Escalations from dossiers. Present each one to the user; adopt a rule or change a skill only with their agreement.\n")
	for _, e := range pending {
		b.WriteString("\n- " + e.Text)
	}
	if err := a.Prompt(d, b.String()); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(d.Path(escalationsDir, escalationDelivered), 0o755); err != nil {
		return 0, err
	}
	for _, e := range pending {
		_ = os.Rename(d.Path(escalationsDir, e.File), d.Path(escalationsDir, escalationDelivered, e.File))
		_ = d.Log("escalation from %s delivered", e.From)
	}
	return len(pending), nil
}

// Conversation describes the session's current conversation.
type Conversation struct {
	Started     time.Time `json:"started"`
	Compactions int       `json:"compactions"`
}

// ConversationOf reads the live transcript, or its archived copy.
func ConversationOf(d *dossier.Dossier) Conversation {
	path := transcriptOf(d.Run.Session)
	if path == "" {
		path = d.Path("transcript.jsonl")
	}
	var c Conversation
	f, err := os.Open(path)
	if err != nil {
		return c
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var rec struct {
			Subtype   string `json:"subtype"`
			Timestamp string `json:"timestamp"`
		}
		if json.Unmarshal(sc.Bytes(), &rec) != nil {
			continue
		}
		if c.Started.IsZero() && rec.Timestamp != "" {
			c.Started, _ = time.Parse(time.RFC3339Nano, rec.Timestamp)
		}
		if rec.Subtype == "compact_boundary" {
			c.Compactions++
		}
	}
	return c
}

// ResolvedEscalations lists the escalations the desk has resolved, oldest first.
func ResolvedEscalations(d *dossier.Dossier) []Escalation {
	return escalationsIn(d, escalationResolved)
}
