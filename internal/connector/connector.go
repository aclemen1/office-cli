// Package connector runs source connectors: external commands that speak
// protocol 1 (describe, poll, transition) as JSON over stdin and stdout.
package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/aclemen1/dossier-cli/internal/store"
)

const Protocol = 1

type File struct {
	Name    string `json:"name"`
	Content string `json:"content,omitempty"`
	Path    string `json:"path,omitempty"`
}

type Signal struct {
	SourceRef   string         `json:"source_ref"`
	ThreadRef   string         `json:"thread_ref"`
	Title       string         `json:"title"`
	Instruction string         `json:"instruction"`
	Summary     map[string]any `json:"summary"`
	Files       []File         `json:"files"`
	URL         string         `json:"url"`
	At          string         `json:"at"`
	In          []string       `json:"in,omitempty"` // dossiers (aliases or ids) that include it
}

type Event struct {
	ThreadRef string         `json:"thread_ref"`
	Kind      string         `json:"kind"`
	Summary   map[string]any `json:"summary"`
	Files     []File         `json:"files"`
	At        string         `json:"at"`
	In        []string       `json:"in,omitempty"`
}

type PollResult struct {
	Signals []Signal `json:"signals"`
	Events  []Event  `json:"events"`
	Cursor  string   `json:"cursor"`
}

type Description struct {
	Name     string   `json:"name"`
	Protocol int      `json:"protocol"`
	Verbs    []string `json:"verbs"`
}

type Runner struct {
	Store  *store.Store
	Source store.SourceConfig
}

func (r Runner) run(verb string, input any, out any) error {
	src := r.Source
	if len(src.Command) == 0 {
		return fmt.Errorf("source %q has no command in %s", src.Name, r.Store.Meta("config.toml"))
	}
	argv := append([]string{}, src.Command...)
	for i := range argv {
		argv[i] = store.ExpandHome(argv[i])
	}
	for i, a := range argv {
		if i > 0 && !filepath.IsAbs(a) && !strings.HasPrefix(a, "-") {
			if p := filepath.Join(r.Store.Root, a); fileExists(p) {
				argv[i] = p
			}
		}
	}
	if p := filepath.Join(r.Store.Root, argv[0]); !filepath.IsAbs(argv[0]) && strings.Contains(argv[0], "/") && fileExists(p) {
		argv[0] = p
	}
	argv = append(argv, verb)
	ctx, cancel := context.WithTimeout(context.Background(), src.TimeoutDuration())
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = r.Store.Root
	env := []string{}
	for _, k := range []string{"HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "PATH", "TMPDIR"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	for k, v := range src.Env {
		env = append(env, k+"="+store.ExpandHome(v))
	}
	cmd.Env = env
	in, _ := json.Marshal(input)
	cmd.Stdin = bytes.NewReader(in)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("source %q: %s timed out after %s", src.Name, verb, src.TimeoutDuration())
	}
	body := bytes.TrimSpace(stdout.Bytes())
	if len(body) == 0 {
		return fmt.Errorf("source %q: %s returned nothing (exit %v); stderr: %s", src.Name, verb, runErr, tail(stderr.String()))
	}
	var probe struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &probe) == nil && probe.Error != nil {
		return fmt.Errorf("source %q: %s failed: %s", src.Name, verb, probe.Error.Message)
	}
	if runErr != nil {
		return fmt.Errorf("source %q: %s exited with %v; stderr: %s", src.Name, verb, runErr, tail(stderr.String()))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("source %q: %s returned invalid JSON: %v", src.Name, verb, err)
	}
	return nil
}

func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 600 {
		s = "…" + s[len(s)-600:]
	}
	return s
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func (r Runner) Describe() (Description, error) {
	var d Description
	err := r.run("describe", map[string]any{}, &d)
	return d, err
}

// PollOptions: DryRun changes nothing at the source; Now takes what the
// source holds at once, without the settle delay a connector may apply.
type PollOptions struct {
	DryRun, Now bool
}

func (r Runner) Poll(cursor string, watch []string, opt PollOptions) (PollResult, error) {
	var p PollResult
	if watch == nil {
		watch = []string{}
	}
	err := r.run("poll", map[string]any{"config": r.Source.Config, "cursor": cursor, "watch": watch, "dry_run": opt.DryRun, "now": opt.Now}, &p)
	return p, err
}

// Dossier names the dossier a call is about, for connectors that tell the
// user (a chat bot answering "→ D-0042").
type Dossier struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	WaitingOn string `json:"waiting_on,omitempty"`
}

func (r Runner) Transition(sourceRef, threadRef, from, to, note string, d Dossier) error {
	var out struct {
		OK bool `json:"ok"`
	}
	return r.run("transition", map[string]any{
		"config": r.Source.Config, "source_ref": sourceRef, "thread_ref": threadRef,
		"from": from, "to": to, "note": note, "dossier": d,
	}, &out)
}

// Opened tells the connector which dossier a signal opened or reached
// (outcome: created, reopened, routed, existing). Optional verb: call it only
// when describe lists "opened".
func (r Runner) Opened(sourceRef, threadRef, outcome string, d Dossier) error {
	var out struct {
		OK bool `json:"ok"`
	}
	return r.run("opened", map[string]any{
		"config": r.Source.Config, "source_ref": sourceRef, "thread_ref": threadRef,
		"outcome": outcome, "dossier": d,
	}, &out)
}

// Claim tells the connector that the item now belongs to its store, after a
// dossier moved there: it makes the item pass its own filter (a reminder gets
// the store's tag). Optional verb: call it only when describe lists "claim".
func (r Runner) Claim(sourceRef string) (string, error) {
	var out struct {
		OK     bool   `json:"ok"`
		Detail string `json:"detail"`
	}
	err := r.run("claim", map[string]any{"config": r.Source.Config, "source_ref": sourceRef}, &out)
	return out.Detail, err
}
