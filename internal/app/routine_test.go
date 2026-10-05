package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/aclemen1/office-cli/internal/dossier"
	"github.com/aclemen1/office-cli/internal/office"
)

// fakeRoutine keeps routines in memory and answers like `routine … --json`.
type fakeRoutine struct {
	tasks map[string]map[string]any
	calls []string
}

func useFakeRoutine(t *testing.T) *fakeRoutine {
	f := &fakeRoutine{tasks: map[string]map[string]any{}}
	old := routineCall
	t.Cleanup(func() { routineCall = old })
	routineCall = f.call
	return f
}

func (f *fakeRoutine) call(stdin string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	verb := args[0]
	if verb == "ls" {
		owner := args[2]
		var out []map[string]any
		var ids []string
		for id := range f.tasks {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			o, _ := f.tasks[id]["owner"].(string)
			if o == owner || (strings.HasSuffix(owner, "*") && strings.HasPrefix(o, strings.TrimSuffix(owner, "*"))) {
				out = append(out, f.tasks[id])
			}
		}
		return json.Marshal(map[string]any{"routines": out})
	}
	id := args[1]
	t, ok := f.tasks[id]
	if verb != "add" && !ok {
		return nil, fmt.Errorf("routine %s: no routine %q", verb, id)
	}
	switch verb {
	case "add":
		if ok {
			return nil, fmt.Errorf("routine add: %q already exists", id)
		}
		t = map[string]any{"id": id, "active": true, "rrules": []string{}}
		f.tasks[id] = t
		fallthrough
	case "edit":
		var rrules []string
		for i := 2; i < len(args); i++ {
			a := args[i]
			switch {
			case a == "--paused":
				t["active"] = false
			case a == "--rrule":
				rrules = append(rrules, args[i+1])
				i++
			case a == "--meta":
				var m map[string]any
				_ = json.Unmarshal([]byte(args[i+1]), &m)
				t["meta"] = m
				i++
			case a == "--body-file":
				t["body"] = stdin
				i++
			case strings.HasPrefix(a, "--acp-arg="):
				acp, _ := t["acp"].(map[string]any)
				acp["args"] = append(acp["args"].([]string), strings.TrimPrefix(a, "--acp-arg="))
			case a == "--acp-command":
				if args[i+1] == "" {
					delete(t, "acp")
				} else {
					t["acp"] = map[string]any{"command": args[i+1], "args": []string{}}
				}
				i++
			case strings.HasPrefix(a, "--"):
				k := strings.TrimPrefix(a, "--")
				if args[i+1] == "" {
					delete(t, k)
				} else {
					t[k] = args[i+1]
				}
				i++
			}
		}
		if len(rrules) > 0 {
			t["rrules"] = rrules
		}
	case "pause":
		t["active"] = false
	case "resume":
		t["active"] = true
	case "rm":
		delete(f.tasks, id)
	case "run":
		return json.Marshal(map[string]any{"id": id, "status": "ok"})
	}
	return json.Marshal(t)
}

func (f *fakeRoutine) active(id string) (bool, bool) {
	t, ok := f.tasks[id]
	if !ok {
		return false, false
	}
	return t["active"].(bool), true
}

func TestRoutineFollowsTheDossierState(t *testing.T) {
	f := newFixture(t)
	r := useFakeRoutine(t)
	f.a.Open(OpenParams{Title: "Briefing", NoStart: true})
	d := mustGet(t, f, "1")
	got, err := f.a.AddRoutine(d, RoutineParams{Name: "brief", RRules: []string{"FREQ=DAILY;BYHOUR=7"}, Prompt: "Prépare le briefing."})
	if err != nil || got.ID != "office/test-d-0001-brief" || got.Name != "brief" || got.Runner != RunnerSession || !got.Active {
		t.Fatalf("add %+v %v", got, err)
	}
	task := r.tasks[got.ID]
	if task["owner"] != "office:test/D-0001" || task["body"] != "Prépare le briefing." || !strings.Contains(task["run"].(string), "prompt D-0001 --office") {
		t.Fatalf("task %+v", task)
	}
	if _, err := f.a.AddRoutine(d, RoutineParams{Name: "Brief!", RRules: []string{"FREQ=DAILY"}, Prompt: "x"}); err == nil {
		t.Fatal("a bad name was accepted")
	}

	f.a.SetState(d, "wait", "", "Patricia")
	if on, _ := r.active(got.ID); !on {
		t.Fatal("waiting is a default state: the routine should run")
	}
	f.a.SetState(d, "close", "", "")
	if on, _ := r.active(got.ID); on {
		t.Fatal("a closed dossier's routine should pause")
	}
	f.a.SetState(d, "reopen", "", "")
	if on, _ := r.active(got.ID); !on {
		t.Fatal("a reopened dossier's routine should resume")
	}
	if b, _ := os.ReadFile(d.Path("log.md")); !strings.Contains(string(b), "routine brief added") || !strings.Contains(string(b), "routine brief paused (done)") {
		t.Fatalf("history:\n%s", b)
	}

	only, err := f.a.AddRoutine(d, RoutineParams{Name: "chase", RRules: []string{"FREQ=WEEKLY"}, Runner: RunnerCommand, Command: "echo hi", States: []string{dossier.Waiting}})
	if err != nil || only.Active {
		t.Fatalf("a routine for waiting dossiers starts paused on an open one: %+v %v", only, err)
	}
	if e, err := f.a.EditRoutine(d, "chase", RoutineEdit{States: []string{dossier.Open}}); err != nil || !e.Active {
		t.Fatalf("edit to open should resume it: %+v %v", e, err)
	}
	if rs, _ := f.a.Routines(d); len(rs) != 2 || rs[0].Name != "brief" || rs[1].Runner != RunnerCommand {
		t.Fatalf("ls %+v", rs)
	}
}

