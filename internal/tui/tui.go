// Package tui is an interactive overview of every store under a root: the
// dossiers, their links, what their agents do, and a jump to their pane.
package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/aclemen1/dossier-cli/internal/app"
	"github.com/aclemen1/dossier-cli/internal/dossier"
	"github.com/aclemen1/dossier-cli/internal/store"
)

const refreshEvery = 5 * time.Second

// Run starts the TUI on the stores found under root.
// Inside a herdr pane it starts in side mode, unless noSide.
func Run(root string, noSide, reset bool) error {
	roots := store.Discover(root)
	if len(roots) == 0 {
		return fmt.Errorf("no dossier store under %s: a store is a directory holding .dossier/config.toml", root)
	}
	go func() { _ = app.SetAgentView(roots) }()
	m := &model{roots: roots, root: root}
	saved := loadState(root)
	if reset {
		saved = defaultState
	}
	m.start = m.restore(saved, !noSide && os.Getenv("HERDR_PANE_ID") != "")
	_, err := tea.NewProgram(m).Run()
	return err
}

type model struct {
	root            string
	roots           []string
	rows            []row
	stores          []*storeView
	byPerson        bool
	byPriority      bool
	waitW           int // width of the waiting column, 0 when nothing waits
	errs            []string
	cursor          int
	offset          int
	scroll          int // first line of the detail panel
	width           int
	height          int
	all             bool
	todo            bool
	ask             *ask // the form on the bottom line, when one is open
	filter          string
	typing          bool
	noDetail        bool   // the detail panel is hidden
	layout          string // where the detail panel goes: auto, right or bottom
	listY           int    // screen line of the list's first row, set by render
	listW           int
	listH           int
	detailW         int
	detailOverflows bool // the detail panel has more lines than room, as last drawn
	legend          bool // the detail panel shows what marks and colours mean
	agentsView      bool // g n: only the agents that no dossier holds
	starredView     bool // g s: only the starred dossiers
	gPending        bool // g was typed: the next key goes somewhere
	side            bool // enter docks the agent at the TUI's right
	placeholder     string
	docked          docked
	lastDocked      docked  // the one before, for '
	docking         bool    // a dock or undock runs
	wantDock        *docked // asked for while one ran
	wantFocus       bool
	dockedReady     bool         // the docked agent waited for you when it came: it keeps that rank
	links           []detailLink // what the detail panel can open, set by detailView
	linkMode        bool         // f: keys move between the links
	linkSel         int
	detailTop       int        // screen line of the detail panel's first content line
	start           tea.Cmd    // run once the program starts: docks the agent docked last time
	saved           savedState // last state written
	sideRatio       float64    // the TUI's share of the width in side mode, 0 for the default
	quitting        bool       // the last save forgets the pane: no TUI to send keys to
	convs           map[string]convAt
	status          string
	statusErr       bool
}

type tickMsg time.Time
type doneMsg struct {
	id, verb, out string
	err           error
}

