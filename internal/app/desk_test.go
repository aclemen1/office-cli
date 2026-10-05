package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aclemen1/office-cli/internal/connector"
	"github.com/aclemen1/office-cli/internal/office"
	"github.com/aclemen1/office-cli/internal/testutil"
)

func TestTheDeskIsNoDossier(t *testing.T) {
	f := newFixture(t)
	f.a.Open(OpenParams{Title: "Affaire", NoStart: true})
	d, err := f.a.LoadAny("desk")
	if err != nil || d.ID != "D-DESK" || !IsDesk(d) {
		t.Fatalf("desk %+v %v", d, err)
	}
	if err := f.a.Attach(d, false); err != nil {
		t.Fatal(err)
	}
	if len(f.calls("session/new")) != 1 || len(f.calls("session/prompt")) != 0 {
		t.Fatal("the desk should start without a prompt")
	}
	if _, err := os.Stat(d.Path("CLAUDE.md")); err != nil {
		t.Fatal("the desk has no charter")
	}
	if all, _ := f.a.All(); len(all) != 1 {
		t.Fatalf("the desk counts as a dossier: %d", len(all))
	}
	if _, err := f.a.Load("D-DESK"); err == nil || !strings.Contains(err.Error(), "desk") {
		t.Fatalf("a dossier action reached the desk: %v", err)
	}
	if _, err := f.a.SetState(f.a.Desk(), "wait", "", "x"); err == nil {
		t.Fatal("the desk took a state")
	}
	if err := f.a.SetAlias(mustGet(t, f, "1"), "desk"); err == nil {
		t.Fatal("alias DESK accepted")
	}
}

func TestIngestResumesTheDeskOutsideTheCap(t *testing.T) {
	f := newFixture(t)
	f.a.S.Config.Lifecycle.MaxSessions = 1
	if _, err := f.a.Open(OpenParams{Title: "Affaire"}); err != nil {
		t.Fatal(err)
	}
	if err := f.a.Attach(f.a.Desk(), true); err != nil {
		t.Fatal(err)
	}
	rep := f.a.Reconcile(map[string]bool{})
	if len(rep.Opened) != 2 || rep.Opened[1].ID != "D-DESK" || len(f.calls("session/load")) != 2 {
		t.Fatalf("the desk should resume once the cap is reached: %+v", rep)
	}
}

