package tui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aclemen1/dossier-cli/internal/app"
	"github.com/aclemen1/dossier-cli/internal/dossier"
	"github.com/aclemen1/dossier-cli/internal/store"
)

func TestTreeNestsIncludedDossiersAndSurvivesCycles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	s, err := store.Init(filepath.Join(root, "pro"), "pro", false)
	if err != nil {
		t.Fatal(err)
	}
	a := &app.App{S: s}
	for _, title := range []string{"Séance", "Point A", "Point B", "Seul"} {
		if _, err := a.Open(app.OpenParams{Title: title, NoStart: true}); err != nil {
			t.Fatal(err)
		}
	}
	a.Link("1", "2", dossier.RelIncludes)
	a.Link("1", "3", dossier.RelIncludes)
	// link refuses a cycle; a hand-edited file can still hold one.
	b, _ := a.Load("3")
	b.Links = append(b.Links, dossier.Link{Rel: dossier.RelIncludes, To: "D-0001"})
	if err := b.Save(); err != nil {
		t.Fatal(err)
	}
	if got := store.Discover(root); len(got) != 1 {
		t.Fatalf("stores %v", got)
	}
	rows, _, errs := load(store.Discover(root), view{})
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	var got []string
	for _, r := range rows {
		if r.header != "" {
			got = append(got, "#"+r.header)
			continue
		}
		line := strings.Repeat(">", r.depth) + r.d.Title
		if r.cycle {
			line += "↻"
		}
		got = append(got, line)
	}
	want := "#pro,Seul,Séance,>Point A,>Point B,>>Séance↻"
	if strings.Join(got, ",") != want {
		t.Fatalf("rows\n got %s\nwant %s", strings.Join(got, ","), want)
	}
	if rows, _, _ := load(store.Discover(root), view{filter: "seul"}); len(rows) != 2 || rows[1].d.Title != "Seul" {
		t.Fatalf("filter: %+v", rows)
	}
}

func TestLinksGroupEveryRelationOfALinkedDossier(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	s, _ := store.Init(filepath.Join(root, "pro"), "pro", false)
	a := &app.App{S: s}
	a.Open(app.OpenParams{Title: "Séance", NoStart: true})
	a.Open(app.OpenParams{Title: "Point", NoStart: true})
	a.Link("1", "2", dossier.RelIncludes)
	a.Link("1", "2", dossier.RelDependsOn)
	rows, _, _ := load(store.Discover(root), view{})
	ls := links(rows[1].store, rows[1].d)
	if len(ls) != 1 || ls[0].id != "D-0002" || strings.Join(ls[0].rels, ",") != "includes,depends on" {
		t.Fatalf("séance links %+v", ls)
	}
	ls = links(rows[2].store, rows[2].d)
	if len(ls) != 1 || strings.Join(ls[0].rels, ",") != "included by,needed by" {
		t.Fatalf("point links %+v", ls)
	}
}

func TestWaitingRowsGroupByPersonSoonestFirst(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	s, _ := store.Init(filepath.Join(root, "pro"), "pro", false)
	a := &app.App{S: s}
	for i, w := range [][2]string{{"Livit (service@livit.ch)", "2026-12-01"}, {"Patricia", "2026-11-01"}, {"livit", "2026-10-15"}} {
		a.Open(app.OpenParams{Title: w[0], NoStart: true})
		d, _ := a.Load(strconv.Itoa(i + 1))
		d.WaitUntil = w[1] + "T23:59:59+01:00"
		if _, err := a.SetState(d, "wait", "", w[0]); err != nil {
			t.Fatal(err)
		}
	}
	rows, _, _ := load(store.Discover(root), view{byPerson: true})
	var got []string
	for _, r := range rows {
		switch {
		case r.person || r.desk:
			got = append(got, "#"+r.header)
		case r.d != nil:
			got = append(got, r.d.ID)
		}
	}
	if want := "#pro,#Livit,D-0003,D-0001,#Patricia,D-0002"; strings.Join(got, ",") != want {
		t.Fatalf("got %s want %s", strings.Join(got, ","), want)
	}
}

