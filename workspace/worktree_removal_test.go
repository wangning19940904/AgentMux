package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// placedWorktree gives one conversation a worktree at a path of its own, the
// way a Ticket's work/ is made.
func placedWorktree(t *testing.T, repo, name string) (core.WorkspaceInitOptions, *core.WorkspaceInitResult) {
	t.Helper()
	opts := core.WorkspaceInitOptions{
		AgentID: "agent-" + name, RuntimeID: "codex", WorkDir: repo, WorkspaceMode: "worktree",
		ConversationScope: "superagent", ConversationKey: "ticket:" + name,
		WorktreePath: filepath.Join(t.TempDir(), "work"),
	}
	res, err := (&Initializer{}).InitializeWorkspace(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	return opts, res
}

func TestRemoveCleanWorktreeKeepsTheBranchAndComesBack(t *testing.T) {
	repo := initGitRepository(t)
	opts, res := placedWorktree(t, repo, "clean")
	work := res.WorkDir
	// The worker commits on a branch of its own choosing and leaves build
	// output behind; AgentMux's own files are untracked beside it.
	gitRun(t, work, "checkout", "-q", "-b", "feat/own")
	if err := os.WriteFile(filepath.Join(work, "feature.txt"), []byte("done"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, work, "add", "feature.txt")
	gitRun(t, work, "commit", "-q", "-m", "feature")
	head := gitRun(t, work, "rev-parse", "HEAD")
	if status := gitRun(t, work, "status", "--porcelain"); !strings.Contains(status, "AGENTS.md") {
		t.Fatalf("the worktree holds AgentMux's own untracked files: %q", status)
	}

	now := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	removal, err := RemoveCleanWorktree(context.Background(), work, now)
	if err != nil || !removal.Removed {
		t.Fatalf("removal = %+v err=%v", removal, err)
	}
	if _, err := os.Lstat(work); !os.IsNotExist(err) {
		t.Fatalf("the directory is gone: %v", err)
	}
	if list := gitRun(t, repo, "worktree", "list", "--porcelain"); strings.Contains(list, work) {
		t.Fatalf("the repository forgets the worktree:\n%s", list)
	}
	if got := gitRun(t, repo, "rev-parse", "feat/own"); got != head {
		t.Fatalf("the branch keeps the work: %s != %s", got, head)
	}
	note, ok := ReadRemovedWorktree(work)
	if !ok || note.Branch != "feat/own" || note.Commit != head || !note.RemovedAt.Equal(now) {
		t.Fatalf("note = %+v ok=%v", note, ok)
	}

	// The conversation's next turn finds its work where it left it.
	again, err := (&Initializer{}).InitializeWorkspace(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if again.WorkDir != work || again.WorktreeBranch != "feat/own" {
		t.Fatalf("restored = %+v", again)
	}
	if got := gitRun(t, work, "rev-parse", "HEAD"); got != head {
		t.Fatalf("restored at %s, want %s", got, head)
	}
	if got := gitRun(t, work, "branch", "--show-current"); got != "feat/own" {
		t.Fatalf("restored on %q", got)
	}
	assertFile(t, filepath.Join(work, "AGENTS.md"))
	if _, ok := ReadRemovedWorktree(work); ok {
		t.Fatal("the note goes once the worktree is back")
	}
}

func TestRemoveCleanWorktreeKeepsWorkThatWouldBeLost(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	repo := initGitRepository(t)

	cases := map[string]func(t *testing.T, work string){
		"a changed file": func(t *testing.T, work string) {
			if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("edited"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"a new file": func(t *testing.T, work string) {
			if err := os.WriteFile(filepath.Join(work, "notes.txt"), []byte("mine"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"a skill the worker wrote": func(t *testing.T, work string) {
			dir := filepath.Join(work, ".agents", "skills", "mine")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("mine"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"a detached head": func(t *testing.T, work string) {
			gitRun(t, work, "checkout", "-q", "--detach")
		},
	}
	for name, dirty := range cases {
		t.Run(name, func(t *testing.T) {
			_, res := placedWorktree(t, repo, strings.ReplaceAll(name, " ", "-"))
			dirty(t, res.WorkDir)
			removal, err := RemoveCleanWorktree(ctx, res.WorkDir, now)
			if err != nil || removal.Removed || removal.Kept == "" {
				t.Fatalf("removal = %+v err=%v", removal, err)
			}
			assertDir(t, res.WorkDir)
			assertFile(t, filepath.Join(res.WorkDir, "AGENTS.md"))
			if _, ok := ReadRemovedWorktree(res.WorkDir); ok {
				t.Fatal("a kept worktree leaves no note")
			}
		})
	}

	t.Run("a repository of its own", func(t *testing.T) {
		removal, err := RemoveCleanWorktree(ctx, repo, now)
		if err != nil || removal.Removed {
			t.Fatalf("removal = %+v err=%v", removal, err)
		}
		assertFile(t, filepath.Join(repo, "README.md"))
	})
	t.Run("a link to a directory", func(t *testing.T) {
		_, res := placedWorktree(t, repo, "linked")
		link := filepath.Join(t.TempDir(), "work")
		if err := os.Symlink(res.WorkDir, link); err != nil {
			t.Skip("symlinks unavailable")
		}
		removal, err := RemoveCleanWorktree(ctx, link, now)
		if err != nil || removal.Removed {
			t.Fatalf("removal = %+v err=%v", removal, err)
		}
		assertDir(t, res.WorkDir)
	})
}

func TestRemovedWorktreeFallsBackWhenItsBranchIsGone(t *testing.T) {
	repo := initGitRepository(t)
	opts, res := placedWorktree(t, repo, "gone")
	work, original := res.WorkDir, res.WorktreeBranch
	gitRun(t, work, "checkout", "-q", "-b", "feat/short-lived")
	if removal, err := RemoveCleanWorktree(context.Background(), work, time.Now()); err != nil || !removal.Removed {
		t.Fatalf("removal = %+v err=%v", removal, err)
	}
	gitRun(t, repo, "branch", "-D", "feat/short-lived")

	again, err := (&Initializer{}).InitializeWorkspace(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if again.WorkDir != work || again.WorktreeBranch != original {
		t.Fatalf("fallback = %+v, want branch %q", again, original)
	}
	if _, ok := ReadRemovedWorktree(work); ok {
		t.Fatal("the note goes once the worktree is back")
	}
}
