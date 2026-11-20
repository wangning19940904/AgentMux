package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// removedWorktreeSuffix names the note left beside a removed worktree: the
// worktree's own path with this ending.
const removedWorktreeSuffix = ".removed.json"

// RemovedWorktree is the note left where a worktree was removed. Everything
// the worktree held is on Branch in Repository, so checking that branch out at
// the same path brings the work back.
type RemovedWorktree struct {
	// Repository is the repository's shared git directory.
	Repository string    `json:"repository"`
	Branch     string    `json:"branch"`
	Commit     string    `json:"commit"`
	RemovedAt  time.Time `json:"removed_at"`
}

// WorktreeRemoval is what RemoveCleanWorktree did. Kept says why the worktree
// stayed when Removed is false.
type WorktreeRemoval struct {
	Removed bool
	Kept    string
	Note    RemovedWorktree
}

// RemoveCleanWorktree removes the linked git worktree at path when nothing in
// it would be lost: it is on a branch, and it has no changes or files of its
// own besides what InitializeWorkspace wrote there. The branch stays, and a
// note beside the path records it, so the next InitializeWorkspace for the
// same path checks the same branch out again. Files git ignores, such as
// build output and installed dependencies, go with the directory.
//
// Callers make sure no turn is running in the worktree.
func RemoveCleanWorktree(ctx context.Context, path string, now time.Time) (WorktreeRemoval, error) {
	path = filepath.Clean(path)
	kept := func(format string, args ...any) (WorktreeRemoval, error) {
		return WorktreeRemoval{Kept: fmt.Sprintf(format, args...)}, nil
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return kept("there is no directory")
	}
	if err != nil {
		return WorktreeRemoval{}, err
	}
	if !info.IsDir() {
		return kept("it is a link or a file, not a directory of its own")
	}
	if !isGitWorktree(ctx, path) {
		return kept("it is not the top of a git worktree")
	}
	common, err := gitCommonDir(ctx, path)
	if err != nil {
		return WorktreeRemoval{}, err
	}
	own, err := gitOutput(ctx, path, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return WorktreeRemoval{}, fmt.Errorf("find the worktree's git directory: %s", own)
	}
	if samePath(own, common) {
		return kept("it is a repository of its own, not a worktree of another")
	}
	branch, err := gitOutput(ctx, path, "symbolic-ref", "--short", "-q", "HEAD")
	if err != nil || branch == "" {
		return kept("it is not on a branch, so its commits would have no name")
	}
	commit, err := gitOutput(ctx, path, "rev-parse", "HEAD")
	if err != nil {
		return kept("its branch has no commit")
	}
	status, err := exec.CommandContext(ctx, "git", "-C", path, "status", "--porcelain=v1", "-z", "--untracked-files=all").Output()
	if err != nil {
		return WorktreeRemoval{}, fmt.Errorf("read the worktree's status: %w", err)
	}
	managedSkills := managedSkillTargets(path)
	var scaffolding []string
	for _, entry := range strings.Split(string(status), "\x00") {
		if entry == "" {
			continue
		}
		rel, untracked := strings.CutPrefix(entry, "?? ")
		if !untracked || !isScaffolding(path, rel, managedSkills) {
			return kept("it has work that is not committed")
		}
		scaffolding = append(scaffolding, rel)
	}
	// git removes a worktree only when it is clean, so what AgentMux wrote
	// goes first. If git still refuses, the next turn writes it again.
	for _, rel := range scaffolding {
		if err := os.Remove(filepath.Join(path, rel)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return WorktreeRemoval{}, err
		}
	}
	if output, err := gitOutput(ctx, common, "worktree", "remove", path); err != nil {
		return kept("git would not remove it: %s", output)
	}
	note := RemovedWorktree{Repository: common, Branch: branch, Commit: commit, RemovedAt: now.UTC()}
	removal := WorktreeRemoval{Removed: true, Note: note}
	data, err := json.MarshalIndent(note, "", "  ")
	if err == nil {
		err = atomicWriteFile(path+removedWorktreeSuffix, append(data, '\n'), 0o644)
	}
	if err != nil {
		return removal, fmt.Errorf("record the removed worktree: %w", err)
	}
	return removal, nil
}

