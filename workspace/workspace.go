// Package workspace prepares per-agent working directories before execution.
package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/skills"
)

const initVersion = 1

// Initializer prepares work directories with AgentMux metadata and native
// agent directories.
type Initializer struct {
	SkillRoots []string
	worktreeMu sync.Mutex
}

// New builds an Initializer using the default global skill roots.
func New(skillRoots ...string) *Initializer {
	if len(skillRoots) == 0 {
		skillRoots = skills.DefaultRoots()
	}
	return &Initializer{SkillRoots: skillRoots}
}

type workspaceFile struct {
	Version         int       `json:"version"`
	RuntimeID       string    `json:"runtime_id,omitempty"`
	AgentID         string    `json:"agent_id,omitempty"`
	WorkspaceMode   string    `json:"workspace_mode,omitempty"`
	BaseWorkDir     string    `json:"base_work_dir,omitempty"`
	WorktreeBranch  string    `json:"worktree_branch,omitempty"`
	ConversationKey string    `json:"conversation_key,omitempty"`
	Skills          []string  `json:"skills,omitempty"`
	MCPServers      []string  `json:"mcp_servers,omitempty"`
	InitializedAt   time.Time `json:"initialized_at"`
}

// InitializeWorkspace ensures the directory and runtime-native scaffolding
// exist. It is idempotent and does not overwrite user prompt files.
func (i *Initializer) InitializeWorkspace(ctx context.Context, opts core.WorkspaceInitOptions) (*core.WorkspaceInitResult, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	baseWorkDir, err := normalizeWorkDir(opts.WorkDir)
	if err != nil {
		return nil, err
	}
	mode := normalizeWorkspaceMode(opts.WorkspaceMode)
	workDir := baseWorkDir
	res := &core.WorkspaceInitResult{
		WorkDir:       workDir,
		BaseWorkDir:   baseWorkDir,
		WorkspaceMode: mode,
		RuntimeID:     opts.RuntimeID,
		AgentID:       opts.AgentID,
	}
	if err := ensureDir(baseWorkDir, res); err != nil {
		return nil, err
	}
	if mode == "worktree" {
		if strings.TrimSpace(opts.ConversationKey) == "" {
			if _, err := gitRepositoryRoot(ctx, baseWorkDir); err != nil {
				return nil, fmt.Errorf("worktree workspace: %w", err)
			}
			res.Warnings = append(res.Warnings, "worktree mode is enabled; a dedicated worktree will be created when the first conversation starts")
		} else {
			created := false
			workDir, res.WorktreeBranch, created, err = i.ensureConversationWorktree(ctx, baseWorkDir, opts)
			if err != nil {
				return nil, err
			}
			res.WorkDir = workDir
			if created {
				res.Created = append(res.Created, workDir)
			}
		}
	}
	if err := ensureDir(filepath.Join(workDir, ".agentmux"), res); err != nil {
		return nil, err
	}
	if err := writeWorkspaceFile(filepath.Join(workDir, ".agentmux", "workspace.json"), opts, res); err != nil {
		return nil, err
	}

	skillDir := ""
	switch normalizeRuntime(opts.RuntimeID) {
	case "claudecode":
		skillDir = filepath.Join(workDir, ".claude", "skills")
		if err := ensureDir(skillDir, res); err != nil {
			return nil, err
		}
		if err := ensurePrompt(filepath.Join(workDir, "CLAUDE.md"), claudePrompt(opts), res); err != nil {
			return nil, err
		}
	case "codex":
		skillDir = filepath.Join(workDir, ".agents", "skills")
		if err := ensureDir(skillDir, res); err != nil {
			return nil, err
		}
		if err := ensurePrompt(filepath.Join(workDir, "AGENTS.md"), codexPrompt(opts), res); err != nil {
			return nil, err
		}
	}
	if skillDir != "" {
		i.linkSkills(opts.Skills, skillDir, res)
	}
	return res, nil
}

