package actions

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/aclemen1/office-cli/internal/spec"
)

// tool projects an action onto an MCP tool. Its dossier parameters default
// to the session's own dossier; any dossier of the office can be named instead.
type tool struct {
	name, action, description string
	self                      []string          // params that default to DOSSIER_ID
	rename                    map[string]string // action param → tool param
	hide                      []string
	dossierOnly               bool           // not served to the desk's session
	deskOnly                  bool           // served only to the desk's session
	deskDescription           string         // replaces description on the desk
	endsSelf                  bool           // ends the calling session when it names its own dossier
	fixed                     map[string]any // action params the tool sets itself, not shown
}

var tools = []tool{
	{name: "ls", action: "ls", description: "List the office's dossiers with their state and what their agent is doing; status todo lists what needs the user."},
	{name: "offices", action: "offices", hide: []string{"root"}, description: "List the offices beside this one: sphere, id prefix, charter, open and waiting dossiers."},
	{name: "show", action: "show", self: []string{"id"}, description: "Show a dossier: state, sources, files, links, history, and the body of its fiche (instruction, notes, sections such as « À ne pas oublier »)."},
	{name: "search", action: "search", description: "Search every dossier of the office, open or closed."},
	{name: "grep", action: "grep", self: []string{"id"}, rename: map[string]string{"id": "dossier"},
		description: "Search the full conversation of a dossier, including the dossiers merged into it."},
	{name: "tree", action: "tree", self: []string{"id"}, description: "Walk a dossier's links: what it includes, what blocks it."},
	{name: "wait", action: "wait", self: []string{"id"}, description: "Mark a dossier as waiting on someone outside; again on a waiting dossier, it corrects whom it waits on and until when."},
	{name: "resume", action: "resume", self: []string{"id"}, hide: []string{"prompt"}, description: "Bring a waiting dossier back to open."},
	{name: "park", action: "park", self: []string{"id"}, description: "Mark a dossier as needing no action from the user for now, e.g. an item to raise at the next meeting. Anything new on it clears the mark."},
	{name: "unpark", action: "unpark", self: []string{"id"}, description: "Mark a dossier as needing action from the user again."},
	{name: "permanent", action: "permanent", self: []string{"id"}, description: "Mark a lasting dossier (a channel, a recurring meeting), or with clear unmark it, when the user asks: it is not closed, and waits without a chase by default. Once a point is handled, park it."},
	{name: "model", action: "model", self: []string{"id"}, description: "Show the model of a dossier's session, or, when the user asks, set its own (e.g. claude-opus-5-5 for development, claude-sonnet-5-5 for follow-up) or clear it back to the office's default. The session takes it at its next start."},
	{name: "star", action: "star", self: []string{"id"}, description: "Star a dossier the user wants at hand: first in its office, always shown. Only when the user asks."},
	{name: "unstar", action: "unstar", self: []string{"id"}, description: "Remove a dossier's star, when the user asks."},
	{name: "close", action: "close", self: []string{"id"}, description: "Close a dossier once the user says it is settled."},
	{name: "delete", action: "delete", self: []string{"id"}, description: "Delete a dossier that should not exist (a test, a mistake), after the user asked: its signal is withdrawn and its directory removed. A settled affair gets close instead."},
	{name: "open", action: "open", self: []string{"in"}, hide: []string{"source", "thread", "url"},
		description:     "Open a new dossier. By default this dossier includes it (an item of this meeting, a side affair); pass in = [] for a dossier on its own, or other dossiers that include it.",
		deskDescription: "Open a new dossier, on its own by default; pass in with the dossiers that include it (a meeting, a lasting dossier)."},
	{name: "retitle", action: "retitle", endsSelf: true, self: []string{"id"}, description: "Rename a dossier, when the user asks: title, tab, and directory (its session restarts on the same conversation)."},
	{name: "alias", action: "alias", self: []string{"id"}, description: "Give a dossier an alias (a lasting dossier, a recurring meeting), replace it, or remove it with clear, when the user asks."},
	{name: "link", action: "link", self: []string{"from"}, description: "Link a dossier to another: includes (part of it) or depends_on (waits for)."},
	{name: "unlink", action: "unlink", self: []string{"from"}, description: "Remove links from a dossier to another."},
	{name: "track", action: "track", self: []string{"id"},
		description: "Attach a thread to a dossier as a source, e.g. the thread of a draft you just wrote: its replies come back there, and its star follows the dossier's state."},
	{name: "merge", action: "merge", self: []string{"from"}, description: "Merge a dossier into another one, after the user agreed."},
	{name: "move", action: "move", description: "Move dossiers to another office (its sphere, e.g. pro): they get a number there, links among them stay. Only when the user asked."},
	{name: "notify", action: "notify", self: []string{"from"}, description: "Tell another dossier something: a decision, new information, a request. Its session gets it as a prompt."},
	{name: "start", action: "start", endsSelf: true, description: "Start a dossier's session when it is stopped, or restart it when it runs (same conversation, current binary). Name the dossier, or desk."},
	{name: "skills", action: "skills", deskOnly: true, description: "List the skills every session of the office gets, or add or remove one when the user asked. Sessions see the change at their next start."},
	{name: "routines", action: "routine", self: []string{"id"}, fixed: map[string]any{"verb": "ls"}, hide: []string{"name", "rrule", "runner", "command", "prompt", "prompt-file", "states", "dtstart", "timeout", "step"},
		description: "List a dossier's routines (scheduled prompts or commands), or with all every routine of the office: name, active or paused, runner, states, next run."},
	{name: "routine_show", action: "routine", self: []string{"id"}, fixed: map[string]any{"verb": "show"}, hide: []string{"rrule", "runner", "command", "prompt", "prompt-file", "states", "dtstart", "timeout", "all", "step"},
		description: "Show one routine of a dossier: schedule, runner, prompt, upcoming and last runs."},
	{name: "routine_add", action: "routine", self: []string{"id"}, fixed: map[string]any{"verb": "add"}, hide: []string{"all", "prompt-file"},
		description: "Add a routine to a dossier (or to desk), only after the user agreed to it: at the times of its RRULE, it prompts the dossier's session (runner session, default), an ephemeral agent in the dossier's directory (agent), or runs a command there (command). It runs while the dossier is in one of its states (open and waiting by default). A routine sends nothing without review, except to the user himself."},
	{name: "routine_edit", action: "routine", self: []string{"id"}, fixed: map[string]any{"verb": "edit"}, hide: []string{"all", "prompt-file"},
		description: "Change a routine of a dossier, after the user agreed: only the fields given change; rrule replaces the whole schedule."},
	{name: "routine_remove", action: "routine", self: []string{"id"}, fixed: map[string]any{"verb": "rm"}, hide: []string{"rrule", "runner", "command", "prompt", "prompt-file", "states", "dtstart", "timeout", "all", "step"},
		description: "Remove a routine of a dossier, when the user asks."},
	{name: "routine_run", action: "routine", self: []string{"id"}, fixed: map[string]any{"verb": "run"}, hide: []string{"rrule", "runner", "command", "prompt", "prompt-file", "states", "dtstart", "timeout", "all", "step"},
		description: "Run a routine of a dossier now, outside its schedule. A session routine prompts the session; when it is this session's own dossier, the prompt arrives after this turn."},
	{name: "usage", action: "usage", description: "Sum up how office was used in this office over a period (tool calls and errors, CLI fallbacks, refusals, corrections, merges, moves), to propose improvements. Counts and office messages only, never third-party text. With of = another office (pro), facts only."},
	{name: "inbox", action: "inbox", fixed: map[string]any{"verb": "ls"}, hide: []string{"name", "note", "dossier", "dry-run"}, deskOnly: true,
		description: "List the office's inbox: each entry (a file or a folder Alain dropped) with its state: settling, sent (with you), filed, attached."},
	{name: "inbox_file", action: "inbox", fixed: map[string]any{"verb": "file"}, hide: []string{"dossier", "dry-run"}, deskOnly: true,
		description: "File an inbox entry (into the memory: the office's [inbox] file command), with a note of context. First step of an entry."},
	{name: "inbox_attach", action: "inbox", fixed: map[string]any{"verb": "attach"}, hide: []string{"note", "dry-run"}, deskOnly: true,
		description: "Record the dossier an inbox entry belongs to, after you notified it or opened it."},
	{name: "inbox_release", action: "inbox", fixed: map[string]any{"verb": "release"}, hide: []string{"note", "dossier"}, deskOnly: true,
		description: "Last step: once filed and attached, office checks every file is kept elsewhere and moves the entry to the Trash. Nothing moves while a step is missing; say so to the user."},
	{name: "escalations", action: "escalations", deskOnly: true, description: "List the escalations not resolved yet: pending (not yet shown to you) and delivered."},
	{name: "resolve", action: "resolve", deskOnly: true, description: "Resolve an escalation once the user decided: the decision is recorded and the dossier it came from is told."},
	{name: "tell", action: "tell", self: []string{"id"},
		description: "Send a text (Markdown, files with attach) to the user through a source such as telegram. The user is always the recipient: nobody else can be chosen. A reply in that thread comes back to this dossier (or to the desk) as an event."},
	{name: "escalate", action: "escalate", self: []string{"id"}, dossierOnly: true,
		description: "Escalate to the office's desk what goes beyond this dossier: a rule to adopt, a skill to change, a request for the user. The desk gets it as a prompt once its session is idle."},
}