func tick() tea.Cmd {
	return tea.Tick(refreshEvery, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// A working agent's mark spins, so that it does not look like one waiting for you.
var (
	spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	frame   int
)

type spinMsg struct{}

func spin(every time.Duration) tea.Cmd {
	return tea.Tick(every, func(time.Time) tea.Msg { return spinMsg{} })
}

// spinning: some agent on screen works. Otherwise the beat slows down: no need
// to redraw eight times a second for nothing.
func (m *model) spinning() bool {
	for _, r := range m.rows {
		if r.activity == "working" {
			return true
		}
	}
	return false
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(tick(), spin(time.Second), m.start, tea.RequestBackgroundColor)
}

func (m *model) selected() *row {
	if m.cursor >= 0 && m.cursor < len(m.rows) && m.rows[m.cursor].d != nil {
		return &m.rows[m.cursor]
	}
	return nil
}

func (m *model) reload() {
	key := ""
	if m.cursor >= 0 && m.cursor < len(m.rows) {
		key = m.rows[m.cursor].key()
	}
	m.rows, m.stores, m.errs = load(m.roots, view{all: m.all, todo: m.todo, filter: m.filter, byPerson: m.byPerson, byPriority: m.byPriority, agentsView: m.agentsView, starred: m.starredView, docked: m.dockedKey()})
	m.syncDocked()
	// The agent shown at the right is being looked at.
	for i := range m.rows {
		if r := m.rows[i]; m.side && r.d != nil && r.store.root == m.docked.root && r.d.ID == m.docked.id {
			m.rows[i].unread = false
		}
	}
	m.waitW = 0
	for _, r := range m.rows {
		if r.d != nil && r.d.State == dossier.Waiting {
			m.waitW = 24
			if m.byPerson {
				m.waitW = 12
			}
			break
		}
	}
	m.cursor = -1
	for i, r := range m.rows {
		if r.selectable() && r.key() == key {
			m.cursor = i
			break
		}
	}
	if m.cursor < 0 {
		m.cursor = 0
		m.move(1)
		m.move(-1)
		m.scroll = 0
	}
}

// move steps the cursor over dossier and agent rows, skipping headers and spacers.
func (m *model) move(step int) {
	for i := m.cursor + step; i >= 0 && i < len(m.rows); i += step {
		if m.rows[i].selectable() {
			if i != m.cursor {
				m.scroll = 0
			}
			m.cursor = i
			return
		}
	}
}

// runArgs calls the installed dossier binary with args on the store root.
func runArgs(root, verb string, args ...string) tea.Cmd {
	return func() tea.Msg {
		exe, err := os.Executable()
		if err != nil {
			return doneMsg{id: verb, verb: verb, err: err}
		}
		out, err := exec.Command(exe, append(args, "--store", root, "--format", "text")...).CombinedOutput()
		return doneMsg{id: verb, verb: verb, out: strings.TrimSpace(string(out)), err: err}
	}
}

// run calls the installed dossier binary on one dossier: attach, restart.
func run(root, id, verb string, args ...string) tea.Cmd {
	return func() tea.Msg {
		exe, err := os.Executable()
		if err != nil {
			return doneMsg{id: id, verb: verb, err: err}
		}
		argv := append([]string{verb, id}, args...)
		argv = append(argv, "--store", root, "--format", "text")
		out, err := exec.Command(exe, argv...).CombinedOutput()
		return doneMsg{id: id, verb: verb, out: strings.TrimSpace(string(out)), err: err}
	}
}

// ingestNow polls every store at once, without the settle delay of a source,
// and sums up what it opened or woke.
func ingestNow(roots []string) tea.Cmd {
	return func() tea.Msg {
		exe, err := os.Executable()
		if err != nil {
			return doneMsg{id: "ingest", verb: "ingest", err: err}
		}
		var news, failures []string
		for _, root := range roots {
			out, err := exec.Command(exe, "ingest", "--now", "--store", root, "--format", "text").CombinedOutput()
			if err != nil {
				failures = append(failures, filepath.Base(root)+": "+firstLine(string(out), err.Error()))
				continue
			}
			for _, l := range strings.Split(string(out), "\n") {
				l = strings.TrimSpace(l)
				if strings.HasPrefix(l, "error") {
					failures = append(failures, filepath.Base(root)+": "+l)
				} else if f := strings.Fields(l); len(f) == 2 && f[0] != "skip" {
					news = append(news, f[0]+" "+f[1])
				}
			}
		}
		if len(failures) > 0 {
			return doneMsg{id: "ingest", verb: "ingest", out: strings.Join(failures, " · "), err: fmt.Errorf("ingest failed")}
		}
		summary := "ingest: nothing new"
		if len(news) > 0 {
			summary = "ingest: " + strings.Join(news, ", ")
		}
		return doneMsg{id: "ingest", verb: "ingest", out: summary}
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case spinMsg:
		if !m.spinning() {
			return m, spin(time.Second)
		}
		frame++
		return m, spin(120 * time.Millisecond)
	case tickMsg:
		m.reload()
		m.save()
		return m, tea.Batch(tick(), m.heal())
	case doneMsg:
		switch {
		case msg.err != nil:
			m.status, m.statusErr = msg.id+": "+firstLine(msg.out, msg.err.Error()), true
		case msg.verb == "restart":
			m.status, m.statusErr = msg.id+": session restarted, no prompt sent", false
		case msg.verb == "park" || msg.verb == "unpark" || msg.verb == "star" || msg.verb == "unstar":
		case msg.verb == "attach":
			m.status, m.statusErr = msg.id+": pane focused", false
		case msg.verb == "dock":
			m.status, m.statusErr = msg.id+": agent docked at the right", false
		case msg.verb == "undock":
			m.status, m.statusErr = msg.id+": agent back in its tab", false
		case msg.verb == "desk":
			m.status, m.statusErr = "desk: fresh conversation started, the previous one is archived", false
		default:
			m.status, m.statusErr = firstLine(msg.out, msg.id+": done"), false
		}
		m.reload()
		if msg.verb == "dock" || msg.verb == "undock" {
			return m, m.dockDone()
		}
	case tea.MouseMsg:
		if m.ask == nil && !m.typing {
			return m, m.mouse(msg)
		}
	case tea.BackgroundColorMsg:
		darkBackground = msg.IsDark()
	case tea.KeyPressMsg:
		if m.ask != nil {
			finished, cmd := m.ask.key(msg)
			if finished {
				m.ask = nil
			}
			return m, cmd
		}
		if m.typing {
			return m, m.typeFilter(msg)
		}
		return m, m.key(msg.String())
	}
	return m, nil
}

// aliases maps the Gmail-style keys onto the TUI's own.
var aliases = map[string]string{"c": "+", "e": "x", "#": "D", "o": "enter"}

// goTo handles the second key of a g sequence, as in Gmail: g d the desk,
// g i the active dossiers, g t to do, g w by person, g a all; gg the top.
func (m *model) goTo(k string) {
	m.offset = 0
	switch k {
	case "g":
		m.cursor = -1
		m.move(1)
		return
	case "esc":
		return
	case "d":
		if m.agentsView {
			m.agentsView = false
			m.reload()
		}
		m.toDesk()
		return
	case "p":
		m.toPlaceholder()
		return
	case "i":
		m.todo, m.all, m.byPerson, m.agentsView, m.starredView = false, false, false, false, false
	case "t":
		m.todo, m.all, m.byPerson, m.agentsView, m.starredView = true, false, false, false, false
	case "w":
		m.todo, m.all, m.byPerson, m.agentsView, m.starredView = false, false, true, false, false
	case "a":
		m.todo, m.all, m.byPerson, m.agentsView, m.starredView = false, true, false, false, false
	case "s":
		m.todo, m.all, m.byPerson, m.agentsView, m.starredView = false, false, false, false, true
	case "n":
		m.todo, m.all, m.byPerson, m.agentsView, m.starredView = false, false, false, true, false
	default:
		m.status, m.statusErr = "g "+k+": unknown; g then g, d, p, i, t, w, a, s or n", true
		return
	}
	m.reload()
}

func (m *model) key(k string) tea.Cmd {
	if m.gPending {
		m.gPending = false
		m.status = ""
		m.goTo(k)
		return nil
	}
	if k == "g" {
		m.gPending = true
		return nil
	}
	if m.linkMode {
		if cmd, handled := m.linkKey(k); handled {
			return cmd
		}
	}
	if k == "f" {
		if len(m.links) == 0 {
			m.status, m.statusErr = "nothing to open in the detail panel", false
			return nil
		}
		m.linkMode, m.linkSel = true, 0
		return nil
	}
	if to, ok := aliases[k]; ok {
		k = to
	}
	r := m.selected()
	if ag := m.selectedAgent(); ag != nil {
		if cmd, handled := m.agentKey(k, ag); handled {
			return cmd
		}
	}
	if m.side && r != nil && (k == "enter" || (r.desk && k == "S")) {
		return m.dock(r, true)
	}
	if m.side && r != nil && (k == "O" || k == "shift+enter") {
		return m.dock(r, false)
	}
	if r != nil && r.desk {
		if cmd, handled := m.deskKey(k, r); handled {
			return cmd
		}
	}
	switch k {
	case "v":
		return m.toggleSide()
	case "L":
		m.cycleLayout()
	case "h":
		return m.sendHome()
	case "'":
		return m.toggleLast()
	case "]":
		return m.nextAttention()
	case ">":
		return m.step(1)
	case "<":
		return m.step(-1)
	case "[":
		return m.lastManifested()
	case "b":
		m.toDesk()
	case "?":
		m.legend = !m.legend
		m.scroll = 0
	case "q", "ctrl+c":
		m.quitting = true
		m.save()
		if m.side {
			return tea.Sequence(m.leaveSide(), tea.Quit)
		}
		return tea.Quit
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "pgup":
		for i := 0; i < m.listHeight()/2; i++ {
			m.move(-1)
		}
	case "pgdown":
		for i := 0; i < m.listHeight()/2; i++ {
			m.move(1)
		}
	case "home":
		m.cursor = -1
		m.move(1)
	case "G", "end":
		m.cursor = len(m.rows)
		m.move(-1)
	case "J", "shift+down", "ctrl+d":
		m.scroll += 3
	case "K", "shift+up", "ctrl+u":
		if m.scroll -= 3; m.scroll < 0 {
			m.scroll = 0
		}
	case "a":
		m.all, m.todo = !m.all, false
		m.reload()
	case "t":
		m.todo, m.all, m.byPerson = !m.todo, false, false
		m.offset = 0
		m.reload()
	case "p":
		m.byPriority = !m.byPriority
		m.reload()
	case "w":
		m.byPerson, m.todo = !m.byPerson, false
		m.offset = 0
		m.reload()
	case "/":
		m.typing = true
	case "esc":
		m.filter, m.status = "", ""
		m.reload()
	case "r":
		m.reload()
	case "i":
		m.status, m.statusErr = "ingest: polling every source now…", false
		return ingestNow(m.roots)
	case "tab":
		m.noDetail = !m.noDetail
	case "enter":
		if r == nil {
			break
		}
		if r.activity == "none" {
			m.status, m.statusErr = r.d.Label()+": starting its session with the open prompt…", false
		} else {
			m.status, m.statusErr = r.d.Label()+": opening its pane…", false
		}
		return run(r.store.root, r.d.ID, "attach")
	case "S":
		if r == nil {
			break
		}
		m.status, m.statusErr = r.d.Label()+": starting its session, no prompt…", false
		return run(r.store.root, r.d.ID, "attach", "--no-prompt")
	case "s":
		if r == nil {
			break
		}
		verb, say := "star", " starred"
		if r.d.Starred {
			verb, say = "unstar", " unstarred"
		}
		m.status, m.statusErr = r.d.Label()+say, false
		return run(r.store.root, r.d.ID, verb)
	case "+":
		m.newDossier(r)
	case "W", "u", "x", "D":
		if r != nil {
			return m.stateKey(k, r)
		}
	case "n":
		if r == nil {
			break
		}
		verb, say := "park", ": marked as needing no action"
		if r.d.NoAction {
			verb, say = "unpark", ": needs action again"
		}
		m.status, m.statusErr = r.d.Label()+say, false
		return run(r.store.root, r.d.ID, verb)
	case "R":
		if r == nil {
			break
		}
		if r.activity == "none" {
			m.status, m.statusErr = r.d.Label()+" has no session to restart", true
			break
		}
		m.status, m.statusErr = r.d.Label()+": restarting its session…", false
		return run(r.store.root, r.d.ID, "restart")
	}
	return nil
}

func (m *model) typeFilter(k tea.KeyPressMsg) tea.Cmd {
	switch k.Keystroke() {
	case "enter":
		m.typing = false
	case "esc":
		m.typing, m.filter = false, ""
	case "backspace":
		if r := []rune(m.filter); len(r) > 0 {
			m.filter = string(r[:len(r)-1])
		}
	case "ctrl+c":
		return tea.Quit
	default:
		m.filter += k.Text
	}
	m.reload()
	return nil
}

func firstLine(s, fallback string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return strings.TrimPrefix(l, "error: ")
		}
	}
	return fallback
}

