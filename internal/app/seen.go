package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/aclemen1/dossier-cli/internal/dossier"
)

// The user's visits live outside the stores, in their own config directory:
// {"<store root>|<id>": RFC 3339 time of the last visit, or "" for unread}.
func seenPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "dossier", "seen.json")
}

// Seen maps a dossier to the time the user last opened its pane.
type Seen map[string]string

// LoadSeen reads the visits. The first call ever counts every dossier of the
// given stores as seen now, so that the past is not all unread.
func LoadSeen(stores ...*App) Seen {
	s := Seen{}
	b, err := os.ReadFile(seenPath())
	if err == nil {
		_ = json.Unmarshal(b, &s)
		return s
	}
	now := time.Now().Format(time.RFC3339)
	for _, a := range stores {
		all, _ := a.All()
		for _, d := range all {
			s[seenKey(a, d)] = now
		}
	}
	_ = s.save()
	return s
}

func (s Seen) save() error {
	p := seenPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, _ := json.Marshal(s)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func seenKey(a *App, d *dossier.Dossier) string { return a.S.Root + "|" + d.ID }

// MarkSeen records a visit now; MarkUnread makes the dossier unread again.
func (a *App) MarkSeen(d *dossier.Dossier) error { return a.mark(d, time.Now().Format(time.RFC3339)) }

func (a *App) MarkUnread(d *dossier.Dossier) error { return a.mark(d, "") }

func (a *App) mark(d *dossier.Dossier, v string) error {
	s := LoadSeen(a)
	s[seenKey(a, d)] = v
	return s.save()
}

// Unread: something happened on the dossier since the user last opened its
// pane, or they marked it unread.
func (s Seen) Unread(a *App, d *dossier.Dossier) bool {
	seen, ok := s[seenKey(a, d)]
	if !ok {
		return true
	}
	if seen == "" {
		return true
	}
	last := LastChange(d)
	if last.IsZero() {
		return false
	}
	t, err := time.Parse(time.RFC3339, seen)
	return err != nil || last.After(t)
}

// Housekeeping lines of the history are no news.
var noise = regexp.MustCompile(`^(session .*(started|restarted|resumed|ended)|transcript archived|tab not closed|skills: |no action required|action required ·)`)

// LastChange is the time of the last meaningful line of the dossier's history.
func LastChange(d *dossier.Dossier) time.Time {
	b, err := os.ReadFile(d.Path("log.md"))
	if err != nil {
		return time.Time{}
	}
	lines := strings.Split(string(b), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l, ok := strings.CutPrefix(lines[i], "- ")
		if !ok {
			continue
		}
		ts, text, ok := strings.Cut(l, " · ")
		if !ok || noise.MatchString(text) {
			continue
		}
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			return t
		}
	}
	return time.Time{}
}
