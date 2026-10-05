package app

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"

	"github.com/aclemen1/office-cli/internal/dossier"
	"github.com/aclemen1/office-cli/internal/office"
)

// agendaCall runs the agenda CLI with --format json. Tests replace it.
var agendaCall = func(argv []string) ([]byte, error) {
	return exec.Command(argv[0], argv[1:]...).Output()
}

// proposeToAgenda puts a dossier on the agenda of the meeting that now
// includes it, when the office keeps agendas ([agenda]) and the holder is a
// meeting, named by its alias. The item is proposed; the chair, or the user
// through an agent, accepts it. Problems go to the history, never fail a link.
func (a *App) proposeToAgenda(meeting, item *dossier.Dossier) {
	cfg := a.S.Config.Agenda
	if len(cfg.Command) == 0 || meeting.Alias == "" {
		return
	}
	mark := "agenda: proposed to " + meeting.Alias
	if b, err := readLog(item); err == nil && strings.Contains(b, mark) {
		return
	}
	argv := append([]string{}, cfg.Command...)
	argv[0] = office.ExpandHome(argv[0])
	argv = append(argv, "item", "add", meeting.Alias, item.Title, "--ref", "office:"+item.ID)
	for _, r := range agendaRefs(item) {
		argv = append(argv, "--ref", r)
	}
	if cfg.Sphere != "" {
		argv = append(argv, "--sphere", cfg.Sphere)
	}
	out, err := agendaCall(append(argv, "--format", "json"))
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if ee, ok := err.(*exec.ExitError); ok && msg == "" {
			msg = strings.TrimSpace(string(ee.Stderr))
		}
		_ = item.Log("agenda: not proposed to %s: %v %s", meeting.Alias, err, firstLine(msg))
		return
	}
	var env struct {
		Result struct {
			ID string `json:"id"`
		} `json:"result"`
	}
	_ = json.Unmarshal(out, &env)
	_ = item.Log("%s (item %s)", mark, env.Result.ID)
	_ = meeting.Log("agenda: %s proposed (item %s)", item.ID, env.Result.ID)
}

// agendaRefs are the source references an agenda can use to recognise an
// item it imported already: a Google Task, the mail thread.
func agendaRefs(d *dossier.Dossier) []string {
	var out []string
	for _, s := range d.Sources {
		switch {
		case strings.HasPrefix(s.ID, "gmail:task/"):
			out = append(out, "gtasks:"+strings.TrimPrefix(s.ID, "gmail:task/"))
		case strings.HasPrefix(s.ID, "gmail:thread/"), strings.HasPrefix(s.ID, "gmail:message/"):
			out = append(out, s.ID)
		}
	}
	for _, t := range d.Threads {
		if strings.HasPrefix(t, "gmail:thread/") && !contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

func readLog(d *dossier.Dossier) (string, error) {
	b, err := os.ReadFile(d.Path("log.md"))
	return string(b), err
}
