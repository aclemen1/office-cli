package app

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func TestListenIngestsASourceThatHasNews(t *testing.T) {
	f := newFixture(t)
	var out syncBuf
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { Listen([]*App{f.a}, &out, stop); close(done) }()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if all, _ := f.a.All(); len(all) == 1 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	close(stop)
	<-done
	if all, _ := f.a.All(); len(all) != 1 {
		t.Fatalf("listen did not ingest:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "listening to fake") {
		t.Fatalf("log:\n%s", out.String())
	}
}
