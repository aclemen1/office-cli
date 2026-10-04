package actions

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/aclemen1/office-cli/internal/spec"
)

// tool projects an action onto an MCP tool. Its dossier parameters default
// to the session's own dossier; any dossier of the office can be named instead.
type tool struct {
	name, action, description string
	self                      []string          // params that default to DOSSIER_ID
	rename                    map[string]string // action param → tool param
	hide                      []string
	dossierOnly               bool // not served to the desk's session
}

var tools = []tool{
	{name: "show", action: "show", self: []string{"id"}, description: "Show a dossier: state, sources, files, links, history, and the body of its fiche (instruction, notes, sections such as « À ne pas oublier »)."},
	{name: "search", action: "search", description: "Search every dossier of the office, open or closed."},
	{name: "grep", action: "grep", self: []string{"id"}, rename: map[string]string{"id": "dossier"},
		description: "Search the full conversation of a dossier, including the dossiers merged into it."},
	{name: "tree", action: "tree", self: []string{"id"}, description: "Walk a dossier's links: what it includes, what blocks it."},
	{name: "wait", action: "wait", self: []string{"id"}, description: "Mark a dossier as waiting on someone outside; again on a waiting dossier, it corrects whom it waits on and until when."},
	{name: "resume", action: "resume", self: []string{"id"}, hide: []string{"prompt"}, description: "Bring a waiting dossier back to open."},
	{name: "park", action: "park", self: []string{"id"}, description: "Mark a dossier as needing no action from the user for now, e.g. an item to raise at the next meeting. Anything new on it clears the mark."},
	{name: "unpark", action: "unpark", self: []string{"id"}, description: "Mark a dossier as needing action from the user again."},
	{name: "star", action: "star", self: []string{"id"}, description: "Star a dossier the user wants at hand: first in its office, always shown. Only when the user asks."},
	{name: "unstar", action: "unstar", self: []string{"id"}, description: "Remove a dossier's star, when the user asks."},
	{name: "close", action: "close", self: []string{"id"}, description: "Close a dossier once the user says it is settled."},
	{name: "delete", action: "delete", self: []string{"id"}, description: "Delete a dossier that should not exist (a test, a mistake), after the user asked: its signal is withdrawn and its directory removed. A settled affair gets close instead."},
	{name: "open", action: "open", self: []string{"in"}, hide: []string{"source", "thread", "url"},
		description: "Open a new dossier. By default this dossier includes it (an item of this meeting, a side affair); pass in = [] for a dossier on its own, or other dossiers that include it."},
	{name: "link", action: "link", self: []string{"from"}, description: "Link a dossier to another: includes (part of it) or depends_on (waits for)."},
	{name: "unlink", action: "unlink", self: []string{"from"}, description: "Remove links from a dossier to another."},
	{name: "track", action: "track", self: []string{"id"},
		description: "Attach a thread to a dossier as a source, e.g. the thread of a draft you just wrote: its replies come back there, and its star follows the dossier's state."},
	{name: "merge", action: "merge", self: []string{"from"}, description: "Merge a dossier into another one, after the user agreed."},
	{name: "move", action: "move", description: "Move dossiers to another office (its sphere, e.g. pro): they get a number there, links among them stay. Only when the user asked."},
	{name: "notify", action: "notify", self: []string{"from"}, description: "Tell another dossier something: a decision, new information, a request. Its session gets it as a prompt."},
	{name: "escalate", action: "escalate", self: []string{"id"}, dossierOnly: true,
		description: "Escalate to the office's desk what goes beyond this dossier: a rule to adopt, a skill to change, a request for the user. The desk gets it as a prompt once its session is idle."},
}

// toolsFor lists the tools served to a session.
func toolsFor(desk bool) []tool {
	var out []tool
	for _, t := range tools {
		if desk && t.dossierOnly {
			continue
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
		if contains(t.hide, p.Name) {
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
