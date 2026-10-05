package app

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/aclemen1/office-cli/internal/acp"
	"github.com/aclemen1/office-cli/internal/connector"
	"github.com/aclemen1/office-cli/internal/office"
)

// listenWait is how long a connector's wait may block before it answers.
const listenWait = 50

// Listen watches every source that declares wait, in each office, and
// ingests a source as soon as it holds something new. Sources without wait
// are left to the periodic ingest. It runs until stop closes; each line of
// out says what happened.
func Listen(apps []*App, out io.Writer, stop <-chan struct{}) error {
	var mu sync.Mutex
	say := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(out, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
	}
	progressSay = say
	var wg sync.WaitGroup
	for _, a := range apps {
		s := a.S
		wg.Add(1)
		go func(a *App) {
			defer wg.Done()
			a.listenProgress(stop)
		}(a)
		for _, src := range s.Config.Sources {
			d, err := (connector.Runner{Office: s, Source: src}).Describe()
			if err != nil || !contains(d.Verbs, "wait") {
				continue
			}
			wg.Add(1)
			served := contains(d.Verbs, "serve")
			go func(src office.SourceConfig) {
				defer wg.Done()
				a.listenSource(src, served, say, stop)
			}(src)
			say("%s: listening to %s", a.OfficeName(), src.Name)
		}
	}
	wg.Wait()
	return nil
}

func (a *App) listenSource(src office.SourceConfig, served bool, say func(string, ...any), stop <-chan struct{}) {
	runner := connector.Runner{Office: a.S, Source: src}
	backoff := 5 * time.Second
	var srv *connector.Server
	defer func() {
		if srv != nil {
			srv.Close()
		}
	}()
	for {
		select {
		case <-stop:
			return
		default:
		}
		// A connector that can serve keeps one process for all of this
		// process's calls; it is started again if it ends.
		if served && (srv == nil || srv.Gone()) {
			s, err := connector.Serve(a.S, src)
			if err != nil {
				say("%s/%s: %v", a.OfficeName(), src.Name, err)
			} else {
				if srv != nil {
					say("%s/%s: server started again", a.OfficeName(), src.Name)
				}
				srv = s
			}
		}
		// The source lock keeps this wait apart from an ingest of the same source: a
		// chat bot answers only one long poll at a time.
		unlock, err := a.S.LockSource(src.Name)
		if err != nil {
			say("%s/%s: %v", a.OfficeName(), src.Name, err)
			time.Sleep(backoff)
			continue
		}
		ready, err := runner.Wait(a.cursors()[src.Name], listenWait)
		unlock()
		if err != nil && strings.Contains(err.Error(), "timed out") {
			continue
		}
		if err != nil {
			say("%s/%s: %v (retry in %s)", a.OfficeName(), src.Name, err, backoff)
			select {
			case <-stop:
				return
			case <-time.After(backoff):
			}
			if backoff < 5*time.Minute {
				backoff *= 2
			}
			continue
		}
		backoff = 5 * time.Second
		if !ready {
			continue
		}
		reps, err := a.Ingest([]string{src.Name}, connector.PollOptions{Now: true})
		if err != nil {
			say("%s/%s: ingest: %v", a.OfficeName(), src.Name, err)
			continue
		}
		for _, r := range reps {
			var got []string
			for _, o := range r.Opened {
				got = append(got, o.ID+" "+o.Outcome)
			}
			line := fmt.Sprintf("%s/%s: %d signal(s), %d event(s)", a.OfficeName(), r.Source, r.Signals, r.Events)
			if len(got) > 0 {
				line += " · " + strings.Join(got, ", ")
			}
			if len(r.Errors) > 0 {
				line += " · errors: " + strings.Join(r.Errors, "; ")
			}
			say("%s", line)
		}
	}
}

// listenProgress keeps the office's placeholders up to date.
func (a *App) listenProgress(stop <-chan struct{}) {
	var conn *acp.Client
	defer func() {
		if conn != nil {
			conn.Close()
		}
	}()
	for {
		select {
		case <-stop:
			return
		case <-time.After(progressEvery):
		}
		a.followProgress(&conn)
	}
}
