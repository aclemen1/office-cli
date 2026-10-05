// Package acp is a minimal Agent Client Protocol client: one ACP server process
// per operation, prompts sent without waiting for the end of the turn.
package acp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type Options struct {
	Command   []string          // ACP server argv
	AgentArgs []string          // appended after "--"
	Env       map[string]string // added to a minimal environment
	Meta      map[string]any    // sent as _meta.herdr on session/new|load
	Cwd       string
	MCP       []MCPServer
}

type MCPServer struct {
	Name    string     `json:"name"`
	Command string     `json:"command"`
	Args    []string   `json:"args"`
	Env     []EnvEntry `json:"env"`
}

type EnvEntry struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type Placement struct {
	PaneID string `json:"paneId"`
	TabID  string `json:"tabId"`
}

type Client struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	mu      sync.Mutex
	nextID  int
	pending map[int]chan response
	updates chan struct{}
	stderr  strings.Builder
	opts    Options
}

type response struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

var passEnv = []string{"HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "PATH", "TMPDIR", "SHELL"}

func Start(o Options) (*Client, error) {
	if len(o.Command) == 0 {
		return nil, fmt.Errorf("[acp] command is empty in the office config")
	}
	argv := append([]string{}, o.Command...)
	if len(o.AgentArgs) > 0 {
		argv = append(argv, "--")
		argv = append(argv, o.AgentArgs...)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = o.Cwd
	var env []string
	for _, k := range passEnv {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	for k, v := range o.Env {
		env = append(env, k+"="+v)
	}
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	c := &Client{cmd: cmd, stdin: stdin, pending: map[int]chan response{}, updates: make(chan struct{}, 64), opts: o}
	cmd.Stderr = &limitedWriter{b: &c.stderr, max: 8192}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("cannot start ACP server %q: %w", argv[0], err)
	}
	go c.read(stdout)
	if _, err := c.call("initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{}}, 30*time.Second); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

type limitedWriter struct {
	b   *strings.Builder
	max int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if w.b.Len() < w.max {
		w.b.Write(p)
	}
	return len(p), nil
}

func (c *Client) read(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var m struct {
			ID     *json.RawMessage `json:"id"`
			Method string           `json:"method"`
			response
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		switch {
		case m.ID != nil && m.Method == "":
			var id int
			_ = json.Unmarshal(*m.ID, &id)
			c.mu.Lock()
			ch := c.pending[id]
			delete(c.pending, id)
			c.mu.Unlock()
			if ch != nil {
				ch <- m.response
			}
		case m.ID != nil:
			// The agent asks the client something. In native mode this should not
			// happen; decline so that the agent's own UI keeps the decision.
			result := map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}
			if m.Method != "session/request_permission" {
				c.send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32601, "message": "not supported by dossier"}})
				continue
			}
			c.send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result})
		case m.Method == "session/update":
			select {
			case c.updates <- struct{}{}:
			default:
			}
		}
	}
}

func (c *Client) send(v any) {
	b, _ := json.Marshal(v)
	c.mu.Lock()
	defer c.mu.Unlock()
	_, _ = c.stdin.Write(append(b, '\n'))
}

func (c *Client) request(method string, params any) chan response {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan response, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	return ch
}

func (c *Client) call(method string, params any, timeout time.Duration) (json.RawMessage, error) {
	select {
	case r := <-c.request(method, params):
		if r.Error != nil {
			return nil, fmt.Errorf("ACP %s failed: %s (code %d)", method, r.Error.Message, r.Error.Code)
		}
		return r.Result, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("ACP %s timed out after %s; server stderr: %s", method, timeout, strings.TrimSpace(c.stderr.String()))
	}
}

func (c *Client) meta() map[string]any {
	if len(c.opts.Meta) == 0 {
		return nil
	}
	return map[string]any{"herdr": c.opts.Meta}
}

func (c *Client) sessionParams(extra map[string]any) map[string]any {
	mcp := c.opts.MCP
	if mcp == nil {
		mcp = []MCPServer{}
	}
	p := map[string]any{"cwd": c.opts.Cwd, "mcpServers": mcp}
	if m := c.meta(); m != nil {
		p["_meta"] = m
	}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

type sessionResult struct {
	SessionID string `json:"sessionId"`
	Meta      struct {
		Herdr Placement `json:"herdr"`
	} `json:"_meta"`
}

func (c *Client) NewSession() (string, Placement, error) {
	raw, err := c.call("session/new", c.sessionParams(nil), 120*time.Second)
	if err != nil {
		return "", Placement{}, err
	}
	var r sessionResult
	_ = json.Unmarshal(raw, &r)
	return r.SessionID, r.Meta.Herdr, nil
}

func (c *Client) LoadSession(id string) (Placement, error) {
	raw, err := c.call("session/load", c.sessionParams(map[string]any{"sessionId": id}), 120*time.Second)
	if err != nil {
		return Placement{}, err
	}
	var r sessionResult
	_ = json.Unmarshal(raw, &r)
	return r.Meta.Herdr, nil
}

// Prompt sends a prompt and returns once the server has started the turn,
// without waiting for its end. delivery goes in _meta: "queue" lets the server
// hold the prompt until the agent's turn ends, without touching what the user
// is typing; "now" asks for it at once.
func (c *Client) Prompt(session, text, delivery string) error {
	// Updates replayed by session/load say nothing about this prompt.
	time.Sleep(300 * time.Millisecond)
	for len(c.updates) > 0 {
		<-c.updates
	}
	ch := c.request("session/prompt", map[string]any{
		"sessionId": session,
		"prompt":    []map[string]any{{"type": "text", "text": text}},
		"_meta":     map[string]any{"delivery": delivery},
	})
	select {
	case r := <-ch:
		if r.Error != nil {
			return fmt.Errorf("ACP session/prompt failed: %s", r.Error.Message)
		}
	case <-c.updates:
		time.Sleep(time.Second)
	case <-time.After(15 * time.Second):
		// The turn may still be starting; herdr-acp types the prompt into the TUI.
	}
	return nil
}

func (c *Client) CloseSession(id string) error {
	_, err := c.call("session/close", map[string]any{"sessionId": id}, 60*time.Second)
	return err
}

// Close detaches: the agent keeps running in its tab.
func (c *Client) Close() {
	_ = c.stdin.Close()
	done := make(chan struct{})
	go func() { _ = c.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		_ = c.cmd.Process.Kill()
	}
}

// TailResult is herdr-acp's _session/tail answer: the last updates of a
// session, in ACP's session/update format, and a cursor for what follows.
type TailResult struct {
	Updates []json.RawMessage `json:"updates"`
	Cursor  string            `json:"cursor"`
	Reset   bool              `json:"reset,omitempty"`
	Status  string            `json:"status,omitempty"`
}

// Tail reads the end of a session without touching it (herdr-acp extension
// _session/tail). after is the cursor of the previous answer, or "".
func (c *Client) Tail(session, after string, limit int) (TailResult, error) {
	var r TailResult
	params := map[string]any{"sessionId": session, "limit": limit}
	if after != "" {
		params["after"] = after
	}
	raw, err := c.call("_session/tail", params, 10*time.Second)
	if err != nil {
		return r, err
	}
	err = json.Unmarshal(raw, &r)
	return r, err
}
