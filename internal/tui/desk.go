package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/aclemen1/dossier-cli/internal/app"
	"github.com/aclemen1/dossier-cli/internal/dossier"
)

// convAt caches a desk's conversation: reading a long transcript on every
// keystroke would stall the TUI.
type convAt struct {
	at time.Time
	c  app.Conversation
}

func (m *model) conversation(d *dossier.Dossier) app.Conversation {
	if m.convs == nil {
		m.convs = map[string]convAt{}
	}
	key := d.Dir + "|" + d.Run.Session
	if c, ok := m.convs[key]; ok && time.Since(c.at) < refreshEvery {
		return c.c
	}
	c := app.ConversationOf(d)
	m.convs[key] = convAt{time.Now(), c}
	return c
}

// deskKey handles the keys that mean something else on the desk row.
func (m *model) deskKey(k string, r *row) (tea.Cmd, bool) {
	d, root := r.d, r.store.root
	switch k {
	case "enter", "s", "o":
		m.status, m.statusErr = d.Label()+": opening the desk…", false
		return run(root, d.ID, "attach"), true
	case "W", "u", "x", "D", "n", "m":
		m.status, m.statusErr = d.Label()+" is the store's desk: it has no state", true
		return nil, true
	case "N":
		if d.Run.Session == "" {
			m.status, m.statusErr = d.Label()+" has no conversation yet: enter starts one", true
			return nil, true
		}
		m.ask = &ask{title: "New conversation for " + d.Label(), fields: []askField{
			{label: "type yes to confirm", hint: "the current one is archived; grep desk still reads it"},
		}, done: func(v []string) tea.Cmd {
			if !strings.EqualFold(v[0], "yes") {
				m.status, m.statusErr = d.Label()+": conversation kept", false
				return nil
			}
			m.status, m.statusErr = d.Label()+": archiving the conversation…", false
			return runArgs(root, "desk", "desk", "--new")
		}}
		return nil, true
	}
	return nil, false
}

// toDesk moves the cursor to the desk of the selected row's store.
func (m *model) toDesk() {
	var root string
	if r := m.selected(); r != nil {
		root = r.store.root
	}
	for i, r := range m.rows {
		if r.desk && (root == "" || r.store.root == root) {
			m.cursor, m.scroll = i, 0
			return
		}
	}
}

func (m *model) deskHeader(r row, sel bool, w int) string {
	dot := " "
	if r.unread {
		dot = lipgloss.NewStyle().Foreground(cAccent).Bold(true).Render("•")
	}
	line := " " + dot + " " + activityMark(r.activity) + "  " + sTitle.Render(strings.ToUpper(r.header)) +
		sMuted.Render("  desk · "+activityWord(r.activity)) + sFaint.Render("  "+r.store.root)
	st := lipgloss.NewStyle().Width(w).MaxWidth(w)
	if sel {
		st = st.Background(cSelBg)
	}
	return st.Render(line)
}

func (m *model) deskView(r *row, w int, add func(...string)) {
	d := r.d
	label := lipgloss.NewStyle().Foreground(cMuted).Width(12)
	field := func(name, value string) { add(label.Render(name) + value) }
	section := func(name string) { add("", sSection.Render(name), "") }

	add(sTitle.Render(d.Label())+sMuted.Render("  ·  "+r.store.name+" store desk"), "",
		lipgloss.NewStyle().Width(w).Inherit(sText).Render("A lasting session that opens dossiers, notifies them and answers about them. It has no state and never closes."))

	section("Session")
	field("agent", activityMark(r.activity)+" "+sText.Render(activityWord(r.activity)))
	if d.Run.Session == "" {
		add("", sMuted.Render("enter starts the desk."))
	} else {
		c := m.conversation(d)
		if !c.Started.IsZero() {
			field("started", sText.Render(c.Started.Local().Format("02.01.2006 15:04"))+sMuted.Render("  "+ago(c.Started)))
		}
		comp := sText.Render("none")
		if c.Compactions > 0 {
			comp = lipgloss.NewStyle().Foreground(cWorking).Render(fmt.Sprintf("%d", c.Compactions)) +
				sMuted.Render("  · N starts a fresh conversation")
		}
		field("compactions", comp)
		field("session", sText.Render(truncate(d.Run.Session, w-12)))
		if d.Run.TabID != "" {
			field("tab", sText.Render(d.Run.TabID))
		}
	}

	section("Keys")
	add(sText.Render("enter") + sMuted.Render(" pane   ") + sText.Render("R") + sMuted.Render(" restart, same conversation   ") +
		sText.Render("N") + sMuted.Render(" new conversation   ") + sText.Render("+") + sMuted.Render(" new dossier"))

	if tail := logTail(d, 12); len(tail) > 0 {
		section("What it did")
		for _, l := range tail {
			ts, text, ok := strings.Cut(l, " · ")
			if t, err := time.Parse(time.RFC3339, ts); ok && err == nil {
				add(sMuted.Render(t.Local().Format("02.01 15:04")+"  ") + sText.Render(truncate(text, w-13)))
			}
		}
	}
}