// ---------------------------------------------------------------- view

var (
	cAccent  = adaptive{Light: "#5A4FCF", Dark: "#A99CFF"}
	cMuted   = adaptive{Light: "#8A8A8A", Dark: "#6E6E6E"}
	cFaint   = adaptive{Light: "#C8C8C8", Dark: "#3E3E3E"}
	cText    = adaptive{Light: "#1F1F1F", Dark: "#E6E6E6"}
	cOpen    = adaptive{Light: "#1F6FD1", Dark: "#6CB6FF"}
	cWaiting = adaptive{Light: "#A04BC2", Dark: "#D59BF0"}
	cDone    = adaptive{Light: "#8A8A8A", Dark: "#6E6E6E"}
	cWorking = adaptive{Light: "#B7791F", Dark: "#F2C14E"}
	cReady   = adaptive{Light: "#2F855A", Dark: "#68D391"}
	cStopped = adaptive{Light: "#C53030", Dark: "#FC8181"}
	cSelBg   = adaptive{Light: "#ECE9FF", Dark: "#2D2A4A"}
	cSel     = adaptive{Light: "#D4CCFF", Dark: "#4B3F99"}

	sTitle   = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	sMuted   = lipgloss.NewStyle().Foreground(cMuted)
	sFaint   = lipgloss.NewStyle().Foreground(cFaint)
	sText    = lipgloss.NewStyle().Foreground(cText)
	sBold    = lipgloss.NewStyle().Bold(true).Foreground(cText)
	sSection = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	sPanel   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cFaint).Padding(1, 2)
)

