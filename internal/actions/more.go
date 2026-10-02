package actions

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/aclemen1/dossier-cli/internal/app"
	"github.com/aclemen1/dossier-cli/internal/spec"
	"github.com/aclemen1/dossier-cli/internal/store"
)

func init() {
	spec.Register(&spec.Action{
		Category: "dossier", Name: "grep", Summary: "Search a dossier's full conversation, compactions included.",
		Params: []spec.Param{
			idParam("Dossier id. Defaults to DOSSIER_ID."),
			{Name: "pattern", Kind: spec.String, Positional: true, Required: true, Help: "Regular expression, case-insensitive."},
			{Name: "limit", Kind: spec.String, Default: "50", Help: "Maximum number of hits."},
		},
		Examples: []string{`dossier grep D-0042 "date de passage"`, `dossier grep D-0042 "devis|offre" --limit 10`},
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
		Examples:    []string{"dossier merge D-0051 --into D-0042"},
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
		Discussion: "Any two dossiers of the store, e.g. a meeting tells an item what was decided, or an item asks its meeting to add a point.",
		Params: []spec.Param{
			{Name: "from", Kind: spec.String, Positional: true, Required: true, Help: "Dossier the event comes from."},
			{Name: "to", Kind: spec.String, Positional: true, Required: true, Help: "Dossier that must know."},
			{Name: "text", Kind: spec.String, Required: true, Help: "What <to> must know."},
		},
		Effects:  []string{"Logs the event in <to> and prompts its session; a dossier without session only gets the log line."},
		Examples: []string{`dossier notify D-0007 D-0042 --text "Décidé en séance du 08.10 : on attend l'offre."`},
		Run: func(ctx *spec.Context) (any, error) {
			return withApp(ctx, true, func(a *app.App) (any, error) {
				from, err := a.LoadAny(ctx.Str("from"))
				if err != nil {
					return nil, err
				}
				to, err := a.Load(ctx.Str("to"))
				if err != nil {
					return nil, err
				}
				if from.ID == to.ID {
					return nil, spec.UserError("%s cannot notify itself", from.ID)
				}
				text := fmt.Sprintf("From dossier %s (%s): %s", from.ID, from.Title, ctx.Str("text"))
				_ = to.Log("from %s: %s", from.ID, ctx.Str("text"))
				if app.IsDesk(from) {
					_ = from.Log("notified %s · %s", to.ID, ctx.Str("text"))
				}
				if _, err := a.Wake(to, "notified by "+from.ID); err != nil {
					return nil, err
				}
				if to.Run.Session == "" {
					return map[string]any{"to": to.ID, "prompted": false}, to.Save()
				}
				return map[string]any{"to": to.ID, "prompted": true}, a.Prompt(to, text)
			})
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
		Examples: []string{"dossier track U-0002 gmail:thread/1a0d7b2f4c0fff93"},
		Run: func(ctx *spec.Context) (any, error) {
			return withApp(ctx, true, func(a *app.App) (any, error) { return a.Track(ctx.Str("id"), ctx.Str("ref")) })
		},
	})

	spec.Register(&spec.Action{
		Category: "dossier", Name: "restart", Summary: "Restart a dossier's session: same conversation, fresh process, tools and environment.",
		Params:   []spec.Param{idParam("Dossier id.")},
		Effects:  []string{"Closes the tab (the agent exits), then resumes the session in a new tab with --resume."},
		Examples: []string{"dossier restart U-0002"},
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
		Category: "dossier", Name: "adopt", Summary: "Make a new dossier of a Claude Code agent you started in a herdr pane: its conversation becomes the dossier's session.",
		Discussion: "The agent keeps running where it is, in its own directory, and receives the dossier's prompts. It gets the " +
			"dossier tools at its first `dossier restart`, which ends it once its turn is over and resumes its conversation through " +
			"the ACP server. Inside the agent's pane, --pane defaults to $HERDR_PANE_ID: an agent can adopt itself.",
		Params: []spec.Param{
			{Name: "pane", Kind: spec.String, Help: "herdr pane of the agent. Defaults to $HERDR_PANE_ID."},
			{Name: "title", Kind: spec.String, Help: "Title of the dossier. Defaults to the pane's title."},
			{Name: "instruction", Kind: spec.String, Help: "What the dossier is about."},
			{Name: "in", Kind: spec.StringList, Help: "Dossier (alias or id) that includes the new one (repeatable)."},
		},
		Effects: []string{"Creates <store>/NNNN-<slug>/ whose session is the agent's, with source herdr:session/<id>.",
			"Copies the conversation so far into transcript.jsonl; sends no prompt."},
		Examples: []string{`dossier adopt --pane w5:p3 --title "rstudio-cli : paquet d'aide" --in U-0032 --store ~/dossiers/pro`,
			`dossier adopt --title "Relais SMTP" --store ~/dossiers/pro`},
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
		Discussion: "The placeholder waits in a tab of its own; `dossier undock` gives it its place back and moves the agent to its " +
			"home workspace, and so does any session start, prompt, restart or close, since the ACP server treats the pane's " +
			"current tab as the session's own. The geometry of the placeholder's tab does not change.",
		Params: []spec.Param{idParam("Dossier id, or desk."),
			{Name: "placeholder", Kind: spec.String, Required: true, Help: "herdr pane id whose place the agent takes."},
			{Name: "no-focus", Kind: spec.Bool, Help: "Leave the focus where it is, e.g. in the TUI."}},
		Effects: []string{"Starts or resumes the session when its pane is gone.",
			"Moves the agent's pane into the placeholder's tab, swaps them, and moves the placeholder to a tab of its own; focuses the agent."},
		Examples: []string{"dossier dock U-0033 --placeholder w5:p8"},
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
		Category: "dossier", Name: "undock", Summary: "Move a docked agent pane back to a tab of its own.",
		Params: []spec.Param{idParam("Dossier id, or desk."),
			{Name: "placeholder", Kind: spec.String, Help: "Only when the agent holds this placeholder's place; otherwise nothing."}},
		Effects:  []string{"Moves the pane into a new tab of its home workspace, labelled like the dossier; nothing when it is not docked."},
		Examples: []string{"dossier undock U-0033"},
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
		Category: "dossier", Name: "desk", Summary: "Focus the store's desk: a lasting session that opens dossiers and answers about them.",
		Discussion: "The desk has no state and never closes; ingest resumes it when its tab is gone. Its id is <prefix>-DESK " +
			"(U-DESK), which attach, restart, prompt, show and grep accept. Its charter is <store>/desk/CLAUDE.md.",
		Params: []spec.Param{{Name: "new", Kind: spec.Bool, Help: "Archive the current conversation into desk/transcripts/ and start a fresh one."}},
		Effects: []string{"Creates <store>/desk/ with its CLAUDE.md on first use.",
			"Starts the desk's session without a prompt when it has none, or resumes it in a tab; focuses the tab.",
			"--new: closes the tab and archives the transcript first; grep desk still reads it."},
		Examples: []string{"dossier desk", "dossier desk --new", "dossier grep desk \"Diego\""},
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
		Examples: []string{"dossier alias U-0006 RDIR", "dossier alias U-RDIR --clear"},
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
					return nil, spec.UserError("give an alias or --clear. Example: dossier alias %s RDIR", d.ID)
				}
				if err := a.SetAlias(d, name); err != nil {
					return nil, err
				}
				return map[string]any{"id": d.ID, "alias": d.Alias, "label": d.Label()}, nil
			})
		},
	})

	spec.Register(&spec.Action{
		Category: "internal", Name: "closetab", Summary: "Wait, then close a dossier's tab; the session stays resumable.",
		Params: []spec.Param{
			idParam("Dossier id."),
			{Name: "delay", Kind: spec.String, Default: "0s", Help: "Wait before closing, e.g. 8s."},
		},
		Examples: []string{"dossier closetab D-0042 --delay 8s"},
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
			dir = store.ExpandHome("~/.claude/skills/dossier")
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
