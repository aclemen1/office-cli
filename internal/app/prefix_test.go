package app

import (
	"strings"
	"testing"
)

func TestOfficePrefixNamesDossiersAndGuardsTheSphere(t *testing.T) {
	f := newFixture(t)
	f.a.S.Config.Office.IDPrefix = "u"
	r, _ := f.a.Open(OpenParams{Title: "Limite de connexions sur les bases PostgreSQL UNISIS", NoStart: true})
	if r.ID != "U-0001" {
		t.Fatalf("id %s", r.ID)
	}
	for _, id := range []string{"1", "U-1", "u0001", "0001-limite"} {
		if d, err := f.a.Load(id); err != nil || d.ID != "U-0001" {
			t.Errorf("Load(%q): %v", id, err)
		}
	}
	_, err := f.a.Load("P-1")
	if err == nil || !strings.Contains(err.Error(), "belongs to another office") {
		t.Fatalf("foreign id: %v", err)
	}
	routed, err := f.a.Open(OpenParams{Title: "Mémo", Instruction: "U-1: relancer le fournisseur", NoStart: true})
	if err != nil || routed.Outcome != "routed" || routed.ID != "U-0001" {
		t.Fatalf("routing by prefix: %+v %v", routed, err)
	}
}

func TestTabLabel(t *testing.T) {
	f := newFixture(t)
	f.a.S.Config.Office.IDPrefix = "P"
	f.a.Open(OpenParams{Title: "RDV Dentiste Eve", NoStart: true})
	f.a.Open(OpenParams{Title: "Limite de connexions sur les bases PostgreSQL UNISIS", NoStart: true})
	short, _ := f.a.Load("1")
	long, _ := f.a.Load("2")
	if got := TabLabel(short); got != "P-0001 · RDV Dentiste Eve" {
		t.Fatalf("short %q", got)
	}
	got := TabLabel(long)
	if got != "P-0002 · Limite de connexions sur les…" || len([]rune(strings.TrimPrefix(got, "P-0002 · "))) > tabTitleLength {
		t.Fatalf("long %q", got)
	}
}

func TestTheTabIsRenamedWhenTheSessionStarts(t *testing.T) {
	f := newFixture(t)
	var renamed []string
	saved := renameTab
	renameTab = func(tab, label string) { renamed = append(renamed, tab+"="+label) }
	defer func() { renameTab = saved }()
	f.a.Open(OpenParams{Title: "Armoire"})
	if len(renamed) != 1 || renamed[0] != "fake:t1=D-0001 · Armoire" {
		t.Fatalf("renamed %v", renamed)
	}
	meta := f.calls("session/new")[0]["params"].(map[string]any)["_meta"].(map[string]any)["herdr"].(map[string]any)
	if meta["tabLabel"] != "D-0001 · Armoire" || meta["interaction"] != nil && meta["interaction"] != "native" {
		t.Fatalf("meta %v", meta)
	}
}