func TestPriorityRaisesAMeetingWhosePointIsDue(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	s, _ := store.Init(filepath.Join(root, "pro"), "pro", false)
	a := &app.App{S: s}
	a.Open(app.OpenParams{Title: "Ouvert", NoStart: true})
	a.Open(app.OpenParams{Title: "Plus tard", NoStart: true})
	a.Open(app.OpenParams{Title: "Séance", NoStart: true})
	a.Open(app.OpenParams{Title: "Point échu", NoStart: true})
	a.Link("3", "4", dossier.RelIncludes)
	for id, until := range map[string]string{"2": "2099-01-01", "4": "2000-01-01"} {
		d, _ := a.Load(id)
		d.WaitUntil = until + "T23:59:59+01:00"
		a.SetState(d, "wait", "", "Patricia")
	}
	order := func(v view) string {
		rows, _, _ := load(store.Discover(root), v)
		var got []string
		for _, r := range rows {
			if r.d != nil && !r.desk {
				got = append(got, r.d.Title)
			}
		}
		return strings.Join(got, ",")
	}
	if got, want := order(view{byPriority: true}), "Séance,Point échu,Ouvert,Plus tard"; got != want {
		t.Fatalf("by priority\n got %s\nwant %s", got, want)
	}
	if got, want := order(view{}), "Ouvert,Séance,Point échu,Plus tard"; got != want {
		t.Fatalf("by number\n got %s\nwant %s", got, want)
	}
}

func TestANoActionDossierComesAfterOpenOnes(t *testing.T) {
	now := time.Now()
	open := &dossier.Dossier{ID: "D-0001", State: dossier.Open}
	quiet := &dossier.Dossier{ID: "D-0002", State: dossier.Open, NoAction: true}
	if !urgencyOf(open, "none", now).before(urgencyOf(quiet, "none", now)) {
		t.Fatal("a no-action dossier should rank after an open one")
	}
}

func TestBMovesToTheDeskOfTheSelectedStore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	for _, sphere := range []string{"perso", "pro"} {
		s, _ := store.Init(filepath.Join(root, sphere), sphere, false)
		(&app.App{S: s}).Open(app.OpenParams{Title: "Affaire " + sphere, NoStart: true})
	}
	m := &model{roots: store.Discover(root)}
	m.reload()
	m.cursor = len(m.rows) - 1
	m.key("b")
	if r := m.selected(); r == nil || !r.desk || r.store.name != "pro" {
		t.Fatalf("selected %+v", r)
	}
	if m.key("x"); m.status == "" || !m.statusErr {
		t.Fatal("x on the desk should be refused")
	}
}

func TestAgentsWithoutDossierFollowTheStores(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	s, _ := store.Init(filepath.Join(root, "pro"), "pro", false)
	a := &app.App{S: s}
	a.Open(app.OpenParams{Title: "Affaire", NoStart: true})
	d, _ := a.Load("1")
	d.Run.Session = "held"
	d.Save()
	old := agentsNow
	t.Cleanup(func() { agentsNow = old })
	agentsNow = func() []app.AgentPane {
		return []app.AgentPane{{PaneID: "w1:p1", Session: "held", Title: "Déjà un dossier"}, {PaneID: "w1:p2", Session: "free", Title: "Libre", Cwd: "/code/smtp-bridge"}}
	}
	m := &model{roots: store.Discover(root)}
	m.reload()
	for _, r := range m.rows {
		if r.agent != nil {
			t.Fatal("the dossiers view shows no agent")
		}
	}
	m.key("g")
	m.key("n")
	var agents []string
	for i, r := range m.rows {
		if r.agent != nil {
			agents = append(agents, r.agent.Title)
			m.cursor = i
		}
	}
	if strings.Join(agents, ",") != "Libre" {
		t.Fatalf("agents %v", agents)
	}
	if !strings.Contains(m.rowView(m.rows[m.cursor], false, 80), "Libre") {
		t.Fatal("the agent row renders empty")
	}
	if m.key("x"); !m.statusErr {
		t.Fatal("x on an agent should be refused")
	}
	if m.key("+"); m.ask == nil || m.ask.fields[0].value != "Libre" {
		t.Fatal("+ should open the adopt form with the pane's title")
	}
}

func TestGmailKeysAndGSequences(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	s, _ := store.Init(filepath.Join(root, "pro"), "pro", false)
	a := &app.App{S: s}
	a.Open(app.OpenParams{Title: "Ouvert", NoStart: true})
	a.Open(app.OpenParams{Title: "Calme", NoStart: true})
	d, _ := a.Load("2")
	a.Park(d, "")
	m := &model{roots: store.Discover(root)}
	m.reload()
	m.key("G")
	m.key("g")
	m.key("g")
	if r := m.selected(); r == nil || !r.desk {
		t.Fatal("gg should reach the first row, the desk")
	}
	m.key("G")
	m.key("g")
	m.key("d")
	if r := m.selected(); r == nil || !r.desk {
		t.Fatal("g d should reach the desk")
	}
	m.key("g")
	m.key("t")
	for _, r := range m.rows {
		if r.d != nil && r.d.Title == "Calme" {
			t.Fatal("g t should hide the dossier with no action")
		}
	}
	m.key("c")
	if m.ask == nil || m.ask.title != "New dossier" {
		t.Fatal("c should open the new-dossier form")
	}
}

