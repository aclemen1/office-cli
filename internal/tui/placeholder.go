package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/aclemen1/dossier-cli/internal/app"
	"github.com/aclemen1/dossier-cli/internal/dossier"
	"github.com/aclemen1/dossier-cli/internal/store"
)

// RunPlaceholder fills the pane that docked agents take the place of: it
// says what the pane is, the time, and where the stores stand. While an agent
// holds its place, it names that agent: enter or a click jumps to it, u gives
// the place back.
// With a tui pane, it closes its own pane and stops once that pane is gone.
func RunPlaceholder(root, tui string) error {
	roots := store.Discover(root)
	p := &placeholder{roots: roots, stamps: stampsOf(roots), self: os.Getenv("HERDR_PANE_ID"), tui: tui}
	_, err := tea.NewProgram(p).Run()
	return err
}

type placeholder struct {
	roots         []string
	self          string
	width, height int
	now           time.Time
	lines         []string
	holder        *heldBy
	counted       time.Time
	note          string
	stamp         time.Time // newest dock.stamp seen
	stamps        []string
	tui           string // the TUI pane it serves, if any
	ticks         int
}

// heldBy is the dossier whose agent sits in this placeholder's place.
type heldBy struct {
	root  string
	d     *dossier.Dossier
	label string
}

type clockMsg time.Time

func clock() tea.Cmd {
	return tea.Tick(250*time.Millisecond, func(t time.Time) tea.Msg { return clockMsg(t) })
}

func (p *placeholder) Init() tea.Cmd {
	p.now = time.Now()
	return tea.Batch(clock(), tea.RequestBackgroundColor)
}

func (p *placeholder) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		p.width, p.height = msg.Width, msg.Height
	case clockMsg:
		p.now = time.Time(msg)
		p.watch()
		if p.ticks++; p.ticks%8 == 0 && p.orphaned() {
			return p, p.leave()
		}
		return p, clock()
	case tea.MouseReleaseMsg:
		if msg.Button == tea.MouseLeft {
			p.jump()
		}
	case tea.BackgroundColorMsg:
		darkBackground = msg.IsDark()
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c":
			return p, tea.Quit
		case "enter":
			p.jump()
		case "u":
			p.giveBack()
		}
	}
	return p, nil
}

// jump focuses the tab where the docked agent is.
func (p *placeholder) jump() {
	if p.holder == nil {
		return
	}
	tab, err := app.PaneTab(p.holder.d.Run.PaneID)
	if err != nil {
		p.note = "the docked agent's pane is gone"
		return
	}
	_ = exec.Command("herdr", "tab", "focus", tab).Run()
}

// giveBack undocks the agent: this pane returns to its place, and is focused there.
func (p *placeholder) giveBack() {
	if p.holder == nil {
		return
	}
	h := p.holder
	if out, err := exec.Command(dossierBin(), "undock", h.d.ID, "--placeholder", p.self, "--store", h.root, "--format", "text").CombinedOutput(); err != nil {
		p.note = firstLine(string(out), err.Error())
		return
	}
	p.counted = time.Time{}
	if tab, err := app.PaneTab(p.self); err == nil {
		_ = exec.Command("herdr", "tab", "focus", tab).Run()
	}
}

// refresh reads the stores at most every refreshEvery.
func (p *placeholder) refresh() {
	if time.Since(p.counted) < refreshEvery && p.lines != nil {
		return
	}
	p.counted, p.lines, p.holder = time.Now(), []string{}, nil
	for _, root := range p.roots {
		s, err := store.Open(root)
		if err != nil {
			continue
		}
		a := &app.App{S: s}
		all, _ := a.All()
		todo, waiting := 0, 0
		for _, d := range append(all, a.Desk()) {
			switch {
			case p.self != "" && d.Run.Placeholder == p.self:
				p.holder = &heldBy{root: root, d: d, label: d.Label() + " · " + d.Title}
			case d.State == dossier.Open && !d.NoAction:
				todo++
			case d.State == dossier.Waiting:
				waiting++
			}
		}
		p.lines = append(p.lines, sBold.Render(s.Config.Store.Sphere)+"  "+
			stateStyle(dossier.Open).Render(fmt.Sprintf("%d to do", todo))+sMuted.Render("  ·  ")+
			stateStyle(dossier.Waiting).Render(fmt.Sprintf("%d waiting", waiting)))
	}
}

func (p *placeholder) View() tea.View { return screen(p.render()) }

func (p *placeholder) render() string {
	if p.width == 0 {
		return ""
	}
	p.refresh()
	t := p.now
	body := []string{
		sTitle.Render("dossier · placeholder"),
		"",
		sBold.Render(t.Format("15:04")) + sMuted.Render(t.Format(":05")),
		sMuted.Render(weekdays[t.Weekday()] + " " + t.Format("02.01.2006")),
		"",
	}
	body = append(body, p.lines...)
	body = append(body, "")
	if h := p.holder; h != nil {
		body = append(body,
			sMuted.Render("in my place: ")+sBold.Render(truncate(h.label, p.width-16)),
			"",
			sText.Render("enter")+sMuted.Render(" or click  jump to it")+sFaint.Render("   ·   ")+sText.Render("u")+sMuted.Render("  take my place back"))
	} else {
		body = append(body, sFaint.Render("A docked agent takes this pane's place;"), sFaint.Render("it waits in the agent's tab meanwhile."))
	}
	if p.note != "" {
		body = append(body, "", lipgloss.NewStyle().Foreground(cStopped).Render(p.note))
	}
	block := lipgloss.JoinVertical(lipgloss.Center, body...)
	return lipgloss.Place(p.width, p.height, lipgloss.Center, lipgloss.Center, block)
}

var weekdays = strings.Fields("Sunday Monday Tuesday Wednesday Thursday Friday Saturday")

// watch rereads the stores at once when an agent docks or undocks: dossier
// touches each store's dock.stamp then.
func (p *placeholder) watch() {
	for _, path := range p.stamps {
		if fi, err := os.Stat(path); err == nil && fi.ModTime().After(p.stamp) {
			p.stamp, p.counted = fi.ModTime(), time.Time{}
		}
	}
}

func stampsOf(roots []string) []string {
	var out []string
	for _, root := range roots {
		if s, err := store.Open(root); err == nil {
			out = append(out, (&app.App{S: s}).DockStamp())
		}
	}
	return out
}

// orphaned: the TUI this placeholder serves is gone.
func (p *placeholder) orphaned() bool {
	if p.tui == "" {
		return false
	}
	_, err := app.PaneTab(p.tui)
	return err != nil
}

// leave sends home the agent that holds this placeholder's place, closes the
// placeholder's pane and stops.
func (p *placeholder) leave() tea.Cmd {
	p.counted = time.Time{}
	p.refresh()
	if h := p.holder; h != nil {
		_ = exec.Command(dossierBin(), "undock", h.d.ID, "--placeholder", p.self, "--store", h.root, "--format", "text").Run()
	}
	if p.self != "" {
		_ = exec.Command("herdr", "pane", "close", p.self).Run()
	}
	return tea.Quit
}