func stateStyle(s string) lipgloss.Style {
	switch s {
	case dossier.Open:
		return lipgloss.NewStyle().Foreground(cOpen)
	case dossier.Waiting:
		return lipgloss.NewStyle().Foreground(cWaiting)
	case dossier.Merged:
		return lipgloss.NewStyle().Foreground(cDone).Strikethrough(true)
	}
	return lipgloss.NewStyle().Foreground(cDone)
}

// activityMark is a one-cell glyph for what the dossier's agent does.
func activityMark(a string) string {
	switch a {
	case "working":
		return lipgloss.NewStyle().Foreground(cWorking).Render(spinner[frame%len(spinner)])
	case "ready":
		return lipgloss.NewStyle().Foreground(cWorking).Bold(true).Render("●")
	case "idle":
		return lipgloss.NewStyle().Foreground(cReady).Render("●")
	case "blocked":
		return lipgloss.NewStyle().Foreground(cStopped).Bold(true).Render("◆")
	case "stopped":
		return lipgloss.NewStyle().Foreground(cStopped).Render("○")
	case "none":
		return sFaint.Render("·")
	}
	return lipgloss.NewStyle().Foreground(cWorking).Render("◌")
}

func activityWord(a string) string {
	switch a {
	case "none":
		return "no session"
	case "stopped":
		return "no tab"
	case "ready":
		return "your turn"
	case "blocked":
		return "asks a permission"
	}
	return a
}

func (m *model) wide() bool { return m.width >= 110 }

// listHeight is the room between the top bar and the bottom bar.
func (m *model) listHeight() int {
	if m.listH > 0 {
		return m.listH
	}
	h := m.height - 8
	if h < 3 {
		h = 3
	}
	return h
}