func TestHSendsTheDockedAgentHome(t *testing.T) {
	m := &model{}
	if m.key("h"); !m.statusErr {
		t.Fatal("h with nothing docked should say so")
	}
	m = &model{side: true, placeholder: "w1:p2", docked: docked{root: "/s", id: "U-0001"}}
	if cmd := m.key("h"); cmd == nil || m.docked.id != "" || !m.side {
		t.Fatalf("h should undock and keep side mode: %+v", m.docked)
	}
}

func TestThePlaceholderRereadsAtOnceWhenAnAgentDocks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	s, _ := store.Init(filepath.Join(root, "pro"), "pro", false)
	a := &app.App{S: s}
	roots := store.Discover(root)
	p := &placeholder{roots: roots, stamps: stampsOf(roots), counted: time.Now(), lines: []string{}}
	p.watch()
	if p.counted.IsZero() {
		t.Fatal("no stamp yet: nothing to reread")
	}
	os.MkdirAll(filepath.Dir(a.DockStamp()), 0o755)
	os.WriteFile(a.DockStamp(), []byte("x"), 0o644)
	p.watch()
	if !p.counted.IsZero() {
		t.Fatal("a touched stamp should make the placeholder reread the stores")
	}
}

func TestNavigationGoesWhereYouAreNeeded(t *testing.T) {
	sv := &storeView{root: "/s"}
	mk := func(id, activity string, unread bool) row {
		return row{store: sv, d: &dossier.Dossier{ID: id, Run: dossier.RunState{Session: "s-" + id}}, activity: activity, unread: unread}
	}
	m := &model{side: true, placeholder: "w1:p9", rows: []row{
		mk("D-1", "idle", false), mk("D-2", "ready", false), mk("D-3", "idle", true), mk("D-4", "blocked", false),
	}}
	var got []string
	for i := 0; i < 4; i++ {
		m.key("]")
		got = append(got, m.docked.id)
		m.dockDone()
	}
	if strings.Join(got, ",") != "D-4,D-2,D-3,D-4" {
		t.Fatalf("] order %v: permission, your turn, unread, then around", got)
	}
	if m.forYou() != 2 {
		t.Fatalf("for you %d", m.forYou())
	}
	m.dockDone()
	m.key("'")
	if m.docked.id != "D-3" {
		t.Fatalf("' should go back to D-3, got %s", m.docked.id)
	}
	m.dockDone()
	m.key("'")
	if m.docked.id != "D-4" {
		t.Fatalf("' again should toggle to D-4, got %s", m.docked.id)
	}
}

func TestTheFooterShowsWhatMakesSenseNow(t *testing.T) {
	keys := func(l [][2]string) string {
		var s []string
		for _, k := range l {
			s = append(s, k[0])
		}
		return strings.Join(s, " ")
	}
	sv := &storeView{root: "/s"}
	waiting := row{store: sv, d: &dossier.Dossier{ID: "D-1", State: dossier.Waiting, Run: dossier.RunState{Session: "x"}}, activity: "idle"}
	m := &model{rows: []row{waiting}}
	g := m.footer()
	if g[0].title != "dossier" || g[1].title != "agent" || g[2].title != "move" || g[3].title != "view" {
		t.Fatalf("themes %+v", g)
	}
	if got := keys(g[0].keys); !strings.Contains(got, "u") || strings.Contains(got, "n") || strings.Contains(keys(g[2].keys), "g d") {
		t.Fatalf("waiting row keys %q, move %q", got, keys(g[2].keys))
	}
	m.key("g")
	if g := m.footer(); keys(g[0].keys) != "g d p i t w a s n" {
		t.Fatalf("after g: %q", keys(g[0].keys))
	}
	m.key("esc")
	if m.gPending {
		t.Fatal("esc should cancel g")
	}
}

