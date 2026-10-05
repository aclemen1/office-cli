package app

import (
	"bufio"
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/aclemen1/office-cli/internal/dossier"
	"github.com/aclemen1/office-cli/internal/spec"
)

// Usage sums up how the user and the agents used office over a period, for an
// analysis that proposes improvements. It holds counts and office's own
// messages, never what third parties wrote; FactsOnly drops the texts that
// agents or the user wrote (escalations, corrections) too.
type Usage struct {
	Office             string         `json:"office"`
	Since              string         `json:"since"`
	Dossiers           int            `json:"dossiers_active"`
	Sessions           int            `json:"sessions_read"`
	ToolCalls          map[string]int `json:"tool_calls"`
	ToolErrors         []UsageItem    `json:"tool_errors"`
	CLICalls           map[string]int `json:"cli_calls"`
	CLIErrors          []UsageItem    `json:"cli_errors"`
	PermissionDenied   []UsageItem    `json:"permission_denied"`
	Corrections        []UsageItem    `json:"user_corrections"`
	FreeAnswers        int            `json:"question_free_answers"`
	Merges             int            `json:"merges"`
	Moves              int            `json:"moves"`
	ReopenedSoon       []Reopened     `json:"reopened_within_48h"`
	UnknownVerbs       []UsageItem    `json:"cli_unknown_verbs"`
	PendingTransitions int            `json:"pending_transitions"`
	Escalations        []string       `json:"escalations"`
}

// Reopened is a dossier closed and reopened within 48 hours: why it was
// closed, and what came after, to tell an early close from a new affair.
type Reopened struct {
	Dossier string `json:"dossier"`
	Closed  string `json:"closed,omitempty"`
	After   string `json:"after,omitempty"`
}

// UsageItem is one kind of event, how often it happened and where.
type UsageItem struct {
	What     string   `json:"what"`
	Count    int      `json:"count"`
	Dossiers []string `json:"dossiers"`
}

var (
	quotedRe     = regexp.MustCompile(`"[^"]{0,400}"|«[^»]{0,400}»|“[^”]{0,400}”`)
	correctionRe = regexp.MustCompile(`(?i)(^|\W)(non[ ,.!]|pas ça|plutôt|je préf[eè]re|j'aimerais plutôt|au lieu|ce n'est pas|c'est faux|tu as oublié|ça ne marche pas|ne fonctionne pas|bug)`)
	officeCLIRe  = regexp.MustCompile(`(?m)(?:^|[;&|(]|\$\()\s*(?:\S*/)?(?:office|theoffice|dossier)\s+([a-z][a-z-]+)(?:\s|$|[;&|)])`)
)

// scrub keeps the shape of an office message and drops quoted values, which
// may carry titles or text.
func scrub(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = quotedRe.ReplaceAllString(s, `"…"`)
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200]) + "…"
	}
	return s
}

type tally struct{ m map[string]*UsageItem }

func (t *tally) add(what, id string) {
	if t.m == nil {
		t.m = map[string]*UsageItem{}
	}
	it, ok := t.m[what]
	if !ok {
		it = &UsageItem{What: what}
		t.m[what] = it
	}
	it.Count++
	for _, x := range it.Dossiers {
		if x == id {
			return
		}
	}
	it.Dossiers = append(it.Dossiers, id)
}

func (t *tally) list() []UsageItem {
	out := []UsageItem{}
	for _, it := range t.m {
		out = append(out, *it)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].What < out[j].What
	})
	return out
}

// Usage reads the conversations, histories and escalations of the office
// since the given time.
func (a *App) Usage(since time.Time, factsOnly bool) Usage {
	u := Usage{Office: a.OfficeName(), Since: since.Format(time.RFC3339), ToolCalls: map[string]int{}, CLICalls: map[string]int{}}
	all, _ := a.All()
	all = append(all, a.Desk())
	var toolErr, cliErr, denied, corr, unknown tally
	for _, d := range all {
		if d.State == dossier.Open || d.State == dossier.Waiting {
			u.Dossiers++
		}
		u.PendingTransitions += len(d.Run.PendingTransitions)
		a.usageLog(d, since, factsOnly, &u)
		if d.Run.Session == "" {
			continue
		}
		path := transcriptOf(d.Run.Session)
		if path == "" {
			continue
		}
		if fi, err := os.Stat(path); err != nil || fi.ModTime().Before(since) {
			continue
		}
		u.Sessions++
		usageTranscript(path, d.ID, since, factsOnly, &u, &toolErr, &cliErr, &denied, &corr, &unknown)
	}
	u.ToolErrors, u.CLIErrors, u.PermissionDenied, u.Corrections = toolErr.list(), cliErr.list(), denied.list(), corr.list()
	u.UnknownVerbs = unknown.list()
	u.Escalations = []string{}
	if !factsOnly {
		desk := a.Desk()
		for _, st := range []string{escalationPending, escalationDelivered, escalationResolved} {
			for _, e := range escalationsIn(desk, st) {
				if fi, err := os.Stat(e.path(desk)); err == nil && !fi.ModTime().Before(since) {
					u.Escalations = append(u.Escalations, e.Status+" · "+e.Text)
				}
			}
		}
	}
	if u.ReopenedSoon == nil {
		u.ReopenedSoon = []Reopened{}
	}
	return u
}

