package actions

import (
	"os"

	"github.com/aclemen1/office-cli/internal/app"
	"github.com/aclemen1/office-cli/internal/office"
	"github.com/aclemen1/office-cli/internal/spec"
)

func init() {
	spec.Register(&spec.Action{
		Category: "graph", Name: "tell", Summary: "Send a text to the user through a source (Telegram): the user is always the recipient.",
		Discussion: "The connector picks the recipient: the user, never anyone else. The message's thread joins the sending dossier (or the desk): " +
			"a reply in it comes back there as an event. A long text is split under the source's limit.",
		Params: []spec.Param{
			idParam("Sending dossier, or desk. Defaults to DOSSIER_ID."),
			{Name: "source", Kind: spec.String, Help: "Source that sends, e.g. telegram. Default: the only one that can."},
			{Name: "text", Kind: spec.String, Help: "Text, in Markdown."},
			{Name: "file", Kind: spec.String, Help: "Read the text from a file."},
			{Name: "attach", Kind: spec.StringList, Help: "File sent along (repeatable)."},
		},
		Effects:  []string{"Sends the message, attaches its thread to the sender and writes a line in its history."},
		Examples: []string{`office tell desk --source telegram --file brief.md`, `office tell P-0014 --text "La routine est prête."`},
		Run: func(ctx *spec.Context) (any, error) {
			text := ctx.Str("text")
			if f := ctx.Str("file"); f != "" {
				b, err := os.ReadFile(office.ExpandHome(f))
				if err != nil {
					return nil, spec.UserError("cannot read --file %s: %v", f, err)
				}
				text = string(b)
			}
			return withApp(ctx, true, func(a *app.App) (any, error) {
				d, err := a.LoadAny(ctx.Str("id"))
				if err != nil {
					return nil, err
				}
				return a.TellUser(d, ctx.Str("source"), text, ctx.List("attach"))
			})
		},
	})
}
