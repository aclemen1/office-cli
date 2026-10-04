package office

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	sectionRe = regexp.MustCompile(`(?m)^\[[^\]]+\]\s*$`)
	skillsRe  = regexp.MustCompile(`(?ms)^skills\s*=\s*\[.*?\]`)
)

// SetSkills rewrites [agent] skills in config.toml and leaves every other
// line, comments included, as it is.
func (s *Office) SetSkills(paths []string) error {
	p := s.Meta("config.toml")
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	text := string(b)
	var arr strings.Builder
	arr.WriteString("skills = [\n")
	for _, x := range paths {
		fmt.Fprintf(&arr, "  %q,\n", x)
	}
	arr.WriteString("]")

	start := strings.Index(text, "[agent]")
	if start < 0 {
		text = strings.TrimRight(text, "\n") + "\n\n[agent]\n" + arr.String() + "\n"
	} else {
		body := start + len("[agent]")
		end := len(text)
		if loc := sectionRe.FindStringIndex(text[body:]); loc != nil {
			end = body + loc[0]
		}
		section := text[body:end]
		if loc := skillsRe.FindStringIndex(section); loc != nil {
			section = section[:loc[0]] + arr.String() + section[loc[1]:]
		} else {
			section = "\n" + arr.String() + section
		}
		text = text[:body] + section + text[end:]
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		return err
	}
	cfg, err := loadConfig(p)
	if err != nil {
		return fmt.Errorf("config.toml no longer parses after the skills change: %w", err)
	}
	s.Config = cfg
	return nil
}

// TildePath writes a path under the home directory as ~/….
func TildePath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
		return "~/" + rel
	}
	return p
}
