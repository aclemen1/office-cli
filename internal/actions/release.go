package actions

import (
	"github.com/aclemen1/office-cli/internal/app"
	"github.com/aclemen1/office-cli/internal/spec"
)

func init() {
	spec.Register(&spec.Action{
		Category: "graph", Name: "release", Summary: "Let a source remove an item once it is kept elsewhere, e.g. an inbox file filed in the memory.",
		Discussion: "The connector checks first (the inbox: every file in artefact, held by mnemo) and keeps the item when a check fails.",
		Params: []spec.Param{
			{Name: "ref", Kind: spec.String, Positional: true, Required: true, Help: "Source reference of the item, e.g. inbox:item/Rio."},
			{Name: "dry-run", Kind: spec.Bool, Help: "Check only; remove nothing."},
		},
		Effects:  []string{"Runs the connector's release; the inbox moves the item to the Trash. A line in the desk's history."},
		Examples: []string{"office release inbox:item/Rio --dry-run", "office release inbox:item/Rio"},
		Run: func(ctx *spec.Context) (any, error) {
			return withApp(ctx, !ctx.Bool("dry-run"), func(a *app.App) (any, error) {
				detail, err := a.Release(ctx.Str("ref"), ctx.Bool("dry-run"))
				return map[string]any{"ref": ctx.Str("ref"), "detail": detail, "dry_run": ctx.Bool("dry-run")}, err
			})
		},
	})
}
