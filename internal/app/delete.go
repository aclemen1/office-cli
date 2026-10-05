package app

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/aclemen1/office-cli/internal/dossier"
	"github.com/aclemen1/office-cli/internal/office"
	"github.com/aclemen1/office-cli/internal/spec"
)

// DeleteResult says where a deleted dossier went.
type DeleteResult struct {
	ID       string   `json:"id"`
	Trash    string   `json:"trash,omitempty"` // macOS: where to get it back
	Unlinked []string `json:"unlinked,omitempty"`
}

// Delete removes a dossier: its signal is withdrawn at every source (as on
// close), its tab closed, the links other dossiers hold to it removed, and its
// directory moved to the Trash on macOS, erased elsewhere.
func (a *App) Delete(d *dossier.Dossier, note string) (DeleteResult, error) {
	res := DeleteResult{ID: d.ID}
	if d.State != dossier.Done && d.State != dossier.Merged {
		from := d.State
		d.Run.PendingTransitions = nil
		if n := a.reflect(d, from, dossier.Done, deleteNote(note)); n > 0 {
			_ = d.Save()
			return res, spec.UserError("%s: %d source(s) did not withdraw the signal (%s); nothing deleted, or the next ingest would open it again. See `office show %s`, then retry",
				d.ID, n, d.Run.PendingTransitions[0].Error, d.ID)
		}
	}
	if err := a.closeSession(d); err != nil {
		_ = d.Log("tab not closed before deletion: %v", err)
	}
	all, _ := a.All()
	for _, x := range all {
		if x.ID == d.ID {
			continue
		}
		var kept []dossier.Link
		for _, l := range x.Links {
			if l.To != d.ID {
				kept = append(kept, l)
			}
		}
		if len(kept) == len(x.Links) {
			continue
		}
		x.Links = kept
		a.syncLinksBlock(x)
		if err := x.Save(); err != nil {
			return res, err
		}
		_ = x.Log("links to %s removed: %s (%s) was deleted", d.ID, d.ID, d.Title)
		res.Unlinked = append(res.Unlinked, x.ID)
	}
	for _, w := range a.dropRoutines(d) {
		_ = d.Log("%s", w)
	}
	if runtime.GOOS == "darwin" {
		trash := filepath.Join(office.ExpandHome("~/.Trash"), fmt.Sprintf("%s %s %s", d.ID, filepath.Base(d.Dir), time.Now().Format("2006-01-02 15.04.05")))
		if err := os.MkdirAll(filepath.Dir(trash), 0o700); err != nil {
			return res, err
		}
		if err := os.Rename(d.Dir, trash); err != nil {
			return res, fmt.Errorf("move %s to the Trash: %w", d.Dir, err)
		}
		res.Trash = trash
		return res, nil
	}
	return res, os.RemoveAll(d.Dir)
}

func deleteNote(note string) string {
	if note == "" {
		return "dossier deleted"
	}
	return "dossier deleted · " + note
}