func TestTheTUIComesBackWhereItWas(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	s, _ := store.Init(filepath.Join(root, "pro"), "pro", false)
	a := &app.App{S: s}
	a.Open(app.OpenParams{Title: "Un", NoStart: true})
	a.Open(app.OpenParams{Title: "Deux", NoStart: true})
	m := &model{roots: store.Discover(root), root: root}
	m.restore(loadState(root), false)
	if !m.byPriority {
		t.Fatal("a first start sorts by priority")
	}
	m.todo, m.byPriority = true, false
	m.reload()
	for i, r := range m.rows {
		if r.d != nil && r.d.Title == "Deux" {
			m.cursor = i
		}
	}
	m.lastDocked = docked{"/s", "D-0001"}
	m.save()
	n := &model{roots: store.Discover(root), root: root}
	n.restore(loadState(root), false)
	if !n.todo || n.byPriority || n.selected() == nil || n.selected().d.Title != "Deux" || n.lastDocked.id != "D-0001" {
		t.Fatalf("restored todo=%v priority=%v selected=%+v last=%+v", n.todo, n.byPriority, n.selected(), n.lastDocked)
	}
}

func TestTheSideRatioComesBackWithinBounds(t *testing.T) {
	if r := (&model{}).ratio(); r != "0.3500" {
		t.Fatalf("default %s", r)
	}
	if r := (&model{sideRatio: 0.5}).ratio(); r != "0.5000" {
		t.Fatalf("saved %s", r)
	}
	if r := (&model{sideRatio: 0.99}).ratio(); r != "0.3500" {
		t.Fatalf("a ratio that leaves no room falls back: %s", r)
	}
}

func TestOShowsTheAgentAndKeepsTheFocus(t *testing.T) {
	sv := &storeView{root: "/s"}
	m := &model{side: true, placeholder: "w1:p9", rows: []row{{store: sv, d: &dossier.Dossier{ID: "D-1", Run: dossier.RunState{Session: "x"}}, activity: "idle"}}}
	if cmd := m.key("O"); cmd == nil || m.docked.id != "D-1" {
		t.Fatalf("O should dock D-1: %+v", m.docked)
	}
}

func TestUnreadIsHerdrsDone(t *testing.T) {
	d := &dossier.Dossier{ID: "D-1"}
	if unreadOf(nil, d, "idle") || !unreadOf(nil, d, "ready") {
		t.Fatal("unread follows herdr: done means the agent's turn ended unseen")
	}
}

func TestStarredDossiersComeFirstAndStayAtHand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	s, _ := store.Init(filepath.Join(root, "pro"), "pro", false)
	a := &app.App{S: s}
	for _, title := range []string{"Un", "Deux", "Trois"} {
		a.Open(app.OpenParams{Title: title, NoStart: true})
	}
	d, _ := a.Load("3")
	a.Star(d, true)
	d.WaitUntil = "2099-01-01T00:00:00+01:00"
	a.SetState(d, "wait", "", "Livit")
	titles := func(m *model) string {
		var got []string
		for _, r := range m.rows {
			if r.d != nil && !r.desk {
				got = append(got, r.d.Title)
			}
		}
		return strings.Join(got, ",")
	}
	m := &model{roots: store.Discover(root)}
	m.reload()
	if got := titles(m); !strings.HasPrefix(got, "Trois") {
		t.Fatalf("a starred dossier comes first: %s", got)
	}
	m.key("g")
	m.key("t")
	if got := titles(m); !strings.Contains(got, "Trois") {
		t.Fatalf("a starred dossier stays in to do, even waiting: %s", got)
	}
	m.key("g")
	m.key("s")
	if got := titles(m); got != "Trois" {
		t.Fatalf("g s shows only the starred: %s", got)
	}
	for _, r := range m.rows {
		if r.d != nil && r.d.Title == "Trois" && !strings.Contains(m.rowView(r, false, 80), "⭐") {
			t.Fatal("a starred row shows a star")
		}
	}
}

func TestTheTUIRecordsItsPaneUntilItQuits(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HERDR_PANE_ID", "w5T:p9")
	m := &model{root: "/s"}
	m.save()
	if s := loadState("/s"); s.Pane != "w5T:p9" {
		t.Fatalf("pane %q", s.Pane)
	}
	m.key("q")
	if s := loadState("/s"); s.Pane != "" {
		t.Fatalf("a quit TUI keeps its pane: %q", s.Pane)
	}
}

