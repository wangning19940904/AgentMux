package skills

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type memorySkillStateStore struct {
	mu     sync.Mutex
	states map[string]bool
}

func (s *memorySkillStateStore) ListSkillStates(context.Context) (map[string]bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]bool, len(s.states))
	for name, enabled := range s.states {
		out[name] = enabled
	}
	return out, nil
}

func (s *memorySkillStateStore) SetSkillEnabled(_ context.Context, name string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states[name] = enabled
	return nil
}

func (s *memorySkillStateStore) DeleteSkillState(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.states, name)
	return nil
}

func TestInstallCopiesLocalSkillIntoWriteRoot(t *testing.T) {
	srcRoot := t.TempDir()
	src := filepath.Join(srcRoot, "demo")
	writeSkill(t, src, "demo", "Local demo skill")
	dstRoot := t.TempDir()

	m := New(dstRoot)
	got, err := m.Install(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "demo" || !got.Enabled {
		t.Fatalf("skill = %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dstRoot, "demo", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
}

func TestMarketplaceSearchMarksInstalled(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, filepath.Join(root, "pdf"), "pdf", "Installed PDF")
	m := New(root)

	items, err := m.Marketplace(context.Background(), "pdf", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 {
		t.Fatal("expected marketplace items")
	}
	if !items[0].Installed {
		t.Fatalf("expected installed marker: %+v", items[0])
	}
}

func TestMarketplaceIncludesAgentBrowserSkill(t *testing.T) {
	m := New(t.TempDir())

	items, err := m.Marketplace(context.Background(), "agent-browser", "vercel-labs", "browser")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %+v", items)
	}
	item := items[0]
	if item.Repo != "vercel-labs/agent-browser" || item.Path != "skills/agent-browser" || !item.Trusted {
		t.Fatalf("item = %+v", item)
	}
}

func TestValidateGitHubRequestRejectsUnsafePath(t *testing.T) {
	if _, _, err := validateGitHubRequest(InstallRequest{Repo: "owner/repo", Path: "../skill"}); err == nil {
		t.Fatal("expected unsafe path error")
	}
	if _, _, err := validateGitHubRequest(InstallRequest{Repo: "not a repo", Path: "skills/demo"}); err == nil {
		t.Fatal("expected repo format error")
	}
}

func TestUninstallRemovesManagedSkill(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "demo")
	writeSkill(t, dir, "demo", "Managed demo")
	m := New(root)

	if err := m.Uninstall(context.Background(), "demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("skill directory still exists: %v", err)
	}
	if err := m.Uninstall(context.Background(), "demo"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing skill error = %v", err)
	}
}

func TestManagedSkillDirRejectsSibling(t *testing.T) {
	parent := t.TempDir()
	m := New(filepath.Join(parent, "managed"))
	managed, err := m.isManagedSkillDir(filepath.Join(parent, "outside", "demo"))
	if err != nil {
		t.Fatal(err)
	}
	if managed {
		t.Fatal("sibling directory was treated as managed")
	}
}

func TestConcurrentListAndToggle(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, filepath.Join(root, "demo"), "demo", "Concurrent demo")
	m := New(root)

	var group sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		group.Add(1)
		go func(worker int) {
			defer group.Done()
			for iteration := 0; iteration < 100; iteration++ {
				if worker%2 == 0 {
					if _, err := m.List(context.Background()); err != nil {
						t.Errorf("List: %v", err)
					}
					continue
				}
				if err := m.SetEnabled(context.Background(), "demo", iteration%2 == 0); err != nil {
					t.Errorf("SetEnabled: %v", err)
				}
			}
		}(worker)
	}
	group.Wait()
}

func TestPersistentEnablementSurvivesManagerRestart(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, filepath.Join(root, "demo"), "demo", "Persistent demo")
	state := &memorySkillStateStore{states: map[string]bool{}}
	first := NewPersistent(state, root)
	if err := first.SetEnabled(context.Background(), "demo", false); err != nil {
		t.Fatal(err)
	}
	second := NewPersistent(state, root)
	items, err := second.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Enabled {
		t.Fatalf("items = %+v", items)
	}
}

func writeSkill(t *testing.T, dir, name, description string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: " + description + "\n---\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
