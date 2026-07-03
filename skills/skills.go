// Package skills implements AgentNexus Skills: unified discovery, installation
// and management of Agent Skills. The default "fs" provider discovers
// SKILL.md files under one or more roots (e.g. ~/.agentnexus/skills); other
// providers (git, registry) can register via core.RegisterSkillManager.
package skills

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/agentnexus/agentnexus/core"
)

func init() {
	core.RegisterSkillManager("fs", func(cfg map[string]any) (core.SkillManager, error) {
		var roots []string
		if r, ok := cfg["roots"].([]any); ok {
			for _, v := range r {
				if s, ok := v.(string); ok {
					roots = append(roots, expand(s))
				}
			}
		}
		return New(roots...), nil
	})
}

// FSManager discovers skills from SKILL.md files on disk.
type FSManager struct {
	roots    []string
	disabled map[string]bool
}

var _ core.SkillManager = (*FSManager)(nil)

// New builds a filesystem skill manager. With no roots it defaults to
// ~/.agentnexus/skills.
func New(roots ...string) *FSManager {
	if len(roots) == 0 {
		home, _ := os.UserHomeDir()
		roots = []string{filepath.Join(home, ".agentnexus", "skills")}
	}
	return &FSManager{roots: roots, disabled: map[string]bool{}}
}

// Name returns the provider id.
func (m *FSManager) Name() string { return "fs" }

// List scans the roots for SKILL.md files and returns discovered skills.
func (m *FSManager) List(ctx context.Context) ([]core.Skill, error) {
	var out []core.Skill
	seen := map[string]bool{}
	for _, root := range m.roots {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if strings.EqualFold(d.Name(), "SKILL.md") {
				s := parseSkill(path)
				if s.Name != "" && !seen[s.Name] {
					seen[s.Name] = true
					s.Enabled = !m.disabled[s.Name]
					s.Source = "local"
					out = append(out, s)
				}
			}
			return nil
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Install is a stub for the filesystem provider: it validates that ref points
// to a directory containing a SKILL.md and returns its parsed metadata.
// Network-backed installation belongs to a git/registry provider.
func (m *FSManager) Install(ctx context.Context, ref string) (*core.Skill, error) {
	p := filepath.Join(expand(ref), "SKILL.md")
	if _, err := os.Stat(p); err != nil {
		return nil, fmt.Errorf("skills: no SKILL.md at %s: %w", ref, err)
	}
	s := parseSkill(p)
	s.Source = "local"
	s.Enabled = true
	return &s, nil
}

// SetEnabled toggles a skill by name (in-memory for the fs provider).
func (m *FSManager) SetEnabled(ctx context.Context, name string, enabled bool) error {
	m.disabled[name] = !enabled
	return nil
}

// parseSkill reads name + description from a SKILL.md front-matter-ish header.
func parseSkill(path string) core.Skill {
	s := core.Skill{Path: path, Name: filepath.Base(filepath.Dir(path))}
	f, err := os.Open(path)
	if err != nil {
		return s
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "name:"):
			s.Name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
		case strings.HasPrefix(line, "description:"):
			s.Description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
		case strings.HasPrefix(line, "# ") && s.Description == "":
			s.Description = strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return s
}

func expand(p string) string {
	if strings.HasPrefix(p, "~") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	return p
}