func TestMergeAndDeleteRemoveRoutines(t *testing.T) {
	f := newFixture(t)
	r := useFakeRoutine(t)
	f.a.Open(OpenParams{Title: "A", NoStart: true})
	f.a.Open(OpenParams{Title: "B", NoStart: true})
	f.a.Open(OpenParams{Title: "C", NoStart: true})
	for _, id := range []string{"1", "3"} {
		if _, err := f.a.AddRoutine(mustGet(t, f, id), RoutineParams{Name: "x", RRules: []string{"FREQ=DAILY"}, Prompt: "p"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.a.Merge("D-0001", "D-0002"); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.active("office/test-d-0001-x"); ok {
		t.Fatal("a merged dossier kept its routine")
	}
	if _, err := f.a.Delete(mustGet(t, f, "3"), ""); err != nil {
		t.Fatal(err)
	}
	if len(r.tasks) != 0 {
		t.Fatalf("a deleted dossier kept its routine: %v", r.tasks)
	}
}

func TestMoveTakesRoutinesAlong(t *testing.T) {
	f := newFixture(t)
	r := useFakeRoutine(t)
	pro, err := office.Init(filepath.Join(filepath.Dir(f.a.S.Root), "pro"), "pro", false)
	if err != nil {
		t.Fatal(err)
	}
	pro.Config.ACP, pro.Config.Agent = f.a.S.Config.ACP, f.a.S.Config.Agent
	f.a.Open(OpenParams{Title: "K3S", NoStart: true})
	d := mustGet(t, f, "1")
	f.a.AddRoutine(d, RoutineParams{Name: "check", RRules: []string{"FREQ=HOURLY"}, Runner: RunnerAgent, Prompt: "Vérifie."})
	res, err := f.a.Move([]string{"D-0001"}, pro)
	if err != nil {
		t.Fatal(err)
	}
	to := res.Moved[0].To
	newID := "office/pro-" + strings.ToLower(to) + "-check"
	task, ok := r.tasks[newID]
	if !ok || len(r.tasks) != 1 {
		t.Fatalf("routines after move: %v", r.tasks)
	}
	if task["owner"] != "office:pro/"+to || task["body"] != "Vérifie." || task["cwd"] != res.Moved[0].Dir {
		t.Fatalf("moved task %+v", task)
	}
}

func TestDeskRoutinesAlwaysRun(t *testing.T) {
	f := newFixture(t)
	useFakeRoutine(t)
	got, err := f.a.AddRoutine(f.a.Desk(), RoutineParams{Name: "brief", RRules: []string{"FREQ=DAILY"}, Prompt: "Briefing.", States: []string{dossier.Done}})
	if err != nil || !got.Active || got.ID != "office/test-d-desk-brief" || !strings.Contains(got.Run, "prompt desk") {
		t.Fatalf("desk routine %+v %v", got, err)
	}
}

func TestRoutineStepsRunInTheDossierAndFollowAMove(t *testing.T) {
	f := newFixture(t)
	r := useFakeRoutine(t)
	f.a.Open(OpenParams{Title: "Soir", NoStart: true})
	d := mustGet(t, f, "1")
	cov, _ := ParseStep("coverage:command:go test -cover ./...")
	imp, _ := ParseStep("improve:session")
	got, err := f.a.AddRoutine(d, RoutineParams{Name: "evening", RRules: []string{"FREQ=DAILY"}, Steps: []RoutineStep{cov, imp},
		Prompt: "## improve\n\n{{steps.coverage.output}}\n"})
	if err != nil || got.Runner != RunnerSteps {
		t.Fatalf("add %+v %v", got, err)
	}
	steps := r.tasks[got.ID]["steps"].(string)
	if !strings.Contains(steps, `"name":"coverage"`) || !strings.Contains(steps, "prompt D-0001") || !strings.Contains(steps, d.Dir) {
		t.Fatalf("steps %s", steps)
	}
	if _, err := ParseStep("Bad Name:session"); err == nil {
		t.Fatal("a bad step name was accepted")
	}
	pro, _ := office.Init(filepath.Join(filepath.Dir(f.a.S.Root), "pro"), "pro", false)
	pro.Config.ACP, pro.Config.Agent = f.a.S.Config.ACP, f.a.S.Config.Agent
	res, err := f.a.Move([]string{"D-0001"}, pro)
	if err != nil {
		t.Fatal(err)
	}
	moved := r.tasks["office/pro-"+strings.ToLower(res.Moved[0].To)+"-evening"]
	if moved == nil || !strings.Contains(moved["steps"].(string), "prompt "+res.Moved[0].To) {
		t.Fatalf("moved %v", r.tasks)
	}
}