func (m *model) View() tea.View { return screen(m.render()) }

func (m *model) render() string {
	if m.width == 0 {
		return ""
	}
	top, bottom := m.topBar(m.width-2), m.bottomBar(m.width-2)
	h := max(3, m.height-len(top)-len(bottom)-3)
	m.listY = 2 + len(top)
	var body string
	switch show := !m.noDetail || m.legend; {
	case show && m.detailAt() == "right":
		lw := m.width * 52 / 100
		dw := m.width - lw - 3
		m.listW, m.listH = lw, h
		list := m.listView(lw, h)
		m.detailTop = m.listY + 2
		det := sPanel.Width(dw - 2).Height(h - 2).Render(m.detailView(dw-8, h-4))
		body = lipgloss.JoinHorizontal(lipgloss.Top, list, "  ", det)
	case show && h >= 12:
		lh := h * 55 / 100
		dh := h - lh
		m.listW, m.listH = m.width, lh
		list := m.listView(m.width, lh)
		m.detailTop = m.listY + lh + 2
		det := sPanel.Width(m.width - 4).Height(dh - 2).Render(m.detailView(m.width-10, dh-4))
		body = lipgloss.JoinVertical(lipgloss.Left, list, det)
	default:
		m.listW, m.listH = m.width, h
		body = m.listView(m.width, h)
	}
	pad := lipgloss.NewStyle().PaddingLeft(1)
	return lipgloss.JoinVertical(lipgloss.Left, "", pad.Render(strings.Join(top, "\n")), "", body, "", pad.Render(strings.Join(bottom, "\n")))
}

// topBar lays the header out on as many lines as the width needs.
func (m *model) topBar(width int) []string {
	scope := "active"
	if m.all {
		scope = "all states"
	}
	if m.todo {
		scope = "to do"
	}
	order := "by number"
	if m.byPriority {
		order = "by priority"
	}
	segs := []string{sTitle.Render("dossier"), sMuted.Render(scope), sMuted.Render(order)}
	if m.byPerson {
		segs = []string{sTitle.Render("dossier"), sMuted.Render("waiting, by person")}
	}
	if m.agentsView {
		segs = []string{sTitle.Render("dossier"), sMuted.Render("agents without dossier")}
	}
	if m.starredView {
		segs = []string{sTitle.Render("dossier"), sMuted.Render("starred")}
	}
	if m.side {
		segs = append(segs, sMuted.Render("side"))
	}
	for _, sv := range m.stores {
		todo, quiet := 0, 0
		for _, d := range sv.all {
			if d.State == dossier.Open && d.NoAction {
				quiet++
			} else if d.State == dossier.Open {
				todo++
			}
		}
		seg := sBold.Render(sv.name) + "  " + stateStyle(dossier.Open).Render(fmt.Sprintf("%d to do", todo))
		if quiet > 0 {
			seg += "  " + sFaint.Render(fmt.Sprintf("%d no action", quiet))
		}
		seg += "  " + stateStyle(dossier.Waiting).Render(fmt.Sprintf("%d waiting", sv.count[dossier.Waiting]))
		segs = append(segs, seg)
	}
	if n := m.forYou(); n > 0 {
		segs = append(segs, lipgloss.NewStyle().Foreground(cWorking).Bold(true).Render(fmt.Sprintf("] %d for you", n)))
	}
	if m.filter != "" || m.typing {
		cur := ""
		if m.typing {
			cur = "▏"
		}
		segs = append(segs, sMuted.Render("filter ")+sText.Render(m.filter+cur))
	}
	return flow(segs, sFaint.Render("  ·  "), width)
}

// bottomBar lays the status and the keys out on as many lines as the width needs.
func (m *model) bottomBar(width int) []string {
	wrap := func(s string) []string {
		return strings.Split(lipgloss.NewStyle().Width(width).Render(s), "\n")
	}
	if m.ask != nil {
		return wrap(m.ask.view())
	}
	var lines []string
	if m.status != "" {
		st := lipgloss.NewStyle().Foreground(cReady)
		if m.statusErr {
			st = lipgloss.NewStyle().Foreground(cStopped)
		}
		lines = append(lines, wrap(st.Render(m.status))...)
	}
	if len(m.errs) > 0 {
		return append(lines, wrap(lipgloss.NewStyle().Foreground(cStopped).Render(m.errs[0]))...)
	}
	// One theme per line, its title in a column of its own; a long theme wraps under itself.
	const titleW = 9
	title := lipgloss.NewStyle().Foreground(cAccent).Width(titleW)
	for _, g := range m.footer() {
		if len(g.keys) == 0 {
			continue
		}
		var segs []string
		for _, k := range g.keys {
			segs = append(segs, sText.Render(k[0])+" "+sMuted.Render(k[1]))
		}
		for i, l := range flow(segs, sFaint.Render("  ·  "), width-titleW) {
			head := strings.Repeat(" ", titleW)
			if i == 0 {
				head = title.Render(g.title)
			}
			lines = append(lines, head+l)
		}
	}
	return lines
}

