package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Layouts of the detail panel: auto puts it at the right when the TUI is
// wide enough, below the list otherwise.
var layouts = []string{"auto", "right", "bottom"}

func (m *model) detailAt() string {
	switch m.layout {
	case "right", "bottom":
		return m.layout
	}
	if m.wide() {
		return "right"
	}
	return "bottom"
}

func (m *model) cycleLayout() {
	for i, l := range layouts {
		if l == m.layoutName() {
			m.layout = layouts[(i+1)%len(layouts)]
			break
		}
	}
	m.status, m.statusErr = "detail panel: "+m.layoutName(), false
}

func (m *model) layoutName() string {
	if m.layout == "" {
		return "auto"
	}
	return m.layout
}

// flow lays segments out on as many lines as the width needs, never cutting one.
func flow(segments []string, sep string, width int) []string {
	var lines []string
	cur := ""
	for _, s := range segments {
		if s == "" {
			continue
		}
		switch {
		case cur == "":
			cur = s
		case lipgloss.Width(cur)+lipgloss.Width(sep)+lipgloss.Width(s) <= width:
			cur += sep + s
		default:
			lines = append(lines, cur)
			cur = s
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// scrollbar is a one-column track for h lines showing total lines from offset.
func scrollbar(h, total, offset int) []string {
	bar := make([]string, h)
	if total <= h || h <= 0 {
		for i := range bar {
			bar[i] = " "
		}
		return bar
	}
	size := max(1, h*h/total)
	top := offset * (h - size) / max(1, total-h)
	for i := range bar {
		if i >= top && i < top+size {
			bar[i] = lipgloss.NewStyle().Foreground(cAccent).Render("┃")
		} else {
			bar[i] = sFaint.Render("│")
		}
	}
	return bar
}

// withBar pads each line to w-1 columns and appends the scrollbar.
func withBar(lines []string, w, total, offset int) []string {
	bar := scrollbar(len(lines), total, offset)
	out := make([]string, len(lines))
	for i, l := range lines {
		l = ansi.Truncate(l, w-1, "")
		out[i] = l + strings.Repeat(" ", max(0, w-1-lipgloss.Width(l))) + bar[i]
	}
	return out
}

var sSelected = lipgloss.NewStyle().Bold(true).Foreground(cText).Background(cSel)

// selectLine shows the selected row plainly on a strong background, with a bar
// in the accent colour: the colours of its marks would hide the selection.
func selectLine(line string, w int) string {
	plain := []rune(ansi.Strip(line))
	if len(plain) > 0 {
		plain = plain[1:]
	}
	return lipgloss.NewStyle().Foreground(cAccent).Background(cSel).Bold(true).Render("▌") +
		sSelected.Width(max(1, w-1)).MaxWidth(max(1, w-1)).Render(string(plain))
}
