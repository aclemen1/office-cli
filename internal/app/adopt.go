package app

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/aclemen1/dossier-cli/internal/dossier"
	"github.com/aclemen1/dossier-cli/internal/spec"
)

// AgentPane is a Claude Code agent running in a herdr pane.
type AgentPane struct {
	PaneID  string `json:"pane_id"`
	TabID   string `json:"tab_id"`
	Session string `json:"session"`
	Cwd     string `json:"cwd"`
	Title   string `json:"title"`
	Status  string `json:"status"`
}

type herdrPane struct {
	PaneID       string `json:"pane_id"`
	TabID        string `json:"tab_id"`
	Agent        string `json:"agent"`
	AgentStatus  string `json:"agent_status"`
	Cwd          string `json:"cwd"`
	Title        string `json:"terminal_title_stripped"`
	AgentSession struct {
		Value string `json:"value"`
	} `json:"agent_session"`
}

func (p herdrPane) agent() (AgentPane, bool) {
	if p.Agent != "claude" || p.AgentSession.Value == "" {
		return AgentPane{}, false
	}
	return AgentPane{PaneID: p.PaneID, TabID: p.TabID, Session: p.AgentSession.Value, Cwd: p.Cwd, Title: p.Title, Status: p.AgentStatus}, true
}

// Agents lists the Claude Code agents running in herdr panes.
func Agents() []AgentPane {
	out, err := herdrCall("pane", "list")
	if err != nil {
		return nil
	}
	var r struct {
		Result struct {
			Panes []herdrPane `json:"panes"`
		} `json:"result"`
	}
	_ = json.Unmarshal(out, &r)
	var list []AgentPane
	for _, p := range r.Result.Panes {
		if a, ok := p.agent(); ok {
			list = append(list, a)
		}
	}
	return list
}

func agentIn(pane string) (AgentPane, error) {
	out, err := herdrCall("pane", "get", pane)
	if err != nil {
		return AgentPane{}, err
	}
	var r struct {
		Result struct {
			Pane herdrPane `json:"pane"`
		} `json:"result"`
	}
	_ = json.Unmarshal(out, &r)
	a, ok := r.Result.Pane.agent()
	if !ok {
		return AgentPane{}, spec.UserError("pane %s runs no Claude Code agent with a session", pane)
	}
	return a, nil
}

// SessionOwner is the dossier or desk whose session this is, if any.
func (a *App) SessionOwner(session string) *dossier.Dossier {
	all, _ := a.All()
	for _, d := range append(all, a.Desk()) {
		if d.Run.Session == session && d.State != dossier.Merged {
			return d
		}
	}
	return nil
}

type AdoptParams struct {
	Pane, Title, Instruction string
	In                       []string
}

// Adopt makes a new dossier of an agent the user started: its conversation
// becomes the dossier's session and keeps running where it is.
func (a *App) Adopt(p AdoptParams) (OpenResult, error) {
	if p.Pane == "" {
		return OpenResult{}, spec.UserError("adopt needs the agent's pane: --pane w5:p3, or run it inside that pane ($HERDR_PANE_ID)")
	}
	ag, err := agentIn(p.Pane)
	if err != nil {
		return OpenResult{}, err
	}
	if owner := a.SessionOwner(ag.Session); owner != nil {
		return OpenResult{}, spec.UserError("this agent is already the session of %s (%s)", owner.Label(), owner.Title)
	}
	if p.Title == "" {
		p.Title = ag.Title
	}
	if p.Instruction == "" {
		p.Instruction = fmt.Sprintf("Adopted agent, started by the user in %s. Its conversation so far is this dossier's history.", ag.Cwd)
	}
	res, err := a.Open(OpenParams{Title: p.Title, Instruction: p.Instruction, SourceRef: "herdr:session/" + ag.Session, In: p.In, NoStart: true})
	if err != nil || res.Outcome != "created" {
		return res, err
	}
	d, err := a.Load(res.ID)
	if err != nil {
		return res, err
	}
	d.Run.Session, d.Run.PaneID, d.Run.TabID, d.Run.Cwd, d.Run.Adopted = ag.Session, ag.PaneID, ag.TabID, ag.Cwd, true
	if err := d.Save(); err != nil {
		return res, err
	}
	_ = d.Log("adopted session %s from pane %s (%s)", ag.Session, ag.PaneID, ag.Cwd)
	_ = a.Archive(d)
	res.Session, res.TabID, res.Outcome = ag.Session, ag.TabID, "adopted"
	return res, nil
}

// takeOver ends the agent the user started and resumes its conversation
// through the ACP server, which gives it the dossier's tools and environment.
func (a *App) takeOver(d *dossier.Dossier) error {
	ag, err := agentIn(d.Run.PaneID)
	if err == nil {
		if ag.Status == "working" || ag.Status == "blocked" {
			return spec.UserError("%s is %s: restart it once its turn has ended", d.Label(), ag.Status)
		}
		if _, err := herdrCall("pane", "send-text", d.Run.PaneID, "/exit"); err != nil {
			return err
		}
		if _, err := herdrCall("pane", "send-keys", d.Run.PaneID, "enter"); err != nil {
			return err
		}
		deadline := time.Now().Add(30 * time.Second)
		for {
			if _, err := agentIn(d.Run.PaneID); err != nil {
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("%s: the agent in pane %s did not exit; end it, then `dossier restart %s`", d.Label(), d.Run.PaneID, d.ID)
			}
			time.Sleep(time.Second)
		}
	}
	old := d.Run.PaneID
	d.Run.PaneID, d.Run.TabID, d.Run.Adopted = "", "", false
	if err := a.resume(d); err != nil {
		_ = d.Save()
		return err
	}
	_ = d.Log("taken over from pane %s: session resumed in tab %s", old, d.Run.TabID)
	return d.Save()
}
