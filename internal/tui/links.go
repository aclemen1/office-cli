package tui

import (
	"os/exec"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// detailLink is something the detail panel names that can be opened: a linked
// dossier, a source's address, a file of the dossier.
type detailLink struct {
	line   int    // line in the detail panel, before scrolling
	kind   string // dossier, url or file
	target string // dossier id or label, address, absolute path
}

// linkLine renders a link of the detail panel, marked when it is the one
// chosen with f.
func (m *model) linkLine(text string, l detailLink, w int) string {
	if m.linkMode && len(m.links) == m.linkSel {
		return selectLine(" "+text, w)
	}
	return text
}

// linkKey handles the keys of link mode; false when the key means nothing there.
func (m *model) linkKey(k string) (tea.Cmd, bool) {
	switch k {
	case "j", "down", "tab":
		if m.linkSel < len(m.links)-1 {
			m.linkSel++
		}
	case "k", "up", "shift+tab":
		if m.linkSel > 0 {
			m.linkSel--
		}
	case "enter", "o":
		if m.linkSel < len(m.links) {
			return m.openLink(m.links[m.linkSel]), true
		}
	case "esc", "f":
		m.linkMode = false
	case "q", "ctrl+c":
		return nil, false
	default:
		return nil, true
	}
	return nil, true
}

// openLink selects a linked dossier in the list, or opens a file or an
// address with macOS's default application.
func (m *model) openLink(l detailLink) tea.Cmd {
	m.linkMode = false
	if l.kind == "dossier" {
		for i, r := range m.rows {
			if r.d != nil && (r.d.ID == l.target || r.d.Label() == l.target) {
				m.cursor, m.scroll = i, 0
				return nil
			}
		}
		m.status, m.statusErr = l.target+" is not in this view: g a shows every state", true
		return nil
	}
	if err := openExternal(l.target); err != nil {
		m.status, m.statusErr = "open "+l.target+": "+err.Error(), true
		return nil
	}
	name := l.target
	if l.kind == "file" {
		name = filepath.Base(l.target)
	}
	m.status, m.statusErr = "opened "+name, false
	return nil
}

// linkAt is the link on a screen line of the detail panel, if any.
func (m *model) linkAt(y int) (detailLink, bool) {
	line := y - m.detailTop + m.scroll
	for _, l := range m.links {
		if l.line == line {
			return l, true
		}
	}
	return detailLink{}, false
}

// openExternal opens a file or an address with macOS's default application. Tests replace it.
var openExternal = func(target string) error { return exec.Command("open", target).Start() }

func isAddress(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// link underlines a style lightly, dotted and grey: what the detail panel can
// open, without changing the colour of its text.
func link(s lipgloss.Style) lipgloss.Style {
	return s.Underline(true).UnderlineStyle(lipgloss.UnderlineDotted).UnderlineColor(cMuted)
}
