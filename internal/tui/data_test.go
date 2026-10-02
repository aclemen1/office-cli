package tui

import (
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
