package app

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aclemen1/dossier-cli/internal/connector"
	"github.com/aclemen1/dossier-cli/internal/dossier"
	"github.com/aclemen1/dossier-cli/internal/spec"
	"github.com/aclemen1/dossier-cli/internal/store"
)

var moves = map[string]struct {
	from []string
	to   string
}{
	"wait":   {[]string{dossier.Open}, dossier.Waiting},
	"resume": {[]string{dossier.Waiting}, dossier.Open},
	"reopen": {[]string{dossier.Done}, dossier.Open},
	"close":  {[]string{dossier.Open, dossier.Waiting}, dossier.Done},
}

var verbHint = map[string]string{
	dossier.Open:    "`dossier wait <id> --on <who>` or `dossier close <id>`",
	dossier.Waiting: "`dossier resume <id>` or `dossier close <id>`",
	dossier.Done:    "`dossier reopen <id>`",
	dossier.Merged:  "the dossier it was merged into",
}

// SetState applies a move, logs it, reflects it in every source and applies
// the lifecycle rules. It returns the number of transitions left pending.
func (a *App) SetState(d *dossier.Dossier, move, note, waitingOn string) (int, error) {
	m := moves[move]
	ok := false
	for _, f := range m.from {
		if d.State == f {
			ok = true
		}
	}
	if !ok {
		return 0, spec.UserError("%s is %s; `%s` applies to %s dossiers. Use %s", d.ID, d.State, move, strings.Join(m.from, " or "), verbHint[d.State])
	}
	from := d.State
	d.State = m.to
	d.NoAction = false
	if m.to == dossier.Waiting {
		d.WaitingOn = waitingOn
	} else {
		d.WaitingOn, d.WaitUntil = "", ""
	}
	line := from + " → " + m.to
	if waitingOn != "" {
		line += " · on " + waitingOn
	}
	if m.to == dossier.Waiting && d.WaitUntil != "" {
		line += " · until " + d.WaitUntil
	}
	if note != "" {
		line += " · " + note
	}
	_ = d.Log("%s", line)
	if m.to == dossier.Done {
		_ = a.Archive(d)
	}
	pending := a.reflect(d, from, m.to, note)
	if m.to == dossier.Done {
		a.notifyDependents(d)
	}
	if err := d.Save(); err != nil {
		return pending, err
	}
	if a.S.Config.ClosesTabOn(m.to) {
		_ = a.Archive(d)
		if os.Getenv("DOSSIER_ID") == d.ID {
			// The session closes its own dossier: let it finish its turn first.
			if err := a.closeTabLater(d, 8*time.Second); err != nil {
				_ = d.Log("tab not closed: %v", err)
			}
		} else if err := a.closeSession(d); err != nil {
			_ = d.Log("tab not closed: %v", err)
		}
	}
	return pending, d.Save()
}

// Park marks an open dossier as needing no action from the user for now.
// Anything new on it clears the mark: see requireAction.
func (a *App) Park(d *dossier.Dossier, note string) error {
	if d.State != dossier.Open {
		return spec.UserError("%s is %s; only an open dossier can be marked as needing no action", d.ID, d.State)
	}
	if d.NoAction {
		return nil
	}
	d.NoAction = true
	line := "no action required"
	if note != "" {
		line += " · " + note
	}
	_ = d.Log("%s", line)
	return d.Save()
}

// requireAction clears the no-action mark, saying why.
func (a *App) requireAction(d *dossier.Dossier, why string) error {
	if !d.NoAction {
		return nil
	}
	d.NoAction = false
	_ = d.Log("action required · %s", why)
	return d.Save()
}

// Unpark clears the no-action mark by hand.
func (a *App) Unpark(d *dossier.Dossier, note string) error {
	if note == "" {
		note = "marked by hand"
	}
	return a.requireAction(d, note)
}

// CorrectWait changes whom a waiting dossier waits on and until when. The
// state does not move, so the sources see no transition.
func (a *App) CorrectWait(d *dossier.Dossier, waitingOn, until, note string) error {
	d.WaitingOn, d.WaitUntil = waitingOn, until
	line := "wait corrected · on " + waitingOn
	if until != "" {
		line += " · until " + until
	}
	if note != "" {
		line += " · " + note
	}
	_ = d.Log("%s", line)
	return d.Save()
}

// Wake brings a dossier back to open: reopens it when done, resumes it when
// waiting. Anything new for a dossier goes through it.
func (a *App) Wake(d *dossier.Dossier, reason string) (int, error) {
	switch d.State {
	case dossier.Done:
		return a.SetState(d, "reopen", reason, "")
	case dossier.Waiting:
		return a.SetState(d, "resume", reason, "")
	}
	return 0, nil
}

