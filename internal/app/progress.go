package app

import (
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/aclemen1/office-cli/internal/acp"
	"github.com/aclemen1/office-cli/internal/connector"
	"github.com/aclemen1/office-cli/internal/dossier"
	"github.com/aclemen1/office-cli/internal/office"
)

// A placeholder tells the user that an item they sent is being handled: the
// connector answers it at once (⏳), office listen shows the agent's steps in
// it, and the agent's answer through tell replaces it; a turn that ends
// without an answer closes it (✓). The open ones are in .office/run/progress.json.

// progressSay writes to office listen's log when it runs in that process.
var progressSay = func(string, ...any) {}

const (
	progressFile    = "progress.json"
	progressMaxAge  = 30 * time.Minute
	progressEvery   = time.Second
	progressQuietOK = 20 * time.Second // an idle session this long after the start has finished
)

type Progress struct {
	Source      string `json:"source"`
	Placeholder string `json:"placeholder"`
	Dossier     string `json:"dossier"`
	Session     string `json:"session"`
	Started     string `json:"started"`
	Cursor      string `json:"cursor,omitempty"`
	Text        string `json:"text,omitempty"`
	Worked      bool   `json:"worked,omitempty"`
}

func (a *App) withProgress(fn func([]Progress) []Progress) {
	unlock, err := a.S.LockSource("progress")
	if err != nil {
		return
	}
	defer unlock()
	var ps []Progress
	if b, err := os.ReadFile(a.S.Meta("run", progressFile)); err == nil {
		_ = json.Unmarshal(b, &ps)
	}
	ps = fn(ps)
	b, _ := json.MarshalIndent(ps, "", "  ")
	_ = os.WriteFile(a.S.Meta("run", progressFile), b, 0o644)
}

// startProgress answers an item delivered to d with a placeholder, when the
// source can.
func (a *App) startProgress(src office.SourceConfig, d *dossier.Dossier, itemRef string) {
	if itemRef == "" || d.Run.Session == "" {
		return
	}
	runner := connector.Runner{Office: a.S, Source: src}
	if desc, err := runner.Describe(); err != nil || !contains(desc.Verbs, "progress") {
		return
	}
	ph, err := runner.Progress("start", itemRef, "", "")
	if err != nil || ph == "" {
		progressSay("%s/%s: placeholder for %s not sent: %v", a.OfficeName(), src.Name, itemRef, err)
		return
	}
	progressSay("%s/%s: placeholder %s for %s (%s)", a.OfficeName(), src.Name, ph, itemRef, d.ID)
	a.withProgress(func(ps []Progress) []Progress {
		return append(ps, Progress{Source: src.Name, Placeholder: ph, Dossier: d.ID, Session: d.Run.Session, Started: time.Now().Format(time.RFC3339)})
	})
}

// takePlaceholders removes and returns the open placeholders of d on a
// source: tell turns the latest into its answer; the earlier ones, for items
// the same answer covers, close with ✓.
func (a *App) takePlaceholders(d *dossier.Dossier, source string) []string {
	var out []string
	a.withProgress(func(ps []Progress) []Progress {
		var kept []Progress
		for _, p := range ps {
			if p.Dossier == d.ID && p.Source == source {
				out = append(out, p.Placeholder)
			} else {
				kept = append(kept, p)
			}
		}
		return kept
	})
	if len(out) > 0 {
		progressSay("%s/%s: tell from %s replaces %s", a.OfficeName(), source, d.ID, out[len(out)-1])
	}
	if len(out) < 2 {
		return out
	}
	if src, ok := a.S.Config.Source(source); ok {
		runner := connector.Runner{Office: a.S, Source: src}
		for _, ph := range out[:len(out)-1] {
			_, _ = runner.Progress("end", "", ph, "✓")
		}
	}
	return out[len(out)-1:]
}

// followProgress updates every open placeholder of the office with the
// session's latest steps, and closes those whose turn ended.
func (a *App) followProgress(conn **acp.Client) {
	var ps []Progress
	if b, err := os.ReadFile(a.S.Meta("run", progressFile)); err != nil || json.Unmarshal(b, &ps) != nil || len(ps) == 0 {
		return
	}
	type change struct {
		p    Progress
		end  bool
		text string
	}
	var changes []change
	for _, p := range ps {
		started, _ := time.Parse(time.RFC3339, p.Started)
		if time.Since(started) > progressMaxAge {
			changes = append(changes, change{p: p, end: true, text: "✓"})
			continue
		}
		if *conn == nil {
			d := &dossier.Dossier{Dir: a.S.Root}
			c, err := a.TailClient(d)
			if err != nil {
				return
			}
			*conn = c
		}
		res, err := (*conn).Tail(p.Session, p.Cursor, 50)
		if err != nil {
			(*conn).Close()
			*conn = nil
			return
		}
		// The first read only sets the cursor: earlier steps belong to earlier items.
		var steps []string
		if p.Cursor != "" {
			steps = progressSteps(res.Updates)
		}
		q := p
		q.Cursor = res.Cursor
		if res.Status == "working" || res.Status == "blocked" {
			q.Worked = true
		}
		if len(steps) > 0 {
			all := strings.Split(q.Text, " · ")
			if q.Text == "" {
				all = nil
			}
			all = append(all, steps...)
			if len(all) > 6 {
				all = all[len(all)-6:]
			}
			q.Text = strings.Join(all, " · ")
		}
		idle := res.Status == "idle" || res.Status == "done" || res.Status == ""
		if idle && (q.Worked || time.Since(started) > progressQuietOK) {
			changes = append(changes, change{p: q, end: true, text: "✓"})
			continue
		}
		if q.Text != p.Text || q.Cursor != p.Cursor || q.Worked != p.Worked {
			changes = append(changes, change{p: q, text: q.Text})
		}
	}
	for _, c := range changes {
		src, ok := a.S.Config.Source(c.p.Source)
		if !ok {
			continue
		}
		runner := connector.Runner{Office: a.S, Source: src}
		var err error
		if c.end {
			_, err = runner.Progress("end", "", c.p.Placeholder, c.text)
			progressSay("%s/%s: placeholder %s closed", a.OfficeName(), c.p.Source, c.p.Placeholder)
		} else if c.text != "" {
			_, err = runner.Progress("update", "", c.p.Placeholder, "⚙ "+c.text)
		}
		if err != nil {
			progressSay("%s/%s: placeholder %s: %v", a.OfficeName(), c.p.Source, c.p.Placeholder, err)
		}
	}
	a.withProgress(func(cur []Progress) []Progress {
		var out []Progress
		for _, p := range cur {
			keep, np := true, p
			for _, c := range changes {
				if c.p.Placeholder == p.Placeholder {
					keep, np = !c.end, c.p
				}
			}
			if keep {
				out = append(out, np)
			}
		}
		return out
	})
}

// progressSteps names the tools the agent called, for the placeholder.
func progressSteps(updates []json.RawMessage) []string {
	var out []string
	for _, raw := range updates {
		var u struct {
			Kind  string `json:"sessionUpdate"`
			Title string `json:"title"`
		}
		if json.Unmarshal(raw, &u) != nil || u.Kind != "tool_call" || u.Title == "" {
			continue
		}
		t := u.Title
		if rest, ok := strings.CutPrefix(t, "mcp__"); ok {
			if server, tool, ok := strings.Cut(rest, "__"); ok {
				t = server + " " + tool
			}
		}
		out = append(out, t)
	}
	return out
}
