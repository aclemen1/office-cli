package office

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/aclemen1/office-cli/internal/spec"
)

const metaDir = ".office"

type Office struct {
	Root   string
	Config Config
	lock   *os.File
}

func (s *Office) Meta(parts ...string) string {
	return filepath.Join(append([]string{s.Root, metaDir}, parts...)...)
}

type userConfig struct {
	DefaultOffice string `toml:"default_office"`
}

func userConfigPath() string {
	dir := ExpandHome("~/.config")
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		dir = xdg
	}
	return filepath.Join(dir, "office", "config.toml")
}

// Resolve finds the office: --office, then OFFICE_DIR, then the office that
// contains the working directory, then default_office.
// A --office without a path separator that is no directory names a sibling of
// the default office by its sphere or directory name: --office pro.
func Resolve(flag string) (*Office, error) {
	if flag != "" && !strings.ContainsRune(flag, filepath.Separator) {
		if _, err := os.Stat(flag); err != nil {
			base, err := resolveDefault()
			if err != nil {
				return nil, err
			}
			for _, dir := range Discover(filepath.Dir(base.Root)) {
				if s, err := Open(dir); err == nil && (strings.EqualFold(s.Config.Office.Sphere, flag) || strings.EqualFold(filepath.Base(dir), flag)) {
					return s, nil
				}
			}
			return nil, spec.NotFound("no office named %q next to %s. List them with `office offices`", flag, base.Root)
		}
	}
	if flag != "" {
		return Open(ExpandHome(flag))
	}
	return resolveDefault()
}

// Sibling is the office next to s whose dossiers carry this id prefix.
func (s *Office) Sibling(prefix string) *Office {
	for _, dir := range Discover(filepath.Dir(s.Root)) {
		if o, err := Open(dir); err == nil && strings.EqualFold(o.Prefix(), prefix) {
			return o
		}
	}
	return nil
}

func resolveDefault() (*Office, error) {
	if c := os.Getenv("OFFICE_DIR"); c != "" {
		return Open(ExpandHome(c))
	}
	if wd, err := os.Getwd(); err == nil {
		for d := wd; ; d = filepath.Dir(d) {
			if fi, err := os.Stat(filepath.Join(d, metaDir, "config.toml")); err == nil && !fi.IsDir() {
				return Open(d)
			}
			if filepath.Dir(d) == d {
				break
			}
		}
	}
	var uc userConfig
	if _, err := toml.DecodeFile(userConfigPath(), &uc); err == nil && uc.DefaultOffice != "" {
		return Open(ExpandHome(uc.DefaultOffice))
	}
	return nil, spec.NotFound("no office found. Create one with `office init ~/offices/perso --sphere perso --default`, or pass --office <path>")
}

func Open(root string) (*Office, error) {
	root, _ = filepath.Abs(root)
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	cfgPath := filepath.Join(root, metaDir, "config.toml")
	if _, err := os.Stat(cfgPath); err != nil {
		return nil, spec.NotFound("%s is not a office (no %s/config.toml). Create it with `office init %s --sphere <name>`", root, metaDir, root)
	}
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		return nil, spec.UserError("cannot read %s: %v", cfgPath, err)
	}
	return &Office{Root: root, Config: cfg}, nil
}

func Init(root, sphere string, makeDefault bool) (*Office, error) {
	root, _ = filepath.Abs(ExpandHome(root))
	if _, err := os.Stat(filepath.Join(root, metaDir)); err == nil {
		return nil, spec.UserError("%s already holds a office (%s/ exists)", root, metaDir)
	}
	sub := func(t string) string { return strings.ReplaceAll(t, "{{sphere}}", sphere) }
	files := map[string]string{
		filepath.Join(metaDir, "config.toml"):            sub(configTemplate),
		filepath.Join(metaDir, "prompts", "open.md"):     openPromptTemplate,
		filepath.Join(metaDir, "prompts", "event.md"):    eventPromptTemplate,
		filepath.Join(metaDir, "prompts", "deadline.md"): deadlinePromptTemplate,
		"index.md":   sub(indexTemplate),
		"CLAUDE.md":  sub(charterTemplate),
		".gitignore": gitignoreTemplate,
	}
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if _, err := os.Stat(p); err == nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			return nil, err
		}
	}
	if makeDefault {
		p := userConfigPath()
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(fmt.Sprintf("default_office = %q\n", root)), 0o644); err != nil {
			return nil, err
		}
	}
	return Open(root)
}

// Lock takes the office-wide lock for mutating operations.
// LockWait is how long Lock waits for another command to release the office.
var LockWait = 30 * time.Second

