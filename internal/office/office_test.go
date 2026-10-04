package office

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("OFFICE_DIR", "")
	return home
}

func TestInitWritesAnOKFBundle(t *testing.T) {
	isolate(t)
	root := filepath.Join(t.TempDir(), "perso")
	s, err := Init(root, "perso", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{".office/config.toml", ".office/prompts/open.md", ".office/prompts/event.md", "index.md", ".gitignore"} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Errorf("missing %s", rel)
		}
	}
	if s.Config.Office.Sphere != "perso" || len(s.Config.ACP.Command) == 0 || s.Config.ACP.Meta["interaction"] != "native" {
		t.Fatalf("config not loaded: %+v", s.Config)
	}
	idx, _ := os.ReadFile(filepath.Join(root, "index.md"))
	if !strings.Contains(string(idx), `okf_version: "0.2"`) {
		t.Fatalf("index.md has no okf_version: %s", idx)
	}
	if _, err := Init(root, "perso", false); err == nil {
		t.Fatal("second init should be refused")
	}
}

func TestResolveOrder(t *testing.T) {
	home := isolate(t)
	a, _ := Init(filepath.Join(t.TempDir(), "a"), "a", true)
	b, _ := Init(filepath.Join(t.TempDir(), "b"), "b", false)
	if _, err := os.Stat(filepath.Join(home, ".config", "office", "config.toml")); err != nil {
		t.Fatal("--default did not write the user config")
	}
	s, err := Resolve("")
	if err != nil || s.Root != a.Root {
		t.Fatalf("default office: %v %v", s, err)
	}
	t.Setenv("OFFICE_DIR", b.Root)
	if s, _ := Resolve(""); s.Root != b.Root {
		t.Fatalf("OFFICE_DIR ignored: %s", s.Root)
	}
	if s, _ := Resolve(a.Root); s.Root != a.Root {
		t.Fatalf("--office ignored: %s", s.Root)
	}
	t.Setenv("OFFICE_DIR", "")
	wd, _ := os.Getwd()
	defer os.Chdir(wd)
	sub := filepath.Join(b.Root, "0001-x", "files")
	os.MkdirAll(sub, 0o755)
	os.Chdir(sub)
	if s, _ := Resolve(""); s.Root != b.Root {
		t.Fatalf("working directory office ignored: %s", s.Root)
	}
}

func TestResolveWithoutOfficeExplainsInit(t *testing.T) {
	isolate(t)
	wd, _ := os.Getwd()
	defer os.Chdir(wd)
	os.Chdir(t.TempDir())
	_, err := Resolve("")
	if err == nil || !strings.Contains(err.Error(), "office init") {
		t.Fatalf("got %v", err)
	}
}

func TestNewDirIsAtomicAndFindDirAcceptsEveryForm(t *testing.T) {
	isolate(t)
	s, _ := Init(filepath.Join(t.TempDir(), "s"), "s", false)
	var wg sync.WaitGroup
	nums := make(chan int, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, _, err := s.NewDir("x")
			if err == nil {
				nums <- n
			}
		}()
	}
	wg.Wait()
	close(nums)
	seen := map[int]bool{}
	for n := range nums {
		if seen[n] {
			t.Fatalf("number %d allocated twice", n)
		}
		seen[n] = true
	}
	if len(seen) != 20 {
		t.Fatalf("allocated %d numbers", len(seen))
	}
	_, dir, _ := s.NewDir("armoire")
	for _, id := range []string{"21", "D-21", "d-0021", "D0021", "0021-armoire"} {
		got, err := s.FindDir(id)
		if err != nil || got != dir {
			t.Errorf("FindDir(%q) = %q, %v", id, got, err)
		}
	}
	if _, err := s.FindDir("99"); err == nil || !strings.Contains(err.Error(), "office ls") {
		t.Fatalf("unknown id: %v", err)
	}
	if _, err := s.FindDir("armoire"); err == nil {
		t.Fatal("a bare slug should not resolve")
	}
}

func TestLockIsExclusive(t *testing.T) {
	isolate(t)
	old := LockWait
	LockWait = 200 * time.Millisecond
	t.Cleanup(func() { LockWait = old })
	s, _ := Init(filepath.Join(t.TempDir(), "s"), "s", false)
	other, _ := Open(s.Root)
	if err := s.Lock(); err != nil {
		t.Fatal(err)
	}
	if err := other.Lock(); err == nil {
		t.Fatal("second lock should fail")
	}
	go func() { time.Sleep(50 * time.Millisecond); s.Unlock() }()
	if err := other.Lock(); err != nil {
		t.Fatalf("a lock released meanwhile should be taken: %v", err)
	}
	other.Unlock()
}

func TestExpandHome(t *testing.T) {
	home := isolate(t)
	if got := ExpandHome("~/x"); got != filepath.Join(home, "x") {
		t.Fatalf("got %s", got)
	}
	if got := ExpandHome("/a/~b"); got != "/a/~b" {
		t.Fatalf("got %s", got)
	}
}

func TestResolveNamesASiblingOfficeBySphere(t *testing.T) {
	root := t.TempDir()
	perso, _ := Init(filepath.Join(root, "perso"), "perso", false)
	if _, err := Init(filepath.Join(root, "pro"), "pro", false); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OFFICE_DIR", perso.Root)
	s, err := Resolve("pro")
	if err != nil || filepath.Base(s.Root) != "pro" {
		t.Fatalf("resolve pro: %v %v", s, err)
	}
	if _, err := Resolve("nope"); err == nil {
		t.Fatal("an unknown sphere was resolved")
	}
}
