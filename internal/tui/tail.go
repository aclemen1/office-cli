package tui

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/aclemen1/office-cli/internal/acp"
	"github.com/aclemen1/office-cli/internal/app"
	"github.com/aclemen1/office-cli/internal/dossier"
)

// The detail panel shows the end of the selected dossier's session, read
// through the ACP server's _session/tail: whatever the agent, office only sees
// ACP updates. One connection per office serves every preview; the last
// sessions seen keep their lines and cursor, so that coming back to a dossier
// shows it at once and asks only for what came since.

const (
	tailEvery  = time.Second
	tailKeep   = 20
	tailCached = 20 // sessions whose preview is kept
)

type tailer struct {
	key     string
	root    string
	a       *app.App
	d       *dossier.Dossier
	cursor  string
	lines   []tailLine
	status  string
	err     string
	touched time.Time
}

type tailLine struct {
	kind, text string
	at         time.Time // when it happened, if the server says (_meta.timestamp)
}

type tailTickMsg struct{}

type tailMsg struct {
	t    *tailer
	conn *acp.Client // a connection opened for this call, to keep
	res  acp.TailResult
	err  error
}

func tailTick() tea.Cmd {
	return tea.Tick(tailEvery, func(time.Time) tea.Msg { return tailTickMsg{} })
}

// tailFor returns the cached preview of a session, or a new one, and drops
// the least recently seen beyond tailCached.
func (m *model) tailFor(key string, r *row) *tailer {
	if m.tailCache == nil {
		m.tailCache = map[string]*tailer{}
	}
	t, ok := m.tailCache[key]
	if !ok {
		t = &tailer{key: key, root: r.office.root, a: r.office.a}
		m.tailCache[key] = t
		if len(m.tailCache) > tailCached {
			var oldest *tailer
			for _, x := range m.tailCache {
				if x != t && (oldest == nil || x.touched.Before(oldest.touched)) {
					oldest = x
				}
			}
			delete(m.tailCache, oldest.key)
		}
	}
	t.d, t.touched = r.d, time.Now()
	return t
}

// tailNext polls the selected dossier's session, and follows the selection.
func (m *model) tailNext() tea.Cmd {
	if m.tailBusy {
		return nil
	}
	r := m.selected()
	if r == nil || m.noDetail || r.d.Run.Session == "" || r.office == nil || r.office.a == nil {
		m.tail = nil
		return nil
	}
	t := m.tailFor(r.office.root+"|"+r.d.ID+"|"+r.d.Run.Session, r)
	m.tail = t
	conn := m.tailConns[t.root]
	m.tailBusy = true
	return func() tea.Msg {
		var opened *acp.Client
		if conn == nil {
			c, err := t.a.TailClient(t.d)
			if err != nil {
				return tailMsg{t: t, err: err}
			}
			conn, opened = c, c
		}
		res, err := conn.Tail(t.d.Run.Session, t.cursor, tailKeep)
		return tailMsg{t: t, conn: opened, res: res, err: err}
	}
}

func (m *model) tailed(msg tailMsg) {
	m.tailBusy = false
	t := msg.t
	if m.tailConns == nil {
		m.tailConns = map[string]*acp.Client{}
	}
	if msg.conn != nil {
		m.tailConns[t.root] = msg.conn
	}
	if msg.err != nil {
		t.err = firstLine(msg.err.Error(), "no preview")
		// The connection may be broken: the next poll opens a new one.
		if c := m.tailConns[t.root]; c != nil {
			delete(m.tailConns, t.root)
			go c.Close()
		}
		return
	}
	t.err = ""
	if msg.res.Reset {
		t.lines = nil
	}
	new := tailLines(msg.res.Updates)
	if n := len(t.lines); n > 0 && len(new) > 0 && t.lines[n-1].kind == "tool" && new[0].kind == "tool" {
		t.lines[n-1].text += " · " + new[0].text
		new = new[1:]
	}
	t.lines = append(t.lines, new...)
	if n := len(t.lines); n > tailKeep {
		t.lines = t.lines[n-tailKeep:]
	}
	t.cursor, t.status = msg.res.Cursor, msg.res.Status
}

// tailLines turns ACP updates into short lines: what the user said, what the
// agent said, the tools it called, its plan.
func tailLines(updates []json.RawMessage) []tailLine {
	var out []tailLine
	for _, raw := range updates {
		var u struct {
			Update  json.RawMessage `json:"update"`
			Kind    string          `json:"sessionUpdate"`
			Title   string          `json:"title"`
			Status  string          `json:"status"`
			Content json.RawMessage `json:"content"`
			Meta    struct {
				Timestamp string `json:"timestamp"`
			} `json:"_meta"`
			Entries []struct {
				Status string `json:"status"`
			} `json:"entries"`
		}
		if json.Unmarshal(raw, &u) != nil {
			continue
		}
		if len(u.Update) > 0 {
			_ = json.Unmarshal(u.Update, &u)
		}
		at, _ := time.Parse(time.RFC3339Nano, u.Meta.Timestamp)
		var c struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(u.Content, &c)
		text := strings.TrimSpace(c.Text)
		switch u.Kind {
		case "user_message_chunk":
			if who, body, ok := peerMessage(text); ok {
				out = append(out, tailLine{kind: "peer", text: who + "\x00" + body, at: at})
			} else if text != "" {
				out = append(out, tailLine{kind: "user", text: text, at: at})
			}
		case "agent_thought_chunk":
			if text != "" {
				out = append(out, tailLine{kind: "thought", text: text, at: at})
			}
		case "agent_message_chunk":
			if text != "" {
				out = append(out, tailLine{kind: "agent", text: text, at: at})
			}
		case "tool_call":
			if u.Title == "" {
				break
			}
			name := toolName(u.Title)
			// Calls in a row share one line.
			if n := len(out); n > 0 && out[n-1].kind == "tool" {
				out[n-1].text += " · " + name
			} else {
				out = append(out, tailLine{kind: "tool", text: name, at: at})
			}
		case "tool_call_update":
			if u.Status == "failed" {
				out = append(out, tailLine{kind: "failed", text: "failed", at: at})
			}
		case "plan":
			done := 0
			for _, e := range u.Entries {
				if e.Status == "completed" {
					done++
				}
			}
			if len(u.Entries) > 0 {
				out = append(out, tailLine{kind: "plan", text: "plan " + strconv.Itoa(done) + "/" + strconv.Itoa(len(u.Entries)), at: at})
			}
		}
	}
	return out
}