func (m *model) listView(w, h int) string {
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+h {
		m.offset = m.cursor - h + 1
	}
	// Keep the store header in view when the first dossier under it is selected.
	if m.cursor > 0 && m.rows[m.cursor-1].header != "" && m.cursor-1 < m.offset {
		m.offset = m.cursor - 1
	}
	var lines []string
	for i := m.offset; i < len(m.rows) && len(lines) < h; i++ {
		line := m.rowView(m.rows[i], false, w-1)
		if i == m.cursor {
			line = selectLine(line, w-1)
		}
		lines = append(lines, line)
	}
	if m.cursor < 0 || m.cursor >= len(m.rows) || !m.rows[m.cursor].selectable() {
		lines = append(lines, "", sMuted.Render("   nothing to show: g a shows every state, esc clears the filter"))
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(withBar(lines[:h], w, len(m.rows), m.offset), "\n")
}

func (m *model) rowView(r row, sel bool, w int) string {
	if r.spacer() {
		return ""
	}
	if r.person {
		n := "1 dossier"
		if r.count > 1 {
			n = fmt.Sprintf("%d dossiers", r.count)
		}
		return " " + lipgloss.NewStyle().Bold(true).Foreground(cWaiting).Render("⏳ "+r.header) + sMuted.Render("  "+n)
	}
	if r.desk {
		return m.deskHeader(r, sel, w)
	}
	if r.agent != nil {
		return m.agentRowView(r, sel, w)
	}
	if r.header != "" && r.store == nil {
		return " " + sTitle.Render(strings.ToUpper(r.header))
	}
	if r.header != "" {
		return " " + sTitle.Render(strings.ToUpper(r.header)) + sFaint.Render("  "+r.store.root)
	}
	d := r.d
	var tree strings.Builder
	for lvl := 0; lvl < r.depth; lvl++ {
		last := r.last[lvl]
		switch {
		case lvl == r.depth-1 && last:
			tree.WriteString("└─ ")
		case lvl == r.depth-1:
			tree.WriteString("├─ ")
		case last:
			tree.WriteString("   ")
		default:
			tree.WriteString("│  ")
		}
	}
	state := stateStyle(d.State).Render(fmt.Sprintf("%-9s", d.State))
	if parked(d) {
		state = sFaint.Render(fmt.Sprintf("%-9s", "no action"))
	}
	label := sBold.Render(fmt.Sprintf("%-7s", d.Label()))
	dot := " "
	if r.unread {
		dot = lipgloss.NewStyle().Foreground(cAccent).Bold(true).Render("•")
	}
	star := "  " // ⭐ takes two columns
	if d.Starred {
		star = "⭐"
	}
	prefix := " " + dot + " " + activityMark(r.activity) + " " + star + " " + label + "  " + state + " " + m.waitCell(d) + sFaint.Render(tree.String())
	var tail []string
	if r.cycle {
		tail = append(tail, "↻ cycle")
	}
	if len(r.blocked) > 0 {
		tail = append(tail, "⛓ "+strings.Join(r.blocked, " "))
	}
	suffix := ""
	if len(tail) > 0 {
		suffix = "   " + strings.Join(tail, "   ")
	}
	room := w - lipgloss.Width(prefix) - lipgloss.Width(suffix) - 2
	title := sText.Render(truncate(d.Title, room))
	if r.unread {
		title = sBold.Render(truncate(d.Title, room))
	}
	line := prefix + title + sMuted.Render(suffix)
	st := lipgloss.NewStyle().Width(w).MaxWidth(w)
	if sel {
		st = st.Background(cSelBg)
	}
	return st.Render(line)
}

// waitCell is the waiting column: whom the dossier waits on (left out in the
// by-person view, where the group says it) and when to chase.
func (m *model) waitCell(d *dossier.Dossier) string {
	if m.waitW == 0 {
		return ""
	}
	cell := ""
	if d.State == dossier.Waiting {
		due := remaining(d.WaitUntil)
		if m.byPerson {
			cell = due
		} else {
			room := m.waitW - 2 - lipgloss.Width(due)
			cell = lipgloss.NewStyle().Bold(true).Foreground(cWaiting).Render(truncate(PersonOf(d.WaitingOn), room)) + " " + due
		}
	}
	return cell + strings.Repeat(" ", max(0, m.waitW-lipgloss.Width(cell)))
}

// remaining says when to chase: in 7d, today, or 2d late in red.
func remaining(ts string) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return sFaint.Render("no date")
	}
	now := time.Now()
	y1, m1, d1 := now.Date()
	y2, m2, d2 := t.Local().Date()
	days := int(time.Date(y2, m2, d2, 0, 0, 0, 0, time.Local).Sub(time.Date(y1, m1, d1, 0, 0, 0, 0, time.Local)).Hours() / 24)
	switch {
	case days < 0:
		return lipgloss.NewStyle().Foreground(cStopped).Bold(true).Render(fmt.Sprintf("%dd late", -days))
	case days == 0:
		return lipgloss.NewStyle().Foreground(cWorking).Bold(true).Render("today")
	case days <= 2:
		return lipgloss.NewStyle().Foreground(cWorking).Render(fmt.Sprintf("in %dd", days))
	}
	return sMuted.Render(fmt.Sprintf("in %dd", days))
}