func TestANewDeskConversationKeepsTheOldOneSearchable(t *testing.T) {
	f := newFixture(t)
	d := f.a.Desk()
	if err := f.a.Attach(d, true); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"user","message":{"role":"user","content":"Ouvre un dossier pour Diego"}}` + "\n" +
		`{"type":"system","subtype":"compact_boundary","timestamp":"2026-10-03T08:00:00Z"}` + "\n"
	os.WriteFile(d.Path("transcript.jsonl"), []byte(line), 0o644)
	if c := ConversationOf(d); c.Compactions != 1 || c.Started.IsZero() {
		t.Fatalf("conversation %+v", c)
	}
	if err := f.a.NewDeskConversation(d); err != nil {
		t.Fatal(err)
	}
	if d.Run.Session != "" {
		t.Fatal("the session stayed")
	}
	if _, err := os.Stat(d.Path("transcripts", "0001.jsonl")); err != nil {
		t.Fatal("the conversation was not archived")
	}
	if hits, _ := f.a.Grep(d, "diego", 10); len(hits) != 1 {
		t.Fatalf("grep %+v", hits)
	}
}

// deskWith starts the desk and sets the agent status herdr reports for its pane.
func deskWith(t *testing.T, f *fixture, status string) {
	t.Helper()
	if err := f.a.Attach(f.a.Desk(), true); err != nil {
		t.Fatal(err)
	}
	pane := f.a.Desk().Run.PaneID
	old := panes
	t.Cleanup(func() { panes = old })
	panes = func() map[string]string { return map[string]string{pane: status} }
}

func TestABusyDeskKeepsEscalationsUntilItIsIdle(t *testing.T) {
	f := newFixture(t)
	f.a.Open(OpenParams{Title: "Citations", NoStart: true})
	deskWith(t, f, "working")
	res, err := f.a.Escalate(mustGet(t, f, "1"), "Règle des citations")
	if err != nil || res.Delivered || res.Pending != 1 {
		t.Fatalf("escalate %+v %v", res, err)
	}
	if len(f.calls("session/prompt")) != 0 {
		t.Fatal("a busy desk was prompted")
	}
	if s := f.a.Show(f.a.Desk()); len(s.Escalations) != 1 || s.Escalations[0].From != "D-0001" {
		t.Fatalf("show desk %+v", s.Escalations)
	}
	deskWith(t, f, "done")
	if n, err := f.a.DeliverEscalations(); n != 1 || err != nil {
		t.Fatalf("deliver %d %v", n, err)
	}
	prompts := f.calls("session/prompt")
	if len(prompts) != 1 || !strings.Contains(fmt.Sprint(prompts[0]), "Règle des citations") {
		t.Fatalf("prompts %v", prompts)
	}
	if len(Escalations(f.a.Desk())) != 0 {
		t.Fatal("a delivered escalation is still pending")
	}
	if n, _ := f.a.DeliverEscalations(); n != 0 {
		t.Fatal("an escalation was delivered twice")
	}
	if log, _ := os.ReadFile(mustGet(t, f, "1").Path("log.md")); !strings.Contains(string(log), "escalated to D-DESK") {
		t.Fatalf("sender log:\n%s", log)
	}
}

func TestAnIdleDeskGetsTheEscalationAtOnce(t *testing.T) {
	f := newFixture(t)
	f.a.Open(OpenParams{Title: "Citations", NoStart: true})
	deskWith(t, f, "idle")
	res, err := f.a.Escalate(mustGet(t, f, "1"), "Règle")
	if err != nil || !res.Delivered || res.Pending != 0 || len(f.calls("session/prompt")) != 1 {
		t.Fatalf("escalate %+v %v", res, err)
	}
}

func TestADeskWithoutSessionKeepsEscalations(t *testing.T) {
	f := newFixture(t)
	f.a.Open(OpenParams{Title: "Citations", NoStart: true})
	res, err := f.a.Escalate(mustGet(t, f, "1"), "Règle")
	if err != nil || res.Delivered || len(f.calls("session/new")) != 0 {
		t.Fatalf("escalate %+v %v", res, err)
	}
	if _, err := f.a.Escalate(f.a.Desk(), "x"); err == nil {
		t.Fatal("the desk escalated to itself")
	}
}

func TestIngestDeliversPendingEscalations(t *testing.T) {
	f := newFixture(t)
	f.a.Open(OpenParams{Title: "Citations", NoStart: true})
	deskWith(t, f, "working")
	f.a.Escalate(mustGet(t, f, "1"), "Règle")
	deskWith(t, f, "idle")
	reps, err := f.a.Ingest(nil, connector.PollOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range reps {
		if r.Source == "escalations" && r.Events == 1 {
			return
		}
	}
	t.Fatalf("ingest delivered nothing: %+v", reps)
}

func TestStarMarksADossierButNotTheDesk(t *testing.T) {
	f := newFixture(t)
	f.a.Open(OpenParams{Title: "Affaire", NoStart: true})
	d := mustGet(t, f, "1")
	if err := f.a.Star(d, true); err != nil {
		t.Fatal(err)
	}
	if d = mustGet(t, f, "1"); !d.Starred {
		t.Fatal("the star is not saved")
	}
	f.a.Star(d, false)
	if d = mustGet(t, f, "1"); d.Starred {
		t.Fatal("unstar kept the star")
	}
	if err := f.a.Star(f.a.Desk(), true); err == nil {
		t.Fatal("the desk took a star")
	}
}

func TestResolveTellsTheDossierAndFilesTheEscalation(t *testing.T) {
	f := newFixture(t)
	f.a.Open(OpenParams{Title: "Citations", NoStart: true})
	f.a.Open(OpenParams{Title: "Skills", NoStart: true})
	deskWith(t, f, "idle")
	f.a.Escalate(mustGet(t, f, "1"), "Règle des citations")
	deskWith(t, f, "working")
	f.a.Escalate(mustGet(t, f, "2"), "Skill compta")
	open := OpenEscalations(f.a.Desk())
	if len(open) != 2 || open[0].Status != escalationDelivered || open[1].Status != escalationPending {
		t.Fatalf("open %+v", open)
	}
	if _, err := f.a.Resolve("D-0003", "x"); err == nil {
		t.Fatal("resolved an escalation nobody sent")
	}
	if _, err := f.a.Resolve("D-0001", " "); err == nil {
		t.Fatal("resolved without a decision")
	}
	res, err := f.a.Resolve("D-0001", "Règle adoptée.")
	if err != nil || res.From != "D-0001" || res.Open != 1 {
		t.Fatalf("resolve %+v %v", res, err)
	}
	if res, err := f.a.Resolve(strings.TrimSuffix(open[1].File, ".md"), "Lien ajouté."); err != nil || res.Open != 0 {
		t.Fatalf("resolve by file %+v %v", res, err)
	}
	done := ResolvedEscalations(f.a.Desk())
	if len(done) != 2 || !strings.Contains(done[0].Text, "Decision (") || !strings.Contains(done[0].Text, "Règle adoptée.") {
		t.Fatalf("resolved %+v", done)
	}
	if log, _ := os.ReadFile(mustGet(t, f, "1").Path("log.md")); !strings.Contains(string(log), "Decision on your escalation: Règle adoptée.") {
		t.Fatalf("dossier log:\n%s", log)
	}
	if _, err := f.a.Resolve("D-0001", "encore"); err == nil {
		t.Fatal("resolved twice")
	}
}

func TestStartGivesAStoppedDossierItsSession(t *testing.T) {
	f := newFixture(t)
	f.a.Open(OpenParams{Title: "Citations", NoStart: true})
	d := mustGet(t, f, "1")
	how, err := f.a.Start(d)
	if err != nil || how != "started" || d.Run.Session == "" || len(f.calls("session/prompt")) != 1 {
		t.Fatalf("start %q %v %+v", how, err, d.Run)
	}
}

func TestTellUserThreadBringsTheReplyBackToTheDesk(t *testing.T) {
	f := newFixture(t)
	deskWith(t, f, "idle")
	res, err := f.a.TellUser(f.a.Desk(), "", "Briefing du jour", nil)
	if err != nil || res.Source != "fake" || len(res.Threads) != 1 {
		t.Fatalf("tell %+v %v", res, err)
	}
	if d := f.a.FindByThread("fake:thread/told/thread-reply"); d == nil || !IsDesk(d) {
		t.Fatalf("the desk does not hold the thread: %v", d)
	}
	if _, err := f.a.Ingest(nil, connector.PollOptions{}); err != nil {
		t.Fatal(err)
	}
	if log, _ := os.ReadFile(f.a.Desk().Path("log.md")); !strings.Contains(string(log), "event: ") {
		t.Fatalf("the reply did not reach the desk:\n%s", log)
	}
	found := false
	for _, p := range f.calls("session/prompt") {
		found = found || strings.Contains(fmt.Sprint(p), "reply.md")
	}
	if !found {
		t.Fatal("the desk was not prompted with the reply")
	}
	if _, err := f.a.TellUser(f.a.Desk(), "", " ", nil); err == nil {
		t.Fatal("an empty message was sent")
	}
	if _, err := f.a.TellUser(f.a.Desk(), "nope", "x", nil); err == nil {
		t.Fatal("an unknown source was accepted")
	}
}

func TestNotifyReachesAnotherOfficeByItsPrefix(t *testing.T) {
	f := newFixture(t)
	root := filepath.Join(filepath.Dir(f.a.S.Root), "pro")
	if _, err := office.Init(root, "pro", false); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(root, ".office", "config.toml")
	b, _ := os.ReadFile(cfg)
	os.WriteFile(cfg, []byte(strings.Replace(string(b), "[office]", "[office]\nid_prefix = \"U\"", 1)), 0o644)
	f.a.Open(OpenParams{Title: "Briefing", NoStart: true})
	from := mustGet(t, f, "1")

	b2, desk, err := f.a.LoadAcross("U-DESK")
	if err != nil || b2 == f.a || !IsDesk(desk) || desk.ID != "U-DESK" {
		t.Fatalf("across %v %+v %v", b2 == f.a, desk, err)
	}
	if res, err := b2.Escalate(from, "Briefing pro, s'il te plaît"); err != nil || res.To != "U-DESK" {
		t.Fatalf("escalate across %+v %v", res, err)
	}
	if e := Escalations(b2.Desk()); len(e) != 1 || e[0].From != "D-0001" {
		t.Fatalf("pro desk escalations %+v", e)
	}
	if e := Escalations(f.a.Desk()); len(e) != 0 {
		t.Fatal("the escalation landed in the wrong office")
	}
	if same, d, err := f.a.LoadAcross("D-0001"); err != nil || same != f.a || d.ID != "D-0001" {
		t.Fatalf("own id %v %v", d, err)
	}
	if _, _, err := f.a.LoadAcross("X-0001"); err == nil {
		t.Fatal("an unknown prefix resolved")
	}
}

func TestASourceCanHandItsItemsToTheDesk(t *testing.T) {
	f := newFixture(t)
	deskWith(t, f, "idle")
	f.a.S.Config.Sources[0].Signals = "desk"
	reps, err := f.a.Ingest(nil, connector.PollOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if all, _ := f.a.All(); len(all) != 0 {
		t.Fatalf("a dossier was opened: %d", len(all))
	}
	got := false
	for _, r := range reps {
		for _, o := range r.Opened {
			got = got || (o.ID == f.a.DeskID() && o.Outcome == "desk")
		}
	}
	if !got {
		t.Fatalf("the desk did not get the item: %+v", reps)
	}
	if d := f.a.FindByThread("fake:thread/thread-reply"); d == nil || !IsDesk(d) {
		t.Fatal("the item's thread should belong to the desk")
	}
}

func TestPromptsAskToBeQueuedUnlessNow(t *testing.T) {
	f := newFixture(t)
	f.a.Open(OpenParams{Title: "Briefing", NoStart: true})
	d := mustGet(t, f, "1")
	if err := f.a.Prompt(d, "après le tour"); err != nil {
		t.Fatal(err)
	}
	f.a.Now = true
	if err := f.a.Prompt(d, "tout de suite"); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range f.calls("session/prompt") {
		meta, _ := p["params"].(map[string]any)["_meta"].(map[string]any)
		got = append(got, fmt.Sprint(meta["delivery"]))
	}
	if strings.Join(got, ",") != "queue,now" {
		t.Fatalf("deliveries %v", got)
	}
}

func TestTheDeskTakesAnItemOnce(t *testing.T) {
	f := newFixture(t)
	deskWith(t, f, "idle")
	s := connector.Signal{SourceRef: "telegram:message/1/18", ThreadRef: "telegram:chat/1/18", Title: "Vocal"}
	if r, err := f.a.toDesk(s); err != nil || r.Outcome != "desk" {
		t.Fatalf("first %+v %v", r, err)
	}
	if r, err := f.a.toDesk(s); err != nil || r.Outcome != "existing" {
		t.Fatalf("the same item reached the desk twice: %+v %v", r, err)
	}
}

func TestAPlaceholderAnswersAnItemUntilTellReplacesIt(t *testing.T) {
	f := newFixture(t)
	deskWith(t, f, "idle")
	f.a.S.Config.Sources[0].Signals = "desk"
	if _, err := f.a.Ingest(nil, connector.PollOptions{}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(f.a.S.Meta("run", "progress.json"))
	if !strings.Contains(string(b), "fake:message/ph1") || !strings.Contains(string(b), f.a.DeskID()) {
		t.Fatalf("no placeholder for the item:\n%s", b)
	}
	if _, err := f.a.TellUser(f.a.Desk(), "", "Réponse", nil); err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	for _, c := range testutil.Calls(f.srcLog) {
		if c["verb"] == "send" {
			sent = c["input"].(map[string]any)
		}
	}
	if fmt.Sprint(sent["replace"]) != "[fake:message/ph1]" {
		t.Fatalf("tell did not replace the placeholder: %v", sent["replace"])
	}
	if b, _ := os.ReadFile(f.a.S.Meta("run", "progress.json")); strings.Contains(string(b), "ph1") {
		t.Fatal("the placeholder stayed open after tell")
	}
}
