package tui

import (
	"time"

	"github.com/aclemen1/office-cli/internal/dossier"
)

// Priority ranks, most urgent first.
const (
	rankYourTurn = iota // the agent finished its turn or asks a permission
	rankChaseDue        // a wait whose chase date is today or past
	rankOpen            // open, oldest update first
	rankNoAction        // open, marked as needing no action for now
	rankWaiting         // a wait still to come, soonest chase first
	rankClosed
)

type urgency struct {
	rank int
	key  string
}

func (u urgency) before(v urgency) bool {
	if u.rank != v.rank {
		return u.rank < v.rank
	}
	return u.key < v.key
}

func urgencyOf(d *dossier.Dossier, activity string, now time.Time) urgency {
	switch {
	case d.State == dossier.Done || d.State == dossier.Merged:
		return urgency{rankClosed, d.ID}
	case activity == "ready" || activity == "blocked" || activity == "asking":
		return urgency{rankYourTurn, utc(d.Updated)}
	case d.State == dossier.Waiting:
		if t, err := time.Parse(time.RFC3339, d.WaitUntil); err == nil {
			y, m, day := now.Date()
			if t.Before(time.Date(y, m, day+1, 0, 0, 0, 0, now.Location())) {
				return urgency{rankChaseDue, utc(d.WaitUntil)}
			}
		}
		return urgency{rankWaiting, untilKey(d)}
	}
	if parked(d) {
		return urgency{rankNoAction, utc(d.Updated)}
	}
	return urgency{rankOpen, utc(d.Updated)}
}

func parked(d *dossier.Dossier) bool { return d.State == dossier.Open && d.NoAction }

func utc(ts string) string {
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	return ts
}

// effective gives each dossier the urgency of its most urgent descendant: a
// meeting rises as soon as one of its points needs you.
func effective(ids []string, children map[string][]string, own map[string]urgency) map[string]urgency {
	out := map[string]urgency{}
	visiting := map[string]bool{}
	var walk func(id string) urgency
	walk = func(id string) urgency {
		if u, ok := out[id]; ok {
			return u
		}
		u := own[id]
		if visiting[id] {
			return u
		}
		visiting[id] = true
		for _, k := range children[id] {
			if v := walk(k); v.before(u) {
				u = v
			}
		}
		delete(visiting, id)
		out[id] = u
		return u
	}
	for _, id := range ids {
		walk(id)
	}
	return out
}
