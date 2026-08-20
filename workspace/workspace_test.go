package workspace

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
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
	assertDir(t, filepath.Join(root, ".agentmux"))
	assertDir(t, filepath.Join(root, ".claude", "skills"))
	assertFile(t, filepath.Join(root, "CLAUDE.md"))
	assertFile(t, filepath.Join(root, ".claude", "skills", "code-review", "SKILL.md"))

	var meta workspaceFile
	data, err := os.ReadFile(filepath.Join(root, ".agentmux", "workspace.json"))
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

func TestInitializeConversationWorktreeCreatesAndReusesStableWorkspace(t *testing.T) {
	repo := initGitRepository(t)
	init := New(filepath.Join(t.TempDir(), "skills"))
	opts := core.WorkspaceInitOptions{
		AgentID:           "agent-one",
		RuntimeID:         "codex",
		WorkDir:           repo,
		WorkspaceMode:     "worktree",
		ConversationScope: "channel:one",
		ConversationKey:   "root:message-one",
	}

	first, err := init.InitializeWorkspace(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if first.WorkDir == repo || first.WorkspaceMode != "worktree" {
		t.Fatalf("worktree result = %+v", first)
	}
	if first.WorktreeBranch == "" || !strings.HasPrefix(first.WorktreeBranch, "agentmux/") {
		t.Fatalf("worktree branch = %q", first.WorktreeBranch)
	}
	assertFile(t, filepath.Join(first.WorkDir, "AGENTS.md"))
	if got := gitRun(t, first.WorkDir, "branch", "--show-current"); got != first.WorktreeBranch {
		t.Fatalf("worktree current branch = %q, want %q", got, first.WorktreeBranch)
	}

	second, err := init.InitializeWorkspace(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if second.WorkDir != first.WorkDir || second.WorktreeBranch != first.WorktreeBranch {
		t.Fatalf("worktree was not reused: first=%+v second=%+v", first, second)
	}

	other := opts
	other.ConversationKey = "root:message-two"
	third, err := init.InitializeWorkspace(context.Background(), other)
	if err != nil {
		t.Fatal(err)
	}
	if third.WorkDir == first.WorkDir || third.WorktreeBranch == first.WorktreeBranch {
		t.Fatalf("different conversations shared a worktree: first=%+v third=%+v", first, third)
	}
}

func TestInitializeConversationWorktreePreservesConfiguredSubdirectory(t *testing.T) {
	repo := initGitRepository(t)
	subdir := filepath.Join(repo, "services", "api")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "README.md"), []byte("api"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "add subdir")

	res, err := New(filepath.Join(t.TempDir(), "skills")).InitializeWorkspace(context.Background(), core.WorkspaceInitOptions{
		AgentID:           "agent-subdir",
		RuntimeID:         "claudecode",
		WorkDir:           subdir,
		WorkspaceMode:     "worktree",
		ConversationScope: "api-agent:agent-subdir",
		ConversationKey:   "conversation:one",
	})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(res.WorkDir) != "api" || filepath.Base(filepath.Dir(res.WorkDir)) != "services" {
		t.Fatalf("worktree subdirectory = %q", res.WorkDir)
	}
	assertFile(t, filepath.Join(res.WorkDir, "README.md"))
	assertFile(t, filepath.Join(res.WorkDir, "CLAUDE.md"))
}

func TestInitializeWorktreeModeWithoutConversationDefersCreation(t *testing.T) {
	repo := initGitRepository(t)
	res, err := New(filepath.Join(t.TempDir(), "skills")).InitializeWorkspace(context.Background(), core.WorkspaceInitOptions{
		AgentID:       "agent-deferred",
		RuntimeID:     "codex",
		WorkDir:       repo,
		WorkspaceMode: "worktree",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.WorkDir != repo || res.WorktreeBranch != "" || len(res.Warnings) == 0 {
		t.Fatalf("deferred worktree result = %+v", res)
	}
}

func TestInitializeWorktreeModeRejectsNonRepository(t *testing.T) {
	_, err := New(filepath.Join(t.TempDir(), "skills")).InitializeWorkspace(context.Background(), core.WorkspaceInitOptions{
		AgentID:           "agent-invalid",
		RuntimeID:         "codex",
		WorkDir:           t.TempDir(),
		WorkspaceMode:     "worktree",
		ConversationScope: "channel:one",
		ConversationKey:   "chat:one",
	})
	if err == nil || !strings.Contains(err.Error(), "not inside a git repository") {
		t.Fatalf("error = %v", err)
	}
}

func TestInitializeWorktreeModeRejectsOptionLikeBaseRef(t *testing.T) {
	repo := initGitRepository(t)
	_, err := New(filepath.Join(t.TempDir(), "skills")).InitializeWorkspace(context.Background(), core.WorkspaceInitOptions{
		AgentID: "agent-invalid-ref", RuntimeID: "codex", WorkDir: repo, WorkspaceMode: "worktree",
		ConversationScope: "channel:one", ConversationKey: "chat:one", WorktreeBaseRef: "--detach",
	})
	if err == nil || !strings.Contains(err.Error(), "base ref") {
		t.Fatalf("error = %v", err)
	}
}

func initGitRepository(t *testing.T) string {
	t.Helper()
	parent := t.TempDir()
	repo := filepath.Join(parent, "project")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "init", "-b", "main")
	gitRun(t, repo, "config", "user.email", "agentmux@example.test")
	gitRun(t, repo, "config", "user.name", "AgentMux Test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("project"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", "README.md")
	gitRun(t, repo, "commit", "-m", "initial")
	return repo
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	argv := append([]string{"-C", dir}, args...)
	output, err := exec.Command("git", argv...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
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
