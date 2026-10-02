package app

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/aclemen1/dossier-cli/internal/dossier"
	"github.com/aclemen1/dossier-cli/internal/spec"
	"github.com/aclemen1/dossier-cli/internal/store"
)

// The desk lives in <store>/desk/. Store.Dirs skips it: its name has no number.
const deskDir = "desk"

func (a *App) DeskID() string { return a.S.Prefix() + "-DESK" }

func (a *App) isDesk(id string) bool { return id != "" && a.NormalizeAlias(id) == "DESK" }

func IsDesk(d *dossier.Dossier) bool { return d.State == dossier.Desk }

// Desk loads the store's desk. A desk never started exists only in memory.
func (a *App) Desk() *dossier.Dossier {
	dir := filepath.Join(a.S.Root, deskDir)
	if d, err := dossier.Load(dir); err == nil {
		return d
	}
	d := dossier.Create(dir, a.DeskID(), "desk · "+a.S.Config.Store.Sphere)
	d.State = dossier.Desk
	return d
}

// ensureDesk writes the desk's directory, charter and dossier.md once.
func (a *App) ensureDesk(d *dossier.Dossier) error {
	if err := os.MkdirAll(d.Dir, 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(d.Path("CLAUDE.md")); os.IsNotExist(err) {
		if err := os.WriteFile(d.Path("CLAUDE.md"), []byte(store.DeskCharter), 0o644); err != nil {
			return err
		}
	}
	if _, err := os.Stat(d.Path("dossier.md")); os.IsNotExist(err) {
		if err := d.Save(); err != nil {
			return err
		}
		_ = d.Log("desk created")
	}
	return nil
}

// LoadAny resolves an id like Load, and also "desk" or U-DESK to the desk.
func (a *App) LoadAny(id string) (*dossier.Dossier, error) {
	if id == "" {
		id = os.Getenv("DOSSIER_ID")
	}
	if a.isDesk(id) {
		return a.Desk(), nil
	}
	return a.Load(id)
}

func (a *App) deskError() error {
	return spec.UserError("%s is the store's desk: it has no state, links or sources. Name a dossier, or use `dossier desk`", a.DeskID())
}

// ActingDesk is the desk when the current session is this store's desk.
func (a *App) ActingDesk() *dossier.Dossier {
	if os.Getenv("DOSSIER_ID") != a.DeskID() {
		return nil
	}
	return a.Desk()
}

// NewDeskConversation archives the desk's conversation and starts a fresh one.
func (a *App) NewDeskConversation(d *dossier.Dossier) error {
	if d.Run.Session == "" {
		return nil
	}
	if err := a.closeSession(d); err != nil {
		return err
	}
	_ = a.Archive(d)
	if err := os.MkdirAll(d.Path("transcripts"), 0o755); err != nil {
		return err
	}
	n := len(archivedTranscripts(d)) + 1
	if _, err := os.Stat(d.Path("transcript.jsonl")); err == nil {
		if err := os.Rename(d.Path("transcript.jsonl"), d.Path("transcripts", fmt.Sprintf("%04d.jsonl", n))); err != nil {
			return err
		}
	}
	_ = d.Log("conversation %d archived (session %s)", n, d.Run.Session)
	d.Run.Session, d.Run.PaneID, d.Run.TabID = "", "", ""
	return d.Save()
}

func archivedTranscripts(d *dossier.Dossier) []string {
	m, _ := filepath.Glob(d.Path("transcripts", "*.jsonl"))
	sort.Strings(m)
	return m
}

// Conversation describes the session's current conversation.
type Conversation struct {
	Started     time.Time `json:"started"`
	Compactions int       `json:"compactions"`
}

// ConversationOf reads the live transcript, or its archived copy.
func ConversationOf(d *dossier.Dossier) Conversation {
	path := transcriptOf(d.Run.Session)
	if path == "" {
		path = d.Path("transcript.jsonl")
	}
	var c Conversation
	f, err := os.Open(path)
	if err != nil {
		return c
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var rec struct {
			Subtype   string `json:"subtype"`
			Timestamp string `json:"timestamp"`
		}
		if json.Unmarshal(sc.Bytes(), &rec) != nil {
			continue
		}
		if c.Started.IsZero() && rec.Timestamp != "" {
			c.Started, _ = time.Parse(time.RFC3339Nano, rec.Timestamp)
		}
		if rec.Subtype == "compact_boundary" {
			c.Compactions++
		}
	}
	return c
}
