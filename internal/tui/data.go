package tui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aclemen1/dossier-cli/internal/app"
	"github.com/aclemen1/dossier-cli/internal/dossier"
	"github.com/aclemen1/dossier-cli/internal/store"
)

// row is one line of the list: a store header or a dossier, indented under the
// dossier that includes it.
type row struct {
	header   string
	store    *storeView
	d        *dossier.Dossier
	activity string
	depth    int
	last     []bool // per ancestor level: was that ancestor the last child
	blocked  []string
	cycle    bool           // already shown above on this branch
	unread   bool           // changed since the user last opened its pane
	person   bool           // header of a waiting group
	desk     bool           // store header, carrying the store's desk in d
	agent    *app.AgentPane // an agent in a herdr pane that no dossier holds
	count    int
}

func (r row) selectable() bool { return r.d != nil || r.agent != nil }

// key identifies the row across reloads.
func (r row) key() string {
	switch {
	case r.d != nil:
		return r.store.root + "|" + r.d.ID
	case r.agent != nil:
		return "agent|" + r.agent.Session
	}
	return ""
}

// agentRows lists the Claude Code agents whose session no dossier or desk holds.
func agentRows(stores []*storeView, filter string) []row {
	held := map[string]bool{}
	for _, sv := range stores {
		for _, d := range append(append([]*dossier.Dossier{}, sv.all...), sv.a.Desk()) {
			if d.Run.Session != "" {
				held[d.Run.Session] = true
			}
		}
	}
	var out []row
	for _, ag := range agentsNow() {
		if held[ag.Session] {
			continue
		}
		if filter != "" && !strings.Contains(strings.ToLower(ag.Title+" "+ag.Cwd), strings.ToLower(filter)) {
			continue
		}
		a := ag
		out = append(out, row{agent: &a, activity: app.Activity(&dossier.Dossier{Run: dossier.RunState{Session: a.Session, PaneID: a.PaneID}}, stores[0].live)})
	}
	if len(out) == 0 {
		return nil
	}
	return append([]row{{}, {header: "agents without dossier"}}, out...)
}

// deskRow is the store header: selectable, it stands for the store's desk.
func deskRow(sv *storeView) row {
	d := sv.a.Desk()
	return row{header: sv.name, store: sv, d: d, desk: true, activity: app.Activity(d, sv.live),
		unread: d.Run.Session != "" && sv.seen.Unread(sv.a, d)}
}

func (r row) spacer() bool { return r.d == nil && r.agent == nil && r.header == "" }

type storeView struct {
	name  string
	root  string
	a     *app.App
	all   []*dossier.Dossier
	seen  app.Seen
	live  map[string]string
	byID  map[string]*dossier.Dossier
	count map[string]int
}

func shown(d *dossier.Dossier, v view) bool {
	switch {
	case v.todo:
		if d.State != dossier.Open || d.NoAction {
			return false
		}
	case !v.all:
		if d.State != dossier.Open && d.State != dossier.Waiting {
			return false
		}
	}
	if v.filter == "" {
		return true
	}
	hay := strings.ToLower(d.ID + " " + d.Alias + " " + d.Title + " " + d.WaitingOn)
	return strings.Contains(hay, strings.ToLower(v.filter))
}

// load reads every store. byPerson lays the waiting dossiers out by whom they
// wait on instead of the store trees.
// view says what load shows and in which order.
type view struct {
	all        bool   // every state, not only open and waiting
	todo       bool   // only open dossiers that need action
	filter     string // substring of id, alias, title or whom it waits on
	byPerson   bool   // waiting dossiers grouped by whom they wait on
	byPriority bool   // what needs you first, instead of by number
}

