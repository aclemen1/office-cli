package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/aclemen1/dossier-cli/internal/actions"
	"github.com/aclemen1/dossier-cli/internal/app"
	"github.com/aclemen1/dossier-cli/internal/spec"
)

const rootHelp = `dossier — one agent session per affair.

Usage: dossier <action> [arguments] [--store <path>] [--format json|text]

MESSAGE FOR LLM / AI AGENTS: run ` + "`dossier schema`" + ` to list categories, then
` + "`dossier schema <category> <action>`" + ` for the exact parameters, examples and
effects of one action. Copy an example from there.

Actions by category:
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(argv []string) int {
	var storeFlag, format string
	help := false
	var rest []string
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "--store" && i+1 < len(argv):
			storeFlag = argv[i+1]
			i++
		case strings.HasPrefix(a, "--store="):
			storeFlag = strings.TrimPrefix(a, "--store=")
		case a == "--format" && i+1 < len(argv):
			format = argv[i+1]
			i++
		case strings.HasPrefix(a, "--format="):
			format = strings.TrimPrefix(a, "--format=")
		case a == "--json":
			format = "json"
		case a == "--help" || a == "-h":
			help = true
		case a == "--version":
			rest = append(rest, "version")
		default:
			rest = append(rest, a)
		}
	}
	if format != "" && format != "json" && format != "text" {
		return spec.Emit(nil, "json", nil, spec.UserError("--format takes json or text, got %q. Example: dossier ls --format text", format))
	}
	if len(rest) == 0 {
		printRoot()
		return 0
	}
	act := spec.FindVerb(rest[0])
	if act == nil {
		return spec.Emit(nil, format, nil, spec.UserError("unknown action %q. Run `dossier schema` to list actions, for example `dossier ls`", rest[0]))
	}
	if help {
		spec.TextSchema(os.Stdout, spec.Leaf{Action: act, Usage: spec.Usage(act)})
		return 0
	}
	args, err := spec.Parse(act, rest[1:])
	if err != nil {
		return spec.Emit(act, format, nil, err)
	}
	// Everything runs in herdr, except what must never fail or helps to diagnose:
	// meta actions, the MCP server and hooks (category internal), doctor.
	if act.Category != "meta" && act.Category != "internal" && act.Name != "doctor" {
		if err := app.HerdrReady(); err != nil {
			return spec.Emit(act, format, nil, err)
		}
	}
	result, err := act.Run(&spec.Context{Args: args, Store: storeFlag, Format: format, Stdin: os.Stdin})
	if act.Name == "tui" && err == nil {
		return 0
	}
	if act.Name == "mcp" {
		if err != nil {
			fmt.Fprintln(os.Stderr, "dossier mcp:", err)
			return 1
		}
		return 0
	}
	if act.Name == "hook" {
		// A hook never fails the agent that runs it.
		return 0
	}
	return spec.Emit(act, format, result, err)
}

func printRoot() {
	fmt.Print(rootHelp)
	for _, c := range spec.Categories() {
		var names []string
		for _, a := range spec.ActionsIn(c) {
			names = append(names, a.Name)
		}
		fmt.Printf("  %-9s %s\n", c, strings.Join(names, ", "))
	}
	fmt.Printf("\nVersion %s\n", actions.Version)
}