// ReadRemovedWorktree returns the note RemoveCleanWorktree left for path.
func ReadRemovedWorktree(path string) (RemovedWorktree, bool) {
	data, err := os.ReadFile(filepath.Clean(path) + removedWorktreeSuffix)
	if err != nil {
		return RemovedWorktree{}, false
	}
	var note RemovedWorktree
	if json.Unmarshal(data, &note) != nil || note.Branch == "" {
		return RemovedWorktree{}, false
	}
	return note, true
}

// restoreRemovedWorktree checks the branch a removed worktree was on out at
// its path again. It reports false when there is nothing to restore or the
// branch can no longer be checked out, and the caller then creates the
// worktree as if it were new.
func restoreRemovedWorktree(ctx context.Context, repoRoot, worktreePath string) (string, bool) {
	note, ok := ReadRemovedWorktree(worktreePath)
	if !ok {
		return "", false
	}
	common, err := gitCommonDir(ctx, repoRoot)
	if err != nil || !samePath(common, note.Repository) || !gitBranchExists(ctx, repoRoot, note.Branch) {
		return "", false
	}
	if _, err := gitOutput(ctx, repoRoot, "worktree", "add", worktreePath, note.Branch); err != nil {
		return "", false
	}
	return note.Branch, true
}

// forgetRemovedWorktree drops the note once the path holds a worktree again.
func forgetRemovedWorktree(worktreePath string) {
	_ = os.Remove(worktreePath + removedWorktreeSuffix)
}

// isScaffolding reports whether an untracked file is one InitializeWorkspace
// wrote, which the next turn writes again, and not the worker's.
func isScaffolding(worktree, rel string, managedSkills []string) bool {
	slashed := filepath.ToSlash(rel)
	switch {
	case slashed == "CLAUDE.md" || slashed == "AGENTS.md" || strings.HasPrefix(slashed, ".agentmux/"):
		return true
	case strings.HasPrefix(slashed, ".claude/skills/") || strings.HasPrefix(slashed, ".agents/skills/"):
		// Managed skills are links to the installed skill, or copies the
		// manifest lists; a skill written here is the worker's.
		target := filepath.Join(worktree, rel)
		if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return true
		}
		for _, managed := range managedSkills {
			if pathContainedBy(managed, target) {
				return true
			}
		}
		return false
	case slashed == ".mcp.json":
		return fileExists(filepath.Join(worktree, ".agentmux", "generated", "claude-mcp.sha256"))
	case slashed == ".codex/config.toml":
		return fileExists(filepath.Join(worktree, ".agentmux", "generated", "codex-mcp.sha256"))
	}
	return false
}

// managedSkillTargets lists the skills linkSkills put in the worktree.
func managedSkillTargets(worktree string) []string {
	data, err := os.ReadFile(filepath.Join(worktree, ".agentmux", "managed-skills.json"))
	if err != nil {
		return nil
	}
	var targets []string
	_ = json.Unmarshal(data, &targets)
	return targets
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// gitCommonDir is the git directory a repository's worktrees share.
func gitCommonDir(ctx context.Context, dir string) (string, error) {
	output, err := gitOutput(ctx, dir, "rev-parse", "--git-common-dir")
	if err != nil || output == "" {
		return "", fmt.Errorf("find the repository of %q: %s", dir, output)
	}
	if !filepath.IsAbs(output) {
		output = filepath.Join(dir, output)
	}
	if resolved, err := filepath.EvalSymlinks(output); err == nil {
		output = resolved
	}
	return filepath.Clean(output), nil
}