// ParseUntil reads the end of a wait: a date (2026-10-09, end of that day), an
// RFC 3339 instant, a duration in days or hours (7d, 48h), or "none".
func ParseUntil(s string, now time.Time) (string, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	switch {
	case s == "none" || s == "aucun" || s == "aucune":
		return "", nil
	case strings.HasSuffix(s, "d"):
		if n, err := strconv.Atoi(strings.TrimSuffix(s, "d")); err == nil && n > 0 {
			return now.AddDate(0, 0, n).Format(time.RFC3339), nil
		}
	case strings.HasSuffix(s, "h"):
		if n, err := strconv.Atoi(strings.TrimSuffix(s, "h")); err == nil && n > 0 {
			return now.Add(time.Duration(n) * time.Hour).Format(time.RFC3339), nil
		}
	}
	if t, err := time.ParseInLocation("2006-01-02", s, now.Location()); err == nil {
		return t.Add(24*time.Hour - time.Second).Format(time.RFC3339), nil
	}
	if t, err := time.Parse(time.RFC3339, strings.ToUpper(s)); err == nil {
		return t.Format(time.RFC3339), nil
	}
	return "", spec.UserError("--until takes a date (2026-10-09), a duration (7d, 48h) or none; got %q. Example: dossier wait --on \"Baer SA\" --until 7d", s)
}

// DefaultWait is the store's wait before a chase prompt: [lifecycle] default_wait, 7d otherwise.
func (a *App) DefaultWait() string {
	if w := strings.TrimSpace(a.S.Config.Lifecycle.DefaultWait); w != "" {
		return w
	}
	return "7d"
}

// Deadlines wakes every waiting dossier whose wait_until has passed and asks
// its session whether to chase.
func (a *App) Deadlines(now time.Time) IngestReport {
	rep := IngestReport{Source: "deadlines"}
	all, _ := a.All()
	for _, d := range all {
		if d.State != dossier.Waiting || d.WaitUntil == "" {
			continue
		}
		until, err := time.Parse(time.RFC3339, d.WaitUntil)
		if err != nil || now.Before(until) {
			continue
		}
		rep.Events++
		waitingOn, since := d.WaitingOn, d.WaitUntil
		if _, err := a.Wake(d, "no answer by "+since); err != nil {
			rep.Errors = append(rep.Errors, d.ID+": "+err.Error())
			continue
		}
		summary := map[string]any{"waiting_on": waitingOn, "until": since}
		text, err := a.renderPrompt(d, "deadline", "", summary, nil)
		if err != nil {
			rep.Errors = append(rep.Errors, d.ID+": "+err.Error())
			continue
		}
		if d.Run.Session != "" {
			if err := a.sendPrompt(d, text); err != nil {
				rep.Errors = append(rep.Errors, d.ID+": "+err.Error())
				continue
			}
		}
		rep.Opened = append(rep.Opened, OpenResult{ID: d.ID, Dir: d.Dir, Outcome: "deadline", Session: d.Run.Session, TabID: d.Run.TabID})
	}
	return rep
}

// reflect calls every source's transition. Failures are kept for `retry`.
func (a *App) reflect(d *dossier.Dossier, from, to, note string) int {
	for _, src := range d.Sources {
		name := src.Name()
		if name == "manual" || name == "" {
			continue
		}
		t := dossier.Transition{Source: name, SourceRef: src.ID, ThreadRef: threadFor(d, name), From: from, To: to, Note: note, At: dossier.Now()}
		cfg, ok := a.S.Config.Source(name)
		if !ok {
			t.Error = "no [[source]] named " + name + " in the store config"
			d.Run.PendingTransitions = append(d.Run.PendingTransitions, t)
			continue
		}
		if err := (connector.Runner{Store: a.S, Source: cfg}).Transition(t.SourceRef, t.ThreadRef, from, to, note, connector.Dossier{ID: d.ID, Title: d.Title, WaitingOn: d.WaitingOn}); err != nil {
			t.Error = err.Error()
			d.Run.PendingTransitions = append(d.Run.PendingTransitions, t)
			_ = d.Log("source %s: %s → %s pending (%s)", name, from, to, firstLine(err.Error()))
		}
	}
	return len(d.Run.PendingTransitions)
}

func threadFor(d *dossier.Dossier, source string) string {
	for _, t := range d.Threads {
		if strings.HasPrefix(t, source+":") {
			return t
		}
	}
	return ""
}

// Retry replays pending transitions, oldest first.
func (a *App) Retry(d *dossier.Dossier) int {
	todo := d.Run.PendingTransitions
	d.Run.PendingTransitions = nil
	for _, t := range todo {
		cfg, ok := a.S.Config.Source(t.Source)
		if ok {
			if err := (connector.Runner{Store: a.S, Source: cfg}).Transition(t.SourceRef, t.ThreadRef, t.From, t.To, t.Note, connector.Dossier{ID: d.ID, Title: d.Title}); err == nil {
				_ = d.Log("source %s: %s → %s delivered on retry", t.Source, t.From, t.To)
				continue
			} else {
				t.Error = err.Error()
			}
		}
		d.Run.PendingTransitions = append(d.Run.PendingTransitions, t)
	}
	_ = d.Save()
	return len(d.Run.PendingTransitions)
}

