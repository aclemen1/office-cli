package app

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/aclemen1/office-cli/internal/connector"
	"github.com/aclemen1/office-cli/internal/dossier"
	"github.com/aclemen1/office-cli/internal/spec"
)

// ---------------------------------------------------------------- grep

type GrepHit struct {
	Dossier string `json:"dossier"`
	Source  string `json:"source"` // archived | live
	Line    int    `json:"line"`
	Role    string `json:"role"`
	Text    string `json:"text"`
}

// Grep searches the full transcripts of a dossier, compactions included: the
// archived copy and the live file. It reads message text, not raw JSON.
func (a *App) Grep(d *dossier.Dossier, pattern string, limit int) ([]GrepHit, error) {
	re, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		return nil, spec.UserError("pattern %q is not a valid regular expression: %v. Example: office grep D-0042 \"date de passage\"", pattern, err)
	}
	if limit <= 0 {
		limit = 50
	}
	hits := []GrepHit{}
	seen := map[string]bool{}
	sources := map[string]string{d.Path("transcript.jsonl"): "archived", transcriptOf(d.Run.Session): "live"}
	paths := archivedTranscripts(d)
	for _, p := range paths {
		sources[p] = "archived"
	}
	for _, path := range append(paths, d.Path("transcript.jsonl"), transcriptOf(d.Run.Session)) {
		if path == "" {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		n := 0
		for sc.Scan() {
			n++
			role, text := messageText(sc.Bytes())
			if text == "" || !re.MatchString(text) {
				continue
			}
			key := role + "\x00" + text
			if seen[key] {
				continue
			}
			seen[key] = true
			hits = append(hits, GrepHit{Dossier: d.ID, Source: sources[path], Line: n, Role: role, Text: snippet(text, re)})
			if len(hits) >= limit {
				f.Close()
				return hits, nil
			}
		}
		f.Close()
	}
	return hits, nil
}

