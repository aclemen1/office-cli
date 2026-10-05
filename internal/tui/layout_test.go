package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/aclemen1/office-cli/internal/dossier"
)

func TestFlowWrapsSegmentsWithoutCuttingThem(t *testing.T) {
	lines := flow([]string{"aaaa", "bbbb", "cccc"}, " · ", 11)
	if len(lines) != 2 || lines[0] != "aaaa · bbbb" || lines[1] != "cccc" {
		t.Fatalf("lines %q", lines)
	}
	if got := flow([]string{"", "x"}, " · ", 10); len(got) != 1 || got[0] != "x" {
		t.Fatalf("empty segments should vanish: %q", got)
	}
}

func TestTheScrollbarShowsWhereTheViewIs(t *testing.T) {
	if bar := scrollbar(4, 4, 0); strings.TrimSpace(strings.Join(bar, "")) != "" {
		t.Fatalf("no scrolling, no bar: %q", bar)
	}
	thumb := func(bar []string) int {
		for i, b := range bar {
			if ansi.Strip(b) == "┃" {
				return i
			}
		}
		return -1
	}
	if top, bottom := thumb(scrollbar(10, 100, 0)), thumb(scrollbar(10, 100, 90)); top != 0 || bottom < 8 {
		t.Fatalf("thumb at top %d, at bottom %d", top, bottom)
	}
	for _, l := range withBar([]string{"abc", "a very long line indeed"}, 10, 40, 0) {
		if w := lipgloss.Width(l); w != 10 {
			t.Fatalf("width %d of %q", w, ansi.Strip(l))
		}
	}
}

func TestTheSelectedLineIsPlainAndMarked(t *testing.T) {
	line := selectLine(" "+lipgloss.NewStyle().Foreground(cAccent).Render("U-0001")+" open", 20)
	plain := ansi.Strip(line)
	if !strings.HasPrefix(plain, "▌U-0001 open") || lipgloss.Width(line) != 20 {
		t.Fatalf("selected %q (width %d)", plain, lipgloss.Width(line))
	}
}

func TestTheHeaderAndFooterWrapOnANarrowTUI(t *testing.T) {
	m := &model{width: 40, height: 30, side: true}
	for _, l := range append(m.topBar(38), m.bottomBar(38)...) {
		if lipgloss.Width(l) > 38 {
			t.Fatalf("line wider than the TUI: %q", ansi.Strip(l))
		}
	}
	if n := len(m.bottomBar(38)); n < 4 {
		t.Fatalf("a narrow footer should wrap over several lines, got %d", n)
	}
}

func TestLCyclesThePanelPlace(t *testing.T) {
	m := &model{width: 80}
	if m.detailAt() != "bottom" {
		t.Fatal("auto puts the panel below a narrow list")
	}
	m.key("L")
	if m.detailAt() != "right" {
		t.Fatalf("L once: %s", m.layoutName())
	}
	m.key("L")
	m.key("L")
	if m.layoutName() != "auto" {
		t.Fatalf("L three times comes back to auto: %s", m.layoutName())
	}
}

func TestTheFooterOffersToScrollAnOverflowingDetail(t *testing.T) {
	has := func(m *model) bool {
		for _, g := range m.footer() {
			for _, k := range g.keys {
				if k[0] == "J K" {
					return true
				}
			}
		}
		return false
	}
	m := &model{width: 80, height: 30}
	m.window([]string{"a", "b"}, 10)
	if has(m) {
		t.Fatal("nothing to scroll")
	}
	m.window(make([]string, 40), 10)
	if !has(m) {
		t.Fatal("an overflowing detail panel should offer J K")
	}
}

func TestTheSelectionKeepsTheRowsColours(t *testing.T) {
	mark := lipgloss.NewStyle().Foreground(cReady).Render("●")
	fg, _, _ := strings.Cut(mark, "●")
	line := selectLine(" "+mark+" U-0001 open", 30)
	if !strings.Contains(line, fg) {
		t.Fatal("the agent's mark lost its colour on the selected row")
	}
	bg, _, _ := strings.Cut(lipgloss.NewStyle().Background(cSel).Render("|"), "|")
	if strings.Count(line, bg) < 2 {
		t.Fatal("the background should come back after the mark's reset")
	}
	if lipgloss.Width(line) != 30 {
		t.Fatalf("width %d", lipgloss.Width(line))
	}
}

func TestTheHeaderTellsToDoFromNoAction(t *testing.T) {
	sv := &officeView{name: "pro", count: map[string]int{dossier.Waiting: 1}, all: []*dossier.Dossier{
		{ID: "U-1", State: dossier.Open}, {ID: "U-2", State: dossier.Open, NoAction: true},
		{ID: "U-3", State: dossier.Open, NoAction: true}, {ID: "U-4", State: dossier.Waiting},
	}}
	m := &model{offices: []*officeView{sv}}
	got := ansi.Strip(strings.Join(m.topBar(200), " "))
	if !strings.Contains(got, "pro  1 to do  2 no action  1 waiting") {
		t.Fatalf("header %q", got)
	}
}

func TestLinksAreUnderlinedLightlyWithDots(t *testing.T) {
	s := link(sText).Render("0001-mail.md")
	if !strings.Contains(s, "4:4") || !strings.Contains(s, "58;") {
		t.Fatalf("expected a dotted underline with its own colour: %q", s)
	}
}

func TestTheFicheRendersItsMarkdown(t *testing.T) {
	md := "\n## Instruction\n\nAlain traite ce point **ce week-end**.\n\n- premier point\n- second point\n"
	out := renderNotes(md, 40)
	plain := ansi.Strip(out)
	if strings.Contains(plain, "##") || strings.Contains(plain, "**") || !strings.Contains(plain, "• premier point") {
		t.Fatalf("not rendered: %q", plain)
	}
	for _, l := range strings.Split(out, "\n") {
		if lipgloss.Width(l) > 40 {
			t.Fatalf("a line is wider than the panel: %q", ansi.Strip(l))
		}
	}
	if renderNotes(md, 40) != out || len(mdCache) == 0 {
		t.Fatal("the rendering should come from the cache the second time")
	}
}

func TestHeaderKeepsItsHeightForAMoment(t *testing.T) {
	m := &model{}
	if got := m.steadyHeader([]string{"a", "b"}); len(got) != 2 {
		t.Fatalf("first %v", got)
	}
	if got := m.steadyHeader([]string{"a"}); len(got) != 2 {
		t.Fatalf("shrank at once: %v", got)
	}
	if got := m.steadyHeader([]string{"a", "b", "c"}); len(got) != 2 {
		t.Fatalf("grew at once: %v", got)
	}
	m.headAt = m.headAt.Add(-2 * headerHold)
	if got := m.steadyHeader([]string{"a"}); len(got) != 1 {
		t.Fatalf("after the hold: %v", got)
	}
}
