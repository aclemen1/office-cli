package app

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"
)

// herdr reports an agent that shows a question (AskUserQuestion) as idle:
// only its transcript tells. A question is pending while its tool_use has no
// tool_result after it.

type askCacheEntry struct {
	mod    time.Time
	size   int64
	asking bool
}

var (
	askMu    sync.Mutex
	askCache = map[string]askCacheEntry{}
)

const askTail = 512 << 10

func pendingQuestion(session string) bool {
	path := transcriptOf(session)
	if path == "" {
		return false
	}
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	askMu.Lock()
	if e, ok := askCache[path]; ok && e.mod.Equal(fi.ModTime()) && e.size == fi.Size() {
		askMu.Unlock()
		return e.asking
	}
	askMu.Unlock()
	asking := scanPendingQuestion(path, fi.Size())
	askMu.Lock()
	askCache[path] = askCacheEntry{fi.ModTime(), fi.Size(), asking}
	askMu.Unlock()
	return asking
}

func scanPendingQuestion(path string, size int64) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	if size > askTail {
		if _, err := f.Seek(size-askTail, io.SeekStart); err != nil {
			return false
		}
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return false
	}
	if size > askTail {
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			b = b[i+1:]
		}
	}
	pending := ""
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.Contains(line, []byte(`"tool_use`)) && !bytes.Contains(line, []byte(`"tool_result"`)) {
			continue
		}
		var e struct {
			Message struct {
				Role    string `json:"role"`
				Content []struct {
					Type      string `json:"type"`
					Name      string `json:"name"`
					ID        string `json:"id"`
					ToolUseID string `json:"tool_use_id"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		for _, c := range e.Message.Content {
			switch {
			case c.Type == "tool_use" && c.Name == "AskUserQuestion":
				pending = c.ID
			case c.Type == "tool_result" && c.ToolUseID == pending:
				pending = ""
			}
		}
	}
	return pending != ""
}