func messageText(line []byte) (string, string) {
	var rec struct {
		Type    string `json:"type"`
		Message struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &rec) != nil || len(rec.Message.Content) == 0 {
		return "", ""
	}
	var s string
	if json.Unmarshal(rec.Message.Content, &s) == nil {
		return rec.Message.Role, s
	}
	var parts []struct {
		Type    string          `json:"type"`
		Text    string          `json:"text"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(rec.Message.Content, &parts) != nil {
		return "", ""
	}
	var b strings.Builder
	for _, p := range parts {
		switch {
		case p.Text != "":
			b.WriteString(p.Text + "\n")
		case len(p.Content) > 0:
			var inner string
			if json.Unmarshal(p.Content, &inner) == nil {
				b.WriteString(inner + "\n")
			}
		}
	}
	return rec.Message.Role, strings.TrimSpace(b.String())
}

func snippet(text string, re *regexp.Regexp) string {
	loc := re.FindStringIndex(text)
	if loc == nil {
		return ""
	}
	r := []rune(text)
	start := len([]rune(text[:loc[0]]))
	from, to := start-120, start+200
	if from < 0 {
		from = 0
	}
	if to > len(r) {
		to = len(r)
	}
	s := strings.ReplaceAll(string(r[from:to]), "\n", " ")
	if from > 0 {
		s = "…" + s
	}
	if to < len(r) {
		s += "…"
	}
	return s
}

// ---------------------------------------------------------------- merge

type MergeResult struct {
	From    string   `json:"from"`
	Into    string   `json:"into"`
	Files   []string `json:"files"`
	Sources []string `json:"sources"`
	Pending int      `json:"pending_transitions,omitempty"`
}

func (r MergeResult) PendingTransitions() int { return r.Pending }

// Merge moves everything of `from` into `into`: context and files, sources,
// threads and links. `from` becomes merged; `into` is reopened when it was
// done, and its session hears about the merge.
func (a *App) Merge(fromID, intoID string) (MergeResult, error) {
	from, err := a.Load(fromID)
	if err != nil {
		return MergeResult{}, err
	}
	into, err := a.Load(intoID)
	if err != nil {
		return MergeResult{}, err
	}
	into = a.follow(into)
	switch {
	case from.ID == into.ID:
		return MergeResult{}, spec.UserError("cannot merge %s into itself", from.ID)
	case from.State == dossier.Merged:
		return MergeResult{}, spec.UserError("%s is already merged into %s", from.ID, from.MergedInto)
	}
	_ = a.Archive(from)
	// Reopen first: the reopen transition belongs to the target's own sources,
	// not to the ones it is about to receive.
	pending := 0
	if into.State == dossier.Done {
		if pending, err = a.SetState(into, "reopen", "merge of "+from.ID, ""); err != nil {
			return MergeResult{}, err
		}
	}

	var files []connector.File
	entries, _ := os.ReadDir(from.Path("context"))
	for _, e := range entries {
		if !e.IsDir() {
			files = append(files, connector.File{Name: e.Name(), Path: from.Path("context", e.Name())})
		}
	}
	paths, err := a.writeContext(into, files)
	if err != nil {
		return MergeResult{}, err
	}
	if fe, _ := os.ReadDir(from.Path("files")); len(fe) > 0 {
		if err := os.MkdirAll(into.Path("files"), 0o755); err != nil {
			return MergeResult{}, err
		}
		for _, e := range fe {
			if e.IsDir() {
				continue
			}
			b, err := os.ReadFile(from.Path("files", e.Name()))
			if err != nil {
				return MergeResult{}, err
			}
			name := from.ID + "-" + e.Name()
			if err := os.WriteFile(into.Path("files", name), b, 0o644); err != nil {
				return MergeResult{}, err
			}
			paths = append(paths, filepath.Join("files", name))
		}
	}

	res := MergeResult{From: from.ID, Into: into.ID, Files: paths, Pending: pending}
	for _, s := range from.Sources {
		into.AddSource(s)
		res.Sources = append(res.Sources, s.ID)
	}
	for _, t := range from.Threads {
		into.AddThread(t)
	}
	for _, l := range from.Links {
		if l.To != into.ID && !into.HasLink(l.Rel, l.To) {
			into.Links = append(into.Links, l)
		}
	}
	a.syncLinksBlock(into)
	from.Sources, from.Threads, from.Links = nil, nil, nil
	a.syncLinksBlock(from)
	// Links that pointed at `from` now point at `into`.
	if all, err := a.All(); err == nil {
		for _, x := range all {
			if x.ID == from.ID || x.ID == into.ID || !strings.Contains(fmt.Sprint(x.Links), from.ID) {
				continue
			}
			var kept []dossier.Link
			for _, l := range x.Links {
				if l.To == from.ID {
					l.To = into.ID
				}
				if !containsLink(kept, l) {
					kept = append(kept, l)
				}
			}
			x.Links = kept
			a.syncLinksBlock(x)
			_ = x.Save()
			_ = x.Log("links to %s now point at %s (merge)", from.ID, into.ID)
		}
	}
	from.State, from.MergedInto, from.WaitingOn = dossier.Merged, into.ID, ""
	_ = from.Log("merged into %s (%s)", into.ID, into.Title)
	if err := from.Save(); err != nil {
		return MergeResult{}, err
	}
	a.syncRoutines(from)
	if os.Getenv("DOSSIER_ID") == from.ID {
		_ = a.closeTabLater(from, 8*time.Second)
	} else {
		_ = a.closeSession(from)
		_ = from.Save()
	}

	_ = into.Log("%s (%s) merged in: %d file(s), sources %s", from.ID, from.Title, len(paths), strings.Join(res.Sources, ", "))
	if err := into.Save(); err != nil {
		return MergeResult{}, err
	}
	text := fmt.Sprintf("Dossier %s (%s) was merged into this one.\nIts files: %s\nIts conversation stays searchable with the grep tool (dossier %s).\nTell me what it changes here.",
		from.ID, from.Title, strings.Join(orNone(paths), ", "), from.ID)
	if tpl, err := a.S.PromptTemplate("event"); err == nil && strings.Contains(tpl, "{{") {
		summary := map[string]any{"merged": from.ID + " · " + from.Title, "grep": "dossier " + from.ID}
		text, _ = a.renderPrompt(into, "event", "", summary, paths)
	}
	if into.Run.Session != "" {
		if err := a.sendPrompt(into, text); err != nil {
			_ = into.Log("could not tell the session about the merge: %v", err)
		}
	}
	return res, nil
}

func orNone(s []string) []string {
	if len(s) == 0 {
		return []string{"none"}
	}
	return s
}

// MergedInto lists the dossiers merged into d, whose transcripts d may search.
func (a *App) MergedInto(d *dossier.Dossier) []*dossier.Dossier {
	all, _ := a.All()
	var out []*dossier.Dossier
	for _, x := range all {
		if x.MergedInto == d.ID {
			out = append(out, x)
		}
	}
	return out
}

func containsLink(ls []dossier.Link, l dossier.Link) bool {
	for _, x := range ls {
		if x == l {
			return true
		}
	}
	return false
}
