package app

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/aclemen1/office-cli/internal/connector"
	"github.com/aclemen1/office-cli/internal/dossier"
	"github.com/aclemen1/office-cli/internal/office"
	"github.com/aclemen1/office-cli/internal/spec"
)

type TellResult struct {
	From    string   `json:"from"`
	Source  string   `json:"source"`
	Threads []string `json:"threads"`
	Sent    int      `json:"sent"`
}

// TellUser sends a text and files to the user through a source that declares
// send; the source alone picks the recipient. The thread joins the sender, so
// that a reply comes back to it as an event.
func (a *App) TellUser(d *dossier.Dossier, source, text string, paths []string) (TellResult, error) {
	res := TellResult{From: d.ID}
	if strings.TrimSpace(text) == "" && len(paths) == 0 {
		return res, spec.UserError("nothing to tell: give --text, --file or --attach")
	}
	var files []connector.File
	for _, p := range paths {
		p = office.ExpandHome(p)
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		if _, err := os.Stat(p); err != nil {
			return res, spec.UserError("cannot attach %s: %v", p, err)
		}
		files = append(files, connector.File{Name: filepath.Base(p), Path: p})
	}
	cfg, err := a.sendSource(source)
	if err != nil {
		return res, err
	}
	res.Source = cfg.Name
	from := d.Label() + " · " + d.Title
	if IsDesk(d) {
		from = "Desk " + a.OfficeName()
	}
	text = "*" + from + "*\n\n" + strings.TrimSpace(text)
	out, err := (connector.Runner{Office: a.S, Source: cfg}).Send(text, files, a.takePlaceholders(d, cfg.Name))
	if err != nil {
		return res, err
	}
	if IsDesk(d) {
		if err := a.ensureDesk(d); err != nil {
			return res, err
		}
	}
	for _, t := range out.ThreadRefs {
		d.AddThread(t)
	}
	res.Threads, res.Sent = out.ThreadRefs, out.Sent
	if err := d.Save(); err != nil {
		return res, err
	}
	_ = d.Log("told the user via %s · %s", cfg.Name, firstLine(text))
	return res, nil
}

// sendSource is the named source, or the only one that can send.
func (a *App) sendSource(name string) (office.SourceConfig, error) {
	var can []office.SourceConfig
	for _, s := range a.S.Config.Sources {
		if name != "" && s.Name != name {
			continue
		}
		d, err := (connector.Runner{Office: a.S, Source: s}).Describe()
		if err == nil && contains(d.Verbs, "send") {
			can = append(can, s)
		}
	}
	switch {
	case len(can) == 1:
		return can[0], nil
	case name != "":
		return office.SourceConfig{}, spec.UserError("source %q cannot send: its connector does not declare send", name)
	case len(can) == 0:
		return office.SourceConfig{}, spec.UserError("no source of this office can send")
	}
	names := make([]string, len(can))
	for i, s := range can {
		names[i] = s.Name
	}
	return office.SourceConfig{}, spec.UserError("several sources can send (%s): pass --source", strings.Join(names, ", "))
}

// toDesk hands a new item to the desk as an event: the desk opens a dossier
// for it or passes it on. Its thread joins the desk, so that a reply to the
// bot's acknowledgement reaches the desk too.
func (a *App) toDesk(s connector.Signal) (OpenResult, error) {
	d := a.Desk()
	if err := a.ensureDesk(d); err != nil {
		return OpenResult{}, err
	}
	// Two ingests may bring the same item: the desk takes it once.
	if s.ThreadRef != "" && d.HasThread(s.ThreadRef) {
		return OpenResult{ID: d.ID, Dir: d.Dir, Outcome: "existing"}, nil
	}
	if s.ThreadRef != "" {
		d.AddThread(s.ThreadRef)
	}
	summary := map[string]any{}
	for k, v := range s.Summary {
		summary[k] = v
	}
	summary["title"] = s.Title
	summary["source_ref"] = s.SourceRef
	r, err := a.event(d, "new item for the desk: open a dossier for it, or pass it on with notify", summary, s.Files, false)
	r.Outcome = "desk"
	return r, err
}