func load(roots []string, v view) ([]row, []*storeView, []string) {
	filter, byPerson := v.filter, v.byPerson
	var rows []row
	var stores []*storeView
	var errs []string
	live := app.Panes()
	var apps []*app.App
	for _, root := range roots {
		if s, err := store.Open(root); err == nil {
			apps = append(apps, &app.App{S: s})
		}
	}
	seen := app.LoadSeen(apps...)
	for _, root := range roots {
		s, err := store.Open(root)
		if err != nil {
			errs = append(errs, root+": "+err.Error())
			continue
		}
		a := &app.App{S: s}
		ds, err := a.All()
		if err != nil {
			errs = append(errs, root+": "+err.Error())
			continue
		}
		name := s.Config.Store.Sphere
		if name == "" {
			name = filepath.Base(root)
		}
		sv := &storeView{name: name, root: root, a: a, all: ds, live: live, seen: seen, byID: map[string]*dossier.Dossier{}, count: map[string]int{}}
		stores = append(stores, sv)
		for _, d := range ds {
			sv.byID[d.ID] = d
			sv.count[d.State]++
		}
		if byPerson {
			continue
		}
		if len(rows) > 0 {
			rows = append(rows, row{})
		}
		rows = append(rows, deskRow(sv))
		rows = append(rows, treeRows(sv, ds, live, v)...)
	}
	if byPerson {
		for _, sv := range stores {
			rows = append(rows, deskRow(sv))
		}
		if waiting := waitingRows(stores, filter); len(waiting) > 0 {
			rows = append(append(rows, row{}), waiting...)
		}
	} else if len(stores) > 0 {
		rows = append(rows, agentRows(stores, filter)...)
	}
	return rows, stores, errs
}

// waitingRows groups the waiting dossiers of every store by whom they wait on;
// the person to chase first comes first, and within a person the soonest date.
func waitingRows(stores []*storeView, filter string) []row {
	type group struct {
		name  string
		first string
		rows  []row
	}
	var groups []*group
	byName := map[string]*group{}
	for _, sv := range stores {
		for _, d := range sv.all {
			if d.State != dossier.Waiting || !shown(d, view{filter: filter}) {
				continue
			}
			name := PersonOf(d.WaitingOn)
			key := strings.ToLower(name)
			g := byName[key]
			if g == nil {
				g = &group{name: name}
				byName[key] = g
				groups = append(groups, g)
			}
			g.rows = append(g.rows, row{store: sv, d: d, activity: app.Activity(d, sv.live), blocked: app.BlockedBy(d, sv.byID), unread: sv.seen.Unread(sv.a, d)})
		}
	}
	for _, g := range groups {
		sort.SliceStable(g.rows, func(i, j int) bool { return untilKey(g.rows[i].d) < untilKey(g.rows[j].d) })
		g.first = untilKey(g.rows[0].d)
	}
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].first < groups[j].first })
	var out []row
	for i, g := range groups {
		if i > 0 {
			out = append(out, row{})
		}
		out = append(out, row{header: g.name, person: true, count: len(g.rows)})
		out = append(out, g.rows...)
	}
	return out
}

// untilKey sorts by chase date; a wait without a date comes last.
func untilKey(d *dossier.Dossier) string {
	if d.WaitUntil == "" {
		return "~"
	}
	if t, err := time.Parse(time.RFC3339, d.WaitUntil); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	return d.WaitUntil
}

// PersonOf is the short name of whom a dossier waits on: "Livit
// (service@livit.ch)" is Livit.
func PersonOf(on string) string {
	on = strings.TrimSpace(on)
	if i := strings.IndexAny(on, "(,"); i > 0 {
		return strings.TrimSpace(on[:i])
	}
	if on == "" {
		return "?"
	}
	return on
}

