package app

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/aclemen1/office-cli/internal/dossier"
	"github.com/aclemen1/office-cli/internal/office"
	"github.com/aclemen1/office-cli/internal/spec"
)

// herdr's agent list shows every agent of the session; agent.view.set filters
// it. The dossiers' view hides the agents of the offices' workspaces, except
// those that wait for the user and those of the workspace on screen. herdr
// forgets the view when its server restarts: the TUI sets it at each start.

const viewSource = "dossier"

func herdrSocket() string {
	if p := os.Getenv("HERDR_SOCKET_PATH"); p != "" {
		return p
	}
	return office.ExpandHome("~/.config/herdr/herdr.sock")
}

// herdrRequest sends one request on herdr's socket and reads its answer.
var herdrRequest = func(method string, params any) error {
	conn, err := net.DialTimeout("unix", herdrSocket(), 2*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	b, _ := json.Marshal(map[string]any{"id": "office:" + method, "method": method, "params": params})
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return err
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return err
	}
	var r struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(line, &r) == nil && r.Error != nil {
		return fmt.Errorf("herdr %s: %s", method, r.Error.Message)
	}
	return nil
}

// workspaceLabel is the herdr workspace the office's ACP server places its
// agents in: the value of --workspace in [acp] command.
func workspaceLabel(s *office.Office) string {
	cmd := s.Config.ACP.Command
	for i := 0; i+1 < len(cmd); i++ {
		if cmd[i] == "--workspace" {
			return cmd[i+1]
		}
	}
	return ""
}

func workspaceIDs(labels map[string]bool) []string {
	out, err := herdrCall("workspace", "list")
	if err != nil {
		return nil
	}
	var r struct {
		Result struct {
			Workspaces []struct {
				ID    string `json:"workspace_id"`
				Label string `json:"label"`
			} `json:"workspaces"`
		} `json:"result"`
	}
	_ = json.Unmarshal(out, &r)
	var ids []string
	for _, w := range r.Result.Workspaces {
		if labels[w.Label] {
			ids = append(ids, w.ID)
		}
	}
	return ids
}

// SetAgentView hides the offices' agents from herdr's agent list.
func SetAgentView(roots []string) error {
	labels := map[string]bool{}
	for _, root := range roots {
		if s, err := office.Open(root); err == nil {
			if l := workspaceLabel(s); l != "" {
				labels[l] = true
			}
		}
	}
	ids := workspaceIDs(labels)
	if len(ids) == 0 {
		return nil
	}
	filter := map[string]any{"op": "any", "filters": []any{
		map[string]any{"op": "not", "filter": map[string]any{"op": "in", "field": "workspace_id", "values": ids}},
		map[string]any{"op": "in", "field": "status", "values": []string{"blocked", "done"}},
		map[string]any{"op": "eq", "field": map[string]any{"token": todoToken}, "value": "1"},
		map[string]any{"op": "eq", "field": "workspace_id", "value": map[string]any{"context": "current_workspace_id"}},
	}}
	return herdrRequest("agent.view.set", map[string]any{"source": viewSource, "label": "without dossiers", "filter": filter})
}

// ClearAgentView shows every agent again.
func ClearAgentView() error {
	return herdrRequest("agent.view.clear", map[string]any{"source": viewSource})
}

// HerdrReady checks that herdr is installed and its server answers: office
// runs its agents in herdr panes and reads their state there.
func HerdrReady() error {
	if _, err := exec.LookPath("herdr"); err != nil {
		return spec.UserError("office needs herdr, which is not on the PATH. Install it from https://herdr.dev")
	}
	conn, err := net.DialTimeout("unix", herdrSocket(), time.Second)
	if err != nil {
		return spec.UserError("office needs a running herdr server; none answers on %s. Start herdr, then retry", herdrSocket())
	}
	return conn.Close()
}

// todoToken marks the pane of a dossier that needs the user: open and not
// parked, or its agent asks a question. The agents' view keeps those panes.
// herdr forgets a token after a day at most: ingest and the TUI report again.
const todoToken = "office_todo"

const todoTTL = 24 * time.Hour

var (
	todoMu   sync.Mutex
	todoSent = map[string]todoReport{}
)

type todoReport struct {
	on bool
	at time.Time
}

func needsUser(d *dossier.Dossier) bool {
	if d.State == dossier.Open && !d.NoAction {
		return true
	}
	return d.Run.Session != "" && pendingQuestion(d.Run.Session)
}

// reportTodo publishes the dossier's todo token on its pane, unless the same
// value went out recently.
func reportTodo(d *dossier.Dossier) {
	pane := d.Run.PaneID
	if pane == "" || IsDesk(d) {
		return
	}
	on := needsUser(d)
	todoMu.Lock()
	last, ok := todoSent[pane]
	todoMu.Unlock()
	if ok && last.on == on && time.Since(last.at) < todoTTL/2 {
		return
	}
	var v any
	if on {
		v = "1"
	}
	params := map[string]any{"pane_id": pane, "source": viewSource, "tokens": map[string]any{todoToken: v}}
	if on {
		params["ttl_ms"] = todoTTL.Milliseconds()
	}
	if herdrRequest("pane.report_metadata", params) != nil {
		return
	}
	todoMu.Lock()
	todoSent[pane] = todoReport{on, time.Now()}
	todoMu.Unlock()
}

// ReportTodos publishes the todo token of every dossier with a pane.
func (a *App) ReportTodos() {
	all, _ := a.All()
	for _, d := range all {
		reportTodo(d)
	}
}

// ReportTodo publishes one dossier's todo token; the TUI calls it on reload.
func ReportTodo(d *dossier.Dossier) { reportTodo(d) }