// ---------------------------------------------------------------- search

type Hit struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	State   string `json:"state"`
	Updated string `json:"updated"`
	Score   int    `json:"score"`
	Snippet string `json:"snippet"`
	File    string `json:"file"`
}

func (a *App) Search(query string, states []string, withTranscripts bool) ([]Hit, error) {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return nil, spec.UserError("search needs words. Example: dossier search \"armoire pharmacie\"")
	}
	all, err := a.All()
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, s := range states {
		if s != "all" {
			want[s] = true
		}
	}
	var hits []Hit
	for _, d := range all {
		if len(want) > 0 && !want[d.State] {
			continue
		}
		files := []string{d.Path("dossier.md")}
		for _, sub := range []string{"context", "files"} {
			m, _ := filepath.Glob(d.Path(sub, "*.md"))
			files = append(files, m...)
		}
		if withTranscripts {
			files = append(files, TranscriptPaths(d)...)
		}
		best := Hit{ID: d.ID, Title: d.Title, State: d.State, Updated: d.Updated}
		found := map[string]bool{}
		for _, f := range files {
			score, snippet := scoreFile(f, words, found)
			if score > best.Score {
				best.Score, best.Snippet = score, snippet
				best.File, _ = filepath.Rel(a.S.Root, f)
			}
		}
		if len(found) == len(words) {
			best.Score += 100 * len(words)
			hits = append(hits, best)
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Updated > hits[j].Updated
	})
	return hits, nil
}

