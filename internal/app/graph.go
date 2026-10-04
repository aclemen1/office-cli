package app

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/aclemen1/office-cli/internal/dossier"
	"github.com/aclemen1/office-cli/internal/spec"
)

var relHint = "includes (B is part of A: an item of a meeting, a sub-affair) or depends_on (A waits for B)"

func validRel(rel string) error {
	if rel == dossier.RelIncludes || rel == dossier.RelDependsOn {
		return nil
	}
	return spec.UserError("--rel %q is not a link type. Use %s. Example: office link D-0007 D-0042 --rel includes", rel, relHint)
}

// reaches reports whether `to` can reach `target` through edges of rel.
func (a *App) reaches(from, target, rel string, index map[string]*dossier.Dossier) bool {
	seen := map[string]bool{}
	stack := []string{from}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if id == target {
			return true
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		if d := index[id]; d != nil {
			stack = append(stack, d.Targets(rel)...)
		}
	}
	return false
}

func (a *App) index() map[string]*dossier.Dossier {
	all, _ := a.All()
	idx := map[string]*dossier.Dossier{}
	for _, d := range all {
		idx[d.ID] = d
	}
	return idx
}

type LinkResult struct {
	From  string         `json:"from"`
	Rel   string         `json:"rel"`
	To    string         `json:"to"`
	Links []dossier.Link `json:"links"`
}

func (a *App) Link(fromID, toID, rel string) (LinkResult, error) {
	if err := validRel(rel); err != nil {
		return LinkResult{}, err
	}
	from, err := a.Load(fromID)
	if err != nil {
		return LinkResult{}, err
	}
	to, err := a.Load(toID)
	if err != nil {
		return LinkResult{}, err
	}
	if from.ID == to.ID {
		return LinkResult{}, spec.UserError("a dossier cannot link to itself (%s). Example: office link D-0007 D-0042 --rel %s", from.ID, rel)
	}
	if from.HasLink(rel, to.ID) {
		return LinkResult{From: from.ID, Rel: rel, To: to.ID, Links: from.Links}, nil
	}
	if a.reaches(to.ID, from.ID, rel, a.index()) {
		return LinkResult{}, spec.UserError("%s %s %s would create a cycle: %s already reaches %s through %s links. Check with `office tree %s --rel %s`",
			from.ID, rel, to.ID, to.ID, from.ID, rel, to.ID, rel)
	}
	from.Links = append(from.Links, dossier.Link{Rel: rel, To: to.ID})
	a.syncLinksBlock(from)
	if err := from.Save(); err != nil {
		return LinkResult{}, err
	}
	_ = from.Log("link %s %s (%s)", rel, to.ID, to.Title)
	return LinkResult{From: from.ID, Rel: rel, To: to.ID, Links: from.Links}, nil
}

func (a *App) Unlink(fromID, toID, rel string) (LinkResult, error) {
	if rel != "" {
		if err := validRel(rel); err != nil {
			return LinkResult{}, err
		}
	}
	from, err := a.Load(fromID)
	if err != nil {
		return LinkResult{}, err
	}
	to, err := a.Load(toID)
	if err != nil {
		return LinkResult{}, err
	}
	var kept []dossier.Link
	removed := 0
	for _, l := range from.Links {
		if l.To == to.ID && (rel == "" || l.Rel == rel) {
			removed++
			continue
		}
		kept = append(kept, l)
	}
	if removed == 0 {
		return LinkResult{}, spec.NotFound("%s has no link to %s. Its links: %s", from.ID, to.ID, linkList(from.Links))
	}
	from.Links = kept
	a.syncLinksBlock(from)
	if err := from.Save(); err != nil {
		return LinkResult{}, err
	}
	_ = from.Log("unlink %s", to.ID)
	return LinkResult{From: from.ID, Rel: rel, To: to.ID, Links: from.Links}, nil
}

func linkList(ls []dossier.Link) string {
	if len(ls) == 0 {
		return "none"
	}
	var s []string
	for _, l := range ls {
		s = append(s, l.Rel+" "+l.To)
	}
	return strings.Join(s, ", ")
}

