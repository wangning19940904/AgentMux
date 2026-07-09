package skills

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

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

func TestValidateGitHubRequestRejectsUnsafePath(t *testing.T) {
	if _, _, err := validateGitHubRequest(InstallRequest{Repo: "owner/repo", Path: "../skill"}); err == nil {
		t.Fatal("expected unsafe path error")
	}
	if _, _, err := validateGitHubRequest(InstallRequest{Repo: "not a repo", Path: "skills/demo"}); err == nil {
		t.Fatal("expected repo format error")
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