func (s *Office) Lock() error {
	f, err := os.OpenFile(s.Meta("lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	// Wait a while for the other command: a TUI dock should not fail because
	// an ingest or an agent's tool call holds the office for a moment.
	deadline := time.Now().Add(LockWait)
	for syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		if time.Now().After(deadline) {
			f.Close()
			return spec.Locked("the office %s is busy with another dossier command; retry in a moment", s.Root)
		}
		time.Sleep(100 * time.Millisecond)
	}
	s.lock = f
	return nil
}

func (s *Office) Unlock() {
	if s.lock != nil {
		_ = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
		s.lock.Close()
		s.lock = nil
	}
}

var dirRe = regexp.MustCompile(`^(\d{4,})-`)

// Dirs lists dossier directories, oldest first.
func (s *Office) Dirs() ([]string, error) {
	entries, err := os.ReadDir(s.Root)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && dirRe.MatchString(e.Name()) {
			out = append(out, filepath.Join(s.Root, e.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}

// NewDir allocates the next number and creates its directory atomically.
func (s *Office) NewDir(slug string) (num int, dir string, err error) {
	dirs, err := s.Dirs()
	if err != nil {
		return 0, "", err
	}
	next := 1
	for _, d := range dirs {
		if m := dirRe.FindStringSubmatch(filepath.Base(d)); m != nil {
			if n, _ := strconv.Atoi(m[1]); n >= next {
				next = n + 1
			}
		}
	}
	for tries := 0; tries < 100; tries++ {
		dir = filepath.Join(s.Root, fmt.Sprintf("%04d-%s", next, slug))
		if err = os.Mkdir(dir, 0o755); err == nil {
			return next, dir, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return 0, "", err
		}
		next++
	}
	return 0, "", err
}

var idRe = regexp.MustCompile(`(?i)^(?:([a-z]{1,4})-?)?0*(\d+)(?:-.*)?$`)

// Prefix is the office's id prefix: P for P-0042. D when the config sets none.
func (s *Office) Prefix() string {
	if p := strings.TrimSpace(s.Config.Office.IDPrefix); p != "" {
		return strings.ToUpper(p)
	}
	return "D"
}

// FormatID renders a dossier number with the office's prefix.
func (s *Office) FormatID(n int) string { return fmt.Sprintf("%s-%04d", s.Prefix(), n) }

// FindDir resolves 42, P-42, P-0042 or 0042-slug to a directory. An id with
// another office's prefix is refused: the spheres stay apart.
func (s *Office) FindDir(id string) (string, error) {
	m := idRe.FindStringSubmatch(strings.TrimSpace(id))
	ex := s.FormatID(42)
	if m == nil {
		return "", spec.UserError("%q is not a dossier id. Use %s, 42 or 0042-slug, for example `office show %s`", id, ex, ex)
	}
	if m[1] != "" && !strings.EqualFold(m[1], s.Prefix()) {
		return "", spec.UserError("%s belongs to another office: this office (%s) numbers its dossiers %s-…. Pass --office for the other one", strings.TrimSpace(id), s.Root, s.Prefix())
	}
	want, _ := strconv.Atoi(m[2])
	dirs, err := s.Dirs()
	if err != nil {
		return "", err
	}
	for _, d := range dirs {
		if n, _ := strconv.Atoi(dirRe.FindStringSubmatch(filepath.Base(d))[1]); n == want {
			return d, nil
		}
	}
	return "", spec.NotFound("no dossier %s in %s. List them with `office ls --status all`", s.FormatID(want), s.Root)
}

func NumberOf(dir string) int {
	if m := dirRe.FindStringSubmatch(filepath.Base(dir)); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

// ResolvePath resolves a path from the config relative to the office root.
func (s *Office) ResolvePath(p string) string {
	p = ExpandHome(p)
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(s.Root, p)
}

func (s *Office) PromptTemplate(name string) (string, error) {
	rel := map[string]string{"open": s.Config.Prompt.Open, "event": s.Config.Prompt.Event, "deadline": s.Config.Prompt.Deadline}[name]
	if rel == "" {
		rel = filepath.Join("prompts", name+".md")
	}
	p := rel
	if !filepath.IsAbs(p) {
		p = s.Meta(rel)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", spec.UserError("cannot read prompt template %s: %v", p, err)
	}
	return string(b), nil
}

// Discover lists the offices under root: root itself when it is one, otherwise
// every direct subdirectory that holds a .office/config.toml.
func Discover(root string) []string {
	if isOffice(root) {
		return []string{root}
	}
	entries, _ := os.ReadDir(root)
	var out []string
	for _, e := range entries {
		p := filepath.Join(root, e.Name())
		if e.IsDir() && isOffice(p) {
			out = append(out, p)
		}
	}
	return out
}

func isOffice(p string) bool {
	fi, err := os.Stat(filepath.Join(p, ".office", "config.toml"))
	return err == nil && !fi.IsDir()
}
