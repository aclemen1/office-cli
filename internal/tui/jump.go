package tui

import (
	"sort"
	"strings"
	"unicode"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"github.com/aclemen1/office-cli/internal/app"
	"github.com/aclemen1/office-cli/internal/dossier"
)

// jump is the « : » picker: it ranks every dossier of every office against a
// fuzzy query, alias first, then title, then the fiche's content.
type jump struct {
	in   textinput.Model
	hits []jumpHit
	sel  int
	back string // pane that had the focus before `tui-key --focus :`
}

type jumpHit struct {
	sv     *officeView
	d      *dossier.Dossier
	field  string // alias, title or content
	score  int
	closed bool
}

var foldT = transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)

func fold(s string) string {
	out, _, err := transform.String(foldT, strings.ToLower(s))
	if err != nil {
		return strings.ToLower(s)
	}
	return out
}

// fuzzy scores q as a subsequence of s; -1 when it is not one. Consecutive
// letters, word starts and a plain substring score higher.
func fuzzy(q, s string) int {
	if q == "" {
		return 0
	}
	qr, sr := []rune(q), []rune(s)
	score, qi, prev := 0, 0, -2
	for si := 0; si < len(sr) && qi < len(qr); si++ {
		if sr[si] != qr[qi] {
			continue
		}
		score++
		if si == prev+1 {
			score += 5
		}
		if si == 0 || !unicode.IsLetter(sr[si-1]) && !unicode.IsDigit(sr[si-1]) {
			score += 8
		}
		prev = si
		qi++
	}
	if qi < len(qr) {
		return -1
	}
	if i := strings.Index(s, q); i == 0 {
		score += 40
	} else if i > 0 {
		score += 20
	}
	return score - len(sr)/20
}

// contentScore needs every word of q in the text, as written.
func contentScore(q, text string) int {
	words := strings.Fields(q)
	if len(words) == 0 {
		return -1
	}
	for _, w := range words {
		if !strings.Contains(text, w) {
			return -1
		}
	}
	return len(words)
}

func rankJump(offices []*officeView, query string) []jumpHit {
	q := fold(strings.TrimSpace(query))
	var hits []jumpHit
	for _, sv := range offices {
		for _, d := range sv.all {
			h := jumpHit{sv: sv, d: d, closed: d.State == dossier.Done || d.State == dossier.Merged}
			switch {
			case q == "":
				h.field = "title"
			case fuzzy(q, fold(d.Alias+" "+d.ID)) >= 0 && (d.Alias != "" || strings.Contains(fold(d.ID), q)):
				h.field, h.score = "alias", fuzzy(q, fold(d.Alias+" "+d.ID))
			case fuzzy(q, fold(d.Title)) >= 0:
				h.field, h.score = "title", fuzzy(q, fold(d.Title))
			case contentScore(q, fold(d.Body())) >= 0:
				h.field, h.score = "content", contentScore(q, fold(d.Body()))
			default:
				continue
			}
			hits = append(hits, h)
		}
	}
	tier := map[string]int{"alias": 0, "title": 1, "content": 2}
	sort.SliceStable(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		if a.closed != b.closed {
			return !a.closed
		}
		if tier[a.field] != tier[b.field] {
			return tier[a.field] < tier[b.field]
		}
		if a.score != b.score {
			return a.score > b.score
		}
		return a.d.Updated > b.d.Updated
	})
	return hits
}

func (m *model) startJump() {
	m.jump = &jump{in: newInput(""), back: takeReturn()}
	m.jump.hits = rankJump(m.offices, "")
}

// jumpKey edits the query like any field (cursor, words, paste); up and down
// choose, enter goes, esc cancels.
func (m *model) jumpKey(msg tea.Msg) tea.Cmd {
	j := m.jump
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.Keystroke() {
		case "esc":
			m.jump = nil
			if back := j.back; back != "" {
				return func() tea.Msg { _ = app.FocusPane(back); return nil }
			}
			return nil
		case "ctrl+c":
			return tea.Quit
		case "enter":
			m.jump = nil
			if j.sel < len(j.hits) {
				return m.jumpTo(j.hits[j.sel])
			}
			return nil
		case "up", "ctrl+p", "shift+tab":
			if j.sel > 0 {
				j.sel--
			}
			return nil
		case "down", "ctrl+n", "tab":
			if j.sel < len(j.hits)-1 {
				j.sel++
			}
			return nil
		}
	}
	before := j.in.Value()
	var cmd tea.Cmd
	j.in, cmd = j.in.Update(msg)
	if j.in.Value() != before {
		j.hits, j.sel = rankJump(m.offices, j.in.Value()), 0
	}
	return cmd
}

// jumpTo selects the dossier in the list as it is: the view stays as it was.
// In side mode, its agent comes to the right with the focus, even when the
// view hides its row.
func (m *model) jumpTo(h jumpHit) tea.Cmd {
	key := h.sv.root + "|" + h.d.ID
	for i, r := range m.rows {
		if r.selectable() && r.key() == key {
			m.cursor, m.scroll = i, 0
			if m.side {
				return m.dock(&m.rows[i], true)
			}
			m.status, m.statusErr = h.d.Label()+" · "+truncate(h.d.Title, 60), false
			return nil
		}
	}
	if m.side {
		return m.dock(&row{office: h.sv, d: h.d, activity: app.Activity(h.d, h.sv.live)}, true)
	}
	m.status, m.statusErr = h.d.Label()+" is "+h.d.State+": the current view hides it", true
	return nil
}

func (m *model) jumpView(w, h int) string {
	j := m.jump
	head := lipgloss.NewStyle().Bold(true).Foreground(cAccent).Render(" jump ") + j.in.View() +
		sFaint.Render("   alias, then title, then content · ↑ ↓ choose · enter go · esc cancel")
	lines := []string{head, ""}
	first := 0
	if j.sel >= h-2 {
		first = j.sel - (h - 3)
	}
	officeW := 0
	for _, x := range j.hits {
		m.labelW = max(m.labelW, labelWidth(x.d))
		officeW = max(officeW, lipgloss.Width(x.sv.name))
		if x.d.State == dossier.Waiting && m.waitW == 0 {
			m.waitW = 24
		}
	}
	for i := first; i < len(j.hits) && len(lines) < h; i++ {
		x := j.hits[i]
		act := app.Activity(x.d, x.sv.live)
		tail := "  " + sFaint.Render(padRight(x.field, 8)) + sMuted.Render(padRight(x.sv.name, officeW))
		r := row{office: x.sv, d: x.d, activity: act, unread: unreadOf(x.sv, x.d, act)}
		line := m.rowView(r, false, w-1-lipgloss.Width(tail)) + tail
		if i == j.sel {
			line = selectLine(line, w-1)
		} else if m.isDocked(r) {
			line = markDocked(line, w-1)
		}
		lines = append(lines, line)
	}
	if len(j.hits) == 0 {
		lines = append(lines, sMuted.Render("   no dossier matches"))
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines[:h], "\n")
}

func padRight(s string, n int) string {
	if w := lipgloss.Width(s); w < n {
		return s + strings.Repeat(" ", n-w)
	}
	return s
}