func scoreFile(path string, words []string, found map[string]bool) (int, string) {
	f, err := os.Open(path)
	if err != nil {
		return 0, ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	score, bestLine, bestLineScore := 0, "", 0
	for sc.Scan() {
		line := strings.ToLower(sc.Text())
		n := 0
		for _, w := range words {
			if strings.Contains(line, w) {
				found[w] = true
				n++
			}
		}
		score += n
		if n > bestLineScore {
			bestLineScore, bestLine = n, strings.TrimSpace(sc.Text())
		}
	}
	if len([]rune(bestLine)) > 200 {
		bestLine = string([]rune(bestLine)[:197]) + "…"
	}
	return score, bestLine
}

// ---------------------------------------------------------------- ingest

type IngestReport struct {
	Source  string       `json:"source"`
	Signals int          `json:"signals"`
	Events  int          `json:"events"`
	Opened  []OpenResult `json:"opened"`
	Skipped []string     `json:"skipped,omitempty"`
	Errors  []string     `json:"errors,omitempty"`
	Cursor  string       `json:"cursor,omitempty"`
}

func (a *App) cursors() map[string]string {
	out := map[string]string{}
	if b, err := os.ReadFile(a.S.Meta("cursors.json")); err == nil {
		_ = json.Unmarshal(b, &out)
	}
	return out
}

func (a *App) saveCursor(source, cursor string) error {
	c := a.cursors()
	c[source] = cursor
	b, _ := json.MarshalIndent(c, "", "  ")
	tmp := a.S.Meta("cursors.json.tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, a.S.Meta("cursors.json"))
}

func (a *App) Ingest(names []string, opt connector.PollOptions) ([]IngestReport, error) {
	dryRun := opt.DryRun
	sources := a.S.Config.Sources
	if len(names) > 0 {
		sources = nil
		for _, n := range names {
			cfg, ok := a.S.Config.Source(n)
			if !ok {
				return nil, spec.UserError("no [[source]] named %q in %s. Declared: %s", n, a.S.Meta("config.toml"), sourceNames(a.S.Config.Sources))
			}
			sources = append(sources, cfg)
		}
	}
	if len(sources) == 0 {
		return nil, spec.UserError("no [[source]] declared in %s. Add one, for example name = \"gmail\" with its command", a.S.Meta("config.toml"))
	}
	var reports []IngestReport
	if !dryRun {
		if err := a.S.Lock(); err != nil {
			return nil, err
		}
		if dl := a.Deadlines(time.Now()); dl.Events > 0 || len(dl.Errors) > 0 {
			reports = append(reports, dl)
		}
		a.S.Unlock()
	}
	all, _ := a.All()
	for _, src := range sources {
		rep := IngestReport{Source: src.Name}
		var watch []string
		for _, d := range all {
			if d.State == dossier.Merged {
				continue
			}
			for _, t := range d.Threads {
				if strings.HasPrefix(t, src.Name+":") {
					watch = append(watch, t)
				}
			}
		}
		res, err := (connector.Runner{Store: a.S, Source: src}).Poll(a.cursors()[src.Name], watch, opt)
		if err != nil {
			rep.Errors = append(rep.Errors, err.Error())
			reports = append(reports, rep)
			continue
		}
		rep.Signals, rep.Events, rep.Cursor = len(res.Signals), len(res.Events), res.Cursor
		if dryRun {
			for _, s := range res.Signals {
				rep.Skipped = append(rep.Skipped, "signal "+s.SourceRef+" · "+s.Title)
			}
			for _, e := range res.Events {
				rep.Skipped = append(rep.Skipped, "event "+e.ThreadRef+" · "+e.Kind)
			}
			reports = append(reports, rep)
			continue
		}
		// The store is locked only to apply what the source sent: polling, which
		// may take long, leaves it free for the TUI and the agents.
		if err := a.S.Lock(); err != nil {
			rep.Errors = append(rep.Errors, err.Error())
			reports = append(reports, rep)
			continue
		}
		failed := false
		runner := connector.Runner{Store: a.S, Source: src}
		var tells *bool
		// tell says to a connector that declares "opened" which dossier an item reached.
		tell := func(sourceRef, threadRef, outcome, id string) {
			if tells == nil {
				d, err := runner.Describe()
				v := err == nil && contains(d.Verbs, "opened")
				tells = &v
			}
			if !*tells {
				return
			}
			title := ""
			if d, err := a.Load(id); err == nil {
				title = d.Title
			}
			if err := runner.Opened(sourceRef, threadRef, outcome, connector.Dossier{ID: id, Title: title}); err != nil {
				rep.Errors = append(rep.Errors, sourceRef+": opened: "+err.Error())
			}
		}
		for _, s := range res.Signals {
			r, err := a.Open(OpenParams{Title: s.Title, SourceRef: s.SourceRef, ThreadRef: s.ThreadRef, URL: s.URL,
				Instruction: s.Instruction, Summary: s.Summary, Files: s.Files, In: s.In})
			if err != nil {
				rep.Errors = append(rep.Errors, s.SourceRef+": "+err.Error())
				if r.ID == "" {
					failed = true
				}
				continue
			}
			if r.Outcome == "existing" {
				rep.Skipped = append(rep.Skipped, s.SourceRef+" already in "+r.ID)
				continue
			}
			rep.Opened = append(rep.Opened, r)
			tell(s.SourceRef, s.ThreadRef, r.Outcome, r.ID)
		}
		for _, e := range res.Events {
			d := a.FindByThread(e.ThreadRef)
			if d == nil {
				rep.Skipped = append(rep.Skipped, "event on unknown thread "+e.ThreadRef)
				continue
			}
			for _, holder := range e.In {
				if err := a.includeIn(holder, d.ID); err != nil {
					rep.Errors = append(rep.Errors, err.Error())
				}
			}
			if _, err := a.Wake(d, "event on "+e.ThreadRef); err != nil {
				rep.Errors = append(rep.Errors, err.Error())
				continue
			}
			summary := e.Summary
			if summary == nil {
				summary = map[string]any{}
			}
			summary["kind"] = e.Kind
			r, err := a.event(d, "", summary, e.Files, false)
			if err != nil {
				rep.Errors = append(rep.Errors, d.ID+": "+err.Error())
				continue
			}
			r.Outcome = "event"
			rep.Opened = append(rep.Opened, r)
			tell(e.ThreadRef, e.ThreadRef, "event", d.ID)
		}
		if !failed && res.Cursor != "" {
			if err := a.saveCursor(src.Name, res.Cursor); err != nil {
				rep.Errors = append(rep.Errors, err.Error())
			}
		}
		a.S.Unlock()
		reports = append(reports, rep)
	}
	if !dryRun {
		handled := map[string]bool{}
		for _, r := range reports {
			for _, o := range r.Opened {
				handled[o.ID] = true
			}
		}
		if err := a.S.Lock(); err != nil {
			return reports, err
		}
		if rep := a.Reconcile(handled); rep.Events > 0 || len(rep.Skipped) > 0 || len(rep.Errors) > 0 {
			reports = append(reports, rep)
		}
		a.S.Unlock()
	}
	return reports, nil
}

func sourceNames(s []store.SourceConfig) string {
	var n []string
	for _, x := range s {
		n = append(n, x.Name)
	}
	if len(n) == 0 {
		return "none"
	}
	return strings.Join(n, ", ")
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// Star marks a dossier the user wants at hand: first in its store, shown in
// every view of its state. Unstar removes the mark.
func (a *App) Star(d *dossier.Dossier, on bool) error {
	if IsDesk(d) {
		return a.deskError()
	}
	if d.Starred == on {
		return nil
	}
	d.Starred = on
	if on {
		_ = d.Log("starred")
	} else {
		_ = d.Log("unstarred")
	}
	return d.Save()
}