func legendView(w int, add func(...string)) {
	section := func(name string) { add("", sSection.Render(name), "") }
	item := func(mark, text string) { add(mark + "  " + sText.Render(truncate(text, w-4))) }
	add(sTitle.Render("Keys and legend") + sMuted.Render("  ·  ? closes it  ·  J K scroll"))

	section("Keys, after Gmail and vim; the older key works too")
	for _, kv := range [][2]string{
		{"c  (+)", "new dossier; on an agent without dossier, adopt it"},
		{"o  enter", "open the agent's pane; without a session, start it"},
		{"e  (x)", "close"}, {"#  (D)", "delete"}, {"U  (m)", "mark unread"},
		{"W  u", "wait, resume"}, {"n", "no action for now"},
		{"s  R  N", "start without prompt, restart, new desk conversation"},
		{"v", "side mode: agents open at the TUI's right"},
		{"h", "send the agent at the right back to its tab; the placeholder returns"},
		{"j k  gg G", "move, top, end"},
		{"g d  (b)", "go to the desk"}, {"g i", "active dossiers"},
		{"g t  (t)", "to do"}, {"g w  (w)", "waiting, by person"}, {"g a  (a)", "all states"},
		{"/  esc", "filter, clear"}, {"i  p  tab", "ingest now, priority order, detail panel"},
	} {
		add(sText.Render(fmt.Sprintf("%-11s", kv[0])) + sMuted.Render(truncate(kv[1], w-12)))
	}

	section("Agent, first mark of a row")
	for _, a := range []struct{ act, text string }{
		{"working", "working"},
		{"ready", "your turn: the turn ended, it waits for you"},
		{"idle", "idle"},
		{"blocked", "asks a permission"},
		{"stopped", "no tab: the session is resumable"},
		{"none", "no session yet"},
	} {
		item(activityMark(a.act), a.text)
	}

	section("State, the coloured word")
	for _, s := range []string{dossier.Open, dossier.Waiting, dossier.Done, dossier.Merged} {
		item(stateStyle(s).Render(fmt.Sprintf("%-9s", s)), map[string]string{
			dossier.Open: "something to do", dossier.Waiting: "waits on someone outside",
			dossier.Done: "closed", dossier.Merged: "merged into another dossier"}[s])
	}
	item(sFaint.Render(fmt.Sprintf("%-9s", "no action")), "open, but nothing to do for now (n)")

	section("Marks")
	item(lipgloss.NewStyle().Foreground(cAccent).Bold(true).Render("•"), "unread: something new since you last opened its pane; the title is bold")
	item(sMuted.Render("⛓"), "blocked by the dossiers named after it")
	item(sMuted.Render("↻"), "already shown above: the links form a cycle")
	item(sFaint.Render("└─"), "included by the dossier above")

	section("Waiting column")
	item(lipgloss.NewStyle().Bold(true).Foreground(cWaiting).Render("Name"), "whom it waits on")
	item(sMuted.Render("in 9d"), "when to chase")
	item(lipgloss.NewStyle().Foreground(cWorking).Render("in 2d"), "chase soon; bold: today")
	item(lipgloss.NewStyle().Foreground(cStopped).Bold(true).Render("2d late"), "the date to chase has passed")

	section("Store line")
	item(sTitle.Render("PRO"), "the store's desk: b jumps to it, enter opens it")
}
