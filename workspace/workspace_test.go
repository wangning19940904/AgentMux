package workspace

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/agentnexus/agentnexus/core"
)

func TestInitializeClaudeWorkspaceCreatesNativeStructure(t *testing.T) {
	root := t.TempDir()
	skillRoot := filepath.Join(t.TempDir(), "skills")
	writeSkill(t, skillRoot, "code-review")

	init := New(skillRoot)
	res, err := init.InitializeWorkspace(context.Background(), core.WorkspaceInitOptions{
		AgentID:   "agent-1",
		RuntimeID: "claudecode",
		WorkDir:   root,
		Skills:    []string{"code-review"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.WorkDir != root {
		t.Fatalf("work_dir = %q, want %q", res.WorkDir, root)
	}
	assertDir(t, filepath.Join(root, ".agentnexus"))
	assertDir(t, filepath.Join(root, ".claude", "skills"))
	assertFile(t, filepath.Join(root, "CLAUDE.md"))
	assertFile(t, filepath.Join(root, ".claude", "skills", "code-review", "SKILL.md"))

	var meta workspaceFile
	data, err := os.ReadFile(filepath.Join(root, ".agentnexus", "workspace.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.RuntimeID != "claudecode" || meta.AgentID != "agent-1" || len(meta.Skills) != 1 {
		t.Fatalf("metadata = %+v", meta)
	}
}

func TestInitializeCodexWorkspaceDoesNotOverwritePrompt(t *testing.T) {
	root := t.TempDir()
	promptPath := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(promptPath, []byte("custom"), 0o644); err != nil {
		t.Fatal(err)
	}
	init := New(filepath.Join(t.TempDir(), "skills"))
	if _, err := init.InitializeWorkspace(context.Background(), core.WorkspaceInitOptions{
		RuntimeID: "codex",
		WorkDir:   root,
	}); err != nil {
		t.Fatal(err)
	}
	assertDir(t, filepath.Join(root, ".agents", "skills"))
	data, err := os.ReadFile(promptPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "custom" {
		t.Fatalf("AGENTS.md overwritten: %q", string(data))
	}
}

func TestInitializeEmptyWorkDirUsesCurrentDirectory(t *testing.T) {
	root := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })

	res, err := New(filepath.Join(t.TempDir(), "skills")).InitializeWorkspace(context.Background(), core.WorkspaceInitOptions{
		RuntimeID: "codex",
	})
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(root)
	got, _ := filepath.EvalSymlinks(res.WorkDir)
	if got != want {
		t.Fatalf("work_dir = %q, want cwd %q", res.WorkDir, root)
	}
	assertFile(t, filepath.Join(root, "AGENTS.md"))
}

func TestCopyDirFallbackHelperCopiesSkillTree(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src")
	writeSkill(t, filepath.Dir(src), filepath.Base(src))
	if err := os.WriteFile(filepath.Join(src, "references.md"), []byte("ref"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "dst")
	if err := copyDir(src, dst); err != nil {
		t.Fatal(err)
	}
	assertFile(t, filepath.Join(dst, "SKILL.md"))
	assertFile(t, filepath.Join(dst, "references.md"))
}

func writeSkill(t *testing.T, root, name string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: test skill\n---\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertDir(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		t.Fatalf("dir %s: info=%v err=%v", path, info, err)
	}
}

func assertFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		t.Fatalf("file %s: info=%v err=%v", path, info, err)
	}
}
