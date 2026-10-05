package actions

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/aclemen1/office-cli/internal/app"
	"github.com/aclemen1/office-cli/internal/dossier"
	"github.com/aclemen1/office-cli/internal/office"
	"github.com/aclemen1/office-cli/internal/spec"
)

func init() {
	spec.Register(&spec.Action{
		Category: "dossier", Name: "grep", Summary: "Search a dossier's full conversation, compactions included.",
		Params: []spec.Param{
			idParam("Dossier id. Defaults to DOSSIER_ID."),
			{Name: "pattern", Kind: spec.String, Positional: true, Required: true, Help: "Regular expression, case-insensitive."},
			{Name: "limit", Kind: spec.String, Default: "50", Help: "Maximum number of hits."},
		},
		Examples: []string{`office grep D-0042 "date de passage"`, `office grep D-0042 "devis|offre" --limit 10`},
		Run: func(ctx *spec.Context) (any, error) {
			return withApp(ctx, false, func(a *app.App) (any, error) {
				d, err := a.LoadAny(ctx.Str("id"))
				if err != nil {
					return nil, err
				}
				limit, _ := strconv.Atoi(ctx.Str("limit"))
				return a.Grep(d, ctx.Str("pattern"), limit)
			})
		},
		Text: func(w io.Writer, r any) {
			for _, h := range r.([]app.GrepHit) {
				fmt.Fprintf(w, "%s %s:%d %s: %s\n", h.Dossier, h.Source, h.Line, h.Role, h.Text)
			}
		},
	})

	spec.Register(&spec.Action{
		Category: "graph", Name: "merge", Summary: "Merge one dossier into another: files, sources, threads and links move over.",
		Discussion: "<from> becomes merged and points at <into>; its session tab closes. A done <into> is reopened. " +
			"The session of <into> hears about the merge; the conversation of <from> stays searchable with grep.",
		Params: []spec.Param{
			{Name: "from", Kind: spec.String, Positional: true, Required: true, Help: "Dossier that disappears into the other."},
			{Name: "into", Kind: spec.String, Required: true, Help: "Dossier that receives everything."},
		},
		Effects: []string{
			"Copies <from>/context and <from>/files into <into>; moves sources, threads and links.",
			"Sets <from> to merged with merged_into; closes its tab.",
			"Reopens <into> when it was done (source transitions follow) and prompts its session.",
		},
		Destructive: true,
		Examples:    []string{"office merge D-0051 --into D-0042"},
		Run: func(ctx *spec.Context) (any, error) {
			return withApp(ctx, true, func(a *app.App) (any, error) { return a.Merge(ctx.Str("from"), ctx.Str("into")) })
		},
		Text: func(w io.Writer, r any) {
			m := r.(app.MergeResult)
			fmt.Fprintf(w, "%s merged into %s · %d file(s)\n", m.From, m.Into, len(m.Files))
		},
	})

	spec.Register(&spec.Action{
		Category: "graph", Name: "notify", Summary: "Send an event from one dossier to another: a decision, new information, a request.",
		Discussion: "Any two dossiers of the office, e.g. a meeting tells an item what was decided, or an item asks its meeting to add a point. " +
			"<to> may be desk: the event becomes an escalation, as with `office escalate`.",
		Params: []spec.Param{
			{Name: "from", Kind: spec.String, Positional: true, Required: true, Help: "Dossier the event comes from."},
			{Name: "to", Kind: spec.String, Positional: true, Required: true, Help: "Dossier that must know, or desk; an id of another office (U-DESK, U-0012) reaches it there."},
			{Name: "text", Kind: spec.String, Required: true, Help: "What <to> must know."},
		},
		Effects:  []string{"Logs the event in <to> and prompts its session; a dossier without session only gets the log line."},
		Examples: []string{`office notify D-0007 D-0042 --text "Décidé en séance du 08.10 : on attend l'offre."`},
		Run: func(ctx *spec.Context) (any, error) {
			return withApp(ctx, true, func(a *app.App) (any, error) {
				from, err := a.LoadAny(ctx.Str("from"))
				if err != nil {
					return nil, err
				}
				b, to, err := a.LoadAcross(ctx.Str("to"))
				if err != nil {
					return nil, err
				}
				if from.ID == to.ID {
					return nil, spec.UserError("%s cannot notify itself", from.ID)
				}
				if b != a {
					// Another office: hold its lock while writing there.
					if err := b.S.Lock(); err != nil {
						return nil, err
					}
					defer b.S.Unlock()
				}
				if app.IsDesk(to) {
					return b.Escalate(from, ctx.Str("text"))
				}
				prompted, err := b.Tell(from, to, ctx.Str("text"))
				return map[string]any{"to": to.ID, "prompted": prompted}, err
			})
		},
	})

	spec.Register(&spec.Action{
		Category: "graph", Name: "escalate", Summary: "Escalate a request from a dossier to the office's desk: a rule to adopt, a skill to change, anything beyond the dossier.",
		Discussion: "The escalation waits in <office>/desk/escalations/ until the desk's session is idle, then reaches it as a prompt; " +
			"ingest delivers what is still pending. `office show desk` lists the pending ones.",
		Params: []spec.Param{
			idParam("Dossier the request comes from. Defaults to DOSSIER_ID."),
			{Name: "text", Kind: spec.String, Required: true, Help: "What the desk must know or decide."},
		},
		Effects: []string{"Writes the escalation in desk/escalations/ and a line in both histories.",
			"Prompts the desk at once when its session is idle; otherwise ingest does it later."},
		Examples: []string{`office escalate P-0011 --text "Règle proposée : une citation dictée au desk se dépose dans 10-Staging/."`},
		Run: func(ctx *spec.Context) (any, error) {
			return withApp(ctx, true, func(a *app.App) (any, error) {
				id := ctx.Str("id")
				if id == "" && os.Getenv("DOSSIER_ID") == "" {
					return nil, spec.UserError("name the dossier that escalates. Example: office escalate P-0011 --text \"…\"")
				}
				from, err := a.LoadAny(id)
				if err != nil {
					return nil, err
				}
				return a.Escalate(from, ctx.Str("text"))
			})
		},
		Text: func(w io.Writer, r any) {
			e := r.(app.EscalateResult)
			if e.Delivered {
				fmt.Fprintf(w, "%s → %s · delivered\n", e.From, e.To)
			} else {
				fmt.Fprintf(w, "%s → %s · pending (%d), delivered when the desk is idle\n", e.From, e.To, e.Pending)
			}
		},
	})

	spec.Register(&spec.Action{
		Category: "dossier", Name: "track", Summary: "Attach a source thread to a dossier (a draft's thread, a related conversation).",
		Discussion: "The reference is <source>:<kind>/<id>, as connectors write it, e.g. gmail:thread/1a0d7b2f4c0fff93. " +
			"Its new messages become events of the dossier, and source transitions (stars) apply to it.",
		Params: []spec.Param{
			idParam("Dossier id. Defaults to DOSSIER_ID."),
			{Name: "ref", Kind: spec.String, Positional: true, Required: true, Help: "Source reference, e.g. gmail:thread/<threadId>."},
		},
		Effects:  []string{"Adds the reference to sources and threads in dossier.md; the next transition reaches it too."},
		Examples: []string{"office track U-0002 gmail:thread/1a0d7b2f4c0fff93"},
		Run: func(ctx *spec.Context) (any, error) {
			return withApp(ctx, true, func(a *app.App) (any, error) { return a.Track(ctx.Str("id"), ctx.Str("ref")) })
		},
	})

	spec.Register(&spec.Action{
		Category: "dossier", Name: "restart", Summary: "Restart a dossier's session: same conversation, fresh process, tools and environment.",
		Params:   []spec.Param{idParam("Dossier id.")},
		Effects:  []string{"Closes the tab (the agent exits), then resumes the session in a new tab with --resume."},
		Examples: []string{"office restart U-0002"},
		Run: func(ctx *spec.Context) (any, error) {
			return withApp(ctx, true, func(a *app.App) (any, error) {
				d, err := a.LoadAny(ctx.Str("id"))
				if err != nil {
					return nil, err
				}
				if err := a.Restart(d); err != nil {
					return nil, err
				}
				return map[string]any{"id": d.ID, "session": d.Run.Session, "tab_id": d.Run.TabID}, nil
			})
		},
	})

	spec.Register(&spec.Action{
		Category: "dossier", Name: "start", Summary: "Start a dossier's session, or restart it when it runs: same conversation, current binary.",
		Params: []spec.Param{idParam("Dossier id, or desk.")},
		Effects: []string{"A stopped session resumes in a new tab; a dossier without session starts one with its open prompt.",
			"A running session is restarted, as with `office restart`."},
		Examples: []string{"office start P-0019", "office start desk"},
		Run: func(ctx *spec.Context) (any, error) {
			return withApp(ctx, true, func(a *app.App) (any, error) {
				d, err := a.LoadAny(ctx.Str("id"))
				if err != nil {
					return nil, err
				}
				how, err := a.Start(d)
				if err != nil {
					return nil, err
				}
				return map[string]any{"id": d.ID, "done": how, "session": d.Run.Session, "tab_id": d.Run.TabID}, nil
			})
		},
	})

	spec.Register(&spec.Action{
		Category: "graph", Name: "escalations", Summary: "List the escalations the desk has not resolved yet.",
		Params:   []spec.Param{{Name: "all", Kind: spec.Bool, Help: "Include resolved escalations."}},
		Effects:  []string{"Read-only."},
		Examples: []string{"office escalations --format text"},
		Run: func(ctx *spec.Context) (any, error) {
			return withApp(ctx, false, func(a *app.App) (any, error) {
				d := a.Desk()
				out := app.OpenEscalations(d)
				if ctx.Bool("all") {
					out = append(out, app.ResolvedEscalations(d)...)
				}
				return out, nil
			})
		},
		Text: func(w io.Writer, r any) {
			for _, e := range r.([]app.Escalation) {
				fmt.Fprintf(w, "%-9s %s · %s\n", e.Status, e.File, e.Text)
			}
		},
	})

	spec.Register(&spec.Action{
		Category: "graph", Name: "resolve", Summary: "Close an escalation: record the desk's decision and tell the dossier it came from.",
		Params: []spec.Param{
			{Name: "escalation", Kind: spec.String, Positional: true, Required: true, Help: "Escalation file (from `office escalations`), or the dossier it came from when it has only one open."},
			{Name: "decision", Kind: spec.String, Required: true, Help: "What the user decided."},
		},
		Effects:  []string{"Moves the escalation to desk/escalations/resolved/ with the decision, logs it, and notifies the dossier."},
		Examples: []string{`office resolve P-0018 --decision "Règle adoptée dans la charte perso."`},
		Run: func(ctx *spec.Context) (any, error) {
			return withApp(ctx, true, func(a *app.App) (any, error) {
				return a.Resolve(ctx.Str("escalation"), ctx.Str("decision"))
			})
		},
	})

	spec.Register(&spec.Action{
		Category: "dossier", Name: "move", Summary: "Move dossiers to another office: number, directory, sources, history and session follow.",
		Discussion: "Each dossier gets a number in the target office (P-0010 becomes U-0037). Links among the moved dossiers stay; " +
			"links to the others go, on both sides. A running session restarts in the target office with its environment and charter. " +
			"The target's connectors take the sources in where they can (a reminder gets the office's tag); the result names the others. " +
			"Only when the user asked: an office is a sphere of their life.",
		Params: []spec.Param{
			{Name: "ids", Kind: spec.StringList, Positional: true, Required: true, Help: "Dossiers to move, e.g. P-0010 P-0011."},
			{Name: "to", Kind: spec.String, Required: true, Help: "Target office: its sphere (pro), its id prefix (U) or its directory."},
		},
		Effects: []string{"Moves each directory into the target office under a new number and rewrites its id and links.",
			"Moves the session's conversation to the new directory's Claude Code project, then restarts a running session there.",
			"Removes the links that stay-behind dossiers held to the moved ones; calls claim on the target's connectors."},
		Destructive: true,
		Examples:    []string{"office move P-0010 P-0011 --to pro", "office move U-0042 --to ~/offices/perso"},
		Run: func(ctx *spec.Context) (any, error) {
			ids := ctx.List("ids")
			return withApp(ctx, true, func(a *app.App) (any, error) {
				target, err := officeNamed(a.S, ctx.Str("to"))
				if err != nil {
					return nil, err
				}
				return a.Move(ids, target)
			})
		},
		Text: func(w io.Writer, r any) {
			res := r.(app.MoveResult)
			for _, m := range res.Moved {
				restarted := ""
				if m.Restarted {
					restarted = " · session restarted"
				}
				fmt.Fprintf(w, "%s → %s · %s%s\n", m.From, m.To, m.Dir, restarted)
			}
			if len(res.Unlinked) > 0 {
				fmt.Fprintf(w, "links removed from %s\n", strings.Join(res.Unlinked, ", "))
			}
			for _, x := range res.Warnings {
				fmt.Fprintf(w, "warning: %s\n", x)
			}
		},
	})

	spec.Register(&spec.Action{
		Category: "dossier", Name: "adopt", Summary: "Make a new dossier of a Claude Code agent you started in a herdr pane: its conversation becomes the dossier's session.",
		Discussion: "The agent keeps running where it is, in its own directory, and receives the dossier's prompts. It gets the " +
			"dossier tools at its first `office restart`, which ends it once its turn is over and resumes its conversation through " +
			"the ACP server. Inside the agent's pane, --pane defaults to $HERDR_PANE_ID: an agent can adopt itself.",
		Params: []spec.Param{
			{Name: "pane", Kind: spec.String, Help: "herdr pane of the agent. Defaults to $HERDR_PANE_ID."},
			{Name: "title", Kind: spec.String, Help: "Title of the dossier. Defaults to the pane's title."},
			{Name: "instruction", Kind: spec.String, Help: "What the dossier is about."},
			{Name: "in", Kind: spec.StringList, Help: "Dossier (alias or id) that includes the new one (repeatable)."},
		},
		Effects: []string{"Creates <office>/NNNN-<slug>/ whose session is the agent's, with source herdr:session/<id>.",
			"Copies the conversation so far into transcript.jsonl; sends no prompt."},
		Examples: []string{`office adopt --pane w5:p3 --title "rstudio-cli : paquet d'aide" --in U-0032 --office ~/offices/pro`,
			`office adopt --title "Relais SMTP" --office ~/offices/pro`},
		Run: func(ctx *spec.Context) (any, error) {
			return withApp(ctx, true, func(a *app.App) (any, error) {
				pane := ctx.Str("pane")
				if pane == "" {
					pane = os.Getenv("HERDR_PANE_ID")
				}
				return a.Adopt(app.AdoptParams{Pane: pane, Title: ctx.Str("title"), Instruction: ctx.Str("instruction"), In: ctx.List("in")})
			})
		},
		Text: func(w io.Writer, r any) {
			o := r.(app.OpenResult)
			fmt.Fprintf(w, "%s %s · %s · session %s\n", o.ID, o.Outcome, o.Dir, o.Session)
		},
	})

	spec.Register(&spec.Action{
		Category: "dossier", Name: "dock", Summary: "Show a dossier's agent in place of a placeholder pane, e.g. the one at the TUI's right.",
		Discussion: "The agent and the placeholder exchange places: the placeholder waits in the agent's own tab, where the agent was. " +
			"`office undock` exchanges them back, and so do a restart and a close, which would otherwise leave a hole. A prompt leaves " +
			"the agent where it is. The geometry of both tabs does not change.",
		Params: []spec.Param{idParam("Dossier id, or desk."),
			{Name: "placeholder", Kind: spec.String, Required: true, Help: "herdr pane id whose place the agent takes."},
			{Name: "no-focus", Kind: spec.Bool, Help: "Leave the focus where it is, e.g. in the TUI."}},
		Effects: []string{"Starts or resumes the session when its pane is gone.",
			"Exchanges the agent's pane with the placeholder through a temporary pane, since herdr swaps panes only within a tab; focuses the agent unless --no-focus."},
		Examples: []string{"office dock U-0033 --placeholder w5:p8"},
		Run: func(ctx *spec.Context) (any, error) {
			return withApp(ctx, true, func(a *app.App) (any, error) {
				d, err := a.LoadAny(ctx.Str("id"))
				if err != nil {
					return nil, err
				}
				if err := a.Dock(d, ctx.Str("placeholder"), !ctx.Bool("no-focus")); err != nil {
					return nil, err
				}
				return map[string]any{"id": d.ID, "pane_id": d.Run.PaneID, "home": d.Run.Home}, nil
			})
		},
	})
	spec.Register(&spec.Action{
		Category: "dossier", Name: "undock", Summary: "Send a docked agent back to its own tab; the placeholder takes its place again.",
		Params: []spec.Param{idParam("Dossier id, or desk."),
			{Name: "placeholder", Kind: spec.String, Help: "Only when the agent holds this placeholder's place; otherwise nothing."}},
		Effects:  []string{"Exchanges the agent and the placeholder again; without a placeholder left, moves the agent to a new tab of its home workspace. Nothing when it is not docked."},
		Examples: []string{"office undock U-0033"},
		Run: func(ctx *spec.Context) (any, error) {
			return withApp(ctx, true, func(a *app.App) (any, error) {
				d, err := a.LoadAny(ctx.Str("id"))
				if err != nil {
					return nil, err
				}
				// A TUI sends home only the agent that holds its own placeholder's place.
				if p := ctx.Str("placeholder"); p != "" && d.Run.Placeholder != p {
					return map[string]any{"id": d.ID, "tab_id": d.Run.TabID, "skipped": "not in place of " + p}, nil
				}
				if err := a.Undock(d); err != nil {
					return nil, err
				}
				return map[string]any{"id": d.ID, "tab_id": d.Run.TabID}, nil
			})
		},
	})

	spec.Register(&spec.Action{
		Category: "dossier", Name: "desk", Summary: "Focus the office's desk: a lasting session that opens dossiers and answers about them.",
		Discussion: "The desk has no state and never closes; ingest resumes it when its tab is gone. Its id is <prefix>-DESK " +
			"(U-DESK), which attach, restart, prompt, show and grep accept. Its charter is <office>/desk/CLAUDE.md.",
		Params: []spec.Param{{Name: "new", Kind: spec.Bool, Help: "Archive the current conversation into desk/transcripts/ and start a fresh one."}},
		Effects: []string{"Creates <office>/desk/ with its CLAUDE.md on first use.",
			"Starts the desk's session without a prompt when it has none, or resumes it in a tab; focuses the tab.",
			"--new: closes the tab and archives the transcript first; grep desk still reads it."},
		Examples: []string{"office desk", "office desk --new", "office grep desk \"Diego\""},
		Run: func(ctx *spec.Context) (any, error) {
			return withApp(ctx, true, func(a *app.App) (any, error) {
				d := a.Desk()
				if ctx.Bool("new") {
					if err := a.NewDeskConversation(d); err != nil {
						return nil, err
					}
				}
				if err := a.Attach(d, true); err != nil {
					return nil, err
				}
				return map[string]any{"id": d.ID, "session": d.Run.Session, "tab_id": d.Run.TabID}, nil
			})
		},
		Text: func(w io.Writer, r any) {
			m := r.(map[string]any)
			fmt.Fprintf(w, "%s · session %s · tab %s\n", m["id"], m["session"], m["tab_id"])
		},
	})

	spec.Register(&spec.Action{
		Category: "dossier", Name: "alias", Summary: "Name a lasting dossier (a recurring meeting): RDIR, then U-RDIR, works wherever an id does.",
		Params: []spec.Param{
			idParam("Dossier id."),
			{Name: "name", Kind: spec.String, Positional: true, Help: "Alias, e.g. RDIR. Omit with --clear."},
			{Name: "clear", Kind: spec.Bool, Help: "Remove the alias."},
		},
		Effects:  []string{"Writes alias in dossier.md and renames the tab; the number stays the reference in links."},
		Examples: []string{"office alias U-0006 RDIR", "office alias U-RDIR --clear"},
		Run: func(ctx *spec.Context) (any, error) {
			return withApp(ctx, true, func(a *app.App) (any, error) {
				d, err := a.Load(ctx.Str("id"))
				if err != nil {
					return nil, err
				}
				name := ctx.Str("name")
				if ctx.Bool("clear") {
					name = ""
				} else if name == "" {
					return nil, spec.UserError("give an alias or --clear. Example: office alias %s RDIR", d.ID)
				}
				if err := a.SetAlias(d, name); err != nil {
					return nil, err
				}
				return map[string]any{"id": d.ID, "alias": d.Alias, "label": d.Label(), "permanent": d.Permanent, "hint": permanentHint(d)}, nil
			})
		},
	})

	spec.Register(&spec.Action{
		Category: "dossier", Name: "retitle", Summary: "Rename a dossier: its title, its tab and its directory, which follows the title.",
		Params: []spec.Param{
			idParam("Dossier id."),
			{Name: "title", Kind: spec.String, Required: true, Help: "New title."},
		},
		Effects: []string{"Writes the title in dossier.md, and in its manual source when that one carried the old title.",
			"Renames the directory to <number>-<slug of the title>; a running session closes, its conversation follows, and it resumes.",
			"Rewrites the links block of the dossiers that point to it."},
		Examples: []string{`office retitle P-0019 --title "Développement d'office"`},
		Run: func(ctx *spec.Context) (any, error) {
			return withApp(ctx, true, func(a *app.App) (any, error) { return a.Retitle(ctx.Str("id"), ctx.Str("title")) })
		},
	})

	spec.Register(&spec.Action{
		Category: "internal", Name: "closetab", Summary: "Wait, then close a dossier's tab; the session stays resumable.",
		Params: []spec.Param{
			idParam("Dossier id."),
			{Name: "delay", Kind: spec.String, Default: "0s", Help: "Wait before closing, e.g. 8s."},
		},
		Examples: []string{"office closetab D-0042 --delay 8s"},
		Run: func(ctx *spec.Context) (any, error) {
			if d, err := time.ParseDuration(ctx.Str("delay")); err == nil {
				time.Sleep(d)
			}
			return withApp(ctx, true, func(a *app.App) (any, error) {
				d, err := a.LoadAny(ctx.Str("id"))
				if err != nil {
					return nil, err
				}
				return nil, a.CloseTab(d)
			})
		},
	})
}

