package app

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"time"

	"github.com/aclemen1/dossier-cli/internal/spec"
	"github.com/aclemen1/dossier-cli/internal/store"
)

// herdr's agent list shows every agent of the session; agent.view.set filters
// it. The dossiers' view hides the agents of the stores' workspaces, except
// those that wait for the user and those of the workspace on screen. herdr
// forgets the view when its server restarts: the TUI sets it at each start.

const viewSource = "dossier"

func herdrSocket() string {
	if p := os.Getenv("HERDR_SOCKET_PATH"); p != "" {
		return p
	}
	return store.ExpandHome("~/.config/herdr/herdr.sock")
}

// herdrRequest sends one request on herdr's socket and reads its answer.
var herdrRequest = func(method string, params any) error {
	conn, err := net.DialTimeout("unix", herdrSocket(), 2*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	b, _ := json.Marshal(map[string]any{"id": "dossier:" + method, "method": method, "params": params})
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

// workspaceLabel is the herdr workspace the store's ACP server places its
// agents in: the value of --workspace in [acp] command.
func workspaceLabel(s *store.Store) string {
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

// SetAgentView hides the stores' agents from herdr's agent list.
func SetAgentView(roots []string) error {
	labels := map[string]bool{}
	for _, root := range roots {
		if s, err := store.Open(root); err == nil {
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
		map[string]any{"op": "eq", "field": "workspace_id", "value": map[string]any{"context": "current_workspace_id"}},
	}}
	return herdrRequest("agent.view.set", map[string]any{"source": viewSource, "label": "without dossiers", "filter": filter})
}

// ClearAgentView shows every agent again.
func ClearAgentView() error {
	return herdrRequest("agent.view.clear", map[string]any{"source": viewSource})
}

// HerdrReady checks that herdr is installed and its server answers: dossier
// runs its agents in herdr panes and reads their state there.
func HerdrReady() error {
	if _, err := exec.LookPath("herdr"); err != nil {
		return spec.UserError("dossier needs herdr, which is not on the PATH. Install it from https://herdr.dev")
	}
	conn, err := net.DialTimeout("unix", herdrSocket(), time.Second)
	if err != nil {
		return spec.UserError("dossier needs a running herdr server; none answers on %s. Start herdr, then retry", herdrSocket())
	}
	return conn.Close()
}
