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
	"time"

	"github.com/aclemen1/office-cli/internal/office"
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
	Office *office.Office
	Source office.SourceConfig
}

func (r Runner) run(verb string, input any, out any) error {
	return r.runFor(verb, input, out, r.Source.TimeoutDuration())
}

func (r Runner) runFor(verb string, input any, out any, timeout time.Duration) error {
	src := r.Source
	if s := serverFor(r.Office, src.Name); s != nil {
		if raw, err := s.call(verb, input, timeout); err != errServerGone {
			if err != nil {
				return fmt.Errorf("source %q: %s failed: %w", src.Name, verb, err)
			}
			if err := json.Unmarshal(raw, out); err != nil {
				return fmt.Errorf("source %q: %s returned invalid JSON: %v", src.Name, verb, err)
			}
			return nil
		}
	}
	argv, err := r.argv()
	if err != nil {
		return err
	}
	argv = append(argv, verb)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = r.Office.Root
	env := []string{}
	for _, k := range []string{"HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "PATH", "TMPDIR"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	for k, v := range src.Env {
		env = append(env, k+"="+office.ExpandHome(v))
	}
	cmd.Env = env
	in, _ := json.Marshal(input)
	cmd.Stdin = bytes.NewReader(in)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("source %q: %s timed out after %s", src.Name, verb, timeout)
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

// Claim tells the connector that the item now belongs to its office, after a
// dossier moved there: it makes the item pass its own filter (a reminder gets
// the office's tag). Optional verb: call it only when describe lists "claim".
func (r Runner) Claim(sourceRef string) (string, error) {
	var out struct {
		OK     bool   `json:"ok"`
		Detail string `json:"detail"`
	}
	err := r.run("claim", map[string]any{"config": r.Source.Config, "source_ref": sourceRef}, &out)
	return out.Detail, err
}

// SendResult names the threads the message opened, one per recipient.
type SendResult struct {
	OK         bool     `json:"ok"`
	ThreadRefs []string `json:"thread_refs"`
	Sent       int      `json:"sent"`
}

// Send writes a text and files to the user through the source. The
// connector chooses the recipient itself (the user, never anyone else).
// Optional verb: call it only when describe lists "send".
func (r Runner) Send(text string, files []File, replace []string) (SendResult, error) {
	var out SendResult
	if files == nil {
		files = []File{}
	}
	if replace == nil {
		replace = []string{}
	}
	err := r.run("send", map[string]any{"config": r.Source.Config, "text": text, "files": files, "replace": replace}, &out)
	return out, err
}

// Wait blocks until the source holds something new after cursor, or until
// seconds pass, without taking anything: the next poll does. Optional verb:
// call it only when describe lists "wait".
func (r Runner) Wait(cursor string, seconds int) (bool, error) {
	var out struct {
		Ready bool `json:"ready"`
	}
	err := r.runFor("wait", map[string]any{"config": r.Source.Config, "cursor": cursor, "timeout": seconds},
		&out, time.Duration(seconds+30)*time.Second)
	return out.Ready, err
}

// Progress drives a placeholder that tells the user their item is being
// handled: op "start" answers the item at ref and returns the placeholder,
// "update" shows text in it, "end" closes it. Optional verb: call it only when
// describe lists "progress".
func (r Runner) Progress(op, ref, placeholder, text string) (string, error) {
	var out struct {
		Placeholder string `json:"placeholder"`
	}
	err := r.run("progress", map[string]any{"config": r.Source.Config, "op": op, "ref": ref, "placeholder": placeholder, "text": text}, &out)
	return out.Placeholder, err
}

// argv is the connector's command, ~ expanded, paths relative to the office resolved.
func (r Runner) argv() ([]string, error) {
	src := r.Source
	if len(src.Command) == 0 {
		return nil, fmt.Errorf("source %q has no command in %s", src.Name, r.Office.Meta("config.toml"))
	}
	argv := append([]string{}, src.Command...)
	for i := range argv {
		argv[i] = office.ExpandHome(argv[i])
	}
	for i, a := range argv {
		if i > 0 && !filepath.IsAbs(a) && !strings.HasPrefix(a, "-") {
			if p := filepath.Join(r.Office.Root, a); fileExists(p) {
				argv[i] = p
			}
		}
	}
	if p := filepath.Join(r.Office.Root, argv[0]); !filepath.IsAbs(argv[0]) && strings.Contains(argv[0], "/") && fileExists(p) {
		argv[0] = p
	}
	return argv, nil
}

func (r Runner) env() []string {
	env := []string{}
	for _, k := range []string{"HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "PATH", "TMPDIR"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	for k, v := range r.Source.Env {
		env = append(env, k+"="+office.ExpandHome(v))
	}
	return env
}
