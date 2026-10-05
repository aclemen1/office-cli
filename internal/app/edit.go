package app

import (
	"fmt"
	"os"
	"strings"

	"github.com/aclemen1/office-cli/internal/dossier"
)

// EditText is what the user edits for d: the fiche's body without the links
// block, or the desk's charter.
func (a *App) EditText(d *dossier.Dossier) (string, error) {
	if IsDesk(d) {
		b, err := os.ReadFile(d.Path("CLAUDE.md"))
		return string(b), err
	}
	return d.EditableBody(), nil
}

type EditResult struct {
	Saved    bool   `json:"saved"`
	Conflict string `json:"conflict,omitempty"` // where the user's version waits
}

// SaveEdit writes the user's version of the text, unless someone changed it
// since it was read: then the version goes to fiche.edited.md (or
// CLAUDE.edited.md) and nothing is overwritten.
func (a *App) SaveEdit(d *dossier.Dossier, before, after string) (EditResult, error) {
	var res EditResult
	if after == before {
		return res, nil
	}
	if IsDesk(d) {
		cur, _ := os.ReadFile(d.Path("CLAUDE.md"))
		if string(cur) != before {
			res.Conflict = d.Path("CLAUDE.edited.md")
			return res, os.WriteFile(res.Conflict, []byte(after), 0o644)
		}
		if err := os.WriteFile(d.Path("CLAUDE.md"), []byte(after), 0o644); err != nil {
			return res, err
		}
		_ = d.Log("charter edited by the user")
		res.Saved = true
		return res, nil
	}
	fresh, err := dossier.Load(d.Dir)
	if err != nil {
		return res, err
	}
	if fresh.EditableBody() != before {
		res.Conflict = d.Path("fiche.edited.md")
		return res, os.WriteFile(res.Conflict, []byte(after), 0o644)
	}
	fresh.SetBody(after)
	a.syncLinksBlock(fresh)
	if err := fresh.Save(); err != nil {
		return res, err
	}
	_ = fresh.Log("fiche edited by the user")
	res.Saved = true
	return res, nil
}

// TellEdit lets the dossier's session know, after its turn, that the user
// changed its fiche, with the lines added and removed.
func (a *App) TellEdit(d *dossier.Dossier, before, after string) error {
	if d.Run.Session == "" {
		return nil
	}
	a.Now = false
	text := fmt.Sprintf("Alain a modifié la fiche de ce dossier (%s). Relis-la avec show avant de poursuivre ; ce qui y est écrit fait foi.\n\n%s",
		d.Label(), diffSummary(before, after))
	return a.sendPrompt(d, text)
}

// diffSummary lists the lines added and removed, a few of each.
func diffSummary(before, after string) string {
	had := map[string]int{}
	for _, l := range strings.Split(before, "\n") {
		had[l]++
	}
	has := map[string]int{}
	for _, l := range strings.Split(after, "\n") {
		has[l]++
	}
	var b strings.Builder
	n := 0
	for _, l := range strings.Split(after, "\n") {
		if strings.TrimSpace(l) != "" && had[l] == 0 && n < 12 {
			b.WriteString("+ " + l + "\n")
			n++
		}
	}
	for _, l := range strings.Split(before, "\n") {
		if strings.TrimSpace(l) != "" && has[l] == 0 && n < 24 {
			b.WriteString("- " + l + "\n")
			n++
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
