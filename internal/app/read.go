package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/aclemen1/office-cli/internal/connector"
	"github.com/aclemen1/office-cli/internal/dossier"
	"github.com/aclemen1/office-cli/internal/spec"
)

// LoadDir loads the dossier whose directory is dir or contains it.
func (a *App) LoadDir(dir string) (*dossier.Dossier, error) {
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	for d := dir; d != "" && d != a.S.Root && d != filepath.Dir(d); d = filepath.Dir(d) {
		if filepath.Dir(d) == a.S.Root {
			return dossier.Load(d)
		}
	}
	return nil, spec.NotFound("%s is not inside a dossier of %s", dir, a.S.Root)
}

type Row struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	State          string   `json:"state"`
	Activity       string   `json:"activity"`
	WaitingOn      string   `json:"waiting_on,omitempty"`
	WaitUntil      string   `json:"wait_until,omitempty"`
	Alias          string   `json:"alias,omitempty"`
	Label          string   `json:"label"`
	NoAction       bool     `json:"no_action,omitempty"`
	Starred        bool     `json:"starred,omitempty"`
	Updated        string   `json:"updated"`
	Pending        int      `json:"pending_transitions,omitempty"`
	BlockedBy      []string `json:"blocked_by,omitempty"`
	BlockedByNames []string `json:"blocked_by_names,omitempty"`
	Model          string   `json:"model,omitempty"`
}

// List lists dossiers by state; with in, only the dossiers that one includes.
// model, when set, keeps the dossiers whose session runs on it (a model id or a part of it, e.g. sonnet).
func (a *App) List(status, in, model string) ([]Row, error) {
	all, err := a.All()
	if err != nil {
		return nil, err
	}
	var within map[string]bool
	if in != "" {
		holder, err := a.Load(in)
		if err != nil {
			return nil, err
		}
		within = map[string]bool{}
		for _, id := range holder.Targets(dossier.RelIncludes) {
			within[id] = true
		}
	}
	panesNow := Panes()
	idx := map[string]*dossier.Dossier{}
	for _, d := range all {
		idx[d.ID] = d
	}
	rows := []Row{}
	for _, d := range all {
		if model != "" && !strings.Contains(a.ModelOf(d), model) {
			continue
		}
		switch status {
		case "active":
			if d.State != dossier.Open && d.State != dossier.Waiting {
				continue
			}
		case "todo":
			if d.State != dossier.Open || d.NoAction {
				continue
			}
		case "all":
		default:
			if d.State != status {
				continue
			}
		}
		if within != nil && !within[d.ID] {
			continue
		}
		rows = append(rows, Row{ID: d.ID, Title: d.Title, State: d.State, Activity: Activity(d, panesNow),
			WaitingOn: d.WaitingOn, WaitUntil: d.WaitUntil, Alias: d.Alias, Label: d.Label(), NoAction: d.NoAction, Starred: d.Starred, Updated: d.Updated, Pending: len(d.Run.PendingTransitions), BlockedBy: BlockedBy(d, idx), BlockedByNames: BlockerNames(BlockedBy(d, idx), idx), Model: a.ModelOf(d)})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows, nil
}

type ShowResult struct {
	*dossier.Dossier
	Activity string   `json:"activity"`
	Session  string   `json:"session,omitempty"`
	RunsOn   string   `json:"runs_on,omitempty"` // the model its session takes
	TabID    string   `json:"tab_id,omitempty"`
	Files    []string `json:"files"`
	Body     string   `json:"body"`
	Outgoing []Edge   `json:"outgoing"`
	Incoming []Edge   `json:"incoming"`
	Log      string   `json:"log"`

	Escalations []Escalation `json:"escalations,omitempty"`
}

func (a *App) Show(d *dossier.Dossier) ShowResult {
	r := ShowResult{Dossier: d, Activity: Activity(d, Panes()), RunsOn: a.ModelOf(d), Session: d.Run.Session, TabID: d.Run.TabID, Files: []string{}}
	for _, sub := range []string{"context", "files"} {
		entries, _ := os.ReadDir(d.Path(sub))
		for _, e := range entries {
			r.Files = append(r.Files, filepath.Join(sub, e.Name()))
		}
	}
	r.Outgoing, r.Incoming = a.Outgoing(d), a.Incoming(d)
	r.Body = strings.TrimSpace(d.Body())
	if b, err := os.ReadFile(d.Path("log.md")); err == nil {
		r.Log = string(b)
	}
	if IsDesk(d) {
		r.Escalations = OpenEscalations(d)
	}
	return r
}

type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

type DoctorReport struct {
	Office string  `json:"office"`
	Checks []Check `json:"checks"`
}

func (r DoctorReport) failed() int {
	n := 0
	for _, c := range r.Checks {
		if !c.OK {
			n++
		}
	}
	return n
}

// PendingTransitions lets a failing doctor exit with code 4.
func (r DoctorReport) PendingTransitions() int { return r.failed() }

func (a *App) Doctor() DoctorReport {
	rep := DoctorReport{Office: a.S.Root}
	add := func(name string, ok bool, detail string) { rep.Checks = append(rep.Checks, Check{name, ok, detail}) }

	cmd := a.S.Config.ACP.Command
	if len(cmd) == 0 {
		add("acp command", false, "[acp] command is empty")
	} else if p, err := exec.LookPath(cmd[0]); err != nil {
		add("acp command", false, cmd[0]+" not found in PATH")
	} else {
		add("acp command", true, p)
	}
	if out, err := exec.Command("herdr", "status").CombinedOutput(); err != nil {
		add("herdr", false, strings.TrimSpace(string(out)))
	} else {
		add("herdr", true, "server reachable")
	}
	for _, src := range a.S.Config.Sources {
		d, err := (connector.Runner{Office: a.S, Source: src}).Describe()
		switch {
		case err != nil:
			add("source "+src.Name, false, err.Error())
		case d.Protocol != connector.Protocol:
			add("source "+src.Name, false, "speaks protocol "+itoa(d.Protocol)+", dossier expects "+itoa(connector.Protocol))
		default:
			add("source "+src.Name, true, strings.Join(d.Verbs, ", "))
		}
	}
	all, _ := a.All()
	pending := 0
	for _, d := range all {
		pending += len(d.Run.PendingTransitions)
	}
	add("pending transitions", pending == 0, itoa(pending)+" pending; replay with `office retry`")
	s := a.Sessions()
	detail := fmt.Sprintf("%d running, cap %d", s.Running, s.Cap)
	if len(s.Stopped) > 0 {
		detail += "; open without a tab: " + strings.Join(s.Stopped, ", ") + " (the next ingest resumes them)"
	}
	add("sessions", true, detail)
	return rep
}

func itoa(n int) string { return strconv.Itoa(n) }