func TestDocksRunOneAtATimeAndTheLastAskWins(t *testing.T) {
	sv := &storeView{root: "/s"}
	mk := func(id string) row {
		return row{store: sv, d: &dossier.Dossier{ID: id, Run: dossier.RunState{Session: "s-" + id}}, activity: "idle"}
	}
	m := &model{side: true, placeholder: "w1:p9", rows: []row{mk("D-1"), mk("D-2"), mk("D-3")}}
	if cmd := m.dock(&m.rows[0], false); cmd == nil || m.docked.id != "D-1" {
		t.Fatal("the first dock runs at once")
	}
	if cmd := m.dock(&m.rows[1], false); cmd != nil {
		t.Fatal("a second dock must wait for the first")
	}
	m.dock(&m.rows[2], true)
	if cmd := m.dockDone(); cmd == nil || m.docked.id != "D-3" || m.wantDock != nil {
		t.Fatalf("when the first is done, only the last ask runs: docked %s", m.docked.id)
	}
}

func TestTheFocusFollowsWhenTheKeyCameFromTheAgent(t *testing.T) {
	sv := &storeView{root: "/s"}
	m := &model{side: true, placeholder: "w1:p9", docking: true, rows: []row{
		{store: sv, d: &dossier.Dossier{ID: "D-1", Run: dossier.RunState{Session: "x"}}, activity: "ready"},
	}}
	old := tuiFocused
	t.Cleanup(func() { tuiFocused = old })
	tuiFocused = func() bool { return true }
	m.key("]")
	if m.wantFocus {
		t.Fatal("] typed in the TUI keeps the focus there")
	}
	tuiFocused = func() bool { return false }
	m.key("]")
	if !m.wantFocus {
		t.Fatal("] relayed from the docked agent: the focus follows to the new agent")
	}
}

func TestAngleBracketsShowTheNeighbourDossier(t *testing.T) {
	sv := &storeView{root: "/s"}
	mk := func(id string) row {
		return row{store: sv, d: &dossier.Dossier{ID: id, Run: dossier.RunState{Session: "s-" + id}}, activity: "idle"}
	}
	m := &model{side: true, placeholder: "w1:p9", rows: []row{mk("D-1"), {}, mk("D-2"), mk("D-3")}, docked: docked{"/s", "D-2"}}
	m.key(">")
	if m.docked.id != "D-3" {
		t.Fatalf("> from D-2: %s", m.docked.id)
	}
	m.dockDone()
	m.key("<")
	m.dockDone()
	m.key("<")
	if m.docked.id != "D-1" {
		t.Fatalf("< twice from D-3 skips the spacer: %s", m.docked.id)
	}
}

func TestTheShownAgentKeepsItsRank(t *testing.T) {
	sv := &storeView{root: "/s"}
	d := &dossier.Dossier{ID: "D-1", State: dossier.Open, Run: dossier.RunState{Session: "x", PaneID: "p"}}
	live := map[string]string{"p": "unknown"}
	if rankActivity(d, live, sv, view{}) != "idle" {
		t.Fatal("not shown: its own activity")
	}
	if rankActivity(d, live, sv, view{docked: "/s|D-1"}) != "ready" {
		t.Fatal("shown at the right: it keeps the your-turn rank")
	}
}

func TestOnlyAnAgentThatWaitedKeepsTheTopRank(t *testing.T) {
	sv := &storeView{root: "/s"}
	idle := row{store: sv, d: &dossier.Dossier{ID: "D-1", Run: dossier.RunState{Session: "x"}}, activity: "idle"}
	ready := row{store: sv, d: &dossier.Dossier{ID: "D-2", Run: dossier.RunState{Session: "y"}}, activity: "ready"}
	m := &model{side: true, placeholder: "w1:p9"}
	m.dock(&idle, false)
	if m.dockedKey() != "" {
		t.Fatal("an idle agent shown at the right must not jump to the top")
	}
	m.dockDone()
	m.dock(&ready, false)
	if m.dockedKey() != "/s|D-2" {
		t.Fatal("an agent that waited for you keeps its rank while shown")
	}
}

func TestAWorkingAgentSpins(t *testing.T) {
	frame = 0
	a := activityMark("working")
	frame = 1
	if activityMark("working") == a {
		t.Fatal("the working mark should change from one frame to the next")
	}
	if activityMark("ready") == a {
		t.Fatal("working and your turn must not look alike")
	}
	m := &model{rows: []row{{activity: "idle"}}}
	if m.spinning() {
		t.Fatal("nothing works: the beat slows down")
	}
	m.rows = append(m.rows, row{activity: "working"})
	if !m.spinning() {
		t.Fatal("an agent works: the mark spins")
	}
}
