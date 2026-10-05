package app

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/aclemen1/office-cli/internal/connector"
	"github.com/aclemen1/office-cli/internal/dossier"
	"github.com/aclemen1/office-cli/internal/office"
	"github.com/aclemen1/office-cli/internal/spec"
)

// The inbox is the office's drop folder. Each top-level entry, a file or a
// whole folder, is one item: once it stops changing it goes to the desk, which
// files it (inbox file), attaches it to a dossier (inbox attach), then releases
// it (inbox release): office checks every file is kept elsewhere and moves the
// item to the Trash. The steps are kept in .office/run/inbox.json.

const inboxRefPrefix = "inbox:item/"

var inboxPartial = []string{".crdownload", ".part", ".download", ".tmp", ".partial"}

// InboxItem is an entry of the inbox and how far the desk got with it.
type InboxItem struct {
	Name        string   `json:"name"`
	Path        string   `json:"path"`
	Kind        string   `json:"kind"` // file or folder
	Files       int      `json:"files"`
	Size        int64    `json:"size"`
	State       string   `json:"state"` // settling, new, sent (with the desk), filed, attached
	Fingerprint string   `json:"fingerprint,omitempty"`
	Signaled    string   `json:"signaled,omitempty"`
	Filed       string   `json:"filed,omitempty"`
	Concepts    []string `json:"concepts,omitempty"`
	Attached    []string `json:"attached,omitempty"`
	Note        string   `json:"note,omitempty"` // the last problem met
	last        time.Time
	paths       []string
}

type inboxState struct {
	Items map[string]*InboxItem `json:"items"`
}

func (a *App) InboxDir() string {
	d := a.S.Config.Inbox.Dir
	if d == "" {
		d = "inbox"
	}
	d = office.ExpandHome(d)
	if !filepath.IsAbs(d) {
		d = filepath.Join(a.S.Root, d)
	}
	return d
}

func (a *App) inboxOn() bool {
	if a.S.Config.Inbox.Off {
		return false
	}
	fi, err := os.Stat(a.InboxDir())
	return err == nil && fi.IsDir()
}

func (a *App) inboxSettle() time.Duration {
	if d, err := time.ParseDuration(a.S.Config.Inbox.Settle); err == nil && d > 0 {
		return d
	}
	return 30 * time.Second
}

func inboxHidden(name string) bool {
	if strings.HasPrefix(name, ".") || name == "Icon\r" {
		return true
	}
	for _, s := range inboxPartial {
		if strings.HasSuffix(name, s) {
			return true
		}
	}
	return false
}

// inboxShape reads an entry: its visible files, size and last change (ctime
// too: a copy that keeps the original mtime still moves the ctime).
func inboxShape(path string) (files []string, size int64, last time.Time) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, 0, time.Time{}
	}
	stamp := func(fi os.FileInfo) {
		if t := fi.ModTime(); t.After(last) {
			last = t
		}
		if t := changeTime(fi); t.After(last) {
			last = t
		}
	}
	stamp(fi)
	if !fi.IsDir() {
		return []string{path}, fi.Size(), last
	}
	_ = filepath.WalkDir(path, func(p string, de os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if p != path && inboxHidden(de.Name()) {
			if de.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := de.Info()
		if err != nil {
			return nil
		}
		stamp(info)
		if !de.IsDir() {
			files = append(files, p)
			size += info.Size()
		}
		return nil
	})
	sort.Strings(files)
	return files, size, last
}

func (a *App) inboxStatePath() string { return a.S.Meta("run", "inbox.json") }

func (a *App) loadInbox() inboxState {
	st := inboxState{Items: map[string]*InboxItem{}}
	if b, err := os.ReadFile(a.inboxStatePath()); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	if st.Items == nil {
		st.Items = map[string]*InboxItem{}
	}
	return st
}