func truncate(s string, n int) string {
	r := []rune(s)
	if n <= 1 {
		return ""
	}
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// when renders a timestamp as a local date and a distance from now.
func when(ts string) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}
	return t.Local().Format("02.01.2006 15:04") + sMuted.Render("  "+ago(t))
}

func ago(t time.Time) string {
	d := time.Since(t)
	future := d < 0
	if future {
		d = -d
	}
	var s string
	switch {
	case d < time.Minute:
		s = "now"
	case d < time.Hour:
		s = fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 48*time.Hour:
		s = fmt.Sprintf("%d h", int(d.Hours()))
	default:
		s = fmt.Sprintf("%d days", int(d.Hours()/24))
	}
	if s == "now" {
		return s
	}
	if future {
		return "in " + s
	}
	return s + " ago"
}

func (m *model) detailView(w, h int) string {
	m.links = nil
	// The last column is kept for the scrollbar.
	m.detailW, w = w, w-1
	r := m.selected()
	var lines []string
	add := func(s ...string) {
		for _, x := range s {
			lines = append(lines, strings.Split(x, "\n")...)
		}
	}
	switch {
	case m.legend:
		legendView(w, add)
		return m.window(lines, h)
	case r == nil && m.selectedAgent() != nil:
		agentView(&m.rows[m.cursor], w, add)
		return m.window(lines, h)
	case r == nil:
		return sMuted.Render("No dossier selected.")
	case r.desk:
		m.deskView(r, w, add)
		return m.window(lines, h)
	}
	d := r.d
	m.links = nil
	addLink := func(text string, l detailLink) {
		l.line = len(lines)
		add(m.linkLine(text, l, w))
		m.links = append(m.links, l)
	}
	wrap := lipgloss.NewStyle().Width(w)
	label := lipgloss.NewStyle().Foreground(cMuted).Width(12)
	field := func(name, value string) { add(label.Render(name) + value) }
	section := func(name string) { add("", sSection.Render(name), "") }

	head := sTitle.Render(d.Label()) + sMuted.Render("  ·  "+r.store.name)
	if d.Label() != d.ID {
		head += sMuted.Render("  ·  " + d.ID)
	}
	add(head, "", wrap.Inherit(sBold).Render(d.Title))
	if len(d.Sources) > 0 {
		add(sMuted.Render("from ") + sText.Render(sourceKind(d.Sources[0].ID)))
	}
	if d.State == dossier.Waiting {
		add("", lipgloss.NewStyle().Bold(true).Foreground(cWaiting).Width(w).Render("⏳ waiting on "+d.WaitingOn))
		if d.WaitUntil != "" {
			add(sMuted.Render("   chase "+dayMonthYear(d.WaitUntil)+"  ") + remaining(d.WaitUntil))
		}
	}

	section("Status")
	if parked(d) {
		field("state", stateStyle(d.State).Render(d.State)+sMuted.Render(" · no action required for now"))
	} else {
		field("state", stateStyle(d.State).Render(d.State))
	}
	field("agent", activityMark(r.activity)+" "+sText.Render(activityWord(r.activity)))
	if len(r.blocked) > 0 {
		field("blocked by", lipgloss.NewStyle().Foreground(cStopped).Render(strings.Join(r.blocked, ", ")))
	}
	if n := len(d.Run.PendingTransitions); n > 0 {
		field("pending", lipgloss.NewStyle().Foreground(cStopped).Render(fmt.Sprintf("%d source transition(s): dossier retry %s", n, d.ID)))
	}
	field("created", sText.Render(when(d.Created)))
	field("updated", sText.Render(when(d.Updated)))
	if d.Run.Session != "" {
		field("session", sText.Render(truncate(d.Run.Session, w-12)))
	}
	if d.Run.TabID != "" {
		field("tab", sText.Render(d.Run.TabID))
	}

	if ls := links(r.store, d); len(ls) > 0 {
		section("Links")
		relW := 0
		for _, l := range ls {
			if n := lipgloss.Width(strings.Join(l.rels, " · ")); n > relW {
				relW = n
			}
		}
		for _, l := range ls {
			addLink(edge(strings.Join(l.rels, " · "), relW, l.id, l.title, l.state, w), detailLink{kind: "dossier", target: l.id})
		}
	}

	if len(d.Sources) > 0 || len(d.Threads) > 0 {
		section("Sources")
		for _, s := range d.Sources {
			add(sBold.Render(sourceKind(s.ID)) + sFaint.Render("  "+truncate(s.ID, w-lipgloss.Width(sourceKind(s.ID))-2)))
			if s.Title != "" && s.Title != d.Title {
				add(sMuted.Render("  " + truncate(s.Title, w-2)))
			}
			if s.Resource != "" {
				if isAddress(s.Resource) {
					addLink("  "+link(sMuted).Render(truncate(s.Resource, w-4)), detailLink{kind: "url", target: s.Resource})
				} else {
					add(sMuted.Render("  " + truncate(s.Resource, w-2)))
				}
			}
		}
		for _, t := range d.Threads {
			if !d.HasSource(t) {
				add(sMuted.Render("thread ") + sText.Render(truncate(t, w-7)))
			}
		}
	}

	if notes := d.Notes(); notes != "" {
		section("Fiche")
		add(renderNotes(notes, w))
	}

	if files := filesOf(d); len(files) > 0 {
		section("Files")
		for _, f := range files {
			addLink(link(sText).Render(truncate(f, w)), detailLink{kind: "file", target: d.Path(f)})
		}
	}

	if tail := logTail(d, 12); len(tail) > 0 {
		section("History")
		for _, l := range tail {
			ts, text, ok := strings.Cut(l, " · ")
			if t, err := time.Parse(time.RFC3339, ts); ok && err == nil {
				add(sMuted.Render(t.Local().Format("02.01 15:04")+"  ") + sText.Render(truncate(text, w-13)))
			} else {
				add(sMuted.Render(truncate(l, w)))
			}
		}
	}

	return m.window(lines, h)
}