// toolsFor lists the tools served to a session.
func toolsFor(desk bool) []tool {
	var out []tool
	for _, t := range tools {
		if (desk && t.dossierOnly) || (!desk && t.deskOnly) {
			continue
		}
		if desk && t.deskDescription != "" {
			t.description = t.deskDescription
		}
		out = append(out, t)
	}
	return out
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// selfFor lists the params that default to the session. On the desk, only
// notify keeps its sender: every other tool names its dossier.
func (t tool) selfFor(desk bool) []string {
	if !desk {
		return t.self
	}
	if t.name == "notify" {
		return t.self
	}
	return nil
}

func (t tool) schema(desk bool) map[string]any {
	selfs := t.selfFor(desk)
	a := spec.FindVerb(t.action)
	props := map[string]any{}
	var required []string
	for _, p := range a.Params {
		if _, ok := t.fixed[p.Name]; ok || contains(t.hide, p.Name) {
			continue
		}
		name := p.Name
		if r, ok := t.rename[p.Name]; ok {
			name = r
		}
		help := p.Help
		if contains(selfs, p.Name) {
			help = strings.TrimSuffix(strings.TrimSuffix(help, " Defaults to DOSSIER_ID."), ".") + ". Defaults to this session's dossier."
		}
		prop := map[string]any{"description": help}
		switch p.Kind {
		case spec.Bool:
			prop["type"] = "boolean"
		case spec.StringList:
			prop["type"] = "array"
			prop["items"] = map[string]any{"type": "string"}
		default:
			prop["type"] = "string"
		}
		if len(p.Enum) > 0 {
			prop["enum"] = p.Enum
		}
		if p.Default != "" {
			prop["default"] = p.Default
		}
		props[name] = prop
		if (p.Required && !contains(selfs, p.Name)) || (desk && contains(t.self, p.Name) && !contains(selfs, p.Name) && p.Name != "in") {
			required = append(required, name)
		}
	}
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

// call maps tool arguments onto the action's argv, so that parsing, defaults
// and error messages are the CLI's own.
func (t tool) call(self string, desk bool, in map[string]any) (any, error) {
	a := spec.FindVerb(t.action)
	args := map[string]any{}
	for k, v := range in {
		for from, to := range t.rename {
			if k == to {
				k = from
			}
		}
		args[k] = v
	}
	for _, p := range t.selfFor(desk) {
		if v, ok := args[p]; !ok || v == "" {
			args[p] = self
		}
	}
	for k, v := range t.fixed {
		args[k] = v
	}
	var argv []string
	for _, p := range a.Params {
		v, ok := args[p.Name]
		if !ok || contains(t.hide, p.Name) {
			continue
		}
		switch x := v.(type) {
		case bool:
			if x {
				argv = append(argv, "--"+p.Name)
			}
		case []any:
			for _, e := range x {
				argv = append(argv, "--"+p.Name, fmt.Sprint(e))
			}
		default:
			if p.Positional {
				argv = append(argv, fmt.Sprint(x))
			} else {
				argv = append(argv, "--"+p.Name, fmt.Sprint(x))
			}
		}
	}
	if _, err := spec.Parse(a, argv); err != nil {
		return nil, err
	}
	if t.endsSelf && !desk && strings.EqualFold(fmt.Sprint(args["id"]), self) {
		return runDetached(t.action, argv)
	}
	return runAction(t.action, argv)
}

// runAction executes a tool call. Tests run it in process.
var runAction = runInstalled

func runInProcess(action string, argv []string) (any, error) {
	a := spec.FindVerb(action)
	parsed, err := spec.Parse(a, argv)
	if err != nil {
		return nil, err
	}
	return a.Run(&spec.Context{Args: parsed, Office: os.Getenv("OFFICE_DIR"), Format: "json", Stdin: strings.NewReader("")})
}

// runInstalled runs the action with the dossier binary installed now, not the
// one this long-lived server was started from: an update reaches running
// sessions without a restart. Only the tool list itself stays as it was.
func runInstalled(action string, argv []string) (any, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(exe, append([]string{action}, append(argv, "--format", "json")...)...)
	cmd.Env = os.Environ()
	cmd.Stdin = strings.NewReader("")
	out, runErr := cmd.Output()
	var env struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
		Error  *spec.Error     `json:"error"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		return nil, fmt.Errorf("office %s did not answer with an envelope (%v): %s", action, runErr, strings.TrimSpace(string(out)))
	}
	if !env.OK {
		if env.Error != nil {
			return nil, env.Error
		}
		return nil, fmt.Errorf("office %s failed", action)
	}
	var result any
	if len(env.Result) > 0 {
		_ = json.Unmarshal(env.Result, &result)
	}
	return result, nil
}

const (
	mcpInstructions = "Tools of the office. They act on this session's dossier by default, and on any " +
		"dossier of the office when you name it. Close or merge only after the user said so."
	mcpDeskInstructions = "Tools of the office. This session is the office's desk, not a dossier: name the " +
		"dossier every tool acts on. Close or merge only after the user said so."
)

// serveMCP speaks MCP over stdio, one JSON-RPC message per line.
func serveMCP(in io.Reader, out io.Writer) error {
	self := os.Getenv("DOSSIER_ID")
	desk := strings.HasSuffix(self, "-DESK")
	instructions := mcpInstructions
	if desk {
		instructions = mcpDeskInstructions
	}
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var req struct {
			ID     *json.RawMessage `json:"id"`
			Method string           `json:"method"`
			Params json.RawMessage  `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &req) != nil || req.ID == nil {
			continue // notifications need no answer
		}
		reply := func(result any) { _ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}) }
		fail := func(code int, msg string) {
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": code, "message": msg}})
		}
		switch req.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(req.Params, &p)
			if p.ProtocolVersion == "" {
				p.ProtocolVersion = "2025-06-18"
			}
			reply(map[string]any{
				"protocolVersion": p.ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "office", "version": Version},
				"instructions":    instructions,
			})
		case "ping":
			reply(map[string]any{})
		case "tools/list":
			var list []map[string]any
			for _, t := range toolsFor(desk) {
				list = append(list, map[string]any{"name": t.name, "description": t.description, "inputSchema": t.schema(desk)})
			}
			reply(map[string]any{"tools": list})
		case "tools/call":
			var p struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			var t *tool
			served := toolsFor(desk)
			for i := range served {
				if served[i].name == p.Name {
					t = &served[i]
				}
			}
			if t == nil {
				fail(-32602, "unknown tool "+p.Name)
				continue
			}
			if self == "" {
				reply(textResult(map[string]any{"ok": false, "error": "DOSSIER_ID is not set: this server serves one dossier session"}, true))
				continue
			}
			if p.Arguments == nil {
				p.Arguments = map[string]any{}
			}
			result, err := t.call(self, desk, p.Arguments)
			if err != nil {
				reply(textResult(map[string]any{"ok": false, "error": spec.Internal(err)}, true))
				continue
			}
			reply(textResult(map[string]any{"ok": true, "result": result}, false))
		default:
			fail(-32601, "method not found: "+req.Method)
		}
	}
	return sc.Err()
}

func textResult(v any, isError bool) map[string]any {
	b, _ := json.Marshal(v)
	return map[string]any{"content": []map[string]any{{"type": "text", "text": string(b)}}, "isError": isError}
}

func init() {
	spec.Register(&spec.Action{
		Category: "internal", Name: "mcp", Summary: "Serve this dossier's tools over MCP (stdio), scoped to DOSSIER_ID.",
		Discussion: "office passes this server to every session it starts. Tools: " + toolNames() + ".",
		Examples:   []string{"DOSSIER_ID=D-0042 office mcp"},
		Run: func(ctx *spec.Context) (any, error) {
			return nil, serveMCP(os.Stdin, os.Stdout)
		},
	})
}

func toolNames() string {
	var n []string
	for _, t := range tools {
		n = append(n, t.name)
	}
	return strings.Join(n, ", ")
}

// runDetached starts the action in its own session and returns at once: it
// closes the calling session, which would otherwise take the action down with it.
var runDetached = func(action string, argv []string) (any, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(exe, append([]string{action}, argv...)...)
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	_ = cmd.Process.Release()
	return map[string]any{"detached": true, "note": "This session closes now and resumes on the same conversation in a new tab."}, nil
}