func (a *App) saveInbox(st inboxState) error {
	if err := os.MkdirAll(a.S.Meta("run"), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(st, "", "  ")
	return os.WriteFile(a.inboxStatePath(), b, 0o644)
}

// withInbox reads and writes the inbox state under its own lock: the TUI, the
// desk's tools and office listen all touch it.
func (a *App) withInbox(fn func(*inboxState) error) error {
	unlock, err := a.S.LockSource("inbox-state")
	if err != nil {
		return err
	}
	defer unlock()
	st := a.loadInbox()
	if err := fn(&st); err != nil {
		return err
	}
	return a.saveInbox(st)
}

// Inbox lists the entries of the inbox with their state; entries gone from
// the folder are forgotten.
func (a *App) Inbox() ([]InboxItem, error) {
	var out []InboxItem
	err := a.withInbox(func(st *inboxState) error {
		out = a.scanInbox(st)
		return nil
	})
	return out, err
}

func (a *App) scanInbox(st *inboxState) []InboxItem {
	entries, _ := os.ReadDir(a.InboxDir())
	present := map[string]bool{}
	var out []InboxItem
	for _, e := range entries {
		if inboxHidden(e.Name()) {
			continue
		}
		path := filepath.Join(a.InboxDir(), e.Name())
		files, size, last := inboxShape(path)
		if len(files) == 0 {
			continue
		}
		present[e.Name()] = true
		it := st.Items[e.Name()]
		if it == nil {
			it = &InboxItem{Name: e.Name()}
			st.Items[e.Name()] = it
		}
		it.Path, it.Files, it.Size, it.last, it.paths = path, len(files), size, last, files
		it.Kind = "file"
		if e.IsDir() {
			it.Kind = "folder"
		}
		fp := fmt.Sprintf("%d:%d:%d", len(files), size, last.Unix())
		switch {
		case it.Signaled != "" && it.Fingerprint != fp:
			// Changed after it went to the desk: a new deposit.
			*it = InboxItem{Name: it.Name, Path: path, Kind: it.Kind, Files: len(files), Size: size, last: last, paths: files}
			it.State = "settling"
		case it.Signaled == "":
			it.State = "settling"
		}
		if it.Signaled == "" && time.Since(last) >= a.inboxSettle() {
			it.State = "new"
		}
		if it.Signaled == "" {
			it.Fingerprint = fp
		}
		out = append(out, *it)
	}
	for name := range st.Items {
		if !present[name] {
			delete(st.Items, name)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// inboxNext is when the next settling entry settles, zero if none.
func (a *App) inboxNext() time.Time {
	var next time.Time
	st := a.loadInbox()
	for _, it := range a.scanInbox(&st) {
		if it.State == "settling" {
			at := it.last.Add(a.inboxSettle())
			if next.IsZero() || at.Before(next) {
				next = at
			}
		}
	}
	return next
}

// ingestInbox hands every settled, new entry to the desk.
func (a *App) ingestInbox(rep *IngestReport) {
	var fresh []InboxItem
	_ = a.withInbox(func(st *inboxState) error {
		for _, it := range a.scanInbox(st) {
			if it.State == "new" {
				st.Items[it.Name].Signaled = time.Now().Format(time.RFC3339)
				st.Items[it.Name].State = "sent"
				fresh = append(fresh, it)
			}
		}
		return nil
	})
	for _, it := range fresh {
		rep.Signals++
		s := connector.Signal{SourceRef: inboxRefPrefix + it.Name, ThreadRef: inboxRefPrefix + it.Name, Title: it.Name,
			Instruction: fmt.Sprintf("Dépôt dans l'inbox : %s (%s, %d fichier(s), %d octets).", it.Path, it.Kind, it.Files, it.Size),
			Summary:     map[string]any{"kind": it.Kind, "path": it.Path, "files": it.Files, "size": it.Size, "item": it.Name}}
		r, err := a.toDesk(s)
		if err != nil {
			rep.Errors = append(rep.Errors, s.SourceRef+": "+err.Error())
			continue
		}
		if r.Outcome != "existing" {
			rep.Opened = append(rep.Opened, r)
		}
	}
}

func (a *App) inboxItem(st *inboxState, name string) (*InboxItem, error) {
	name = strings.TrimPrefix(name, inboxRefPrefix)
	a.scanInbox(st)
	it := st.Items[name]
	if it == nil {
		return nil, spec.NotFound("no %q in the inbox %s. List it with `office inbox ls`", name, a.InboxDir())
	}
	return it, nil
}

func expandArgs(tpl []string, vars map[string]string) []string {
	out := make([]string, len(tpl))
	for i, t := range tpl {
		for k, v := range vars {
			t = strings.ReplaceAll(t, "{"+k+"}", v)
		}
		out[i] = office.ExpandHome(t)
	}
	return out
}

// inboxRun runs a configured command. Tests replace it.
var inboxRun = func(argv []string) ([]byte, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	out, err := cmd.Output()
	if ee, ok := err.(*exec.ExitError); ok && len(out) == 0 {
		out = ee.Stderr
	}
	return out, err
}

// InboxFile files an entry with the [inbox] file command (the memory) and
// records the concepts it made.
func (a *App) InboxFile(name, note string) (InboxItem, error) {
	var res InboxItem
	tpl := a.S.Config.Inbox.File
	if len(tpl) == 0 {
		return res, spec.UserError("no [inbox] file command in %s", a.S.Meta("config.toml"))
	}
	var path string
	if err := a.withInbox(func(st *inboxState) error {
		it, err := a.inboxItem(st, name)
		path, name = it.Path, it.Name
		return err
	}); err != nil {
		return res, err
	}
	out, err := inboxRun(expandArgs(tpl, map[string]string{"path": path, "sphere": a.OfficeName(), "note": note}))
	concepts, problem := filedConcepts(out, err)
	return res, a.withInbox(func(st *inboxState) error {
		it, e := a.inboxItem(st, name)
		if e != nil {
			return e
		}
		if problem != "" {
			it.Note = "file: " + problem
			res = *it
			return spec.UserError("%s not filed: %s", name, problem)
		}
		it.Filed, it.Concepts, it.Note = time.Now().Format(time.RFC3339), concepts, ""
		it.State = "filed"
		if len(it.Attached) > 0 {
			it.State = "attached"
		}
		res = *it
		_ = a.Desk().Log("inbox %s filed · %d concept(s)", name, len(concepts))
		return nil
	})
}

// filedConcepts reads the file command's answer: a JSON envelope whose result
// is a list of deposits ({status, concepts, error}), or one of them.
func filedConcepts(out []byte, err error) ([]string, string) {
	var env struct {
		OK     *bool           `json:"ok"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(out, &env) != nil {
		if err != nil {
			return nil, firstLine(strings.TrimSpace(string(out)) + " " + err.Error())
		}
		return nil, ""
	}
	if env.Error != nil {
		return nil, env.Error.Message
	}
	type deposit struct {
		Status   string   `json:"status"`
		Concepts []string `json:"concepts"`
		Error    string   `json:"error"`
	}
	var list []deposit
	if json.Unmarshal(env.Result, &list) != nil {
		var one deposit
		if json.Unmarshal(env.Result, &one) == nil {
			list = []deposit{one}
		}
	}
	var concepts []string
	for _, d := range list {
		if d.Status == "failed" || d.Error != "" {
			return nil, firstLine(d.Error)
		}
		concepts = append(concepts, d.Concepts...)
	}
	if err != nil {
		return nil, err.Error()
	}
	return concepts, ""
}

// InboxAttach records the dossier an entry belongs to.
func (a *App) InboxAttach(name string, d *dossier.Dossier) (InboxItem, error) {
	var res InboxItem
	err := a.withInbox(func(st *inboxState) error {
		it, err := a.inboxItem(st, name)
		if err != nil {
			return err
		}
		if !contains(it.Attached, d.ID) {
			it.Attached = append(it.Attached, d.ID)
		}
		if it.Filed != "" {
			it.State = "attached"
		}
		res = *it
		return nil
	})
	if err == nil {
		_ = d.Log("inbox item %s attached (%s)", res.Name, res.Path)
		_ = a.Desk().Log("inbox %s attached to %s", res.Name, d.ID)
	}
	return res, err
}

// InboxRelease moves an entry to the Trash once it is filed, attached, and
// every file passes the [inbox] check. Nothing moves while a step is missing.
// noDossier, when the user wants the entry attached to no dossier, says why;
// the entry must still be filed and pass the check.
func (a *App) InboxRelease(name string, dryRun bool, noDossier string) (string, error) {
	var it InboxItem
	if err := a.withInbox(func(st *inboxState) error {
		x, err := a.inboxItem(st, name)
		if err == nil {
			it = *x
		}
		return err
	}); err != nil {
		return "", err
	}
	var missing []string
	if it.Filed == "" {
		missing = append(missing, "not filed (inbox file)")
	}
	if len(it.Attached) == 0 && strings.TrimSpace(noDossier) == "" {
		missing = append(missing, "attached to no dossier (inbox attach, or release with a reason for no dossier)")
	}
	if tpl := a.S.Config.Inbox.Check; len(tpl) > 0 {
		for _, f := range it.paths {
			if why := a.inboxCheck(tpl, f); why != "" {
				rel, _ := filepath.Rel(a.InboxDir(), f)
				missing = append(missing, rel+": "+why)
			}
		}
	}
	if len(missing) > 0 {
		note := strings.Join(missing, "; ")
		_ = a.withInbox(func(st *inboxState) error {
			if x := st.Items[it.Name]; x != nil {
				x.Note = "release: " + note
			}
			return nil
		})
		return "", spec.UserError("%s kept in the inbox: %s", it.Name, note)
	}
	if dryRun {
		return fmt.Sprintf("%d file(s) checked; would go to the Trash", len(it.paths)), nil
	}
	dest, err := toTrash(it.Path)
	if err != nil {
		return "", err
	}
	_ = a.withInbox(func(st *inboxState) error {
		delete(st.Items, it.Name)
		return nil
	})
	for _, id := range it.Attached {
		if d, err := a.Load(id); err == nil {
			_ = d.Log("inbox item %s released to the Trash", it.Name)
		}
	}
	detail := fmt.Sprintf("%d file(s) checked; moved to %s", len(it.paths), dest)
	if len(it.Attached) == 0 {
		detail += "; no dossier: " + strings.TrimSpace(noDossier)
	}
	_ = a.Desk().Log("inbox %s released · %s", it.Name, detail)
	return detail, nil
}

// inboxCheck runs the check on one file: its JSON answer must say the file is
// found and, when [inbox] holder is set, held by it.
func (a *App) inboxCheck(tpl []string, file string) string {
	out, err := inboxRun(expandArgs(tpl, map[string]string{"file": file, "sphere": a.OfficeName()}))
	var env struct {
		Result struct {
			Found   bool `json:"found"`
			Holders []struct {
				By string `json:"by"`
			} `json:"holders"`
		} `json:"result"`
	}
	if json.Unmarshal(out, &env) != nil {
		if err != nil {
			return "check failed: " + firstLine(strings.TrimSpace(string(out)))
		}
		return "check gave no answer"
	}
	if !env.Result.Found {
		return "not kept elsewhere"
	}
	if h := a.S.Config.Inbox.Holder; h != "" {
		for _, x := range env.Result.Holders {
			if strings.HasPrefix(x.By, h) {
				return ""
			}
		}
		return "kept, but " + h + " does not hold it"
	}
	return ""
}

// toTrash moves a path to the Trash on macOS, or removes it elsewhere.
func toTrash(path string) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", os.RemoveAll(path)
	}
	dir := office.ExpandHome("~/.Trash")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	dest := filepath.Join(dir, filepath.Base(path))
	if _, err := os.Stat(dest); err == nil {
		dest = filepath.Join(dir, filepath.Base(path)+" "+time.Now().Format("2006-01-02 15.04.05"))
	}
	return dest, os.Rename(path, dest)
}

func hasSource(srcs []office.SourceConfig, name string) bool {
	for _, s := range srcs {
		if s.Name == name {
			return true
		}
	}
	return false
}