// installSkill writes the embedded skill where an agent harness finds it.
func installSkill(target, dir string) (string, error) {
	if dir == "" {
		switch strings.ToLower(target) {
		case "claude", "claude-code", "claudecode":
			dir = office.ExpandHome("~/.claude/skills/office")
		default:
			return "", spec.UserError("--for %q is not supported yet; use --for claude, or --dir <skills directory>/dossier", target)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	p := filepath.Join(dir, "SKILL.md")
	return p, os.WriteFile(p, []byte(skillText), 0o644)
}

// officeNamed finds an office by sphere (pro), id prefix (U) or directory,
// among the offices beside the current one.
func officeNamed(current *office.Office, name string) (*office.Office, error) {
	if p := office.ExpandHome(name); strings.ContainsRune(p, os.PathSeparator) {
		return office.Open(p)
	}
	var known []string
	for _, dir := range office.Discover(filepath.Dir(current.Root)) {
		s, err := office.Open(dir)
		if err != nil {
			continue
		}
		if strings.EqualFold(s.Config.Office.Sphere, name) || strings.EqualFold(s.Prefix(), name) {
			return s, nil
		}
		known = append(known, s.Config.Office.Sphere)
	}
	return nil, spec.UserError("no office %q beside %s; known: %s", name, current.Root, strings.Join(known, ", "))
}

// permanentHint proposes to make a named dossier permanent.
func permanentHint(d *dossier.Dossier) string {
	if d.Alias == "" || d.Permanent {
		return ""
	}
	return fmt.Sprintf("an alias often names a lasting dossier: `office permanent %s` keeps it from being closed", d.ID)
}