// syncLinksBlock writes the outgoing links as Markdown links, so that any
// Markdown or OKF reader (Obsidian's graph view among them) sees the graph.
func (a *App) syncLinksBlock(d *dossier.Dossier) {
	if len(d.Links) == 0 {
		d.SetLinksBlock("")
		return
	}
	var b strings.Builder
	labels := map[string]string{dossier.RelIncludes: "Includes", dossier.RelDependsOn: "Depends on"}
	for _, rel := range []string{dossier.RelIncludes, dossier.RelDependsOn} {
		targets := d.Targets(rel)
		if len(targets) == 0 {
			continue
		}
		fmt.Fprintf(&b, "**%s**\n\n", labels[rel])
		for _, id := range targets {
			title, rel := id, id
			if dir, err := a.S.FindDir(id); err == nil {
				if t, err := dossier.Load(dir); err == nil {
					title = t.Title
				}
				rel = "../" + filepath.Base(dir) + "/dossier.md"
			}
			fmt.Fprintf(&b, "- [%s · %s](%s)\n", id, title, rel)
		}
		b.WriteString("\n")
	}
	d.SetLinksBlock(b.String())
}

// Edge is a link seen from one dossier, outgoing or incoming.
type Edge struct {
	Rel   string `json:"rel"`
	ID    string `json:"id"`
	Title string `json:"title"`
	State string `json:"state"`
}

// Incoming lists the links that point at d, including merge edges.
func (a *App) Incoming(d *dossier.Dossier) []Edge {
	var out []Edge
	all, _ := a.All()
	for _, x := range all {
		for _, l := range x.Links {
			if l.To == d.ID {
				out = append(out, Edge{Rel: l.Rel, ID: x.ID, Title: x.Title, State: x.State})
			}
		}
		if x.MergedInto == d.ID {
			out = append(out, Edge{Rel: "merged_into", ID: x.ID, Title: x.Title, State: x.State})
		}
	}
	return out
}

func (a *App) Outgoing(d *dossier.Dossier) []Edge {
	idx := a.index()
	var out []Edge
	add := func(rel, id string) {
		e := Edge{Rel: rel, ID: id}
		if t := idx[id]; t != nil {
			e.Title, e.State = t.Title, t.State
		}
		out = append(out, e)
	}
	for _, l := range d.Links {
		add(l.Rel, l.To)
	}
	if d.MergedInto != "" {
		add("merged_into", d.MergedInto)
	}
	return out
}

// BlockedBy lists the depends_on targets that are not closed yet.
func BlockedBy(d *dossier.Dossier, idx map[string]*dossier.Dossier) []string {
	var out []string
	for _, id := range d.Targets(dossier.RelDependsOn) {
		if t := idx[id]; t == nil || (t.State != dossier.Done && t.State != dossier.Merged) {
			out = append(out, id)
		}
	}
	return out
}

type TreeNode struct {
	Rel       string     `json:"rel,omitempty"`
	ID        string     `json:"id"`
	Label     string     `json:"label"`
	Title     string     `json:"title"`
	State     string     `json:"state"`
	BlockedBy []string   `json:"blocked_by,omitempty"`
	Children  []TreeNode `json:"children,omitempty"`
	Seen      bool       `json:"seen_above,omitempty"`
}

func (a *App) Tree(d *dossier.Dossier, rel string, depth int) TreeNode {
	idx := a.index()
	var walk func(x *dossier.Dossier, via string, level int, path map[string]bool) TreeNode
	walk = func(x *dossier.Dossier, via string, level int, path map[string]bool) TreeNode {
		n := TreeNode{Rel: via, ID: x.ID, Label: x.Label(), Title: x.Title, State: x.State, BlockedBy: BlockedBy(x, idx)}
		if path[x.ID] {
			n.Seen = true
			return n
		}
		if level >= depth {
			return n
		}
		path[x.ID] = true
		defer delete(path, x.ID)
		for _, l := range x.Links {
			if rel != "all" && l.Rel != rel {
				continue
			}
			if t := idx[l.To]; t != nil {
				n.Children = append(n.Children, walk(t, l.Rel, level+1, path))
			} else {
				n.Children = append(n.Children, TreeNode{Rel: l.Rel, ID: l.To, Title: "(missing)", State: "?"})
			}
		}
		return n
	}
	return walk(d, "", 0, map[string]bool{})
}

// notifyDependents tells every open dossier that depends on d that d is closed.
// Dossiers without a session only get a line in their history.
func (a *App) notifyDependents(d *dossier.Dossier) {
	all, _ := a.All()
	for _, x := range all {
		if !x.HasLink(dossier.RelDependsOn, d.ID) || x.State == dossier.Done || x.State == dossier.Merged {
			continue
		}
		_ = x.Log("dependency %s (%s) is done", d.ID, d.Title)
		if x.Run.Session == "" {
			continue
		}
		text := fmt.Sprintf("Dossier %s (%s), which this dossier depends on, is now done. Check what it unblocks here and tell me.", d.ID, d.Title)
		if err := a.sendPrompt(x, text); err != nil {
			_ = x.Log("could not notify session: %v", err)
		}
	}
}
