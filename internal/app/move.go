package app

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/aclemen1/dossier-cli/internal/connector"
	"github.com/aclemen1/dossier-cli/internal/dossier"
	"github.com/aclemen1/dossier-cli/internal/spec"
	"github.com/aclemen1/dossier-cli/internal/store"
)

type Moved struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Dir       string `json:"dir"`
	Restarted bool   `json:"restarted,omitempty"`
}

type MoveResult struct {
	Moved    []Moved  `json:"moved"`
	Unlinked []string `json:"unlinked,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// Move moves dossiers to another store: each gets a number there, its
// directory, sources, history and session follow; links among the moved
// dossiers stay, links to the others go. A running session restarts in the
// target store, with its environment and charter.
func (a *App) Move(ids []string, target *store.Store) (MoveResult, error) {
	var res MoveResult
	if len(ids) == 0 {
		return res, spec.UserError("move needs the dossiers to move, e.g. dossier move P-0010 P-0011 --to pro")
	}
	if target.Root == a.S.Root {
		return res, spec.UserError("%s is already in %s", strings.Join(ids, ", "), target.Root)
	}
	if err := target.Lock(); err != nil {
		return res, err
	}
	defer target.Unlock()
	to := &App{S: target}

	var ds []*dossier.Dossier
	seen := map[string]bool{}
	for _, id := range ids {
		d, err := a.Load(id)
		if err != nil {
			return res, err
		}
		if d.State == dossier.Merged {
			return res, spec.UserError("%s is merged into %s: move that one", d.ID, d.MergedInto)
		}
		if !seen[d.ID] {
			seen[d.ID] = true
			ds = append(ds, d)
		}
	}

	// New numbers first, so that links among the moved dossiers can follow.
	newID, newDir := map[string]string{}, map[string]string{}
	for _, d := range ds {
		_, slug, _ := strings.Cut(filepath.Base(d.Dir), "-")
		num, dir, err := target.NewDir(slug)
		if err != nil {
			return res, err
		}
		newID[d.ID], newDir[d.ID] = target.FormatID(num), dir
	}

	for _, d := range ds {
		running := d.Run.Session != "" && paneAlive(d.Run.PaneID)
		if running {
			if err := a.closeSession(d); err != nil {
				return res, fmt.Errorf("%s: %w", d.ID, err)
			}
		}
		_ = a.Archive(d)
		oldID, oldDir, dir := d.ID, d.Dir, newDir[d.ID]
		if err := os.Remove(dir); err != nil {
			return res, err
		}
		if err := os.Rename(oldDir, dir); err != nil {
			return res, fmt.Errorf("move %s to %s: %w", oldDir, dir, err)
		}
		m, err := dossier.Load(dir)
		if err != nil {
			return res, err
		}
		m.ID = newID[oldID]
		var kept []dossier.Link
		for _, l := range m.Links {
			if id, ok := newID[l.To]; ok {
				l.To = id
				kept = append(kept, l)
			} else {
				res.Warnings = append(res.Warnings, fmt.Sprintf("%s: link %s %s dropped, it stays in %s", m.ID, l.Rel, l.To, a.S.Root))
			}
		}
		m.Links = kept
		if m.Alias != "" {
			if other := to.byAlias(m.Alias); other != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("%s: alias %s dropped, %s already has it", m.ID, m.Alias, other.ID))
				m.Alias = ""
			}
		}
		if m.Run.Cwd == "" && m.Run.Session != "" {
			if err := moveConversation(m.Run.Session, oldDir, dir); err != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("%s: conversation not moved (%v); it resumes from the archived transcript only", m.ID, err))
			}
		}
		m.Run.PaneID, m.Run.TabID, m.Run.Home, m.Run.Placeholder = "", "", "", ""
		if err := m.Save(); err != nil {
			return res, err
		}
		_ = m.Log("moved from %s (%s) as %s", oldID, a.S.Root, m.ID)
		res.Warnings = append(res.Warnings, to.claimSources(m)...)
		mv := Moved{From: oldID, To: m.ID, Dir: dir}
		if running {
			if err := to.resume(m); err != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("%s: session not restarted (%v); `dossier attach %s` resumes it", m.ID, err, m.ID))
			} else {
				mv.Restarted = true
			}
		}
		res.Moved = append(res.Moved, mv)
	}
	for _, d := range ds {
		to.syncLinksBlockOf(newID[d.ID])
	}

	// The dossiers that stay drop their links to the moved ones.
	all, _ := a.All()
	for _, x := range all {
		var kept []dossier.Link
		for _, l := range x.Links {
			if _, moved := newID[l.To]; !moved {
				kept = append(kept, l)
			}
		}
		if len(kept) == len(x.Links) {
			continue
		}
		var gone []string
		for _, l := range x.Links {
			if id, moved := newID[l.To]; moved {
				gone = append(gone, l.To+" → "+id)
			}
		}
		x.Links = kept
		a.syncLinksBlock(x)
		if err := x.Save(); err != nil {
			return res, err
		}
		_ = x.Log("links removed: %s moved to %s", strings.Join(gone, ", "), target.Root)
		res.Unlinked = append(res.Unlinked, x.ID)
	}
	return res, nil
}

func (a *App) syncLinksBlockOf(id string) {
	if d, err := a.Load(id); err == nil {
		a.syncLinksBlock(d)
		_ = d.Save()
	}
}

// claimSources asks this store's connectors to take the moved dossier's items
// in (a reminder gets the store's tag), and names the sources it cannot follow.
func (a *App) claimSources(d *dossier.Dossier) []string {
	var warn []string
	for _, s := range d.Sources {
		name := s.Name()
		if name == "manual" || name == "herdr" {
			continue
		}
		cfg, ok := a.S.Config.Source(name)
		if !ok {
			warn = append(warn, fmt.Sprintf("%s: %s has no source %q here; its signal stays with the old store's connector", d.ID, s.ID, name))
			continue
		}
		r := connector.Runner{Store: a.S, Source: cfg}
		desc, err := r.Describe()
		if err != nil || !contains(desc.Verbs, "claim") {
			warn = append(warn, fmt.Sprintf("%s: source %s cannot take %s in; check that it sees it", d.ID, name, s.ID))
			continue
		}
		if detail, err := r.Claim(s.ID); err != nil {
			warn = append(warn, fmt.Sprintf("%s: %s not claimed: %v", d.ID, s.ID, err))
		} else {
			_ = d.Log("source %s claimed: %s", s.ID, detail)
		}
	}
	return warn
}

var notAlnum = regexp.MustCompile(`[^A-Za-z0-9-]`)

// projectDir is where Claude Code keeps the conversations started in dir.
func projectDir(dir string) string {
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	return filepath.Join(store.ExpandHome("~/.claude/projects"), notAlnum.ReplaceAllString(dir, "-"))
}

// moveConversation moves a session's transcript, and its side directory, to
// the project of the new directory: --resume looks for it there.
func moveConversation(session, oldDir, newDir string) error {
	src := transcriptOf(session)
	if src == "" {
		return fmt.Errorf("no transcript for session %s", session)
	}
	dst := projectDir(newDir)
	if filepath.Dir(src) == dst {
		return nil
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, filepath.Join(dst, session+".jsonl")); err != nil {
		return err
	}
	side := filepath.Join(filepath.Dir(src), session)
	if _, err := os.Stat(side); err == nil {
		_ = os.Rename(side, filepath.Join(dst, session))
	}
	return nil
}
