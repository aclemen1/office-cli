package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/aclemen1/office-cli/internal/dossier"
	"github.com/aclemen1/office-cli/internal/office"
	"github.com/aclemen1/office-cli/internal/spec"
)

// A dossier's routines live in the routine CLI under
// office/<office>-<dossier id in lower case>-<name>, owned by
// office:<office>/<id>. Their meta holds the runner and the dossier states in
// which they run; office pauses, resumes, removes and moves them as the
// dossier changes.

const (
	RunnerSession = "session"
	RunnerAgent   = "agent"
	RunnerCommand = "command"
)

var defaultRoutineStates = []string{dossier.Open, dossier.Waiting}

var routineNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// errNoRoutine says the routine CLI is not installed: state changes then go on
// without routines.
var errNoRoutine = errors.New("the routine command is not installed")

// routineCall runs the routine CLI with --json, stdin as the body. Tests replace it.
var routineCall = func(stdin string, args ...string) ([]byte, error) {
	argv := append(office.RoutineCommand(), append(args, "--json")...)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = strings.NewReader(stdin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if errors.Is(err, exec.ErrNotFound) {
		return nil, errNoRoutine
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		var env struct {
			Error any `json:"error"`
		}
		if json.Unmarshal(out, &env) == nil && env.Error != nil {
			msg = fmt.Sprint(env.Error)
		}
		if msg == "" {
			msg = strings.TrimSpace(string(out))
		}
		return nil, fmt.Errorf("routine %s: %s", args[0], msg)
	}
	return out, nil
}

type RoutineACP struct {
	Command string          `json:"command"`
	Args    []string        `json:"args,omitempty"`
	Meta    json.RawMessage `json:"meta,omitempty"`
}

// Routine is a routine as the routine CLI reports it, with the dossier's view
// of it: its short name, runner and states.
type Routine struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Runner   string          `json:"runner"`
	States   []string        `json:"states,omitempty"`
	Active   bool            `json:"active"`
	Running  bool            `json:"running"`
	RRules   []string        `json:"rrules"`
	TZ       string          `json:"tz,omitempty"`
	Dtstart  string          `json:"dtstart,omitempty"`
	Timeout  string          `json:"timeout,omitempty"`
	Next     *string         `json:"next"`
	Owner    string          `json:"owner,omitempty"`
	Cwd      string          `json:"cwd,omitempty"`
	Run      string          `json:"run,omitempty"`
	ACP      *RoutineACP     `json:"acp,omitempty"`
	Meta     map[string]any  `json:"meta,omitempty"`
	LastRun  json.RawMessage `json:"lastRun,omitempty"`
	Upcoming []string        `json:"upcoming,omitempty"`
	Body     string          `json:"body,omitempty"`
}

func (r *Routine) fill(prefix string) {
	r.Name = strings.TrimPrefix(r.ID, prefix)
	r.Runner, _ = r.Meta["runner"].(string)
	r.States = nil
	if l, ok := r.Meta["states"].([]any); ok {
		for _, s := range l {
			if s, ok := s.(string); ok {
				r.States = append(r.States, s)
			}
		}
	}
	if len(r.States) == 0 && r.Meta["states"] == nil {
		r.States = append([]string{}, defaultRoutineStates...)
	}
}

// OfficeName names the office in routine ids and owners: its sphere.
func (a *App) OfficeName() string {
	if s := strings.TrimSpace(a.S.Config.Office.Sphere); s != "" {
		return strings.ToLower(s)
	}
	return strings.ToLower(filepath.Base(a.S.Root))
}

func (a *App) routineOwner(id string) string { return "office:" + a.OfficeName() + "/" + id }

func (a *App) routinePrefix(id string) string {
	return "office/" + a.OfficeName() + "-" + strings.ToLower(id) + "-"
}

