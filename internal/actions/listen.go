package actions

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/aclemen1/office-cli/internal/app"
	"github.com/aclemen1/office-cli/internal/office"
	"github.com/aclemen1/office-cli/internal/spec"
)

func init() {
	spec.Register(&spec.Action{
		Category: "office", Name: "listen", Summary: "Ingest a source the moment it holds something new, for every source whose connector declares wait.",
		Discussion: "A lasting process, e.g. under launchd with KeepAlive. It covers every office under the root. Each connector's wait blocks until " +
			"the source has news (a chat bot uses long polling), then the source is ingested at once. Sources without wait keep to the periodic ingest.",
		Params: []spec.Param{
			{Name: "root", Kind: spec.String, Positional: true, Help: "Directory holding the offices. Defaults to the parent of the resolved office."},
		},
		Effects:  []string{"Runs until stopped; writes one line per event to stdout."},
		Examples: []string{"office listen", "office listen ~/offices"},
		Run: func(ctx *spec.Context) (any, error) {
			root, err := rootOf(ctx)
			if err != nil {
				return nil, err
			}
			stop := make(chan struct{})
			sig := make(chan os.Signal, 1)
			signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
			// A connector may block in wait for a minute: stop at once instead.
			go func() { <-sig; close(stop); os.Exit(0) }()
			var apps []*app.App
			for _, dir := range office.Discover(root) {
				s, err := office.Open(dir)
				if err != nil {
					fmt.Fprintln(os.Stderr, err)
					continue
				}
				apps = append(apps, &app.App{S: s})
			}
			return nil, app.Listen(apps, os.Stdout, stop)
		},
	})
}