// window shows the detail lines from the scroll position.
func (m *model) window(lines []string, h int) string {
	// In link mode, the chosen link stays in view.
	if m.linkMode && m.linkSel < len(m.links) {
		if l := m.links[m.linkSel].line; l < m.scroll {
			m.scroll = l
		} else if l >= m.scroll+h {
			m.scroll = l - h + 1
		}
	}
	if max := len(lines) - h; m.scroll > max {
		m.scroll = max
	}
	if m.scroll < 0 {
		m.scroll = 0
	}
	total := len(lines)
	m.detailOverflows = total > h
	lines = lines[m.scroll:]
	if len(lines) > h {
		lines = lines[:h]
	}
	if total <= h {
		return strings.Join(lines, "\n")
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(withBar(lines, m.detailW, total, m.scroll), "\n")
}

// sourceKind names what a source reference points at: a Gmail thread, a
// voice memo, a reminder, or a dossier added by hand.
func sourceKind(ref string) string {
	name, rest, _ := strings.Cut(ref, ":")
	kind, _, _ := strings.Cut(rest, "/")
	switch name + ":" + kind {
	case "gmail:thread":
		return "✉  Gmail thread"
	case "gmail:task", "tasks:task":
		return "☑  Google Tasks task"
	case "memos:memo":
		return "♪  Voice memo"
	case "reminders:item":
		return "⚑  Apple Reminder"
	}
	if name == "manual" {
		return "✎  Added by hand or by an agent"
	}
	return name + " " + kind
}

func dayMonthYear(ts string) string {
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		return t.Local().Format("02.01.2006")
	}
	return ts
}

// renderNotesPlain shows the Markdown body when glamour fails: headings bold, the rest wrapped.
func renderNotesPlain(md string, w int) string {
	var out []string
	wrap := lipgloss.NewStyle().Width(w).Inherit(sText)
	blank := false
	for _, l := range strings.Split(md, "\n") {
		t := strings.TrimSpace(l)
		switch {
		case t == "":
			if !blank && len(out) > 0 {
				out = append(out, "")
			}
			blank = true
			continue
		case strings.HasPrefix(t, "#"):
			out = append(out, sBold.Render(strings.TrimSpace(strings.TrimLeft(t, "#"))))
		case strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* "):
			out = append(out, lipgloss.NewStyle().Width(w).PaddingLeft(2).Inherit(sText).Render("• "+t[2:]))
		default:
			out = append(out, wrap.Render(t))
		}
		blank = false
	}
	return strings.Join(out, "\n")
}

func filesOf(d *dossier.Dossier) []string {
	var out []string
	for _, sub := range []string{"context", "files"} {
		entries, _ := os.ReadDir(d.Path(sub))
		for _, e := range entries {
			out = append(out, filepath.Join(sub, e.Name()))
		}
	}
	return out
}

func edge(rels string, relW int, id, title, state string, w int) string {
	head := sMuted.Render(rels+strings.Repeat(" ", relW-lipgloss.Width(rels))+"   ") + link(sBold).Render(id) + "  "
	st := stateStyle(state).Render(state)
	room := w - lipgloss.Width(head) - lipgloss.Width(st) - 2
	return head + link(sText).Render(truncate(title, room)) + "  " + st
}