var logLineRe = regexp.MustCompile(`^- (\S+) · (.*)$`)

func (a *App) usageLog(d *dossier.Dossier, since time.Time, factsOnly bool, u *Usage) {
	f, err := os.Open(d.Path("log.md"))
	if err != nil {
		return
	}
	defer f.Close()
	var closedAt time.Time
	closedWhy := ""
	after := -1 // the reopening that waits for the next line
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		m := logLineRe.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		at, err := time.Parse(time.RFC3339, m[1])
		if err != nil {
			continue
		}
		line := m[2]
		if strings.Contains(line, "→ done") {
			closedAt, closedWhy = at, line
		}
		if after >= 0 && !at.Before(since) {
			if !factsOnly {
				u.ReopenedSoon[after].After = scrub(line)
			}
			after = -1
		}
		if at.Before(since) {
			continue
		}
		switch {
		case strings.HasPrefix(line, "merged into "):
			u.Merges++
		case strings.HasPrefix(line, "moved from "):
			u.Moves++
		case strings.HasPrefix(line, "done → open") && !closedAt.IsZero() && at.Sub(closedAt) < 48*time.Hour:
			r := Reopened{Dossier: d.ID}
			if !factsOnly {
				r.Closed = scrub(closedWhy)
			}
			u.ReopenedSoon = append(u.ReopenedSoon, r)
			after = len(u.ReopenedSoon) - 1
			continue
		}
	}
}

func textOf(c json.RawMessage) string {
	var s string
	if json.Unmarshal(c, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(c, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Type == "text" {
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return ""
}

func usageTranscript(path, id string, since time.Time, factsOnly bool, u *Usage, toolErr, cliErr, denied, corr, unknown *tally) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	type use struct{ name, cli string }
	uses := map[string]use{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		var e struct {
			Type      string `json:"type"`
			Timestamp string `json:"timestamp"`
			Origin    struct {
				Kind string `json:"kind"`
			} `json:"origin"`
			Message struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		if at, err := time.Parse(time.RFC3339Nano, e.Timestamp); err != nil || at.Before(since) {
			continue
		}
		var blocks []struct {
			Type      string          `json:"type"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			ToolUseID string          `json:"tool_use_id"`
			IsError   bool            `json:"is_error"`
			Content   json.RawMessage `json:"content"`
			Input     struct {
				Command string `json:"command"`
			} `json:"input"`
		}
		_ = json.Unmarshal(e.Message.Content, &blocks)
		if e.Origin.Kind == "human" && len(blocks) == 0 {
			// A pasted block (a mail, a log) is not the user's own words.
			if t := textOf(e.Message.Content); correctionRe.MatchString(t) && !strings.Contains(t, "<pasted_content") {
				what := "correction"
				if !factsOnly {
					what = scrub(t)
				}
				corr.add(what, id)
			}
			continue
		}
		for _, b := range blocks {
			switch b.Type {
			case "tool_use":
				x := use{name: b.Name}
				switch {
				case strings.HasPrefix(b.Name, "mcp__office__"), strings.HasPrefix(b.Name, "mcp__dossier__"):
					u.ToolCalls[b.Name[strings.LastIndex(b.Name, "__")+2:]]++
				case b.Name == "Bash":
					if m := officeCLIRe.FindStringSubmatch(b.Input.Command); m != nil {
						x.cli = m[1]
						if spec.FindVerb(m[1]) == nil {
							x.cli += " (unknown verb)"
							unknown.add(m[1], id)
						} else {
							u.CLICalls[x.cli]++
						}
					}
				}
				uses[b.ID] = x
			case "tool_result":
				x, ok := uses[b.ToolUseID]
				if !ok || !b.IsError {
					if ok && x.name == "AskUserQuestion" && strings.Contains(textOf(b.Content), "Other") {
						u.FreeAnswers++
					}
					continue
				}
				msg := textOf(b.Content)
				switch {
				case strings.Contains(msg, "has been denied") || strings.Contains(msg, "permission"):
					tool := x.name
					if x.cli != "" {
						tool = "Bash office " + x.cli
					}
					denied.add(tool, id)
				case strings.HasPrefix(x.name, "mcp__office__"), strings.HasPrefix(x.name, "mcp__dossier__"):
					toolErr.add(x.name[strings.LastIndex(x.name, "__")+2:]+": "+scrub(errorMessage(msg)), id)
				case x.cli != "":
					cliErr.add(x.cli+": "+scrub(errorMessage(msg)), id)
				}
			}
		}
	}
}

// errorMessage pulls the message out of office's JSON envelope.
func errorMessage(s string) string {
	var env struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if i := strings.Index(s, "{"); i >= 0 && json.Unmarshal([]byte(s[i:]), &env) == nil && env.Error.Message != "" {
		return env.Error.Message
	}
	if i := strings.Index(s, "error: "); i >= 0 {
		return s[i+7:]
	}
	return s
}
