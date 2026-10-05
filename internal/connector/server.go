package connector

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/aclemen1/office-cli/internal/office"
)

// A connector that declares "serve" can run as a lasting process: one JSON
// request per line on its stdin, {"id", "verb", "input"}, one answer per line
// on its stdout, {"id", "result"} or {"id", "error"}. office listen keeps one
// per source, so that its waits, polls and placeholder edits need no process
// start; every Runner call of that process goes through it. Elsewhere,
// connectors still run once per call.

var errServerGone = errors.New("connector server gone")

type Server struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	mu      sync.Mutex
	next    int
	pending map[int]chan serverReply
	gone    bool
	key     string
}

type serverReply struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

var servers sync.Map // office root + source name → *Server

func serverKey(o *office.Office, source string) string { return o.Root + "\x00" + source }

func serverFor(o *office.Office, source string) *Server {
	if o == nil {
		return nil
	}
	if v, ok := servers.Load(serverKey(o, source)); ok {
		return v.(*Server)
	}
	return nil
}

// Serve starts the source's connector as a lasting process and routes this
// process's calls for that source through it.
func Serve(o *office.Office, src office.SourceConfig) (*Server, error) {
	r := Runner{Office: o, Source: src}
	argv, err := r.argv()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(argv[0], append(argv[1:], "serve")...)
	cmd.Dir, cmd.Env = o.Root, r.env()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("source %q: cannot start its server: %w", src.Name, err)
	}
	s := &Server{cmd: cmd, stdin: stdin, pending: map[int]chan serverReply{}, key: serverKey(o, src.Name)}
	servers.Store(s.key, s)
	go s.read(stdout)
	return s, nil
}

func (s *Server) read(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		var m struct {
			ID int `json:"id"`
			serverReply
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		s.mu.Lock()
		ch := s.pending[m.ID]
		delete(s.pending, m.ID)
		s.mu.Unlock()
		if ch != nil {
			ch <- m.serverReply
		}
	}
	s.mu.Lock()
	s.gone = true
	for id, ch := range s.pending {
		close(ch)
		delete(s.pending, id)
	}
	s.mu.Unlock()
	servers.CompareAndDelete(s.key, s)
	_ = s.cmd.Wait()
}

// Gone says whether the process has ended.
func (s *Server) Gone() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gone
}

func (s *Server) call(verb string, input any, timeout time.Duration) (json.RawMessage, error) {
	s.mu.Lock()
	if s.gone {
		s.mu.Unlock()
		return nil, errServerGone
	}
	s.next++
	id := s.next
	ch := make(chan serverReply, 1)
	s.pending[id] = ch
	b, _ := json.Marshal(map[string]any{"id": id, "verb": verb, "input": input})
	_, err := s.stdin.Write(append(b, '\n'))
	s.mu.Unlock()
	if err != nil {
		return nil, errServerGone
	}
	select {
	case r, ok := <-ch:
		if !ok {
			return nil, errServerGone
		}
		if r.Error != nil {
			return nil, errors.New(r.Error.Message)
		}
		return r.Result, nil
	case <-time.After(timeout):
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, fmt.Errorf("%s timed out after %s", verb, timeout)
	}
}

// Close ends the process; calls fall back to one process per call.
func (s *Server) Close() {
	servers.CompareAndDelete(s.key, s)
	_ = s.stdin.Close()
}
