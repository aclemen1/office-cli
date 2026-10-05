package actions

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/aclemen1/office-cli/internal/app"
	"github.com/aclemen1/office-cli/internal/office"
	"github.com/aclemen1/office-cli/internal/spec"
)

func init() {
	spec.Register(&spec.Action{
		Category: "dossier", Name: "routine", Summary: "Schedule a dossier's routines through the routine CLI: add, ls, show, edit, rm, run.",
		Discussion: "A routine prompts the dossier's session (runner session), an ephemeral agent opened in the dossier's directory (agent) or runs a command there (command), " +
			"at the times of its RRULE. It runs while the dossier is in one of its states (open and waiting by default): office pauses it in the others, " +
			"removes it when the dossier is merged or deleted, and moves it with the dossier. The desk's routines always run. " +
			"Add, edit and remove a routine only after the user agreed.",
		Params: []spec.Param{
			{Name: "verb", Kind: spec.String, Positional: true, Default: "ls", Enum: []string{"add", "ls", "show", "edit", "rm", "run"}, Help: "add, ls, show, edit, rm or run."},
			idParam("Dossier id, or desk. Defaults to DOSSIER_ID."),
			{Name: "name", Kind: spec.String, Positional: true, Help: "Routine name, e.g. brief."},
			{Name: "rrule", Kind: spec.StringList, Help: "iCalendar RRULE without DTSTART, e.g. FREQ=DAILY;BYHOUR=7;BYMINUTE=0 (repeatable)."},
			{Name: "runner", Kind: spec.String, Enum: []string{"session", "agent", "command"}, Help: "Who runs it: session (the dossier's own, default), agent (ephemeral) or command."},
			{Name: "command", Kind: spec.String, Help: "Shell command of a command routine, run in the dossier's directory."},
			{Name: "prompt", Kind: spec.String, Help: "Prompt of a session or agent routine; stdin of a command routine."},
			{Name: "prompt-file", Kind: spec.String, Help: "Read the prompt from a file."},
			{Name: "step", Kind: spec.StringList, Help: "A step, name:runner[:command] (repeatable, in order): the routine runs them one after the other; each step's text is the body's \"## <name>\" section, where {{steps.<earlier>.output}} gives an earlier step's output."},
			{Name: "states", Kind: spec.StringList, Help: "Dossier states in which it runs: open, waiting, done (repeatable). Default: open and waiting."},
			{Name: "dtstart", Kind: spec.String, Help: "Local start of the series, e.g. 2026-10-05T07:00."},
			{Name: "timeout", Kind: spec.String, Help: "Longest run, e.g. 20m."},
			{Name: "all", Kind: spec.Bool, Help: "ls: every routine of the office."},
		},
		Effects: []string{"add, edit, rm and run call `routine` and write a line in the dossier's history.",
			"ls and show read only."},
		Examples: []string{
			`office routine add P-0014 brief --rrule "FREQ=DAILY;BYHOUR=7;BYMINUTE=0" --prompt "Prépare le briefing du jour."`,
			`office routine add desk brief --rrule "FREQ=DAILY;BYHOUR=7" --runner agent --prompt-file brief.md`,
			"office routine ls --all", "office routine show P-0014 brief", "office routine rm P-0014 brief"},
		Run: func(ctx *spec.Context) (any, error) {
			verb, name := ctx.Str("verb"), ctx.Str("name")
			return withApp(ctx, verb == "add" || verb == "edit" || verb == "rm", func(a *app.App) (any, error) {
				if verb == "ls" && ctx.Bool("all") {
					return a.Routines(nil)
				}
				d, err := a.LoadAny(ctx.Str("id"))
				if err != nil {
					return nil, err
				}
				if verb == "ls" {
					return a.Routines(d)
				}
				if name == "" {
					return nil, spec.UserError("routine %s needs a routine name, e.g. `office routine %s %s brief`", verb, verb, d.ID)
				}
				prompt, hasPrompt, err := promptOf(ctx)
				if err != nil {
					return nil, err
				}
				var steps []app.RoutineStep
				for _, s := range ctx.List("step") {
					st, err := app.ParseStep(s)
					if err != nil {
						return nil, err
					}
					steps = append(steps, st)
				}
				switch verb {
				case "add":
					return a.AddRoutine(d, app.RoutineParams{Name: name, RRules: ctx.List("rrule"), Runner: ctx.Str("runner"),
						Command: ctx.Str("command"), Prompt: prompt, States: ctx.List("states"), Dtstart: ctx.Str("dtstart"), Timeout: ctx.Str("timeout"), Steps: steps})
				case "show":
					return a.ShowRoutine(d, name)
				case "edit":
					e := app.RoutineEdit{RRules: ctx.List("rrule"), States: ctx.List("states"), Steps: steps,
						Runner: opt(ctx, "runner"), Command: opt(ctx, "command"), Dtstart: opt(ctx, "dtstart"), Timeout: opt(ctx, "timeout")}
					if hasPrompt {
						e.Prompt = &prompt
					}
					return a.EditRoutine(d, name, e)
				case "rm":
					return map[string]any{"removed": name}, a.RemoveRoutine(d, name)
				case "run":
					return a.RunRoutine(d, name)
				}
				return nil, spec.UserError("unknown verb %q", verb)
			})
		},
		Text: func(w io.Writer, r any) {
			show := func(x app.Routine) {
				state := "active"
				if !x.Active {
					state = "paused"
				}
				next := "-"
				if x.Next != nil {
					next = *x.Next
				}
				fmt.Fprintf(w, "%-24s %-7s %-7s %-14s next %s · %s\n", x.Name, state, x.Runner, strings.Join(x.States, ","), next, strings.Join(x.RRules, " + "))
			}
			switch v := r.(type) {
			case []app.Routine:
				for _, x := range v {
					show(x)
				}
			case app.Routine:
				show(v)
				if v.Body != "" {
					fmt.Fprintf(w, "\n%s\n", strings.TrimSpace(v.Body))
				}
			case json.RawMessage:
				var run struct {
					Status   string `json:"status"`
					ExitCode *int   `json:"exitCode"`
					Started  string `json:"started"`
					Ended    string `json:"ended"`
					Log      string `json:"log"`
				}
				if json.Unmarshal(v, &run) != nil || run.Status == "" {
					fmt.Fprintf(w, "%s\n", v)
					return
				}
				line := run.Status
				if run.ExitCode != nil {
					line += fmt.Sprintf(" · exit %d", *run.ExitCode)
				}
				fmt.Fprintf(w, "%s · %s → %s\nlog %s\n", line, run.Started, run.Ended, run.Log)
			case map[string]any:
				fmt.Fprintf(w, "removed %v\n", v["removed"])
			default:
				fmt.Fprintf(w, "%v\n", v)
			}
		},
	})
}

func opt(ctx *spec.Context, name string) *string {
	if _, ok := ctx.Args[name]; !ok {
		return nil
	}
	v := ctx.Str(name)
	return &v
}

func promptOf(ctx *spec.Context) (string, bool, error) {
	if f := ctx.Str("prompt-file"); f != "" {
		b, err := os.ReadFile(office.ExpandHome(f))
		if err != nil {
			return "", false, spec.UserError("cannot read --prompt-file %s: %v", f, err)
		}
		return string(b), true, nil
	}
	if _, ok := ctx.Args["prompt"]; ok {
		return ctx.Str("prompt"), true, nil
	}
	return "", false, nil
}
