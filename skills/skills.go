// Package skills implements AgentMux Skills: unified discovery, installation
// and management of Agent Skills. The default "fs" provider discovers
// SKILL.md files under one or more roots (e.g. ~/.agentmux/skills); other
// providers can implement core.SkillManager and be injected by the composition root.
package skills

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/wangning19940904/AgentMux/core"
)

// FSManager discovers skills from SKILL.md files on disk.
type FSManager struct {
	roots    []string
	state    StateStore
	stateMu  sync.RWMutex
	disabled map[string]bool
}

type StateStore interface {
	ListSkillStates(context.Context) (map[string]bool, error)
	SetSkillEnabled(context.Context, string, bool) error
	DeleteSkillState(context.Context, string) error
}

var _ core.SkillManager = (*FSManager)(nil)

// New builds a filesystem skill manager. With no roots it defaults to
// ~/.agentmux/skills.
func New(roots ...string) *FSManager {
	return NewPersistent(nil, roots...)
}

// NewPersistent builds a filesystem manager whose enablement state survives
// daemon restarts in the supplied PostgreSQL store.
func NewPersistent(state StateStore, roots ...string) *FSManager {
	if len(roots) == 0 {
		roots = DefaultRoots()
	}
	return &FSManager{roots: roots, state: state, disabled: map[string]bool{}}
}

// DefaultRoots returns the global AgentMux skill roots. The first root is the
// write target; the legacy root is still scanned for compatibility.
func DefaultRoots() []string {
	home, _ := os.UserHomeDir()
	return []string{
		filepath.Join(home, ".agentmux", "tools", "skills"),
		filepath.Join(home, ".agentmux", "skills"),
	}
}

// Name returns the provider id.
func (m *FSManager) Name() string { return "fs" }

// List scans the roots for SKILL.md files and returns discovered skills.
func (m *FSManager) List(ctx context.Context) ([]core.Skill, error) {
	if m.state != nil {
		states, err := m.state.ListSkillStates(ctx)
		if err != nil {
			return nil, err
		}
		m.stateMu.Lock()
		m.disabled = make(map[string]bool, len(states))
		for name, enabled := range states {
			m.disabled[name] = !enabled
		}
		m.stateMu.Unlock()
	}
	m.stateMu.RLock()
	disabled := make(map[string]bool, len(m.disabled))
	for name, value := range m.disabled {
		disabled[name] = value
	}
	m.stateMu.RUnlock()
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
					s.Enabled = !disabled[s.Name]
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

// Install copies a local skill directory (must contain a SKILL.md) into the
// first managed root and returns its parsed metadata. Network-backed
// installation belongs to a git/registry provider.
func (m *FSManager) Install(ctx context.Context, ref string) (*core.Skill, error) {
	src := expand(ref)
	p := filepath.Join(src, "SKILL.md")
	if _, err := os.Stat(p); err != nil {
		return nil, fmt.Errorf("skills: no SKILL.md at %s: %w", ref, err)
	}
	s := ParseSkillFile(p)
	if s.Name == "" {
		s.Name = filepath.Base(src)
	}
	if len(m.roots) > 0 {
		dst := filepath.Join(m.roots[0], s.Name)
		if err := copyDir(src, dst); err != nil {
			return nil, err
		}
		s = ParseSkillFile(filepath.Join(dst, "SKILL.md"))
		if s.Name == "" {
			s.Name = filepath.Base(dst)
		}
	}
	s.Source = "local"
	s.Enabled = true
	if m.state != nil {
		if err := m.state.SetSkillEnabled(ctx, s.Name, true); err != nil {
			return nil, err
		}
	}
	return &s, nil
}

// SetEnabled toggles a skill by name. The fs provider keeps this state
// in-memory only, so toggles do not survive a daemon restart.
func (m *FSManager) SetEnabled(ctx context.Context, name string, enabled bool) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("skills: name is required")
	}
	if m.state != nil {
		if err := m.state.SetSkillEnabled(ctx, name, enabled); err != nil {
			return err
		}
	}
	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	m.disabled[name] = !enabled
	return nil
}

// Uninstall removes the discovered installation for name only when its
// directory is contained by one of the configured managed roots. The client
// supplies a logical name, never a filesystem path.
func (m *FSManager) Uninstall(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("skills: name is required")
	}
	items, err := m.List(ctx)
	if err != nil {
		return err
	}
	for _, skill := range items {
		if skill.Name != name {
			continue
		}
		dir := filepath.Dir(skill.Path)
		managed, err := m.isManagedSkillDir(dir)
		if err != nil {
			return err
		}
		if !managed {
			return fmt.Errorf("skills: refusing to remove %q outside managed roots", name)
		}
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("skills: remove %q: %w", name, err)
		}
		if m.state != nil {
			if err := m.state.DeleteSkillState(ctx, name); err != nil {
				return err
			}
		}
		m.stateMu.Lock()
		delete(m.disabled, name)
		m.stateMu.Unlock()
		return nil
	}
	return fmt.Errorf("skills: %q: %w", name, os.ErrNotExist)
}

func (m *FSManager) isManagedSkillDir(dir string) (bool, error) {
	candidate, err := filepath.Abs(dir)
	if err != nil {
		return false, fmt.Errorf("skills: resolve install path: %w", err)
	}
	for _, configuredRoot := range m.roots {
		root, err := filepath.Abs(configuredRoot)
		if err != nil {
			return false, fmt.Errorf("skills: resolve managed root: %w", err)
		}
		relative, err := filepath.Rel(root, candidate)
		if err != nil {
			continue
		}
		if relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true, nil
		}
	}
	return false, nil
}

// parseSkill reads name + description from a SKILL.md front-matter-ish header.
func parseSkill(path string) core.Skill {
	return ParseSkillFile(path)
}

// ParseSkillFile reads name + description from a SKILL.md front-matter-ish
// header.
func ParseSkillFile(path string) core.Skill {
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

func copyDir(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("skills: %s is not a directory", src)
	}
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == ".git" {
			continue
		}
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}
		if err := copyFile(srcPath, dstPath); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func expand(p string) string {
	if strings.HasPrefix(p, "~") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	return p
}
