package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aclemen1/office-cli/internal/dossier"
	"github.com/aclemen1/office-cli/internal/spec"
)

type RetitleResult struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Dir       string   `json:"dir"`
	Restarted bool     `json:"restarted,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
}

// Retitle renames a dossier: its title, the title of a manual source, its tab,
// and its directory, which follows the new title's slug. A running session is
// closed, its conversation follows the directory, then it resumes.
func (a *App) Retitle(id, title string) (RetitleResult, error) {
	var res RetitleResult
	title = strings.TrimSpace(title)
	if title == "" {
		return res, spec.UserError("a dossier needs a title")
	}
	d, err := a.Load(id)
	if err != nil {
		return res, err
	}
	old := d.Title
	res.ID, res.Title, res.Dir = d.ID, title, d.Dir

	num, _, _ := strings.Cut(filepath.Base(d.Dir), "-")
	newDir := filepath.Join(a.S.Root, num+"-"+dossier.Slug(title))
	running, renamed := false, newDir != d.Dir
	if renamed {
		if _, err := os.Stat(newDir); err == nil {
			return res, spec.UserError("%s already exists", newDir)
		}
		running = d.Run.Session != "" && paneAlive(d.Run.PaneID)
		if running {
			if err := a.closeSession(d); err != nil {
				return res, err
			}
		}
		_ = a.Archive(d)
		oldDir := d.Dir
		if err := os.Rename(oldDir, newDir); err != nil {
			return res, fmt.Errorf("rename %s: %w", oldDir, err)
		}
		if d, err = dossier.Load(newDir); err != nil {
			return res, err
		}
		if d.Run.Cwd == "" && d.Run.Session != "" {
			if err := moveConversation(d.Run.Session, oldDir, newDir); err != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("conversation not moved (%v); it resumes from the archived transcript only", err))
			}
		}
		res.Dir = newDir
		d.Run.PaneID, d.Run.TabID = "", ""
	}
	d.Title = title
	for i, s := range d.Sources {
		if s.Name() == "manual" && s.Title == old {
			d.Sources[i].Title = title
		}
	}
	if err := d.Save(); err != nil {
		return res, err
	}
	_ = d.Log("title %q → %q", old, title)
	if renamed {
		res.Warnings = append(res.Warnings, a.retargetRoutines(d)...)
	}
	if running {
		if err := a.resume(d); err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("session not restarted (%v); `office start %s` resumes it", err, d.ID))
		} else {
			res.Restarted = true
		}
	} else {
		renameTab(d.Run.TabID, TabLabel(d))
	}
	for _, x := range a.Incoming(d) {
		a.syncLinksBlockOf(x.ID)
	}
	return res, nil
}
