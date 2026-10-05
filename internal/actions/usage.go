package actions

import (
	"time"

	"github.com/aclemen1/office-cli/internal/app"
	"github.com/aclemen1/office-cli/internal/spec"
)

func init() {
	spec.Register(&spec.Action{
		Category: "office", Name: "usage", Summary: "Sum up how office was used over a period, to find improvements: tool calls and errors, CLI fallbacks, refusals, corrections, merges, moves.",
		Discussion: "Counts and office's own messages, quoted values removed; never what third parties wrote. " +
			"--facts-only also drops what agents or the user wrote (escalations, corrections), e.g. to share another sphere's usage.",
		Params: []spec.Param{
			{Name: "since", Kind: spec.String, Default: "24h", Help: "Period: a duration (24h, 7d)."},
			{Name: "facts-only", Kind: spec.Bool, Help: "Counts and office messages only."},
			{Name: "of", Kind: spec.String, Help: "Another office, by its sphere (pro): its usage is then facts only."},
		},
		Effects:  []string{"Read-only."},
		Examples: []string{"office usage", "office usage --since 7d", "office usage --office pro --facts-only"},
		Run: func(ctx *spec.Context) (any, error) {
			d, err := parsePeriod(ctx.Str("since"))
			if err != nil {
				return nil, err
			}
			return withApp(ctx, false, func(a *app.App) (any, error) {
				facts := ctx.Bool("facts-only")
				if of := ctx.Str("of"); of != "" {
					b, err := app.New(of)
					if err != nil {
						return nil, err
					}
					if b.S.Root != a.S.Root {
						a, facts = b, true
					}
				}
				return a.Usage(time.Now().Add(-d), facts), nil
			})
		},
	})
}

func parsePeriod(s string) (time.Duration, error) {
	if n := len(s); n > 1 && s[n-1] == 'd' {
		if d, err := time.ParseDuration(s[:n-1] + "h"); err == nil {
			return d * 24, nil
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, spec.UserError("period %q: use a duration such as 24h or 7d", s)
	}
	return d, nil
}