// tailVerbose is how many lines the latest entry may take.
const tailVerbose = 12

// tailView renders the preview for the detail panel, w columns wide: the
// latest entry in full, the others on one line; newest first unless the
// user chose otherwise (l).
func (m *model) tailView(r *row, w int) []string {
	t := m.tail
	if t == nil || r == nil || r.office == nil || t.key != r.office.root+"|"+r.d.ID+"|"+r.d.Run.Session {
		return nil
	}
	if t.err != "" {
		return []string{sFaint.Render(truncate(t.err, w))}
	}
	if len(t.lines) == 0 {
		return []string{sFaint.Render("nothing yet")}
	}
	n := len(t.lines)
	blocks := make([][]string, n)
	timed := false
	for _, l := range t.lines {
		timed = timed || !l.at.IsZero()
	}
	for i, l := range t.lines {
		blocks[i] = renderTailLine(l, w, i == n-1, timed)
	}
	// A blank line sets each exchange apart: before a message that comes in,
	// read in the chosen order.
	starts := func(i int) bool { return t.lines[i].kind == "user" || t.lines[i].kind == "peer" }
	var out []string
	for i := range blocks {
		j := i
		if !m.liveOldestFirst {
			j = n - 1 - i
		}
		if m.liveOldestFirst && starts(j) && len(out) > 0 {
			out = append(out, "")
		}
		out = append(out, blocks[j]...)
		if !m.liveOldestFirst && starts(j) && i < n-1 {
			out = append(out, "")
		}
	}
	return out
}

func renderTailLine(l tailLine, w int, verbose, timed bool) []string {
	// The time, dimmed, leads the first line of an entry when the server gives it.
	stamp, pad := "", ""
	if timed {
		stamp, pad = "      ", "      "
		if !l.at.IsZero() {
			stamp = sFaint.Render(l.at.Local().Format("15:04")) + " "
		}
		w -= 6
	}
	mark, style := "  ", sText
	switch l.kind {
	case "user":
		mark, style = lipgloss.NewStyle().Foreground(cAccent).Render("› "), lipgloss.NewStyle().Foreground(cAccent).Bold(true)
	case "tool":
		mark, style = sMuted.Render("⚙ "), sMuted
	case "failed":
		mark, style = lipgloss.NewStyle().Foreground(cStopped).Render("✗ "), lipgloss.NewStyle().Foreground(cStopped)
	case "plan":
		mark, style = sMuted.Render("☰ "), sMuted
	case "thought":
		mark, style = sFaint.Render("∴ "), sFaint
	}
	text := l.text
	if l.kind == "peer" {
		who, body, _ := strings.Cut(l.text, "\x00")
		mark = sMuted.Render("⇄ ") + lipgloss.NewStyle().Foreground(cWaiting).Bold(true).Render(who) + " "
		text, w = body, w-lipgloss.Width(who)-1
	}
	if !verbose {
		return []string{stamp + mark + style.Render(truncate(firstLine(text, ""), w-2))}
	}
	var out []string
	for _, para := range strings.Split(text, "\n") {
		wrapped := lipgloss.NewStyle().Width(max(10, w-2)).Render(para)
		for _, line := range strings.Split(wrapped, "\n") {
			if len(out) == tailVerbose {
				return append(out, "  "+sFaint.Render("…"))
			}
			lead := pad + "  "
			if len(out) == 0 {
				lead = stamp + mark
			}
			out = append(out, lead+style.Render(strings.TrimRight(line, " ")))
		}
	}
	return out
}

var (
	peerOpenRe = regexp.MustCompile(`(?s)^\s*<cross-session-message\b([^>]*)>\s*(.*?)\s*(?:</cross-session-message>\s*)?$`)
	peerNameRe = regexp.MustCompile(`from-name="([^"]*)"`)
)

// peerMessage reads a message another session sent through Claude Code's
// messaging: who sent it, and its text without the envelope.
func peerMessage(s string) (who, body string, ok bool) {
	m := peerOpenRe.FindStringSubmatch(s)
	if m == nil {
		return "", "", false
	}
	who = "another session"
	if n := peerNameRe.FindStringSubmatch(m[1]); n != nil && n[1] != "" {
		who = n[1]
	}
	return who, strings.TrimSpace(m[2]), true
}

// toolName shows mcp__office__notify as "office notify".
func toolName(t string) string {
	if rest, ok := strings.CutPrefix(t, "mcp__"); ok {
		if server, tool, ok := strings.Cut(rest, "__"); ok {
			return server + " " + tool
		}
	}
	return t
}

// liveTitle names the preview section with its order.
func (m *model) liveTitle() string {
	if m.liveOldestFirst {
		return "Live · oldest first"
	}
	return "Live · newest first"
}