func normalizeWorkDir(raw string) (string, error) {
	path := strings.TrimSpace(raw)
	if path == "" {
		var err error
		path, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	if strings.HasPrefix(path, "~") {
		if path != "~" && !strings.HasPrefix(path, "~/") {
			return "", fmt.Errorf("only current-user home paths are supported")
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	path = os.ExpandEnv(path)
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func normalizeWorkspaceMode(raw string) string {
	if strings.EqualFold(strings.TrimSpace(raw), "worktree") {
		return "worktree"
	}
	return "shared"
}

var worktreeSlugUnsafe = regexp.MustCompile(`[^a-z0-9]+`)

// ensureConversationWorktree returns a deterministic git worktree for one
// durable conversation. The lock prevents duplicate branch/path creation by
// concurrent first messages in this process; git itself remains the final
// cross-process arbiter.
func (i *Initializer) ensureConversationWorktree(
	ctx context.Context,
	baseWorkDir string,
	opts core.WorkspaceInitOptions,
) (workDir, branch string, created bool, err error) {
	i.worktreeMu.Lock()
	defer i.worktreeMu.Unlock()

	repoRoot, err := gitRepositoryRoot(ctx, baseWorkDir)
	if err != nil {
		return "", "", false, fmt.Errorf("worktree workspace: %w", err)
	}
	containedBase := baseWorkDir
	// macOS exposes /var as a symlink to /private/var while git reports the
	// canonical path. Canonicalize only for containment/relative calculation;
	// shared workspaces continue to preserve the exact user-facing path.
	if resolved, resolveErr := filepath.EvalSymlinks(baseWorkDir); resolveErr == nil {
		containedBase = resolved
	}
	rel, err := filepath.Rel(repoRoot, containedBase)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", false, fmt.Errorf("worktree workspace: working directory %q is outside repository %q", baseWorkDir, repoRoot)
	}

	slug := conversationWorktreeSlug(opts)
	branch = "agentmux/" + slug
	worktreeRoot := repoRoot + ".agentmux-worktrees"
	worktreePath := filepath.Join(worktreeRoot, slug)
	resolvedWorkDir := worktreePath
	if rel != "." {
		resolvedWorkDir = filepath.Join(worktreePath, rel)
	}

	if info, statErr := os.Stat(worktreePath); statErr == nil {
		if !info.IsDir() || !isGitWorktree(ctx, worktreePath) {
			return "", "", false, fmt.Errorf("worktree workspace target %q exists but is not a git worktree", worktreePath)
		}
		if err := ensurePlainDir(resolvedWorkDir); err != nil {
			return "", "", false, err
		}
		return resolvedWorkDir, branch, false, nil
	} else if !os.IsNotExist(statErr) {
		return "", "", false, statErr
	}

	if err := os.MkdirAll(worktreeRoot, 0o755); err != nil {
		return "", "", false, fmt.Errorf("create worktree root: %w", err)
	}
	baseRef := strings.TrimSpace(opts.WorktreeBaseRef)
	if baseRef == "" {
		baseRef = "HEAD"
	}
	if strings.HasPrefix(baseRef, "-") || len(baseRef) > 255 || strings.IndexFunc(baseRef, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return "", "", false, fmt.Errorf("worktree base ref %q is invalid", baseRef)
	}
	if output, verifyErr := gitOutput(ctx, repoRoot, "rev-parse", "--verify", baseRef+"^{commit}"); verifyErr != nil {
		return "", "", false, fmt.Errorf("worktree base ref %q is invalid: %s", baseRef, output)
	}

	args := []string{"worktree", "add"}
	if gitBranchExists(ctx, repoRoot, branch) {
		args = append(args, worktreePath, branch)
	} else {
		args = append(args, "-b", branch, worktreePath, baseRef)
	}
	if output, addErr := gitOutput(ctx, repoRoot, args...); addErr != nil {
		// Another daemon may have won the exact same deterministic creation.
		// Re-probe the path before surfacing the git error.
		if !isGitWorktree(ctx, worktreePath) {
			return "", "", false, fmt.Errorf("create conversation worktree: %s", output)
		}
	}
	if err := ensurePlainDir(resolvedWorkDir); err != nil {
		return "", "", false, err
	}
	return resolvedWorkDir, branch, true, nil
}

func conversationWorktreeSlug(opts core.WorkspaceInitOptions) string {
	identity := strings.Join([]string{
		strings.TrimSpace(opts.AgentID),
		strings.TrimSpace(opts.ConversationScope),
		strings.TrimSpace(opts.ConversationKey),
	}, "\x00")
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(identity)))[:10]
	hint := strings.ToLower(strings.TrimSpace(opts.ConversationKey))
	if _, tail, ok := strings.Cut(hint, ":"); ok {
		hint = tail
	}
	hint = strings.Trim(worktreeSlugUnsafe.ReplaceAllString(hint, "-"), "-")
	if len(hint) > 32 {
		hint = strings.Trim(hint[:32], "-")
	}
	if hint == "" {
		hint = "conversation"
	}
	return hint + "-" + digest
}

func gitRepositoryRoot(ctx context.Context, workDir string) (string, error) {
	output, err := gitOutput(ctx, workDir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("%q is not inside a git repository: %s", workDir, output)
	}
	root := strings.TrimSpace(output)
	if root == "" {
		return "", fmt.Errorf("git returned an empty repository root for %q", workDir)
	}
	return filepath.Clean(root), nil
}

func gitBranchExists(ctx context.Context, repoRoot, branch string) bool {
	cmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return cmd.Run() == nil
}

func isGitWorktree(ctx context.Context, path string) bool {
	root, err := gitRepositoryRoot(ctx, path)
	if err != nil {
		return false
	}
	want, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	return samePath(root, want)
}

func samePath(left, right string) bool {
	leftResolved, leftErr := filepath.EvalSymlinks(left)
	rightResolved, rightErr := filepath.EvalSymlinks(right)
	if leftErr == nil {
		left = leftResolved
	}
	if rightErr == nil {
		right = rightResolved
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func ensurePlainDir(path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return fmt.Errorf("create worktree subdirectory %q: %w", path, err)
	}
	return nil
}

func gitOutput(ctx context.Context, repoRoot string, args ...string) (string, error) {
	argv := append([]string{"-C", repoRoot}, args...)
	output, err := exec.CommandContext(ctx, "git", argv...).CombinedOutput()
	return strings.TrimSpace(string(output)), err
}

func normalizeRuntime(runtime string) string {
	switch strings.ToLower(strings.TrimSpace(runtime)) {
	case "claude", "claudecode-cli", "claude-code", "claude-code-cli":
		return "claudecode"
	case "codex-cli":
		return "codex"
	default:
		return strings.ToLower(strings.TrimSpace(runtime))
	}
}

func ensureDir(path string, res *core.WorkspaceInitResult) error {
	if info, err := os.Stat(path); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("%s exists and is not a directory", path)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	res.Created = append(res.Created, path)
	return nil
}

func writeWorkspaceFile(path string, opts core.WorkspaceInitOptions, res *core.WorkspaceInitResult) error {
	payload := workspaceFile{
		Version:         initVersion,
		RuntimeID:       opts.RuntimeID,
		AgentID:         opts.AgentID,
		WorkspaceMode:   res.WorkspaceMode,
		BaseWorkDir:     res.BaseWorkDir,
		WorktreeBranch:  res.WorktreeBranch,
		ConversationKey: opts.ConversationKey,
		Skills:          append([]string(nil), opts.Skills...),
		MCPServers:      append([]string(nil), opts.MCPServers...),
		InitializedAt:   time.Now().UTC(),
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	res.Updated = append(res.Updated, path)
	return nil
}

func ensurePrompt(path, body string, res *core.WorkspaceInitResult) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return err
	}
	res.Created = append(res.Created, path)
	return nil
}

func claudePrompt(opts core.WorkspaceInitOptions) string {
	return fmt.Sprintf(`# CLAUDE.md

This workspace is initialized by AgentMux for Claude Code.

- Agent: %s
- Runtime: %s

Keep project-specific guidance here. AgentMux will not overwrite this file after creation.
`, displayOrDash(opts.AgentID), displayOrDash(opts.RuntimeID))
}

func codexPrompt(opts core.WorkspaceInitOptions) string {
	return fmt.Sprintf(`# AGENTS.md

This workspace is initialized by AgentMux for Codex.

- Agent: %s
- Runtime: %s

Keep project-specific guidance here. AgentMux will not overwrite this file after creation.
`, displayOrDash(opts.AgentID), displayOrDash(opts.RuntimeID))
}

func displayOrDash(v string) string {
	if strings.TrimSpace(v) == "" {
		return "-"
	}
	return strings.TrimSpace(v)
}

func (i *Initializer) linkSkills(names []string, targetRoot string, res *core.WorkspaceInitResult) {
	if len(names) == 0 {
		return
	}
	installed := discoverInstalledSkills(i.SkillRoots)
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		src, ok := installed[name]
		if !ok {
			res.Warnings = append(res.Warnings, fmt.Sprintf("skill %q not found in global roots", name))
			continue
		}
		target := filepath.Join(targetRoot, filepath.Base(src))
		if _, err := os.Lstat(target); err == nil {
			continue
		}
		if err := os.Symlink(src, target); err == nil {
			res.Created = append(res.Created, target)
			continue
		} else {
			res.Warnings = append(res.Warnings, fmt.Sprintf("symlink skill %q failed, copied instead: %v", name, err))
		}
		if err := copyDir(src, target); err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("copy skill %q failed: %v", name, err))
			continue
		}
		res.Created = append(res.Created, target)
	}
}

func discoverInstalledSkills(roots []string) map[string]string {
	out := map[string]string{}
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.EqualFold(d.Name(), "SKILL.md") {
				return nil
			}
			dir := filepath.Dir(path)
			name := skills.ParseSkillFile(path).Name
			if name == "" {
				name = filepath.Base(dir)
			}
			if _, exists := out[name]; !exists {
				out[name] = dir
			}
			return nil
		})
	}
	return out
}

func copyDir(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", src)
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