type RoutineParams struct {
	Name    string
	RRules  []string
	Runner  string
	Command string // command runner: the shell command
	Prompt  string // session and agent runners: the prompt; command runner: its stdin
	States  []string
	Dtstart string
	Timeout string
	Steps   []RoutineStep // several runners in order; the body holds a "## <name>" section per step
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// runnerArgs turns a runner into routine add options.
func (a *App) runnerArgs(d *dossier.Dossier, runner, command string) ([]string, error) {
	switch runner {
	case RunnerSession:
		exe, err := os.Executable()
		if err != nil {
			return nil, err
		}
		id := d.ID
		if IsDesk(d) {
			id = "desk"
		}
		run := fmt.Sprintf("%s prompt %s --office %s --file /dev/stdin", shellQuote(exe), id, shellQuote(a.S.Root))
		return []string{"--run", run, "--cwd", d.Dir}, nil
	case RunnerAgent:
		acp := a.S.Config.ACP.Command
		if len(acp) == 0 {
			return nil, spec.UserError("this office has no [acp] command for an agent routine")
		}
		args := []string{"--acp-command", office.ExpandHome(acp[0])}
		for _, x := range acp[1:] {
			args = append(args, "--acp-arg="+office.ExpandHome(x))
		}
		return append(args, "--acp-arg=--trust-folders", "--cwd", d.Dir), nil
	case RunnerCommand:
		if strings.TrimSpace(command) == "" {
			return nil, spec.UserError("a command routine needs --command")
		}
		return []string{"--run", command, "--cwd", d.Dir}, nil
	}
	return nil, spec.UserError("runner %q: use session, agent or command", runner)
}

func checkStates(states []string) error {
	for _, s := range states {
		switch s {
		case dossier.Open, dossier.Waiting, dossier.Done:
		default:
			return spec.UserError("state %q: a routine runs in open, waiting or done", s)
		}
	}
	return nil
}

func routineMeta(runner string, states []string) string {
	b, _ := json.Marshal(map[string]any{"runner": runner, "states": states})
	return string(b)
}

func parseRoutine(out []byte, prefix string) (Routine, error) {
	var r Routine
	if err := json.Unmarshal(out, &r); err != nil {
		return r, fmt.Errorf("routine: unreadable answer: %v", err)
	}
	r.fill(prefix)
	return r, nil
}

// runsIn says whether a routine of d runs in d's current state. A desk has
// no state: its routines always run.
func runsIn(d *dossier.Dossier, states []string) bool {
	if IsDesk(d) {
		return true
	}
	for _, s := range states {
		if s == d.State {
			return true
		}
	}
	return false
}

// AddRoutine creates a routine of d. It starts paused when d's state is not
// one of its states.
func (a *App) AddRoutine(d *dossier.Dossier, p RoutineParams) (Routine, error) {
	if !routineNameRe.MatchString(p.Name) {
		return Routine{}, spec.UserError("routine name %q: lower case letters, digits, '.', '_' and '-', e.g. brief", p.Name)
	}
	if len(p.RRules) == 0 {
		return Routine{}, spec.UserError("a routine needs --rrule, e.g. FREQ=DAILY;BYHOUR=7;BYMINUTE=0")
	}
	if len(p.Steps) > 0 {
		p.Runner = RunnerSteps
	}
	if p.Runner == "" {
		p.Runner = RunnerSession
	}
	if p.Runner != RunnerCommand && p.Runner != RunnerSteps && strings.TrimSpace(p.Prompt) == "" {
		return Routine{}, spec.UserError("a %s routine needs --prompt or --prompt-file", p.Runner)
	}
	if len(p.States) == 0 {
		p.States = defaultRoutineStates
	}
	if err := checkStates(p.States); err != nil {
		return Routine{}, err
	}
	meta := routineMeta(p.Runner, p.States)
	var runArgs []string
	if p.Runner == RunnerSteps {
		steps, err := a.stepsArg(d, p.Steps)
		if err != nil {
			return Routine{}, err
		}
		runArgs, meta = []string{"--steps", steps}, stepsMeta(p.Steps, p.States)
	} else {
		var err error
		if runArgs, err = a.runnerArgs(d, p.Runner, p.Command); err != nil {
			return Routine{}, err
		}
	}
	prefix := a.routinePrefix(d.ID)
	args := []string{"add", prefix + p.Name}
	for _, r := range p.RRules {
		args = append(args, "--rrule", r)
	}
	args = append(args, runArgs...)
	args = append(args, "--owner", a.routineOwner(d.ID), "--meta", meta, "--body-file", "-")
	if p.Timeout != "" {
		args = append(args, "--timeout", p.Timeout)
	}
	if p.Dtstart != "" {
		args = append(args, "--dtstart", p.Dtstart)
	}
	if !runsIn(d, p.States) {
		args = append(args, "--paused")
	}
	out, err := routineCall(p.Prompt, args...)
	if err != nil {
		return Routine{}, err
	}
	r, err := parseRoutine(out, prefix)
	if err != nil {
		return r, err
	}
	_ = d.Log("routine %s added · %s · %s · runs while %s", p.Name, strings.Join(p.RRules, " + "), p.Runner, strings.Join(p.States, ", "))
	return r, nil
}

// Routines lists the routines of d, or of every dossier of the office when d is nil.
func (a *App) Routines(d *dossier.Dossier) ([]Routine, error) {
	owner, prefix := "office:"+a.OfficeName()+"/*", ""
	if d != nil {
		owner, prefix = a.routineOwner(d.ID), a.routinePrefix(d.ID)
	}
	return a.routinesOf(owner, prefix)
}

func (a *App) routinesOf(owner, prefix string) ([]Routine, error) {
	out, err := routineCall("", "ls", "--owner", owner)
	if err != nil {
		return nil, err
	}
	var env struct {
		Routines []Routine `json:"routines"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		return nil, fmt.Errorf("routine ls: unreadable answer: %v", err)
	}
	for i := range env.Routines {
		p := prefix
		if p == "" {
			if _, id, ok := strings.Cut(env.Routines[i].Owner, "/"); ok {
				p = a.routinePrefix(id)
			}
		}
		env.Routines[i].fill(p)
	}
	if env.Routines == nil {
		env.Routines = []Routine{}
	}
	return env.Routines, nil
}

func (a *App) routineID(d *dossier.Dossier, name string) (string, error) {
	if !routineNameRe.MatchString(name) {
		return "", spec.UserError("routine name %q: list them with `office routine ls %s`", name, d.ID)
	}
	return a.routinePrefix(d.ID) + name, nil
}

// ShowRoutine shows one routine of d with its last runs.
func (a *App) ShowRoutine(d *dossier.Dossier, name string) (Routine, error) {
	id, err := a.routineID(d, name)
	if err != nil {
		return Routine{}, err
	}
	out, err := routineCall("", "show", id)
	if err != nil {
		return Routine{}, err
	}
	return parseRoutine(out, a.routinePrefix(d.ID))
}

// RoutineEdit holds the fields to change; nil leaves a field as it is.
type RoutineEdit struct {
	RRules  []string
	Steps   []RoutineStep
	Runner  *string
	Command *string
	Prompt  *string
	States  []string
	Dtstart *string
	Timeout *string
}

// EditRoutine changes a routine of d, then pauses or resumes it for d's state.
func (a *App) EditRoutine(d *dossier.Dossier, name string, e RoutineEdit) (Routine, error) {
	cur, err := a.ShowRoutine(d, name)
	if err != nil {
		return Routine{}, err
	}
	runner, states := cur.Runner, cur.States
	if e.Runner != nil {
		runner = *e.Runner
	}
	if e.States != nil {
		if err := checkStates(e.States); err != nil {
			return Routine{}, err
		}
		states = e.States
	}
	args := []string{"edit", cur.ID}
	var changed []string
	for _, r := range e.RRules {
		args = append(args, "--rrule", r)
	}
	if len(e.RRules) > 0 {
		changed = append(changed, "rrule")
	}
	if e.Runner != nil || e.Command != nil {
		command := cur.Run
		if e.Command != nil {
			command = *e.Command
		}
		ra, err := a.runnerArgs(d, runner, command)
		if err != nil {
			return Routine{}, err
		}
		if runner == RunnerAgent {
			args = append(args, "--run", "")
		} else {
			args = append(args, "--acp-command", "")
		}
		args = append(args, ra...)
		changed = append(changed, "runner")
	}
	if e.Steps != nil {
		steps, err := a.stepsArg(d, e.Steps)
		if err != nil {
			return Routine{}, err
		}
		args = append(args, "--steps", steps)
		runner = RunnerSteps
		changed = append(changed, "steps")
	}
	if e.Runner != nil || e.States != nil || e.Steps != nil {
		meta := routineMeta(runner, states)
		if runner == RunnerSteps {
			steps := e.Steps
			if steps == nil {
				steps = metaSteps(cur.Meta)
			}
			meta = stepsMeta(steps, states)
		}
		args = append(args, "--meta", meta)
		changed = append(changed, "states")
	}
	if e.Dtstart != nil {
		args = append(args, "--dtstart", *e.Dtstart)
		changed = append(changed, "dtstart")
	}
	if e.Timeout != nil {
		args = append(args, "--timeout", *e.Timeout)
		changed = append(changed, "timeout")
	}
	body := ""
	if e.Prompt != nil {
		args = append(args, "--body-file", "-")
		body = *e.Prompt
		changed = append(changed, "prompt")
	}
	if len(changed) == 0 {
		return cur, spec.UserError("nothing to change in routine %s", name)
	}
	out, err := routineCall(body, args...)
	if err != nil {
		return Routine{}, err
	}
	r, err := parseRoutine(out, a.routinePrefix(d.ID))
	if err != nil {
		return r, err
	}
	_ = d.Log("routine %s edited · %s", name, strings.Join(changed, ", "))
	if want := runsIn(d, r.States); want != r.Active {
		verb := "pause"
		if want {
			verb = "resume"
		}
		if out, err := routineCall("", verb, r.ID); err == nil {
			r, _ = parseRoutine(out, a.routinePrefix(d.ID))
		}
	}
	return r, nil
}

// RemoveRoutine deletes a routine of d.
func (a *App) RemoveRoutine(d *dossier.Dossier, name string) error {
	id, err := a.routineID(d, name)
	if err != nil {
		return err
	}
	if _, err := routineCall("", "rm", id); err != nil {
		return err
	}
	_ = d.Log("routine %s removed", name)
	return nil
}

// RunRoutine runs a routine of d now, outside its schedule.
func (a *App) RunRoutine(d *dossier.Dossier, name string) (json.RawMessage, error) {
	id, err := a.routineID(d, name)
	if err != nil {
		return nil, err
	}
	out, err := routineCall("", "run", id)
	if err != nil {
		return nil, err
	}
	_ = d.Log("routine %s run by hand", name)
	return json.RawMessage(out), nil
}

// syncRoutines pauses or resumes d's routines for its state, and removes them
// once d is merged. It never fails a state change: problems go to the history.
func (a *App) syncRoutines(d *dossier.Dossier) {
	rs, err := a.Routines(d)
	if errors.Is(err, errNoRoutine) || (err == nil && len(rs) == 0) {
		return
	}
	if err != nil {
		_ = d.Log("routines not synced: %v", err)
		return
	}
	for _, r := range rs {
		verb := ""
		switch {
		case d.State == dossier.Merged:
			verb = "rm"
		case runsIn(d, r.States) && !r.Active:
			verb = "resume"
		case !runsIn(d, r.States) && r.Active:
			verb = "pause"
		default:
			continue
		}
		if _, err := routineCall("", verb, r.ID); err != nil {
			_ = d.Log("routine %s: %v", r.Name, err)
			continue
		}
		_ = d.Log("routine %s %s (%s)", r.Name, map[string]string{"rm": "removed", "resume": "resumed", "pause": "paused"}[verb], d.State)
	}
}

// dropRoutines removes every routine of d, before d is deleted.
func (a *App) dropRoutines(d *dossier.Dossier) []string {
	rs, err := a.Routines(d)
	if err != nil {
		if errors.Is(err, errNoRoutine) {
			return nil
		}
		return []string{fmt.Sprintf("routines of %s not removed: %v", d.ID, err)}
	}
	var warn []string
	for _, r := range rs {
		if _, err := routineCall("", "rm", r.ID); err != nil {
			warn = append(warn, fmt.Sprintf("routine %s: %v", r.ID, err))
		}
	}
	return warn
}

// retargetRoutines points d's routines at its directory again, after a rename.
func (a *App) retargetRoutines(d *dossier.Dossier) []string {
	rs, err := a.Routines(d)
	if err != nil {
		if errors.Is(err, errNoRoutine) {
			return nil
		}
		return []string{fmt.Sprintf("routines not updated: %v", err)}
	}
	var warn []string
	for _, r := range rs {
		args := []string{"edit", r.ID, "--cwd", d.Dir}
		if r.Runner == RunnerSteps {
			steps, err := a.stepsArg(d, metaSteps(r.Meta))
			if err != nil {
				warn = append(warn, fmt.Sprintf("routine %s: %v", r.Name, err))
				continue
			}
			args = []string{"edit", r.ID, "--steps", steps}
		}
		if _, err := routineCall("", args...); err != nil {
			warn = append(warn, fmt.Sprintf("routine %s: %v", r.Name, err))
		}
	}
	return warn
}

// moveRoutines recreates the routines of oldID (in a's office) for m in to's
// office, under m's id and owner, then removes the old ones.
func (a *App) moveRoutines(oldID string, to *App, m *dossier.Dossier) []string {
	rs, err := a.routinesOf(a.routineOwner(oldID), a.routinePrefix(oldID))
	if err != nil {
		if errors.Is(err, errNoRoutine) {
			return nil
		}
		return []string{fmt.Sprintf("%s: routines not moved: %v", m.ID, err)}
	}
	var warn []string
	for _, old := range rs {
		full, err := routineCall("", "show", old.ID)
		if err != nil {
			warn = append(warn, fmt.Sprintf("routine %s: %v", old.ID, err))
			continue
		}
		r, _ := parseRoutine(full, a.routinePrefix(oldID))
		runner := r.Runner
		if runner == "" {
			runner = RunnerCommand
		}
		meta := routineMeta(runner, r.States)
		var ra []string
		if runner == RunnerSteps {
			steps, err := to.stepsArg(m, metaSteps(r.Meta))
			if err != nil {
				warn = append(warn, fmt.Sprintf("routine %s: %v", old.ID, err))
				continue
			}
			ra, meta = []string{"--steps", steps}, stepsMeta(metaSteps(r.Meta), r.States)
		} else if ra, err = to.runnerArgs(m, runner, r.Run); err != nil {
			warn = append(warn, fmt.Sprintf("routine %s: %v", old.ID, err))
			continue
		}
		args := []string{"add", to.routinePrefix(m.ID) + r.Name}
		for _, x := range r.RRules {
			args = append(args, "--rrule", x)
		}
		args = append(args, ra...)
		args = append(args, "--owner", to.routineOwner(m.ID), "--meta", meta, "--body-file", "-")
		if r.Timeout != "" {
			args = append(args, "--timeout", r.Timeout)
		}
		if r.Dtstart != "" {
			args = append(args, "--dtstart", r.Dtstart)
		}
		if r.TZ != "" {
			args = append(args, "--tz", r.TZ)
		}
		if !r.Active || !runsIn(m, r.States) {
			args = append(args, "--paused")
		}
		if _, err := routineCall(r.Body, args...); err != nil {
			warn = append(warn, fmt.Sprintf("routine %s not moved: %v", old.ID, err))
			continue
		}
		if _, err := routineCall("", "rm", old.ID); err != nil {
			warn = append(warn, fmt.Sprintf("routine %s copied but not removed: %v", old.ID, err))
		}
		_ = m.Log("routine %s moved from %s", r.Name, old.ID)
	}
	return warn
}

// RunnerSteps marks a routine whose steps run one after the other; meta.steps
// keeps each step's runner, so that office can rebuild them when the dossier
// moves or is renamed.
const RunnerSteps = "steps"

// RoutineStep is one step of a routine: its name, runner and, for a command,
// the command. Its text is the body's "## <name>" section.
type RoutineStep struct {
	Name    string `json:"name"`
	Runner  string `json:"runner"`
	Command string `json:"command,omitempty"`
}

var stepNameRe = regexp.MustCompile(`^[a-z0-9_-]+$`)

// ParseStep reads name:runner[:command].
func ParseStep(s string) (RoutineStep, error) {
	parts := strings.SplitN(s, ":", 3)
	st := RoutineStep{Name: parts[0]}
	if len(parts) > 1 {
		st.Runner = parts[1]
	}
	if len(parts) > 2 {
		st.Command = parts[2]
	}
	if !stepNameRe.MatchString(st.Name) {
		return st, spec.UserError("step %q: name:runner[:command], the name in lower case letters, digits, _ and -", s)
	}
	if st.Runner == "" {
		st.Runner = RunnerSession
	}
	return st, nil
}

// stepObject is a step as routine expects it, run in the dossier's directory.
func (a *App) stepObject(d *dossier.Dossier, s RoutineStep) (map[string]any, error) {
	args, err := a.runnerArgs(d, s.Runner, s.Command)
	if err != nil {
		return nil, fmt.Errorf("step %s: %w", s.Name, err)
	}
	o := map[string]any{"name": s.Name}
	var acp map[string]any
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--run":
			o["run"] = args[i+1]
			i++
		case args[i] == "--cwd":
			o["cwd"] = args[i+1]
			i++
		case args[i] == "--acp-command":
			acp = map[string]any{"command": args[i+1], "args": []string{}}
			i++
		case strings.HasPrefix(args[i], "--acp-arg="):
			acp["args"] = append(acp["args"].([]string), strings.TrimPrefix(args[i], "--acp-arg="))
		}
	}
	if acp != nil {
		o["acp"] = acp
	}
	return o, nil
}

func (a *App) stepsArg(d *dossier.Dossier, steps []RoutineStep) (string, error) {
	var out []map[string]any
	for _, s := range steps {
		o, err := a.stepObject(d, s)
		if err != nil {
			return "", err
		}
		out = append(out, o)
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

func stepsMeta(steps []RoutineStep, states []string) string {
	b, _ := json.Marshal(map[string]any{"runner": RunnerSteps, "states": states, "steps": steps})
	return string(b)
}

func metaSteps(m map[string]any) []RoutineStep {
	b, _ := json.Marshal(m["steps"])
	var out []RoutineStep
	_ = json.Unmarshal(b, &out)
	return out
}
