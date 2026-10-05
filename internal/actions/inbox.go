package actions

import (
	"fmt"
	"io"
	"strings"

	"github.com/aclemen1/office-cli/internal/app"
	"github.com/aclemen1/office-cli/internal/spec"
)

func init() {
	spec.Register(&spec.Action{
		Category: "office", Name: "inbox", Summary: "The office's inbox: list its entries, file one, attach it to a dossier, release it to the Trash.",
		Discussion: "Each top-level entry of <office>/inbox, a file or a whole folder, goes to the desk once it stops changing. " +
			"The desk files it ([inbox] file, e.g. into the memory), attaches it to a dossier, then releases it: office checks every file " +
			"([inbox] check) and moves the entry to the Trash. Nothing moves while a step is missing.",
		Params: []spec.Param{
			{Name: "verb", Kind: spec.String, Positional: true, Default: "ls", Enum: []string{"ls", "show", "file", "attach", "release"}, Help: "ls, show, file, attach or release."},
			{Name: "name", Kind: spec.String, Positional: true, Help: "The entry's name in the inbox (or inbox:item/<name>)."},
			{Name: "note", Kind: spec.String, Help: "file: context for the filing, e.g. what the document is for."},
			{Name: "dossier", Kind: spec.String, Help: "attach: the dossier it belongs to."},
			{Name: "dry-run", Kind: spec.Bool, Help: "release: check only."},
			{Name: "no-dossier", Kind: spec.String, Help: "release: the entry belongs to no dossier, and why (the user's choice); it must still be filed and pass the check."},
		},
		Effects: []string{"ls and show read only.", "file runs [inbox] file; attach records the dossier; release checks then moves the entry to the Trash.",
			"Each step writes a line in the desk's history."},
		Examples: []string{"office inbox", `office inbox file Rio --note "voyage à Rio"`, "office inbox attach Rio --dossier P-0026", "office inbox release Rio --dry-run"},
		Run: func(ctx *spec.Context) (any, error) {
			verb, name := ctx.Str("verb"), ctx.Str("name")
			if verb != "ls" && name == "" {
				return nil, spec.UserError("inbox %s needs the entry's name, e.g. `office inbox %s Rio`", verb, verb)
			}
			return withApp(ctx, verb == "attach" || verb == "release", func(a *app.App) (any, error) {
				switch verb {
				case "file":
					return a.InboxFile(name, ctx.Str("note"))
				case "attach":
					d, err := a.Load(ctx.Str("dossier"))
					if err != nil {
						return nil, err
					}
					return a.InboxAttach(name, d)
				case "release":
					detail, err := a.InboxRelease(name, ctx.Bool("dry-run"), ctx.Str("no-dossier"))
					return map[string]any{"name": name, "detail": detail}, err
				}
				items, err := a.Inbox()
				if err != nil || verb == "ls" {
					return items, err
				}
				for _, it := range items {
					if it.Name == strings.TrimPrefix(name, "inbox:item/") {
						return it, nil
					}
				}
				return nil, spec.NotFound("no %q in the inbox", name)
			})
		},
		Text: func(w io.Writer, r any) {
			line := func(it app.InboxItem) {
				extra := ""
				if len(it.Attached) > 0 {
					extra += " · " + strings.Join(it.Attached, ", ")
				}
				if it.Note != "" {
					extra += " · " + it.Note
				}
				fmt.Fprintf(w, "%-9s %-6s %3d file(s)  %s%s\n", it.State, it.Kind, it.Files, it.Name, extra)
			}
			switch v := r.(type) {
			case []app.InboxItem:
				for _, it := range v {
					line(it)
				}
			case app.InboxItem:
				line(v)
			default:
				fmt.Fprintf(w, "%v\n", v)
			}
		},
	})
}