// treeRows lays the shown dossiers out as a forest: a dossier appears under
// each shown dossier that includes it, and at the top level only when no shown
// dossier includes it.
func treeRows(sv *storeView, ds []*dossier.Dossier, live map[string]string, v view) []row {
	visible := map[string]bool{}
	for _, d := range ds {
		if shown(d, v) {
			visible[d.ID] = true
		}
	}
	children := map[string][]string{}
	pair := map[[2]string]bool{}
	held := map[string]bool{}
	for _, d := range ds {
		for _, id := range d.Targets(dossier.RelIncludes) {
			if !visible[d.ID] || !visible[id] || d.ID == id || pair[[2]string{d.ID, id}] {
				continue
			}
			pair[[2]string{d.ID, id}] = true
			children[d.ID] = append(children[d.ID], id)
			held[id] = true
		}
	}
	var rank map[string]urgency
	if v.byPriority {
		own := map[string]urgency{}
		var ids []string
		now := time.Now()
		for _, d := range ds {
			if visible[d.ID] {
				own[d.ID] = urgencyOf(d, app.Activity(d, live), now)
				ids = append(ids, d.ID)
			}
		}
		rank = effective(ids, children, own)
	}
	order := func(ids []string) {
		sort.SliceStable(ids, func(i, j int) bool {
			a, b := ids[i], ids[j]
			if rank != nil && rank[a] != rank[b] {
				return rank[a].before(rank[b])
			}
			return less(sv.byID[a], sv.byID[b])
		})
	}
	var roots []string
	for _, d := range ds {
		if visible[d.ID] && !held[d.ID] {
			roots = append(roots, d.ID)
		}
	}
	order(roots)
	idx := sv.byID
	var out []row
	var walk func(id string, depth int, last []bool, path map[string]bool)
	walk = func(id string, depth int, last []bool, path map[string]bool) {
		d := idx[id]
		out = append(out, row{store: sv, d: d, activity: app.Activity(d, live), depth: depth,
			last: append([]bool{}, last...), blocked: app.BlockedBy(d, idx), cycle: path[id], unread: sv.seen.Unread(sv.a, d)})
		if path[id] {
			return
		}
		path[id] = true
		kids := append([]string{}, children[id]...)
		order(kids)
		for i, k := range kids {
			walk(k, depth+1, append(last, i == len(kids)-1), path)
		}
		delete(path, id)
	}
	for _, id := range roots {
		// A dossier with its points stands apart from its neighbours.
		grouped := len(children[id]) > 0
		if grouped && len(out) > 0 && !out[len(out)-1].spacer() {
			out = append(out, row{})
		}
		walk(id, 0, nil, map[string]bool{})
		if grouped {
			out = append(out, row{})
		}
	}
	if n := len(out); n > 0 && out[n-1].spacer() {
		out = out[:n-1]
	}
	// A cycle of links leaves dossiers that no root reaches: start from them too.
	for {
		seen := map[string]bool{}
		for _, r := range out {
			if r.d != nil {
				seen[r.d.ID] = true
			}
		}
		var rest []string
		for _, d := range ds {
			if visible[d.ID] && !seen[d.ID] {
				rest = append(rest, d.ID)
			}
		}
		if len(rest) == 0 {
			break
		}
		order(rest)
		walk(rest[0], 0, nil, map[string]bool{})
	}
	return out
}

// less puts lasting dossiers (with an alias) first, then open before waiting
// before closed, then by number.
func less(a, b *dossier.Dossier) bool {
	if (a.Alias != "") != (b.Alias != "") {
		return a.Alias != ""
	}
	if ra, rb := stateRank(a.State), stateRank(b.State); ra != rb {
		return ra < rb
	}
	return a.Num() < b.Num()
}

func stateRank(s string) int {
	switch s {
	case dossier.Open:
		return 0
	case dossier.Waiting:
		return 1
	case dossier.Done:
		return 2
	}
	return 3
}

func logTail(d *dossier.Dossier, n int) []string {
	b, err := os.ReadFile(d.Path("log.md"))
	if err != nil {
		return nil
	}
	var lines []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "- ") {
			lines = append(lines, strings.TrimPrefix(l, "- "))
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// linked is one dossier linked to the selected one, with every relation
// between them, named from the selected dossier's side.
type linked struct {
	id, title, state string
	rels             []string
}

var (
	outName = map[string]string{"includes": "includes", "depends_on": "depends on", "merged_into": "merged into"}
	inName  = map[string]string{"includes": "included by", "depends_on": "needed by", "merged_into": "merged from"}
)

func links(sv *storeView, d *dossier.Dossier) []linked {
	var out []linked
	pos := map[string]int{}
	add := func(e app.Edge, names map[string]string) {
		rel := names[e.Rel]
		if rel == "" {
			rel = e.Rel
		}
		i, ok := pos[e.ID]
		if !ok {
			i = len(out)
			pos[e.ID] = i
			id := e.ID
			if t := sv.byID[e.ID]; t != nil {
				id = t.Label()
			}
			out = append(out, linked{id: id, title: e.Title, state: e.State})
		}
		for _, r := range out[i].rels {
			if r == rel {
				return
			}
		}
		out[i].rels = append(out[i].rels, rel)
	}
	for _, e := range sv.a.Outgoing(d) {
		add(e, outName)
	}
	for _, e := range sv.a.Incoming(d) {
		add(e, inName)
	}
	return out
}

// agentsNow lists the agents in herdr panes. Tests replace it.
var agentsNow = app.Agents
