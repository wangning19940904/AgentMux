package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// conversationBaseDir resolves the root that holds per-conversation working
// directories for an agent whose configured workDir is agentWorkDir.
func conversationBaseDir(agentWorkDir string) string {
	if strings.TrimSpace(agentWorkDir) != "" {
		return filepath.Join(agentWorkDir, ".agentnexus", "conversations")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".agentnexus", "conversations")
	}
	return filepath.Join(home, ".agentnexus", "conversations")
}

// conversationCwd computes the isolated working directory (sandbox) for a
// conversation identified by (scope, chatID) under an agent's base dir. The
// scope and chatID are sanitized and the result is verified to stay within
// base to prevent path traversal from hostile chat ids.
func conversationCwd(base, agentID, scope, chatID string) (string, error) {
	base = filepath.Clean(base)
	dir := filepath.Join(base,
		sanitizeSegment(agentID),
		sanitizeSegment(scope),
		sanitizeSegment(chatID),
		"cwd",
	)
	rel, err := filepath.Rel(base, dir)
	if err != nil {
		return "", fmt.Errorf("resolve conversation cwd: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("conversation path escapes base directory")
	}
	return dir, nil
}

// sanitizeSegment reduces an arbitrary identifier to a single safe path
// segment: separators and traversal sequences are replaced so it can never
// escape its parent directory.
func sanitizeSegment(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "_"
	}
	repl := func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_', r == '.':
			return r
		default:
			return '_'
		}
	}
	s = strings.Map(repl, s)
	s = strings.ReplaceAll(s, "..", "__")
	s = strings.Trim(s, ".")
	if s == "" {
		return "_"
	}
	if len(s) > 128 {
		s = s[:128]
	}
	return s
}

func (e *Engine) initializeWorkspace(ctx context.Context, opts WorkspaceInitOptions, fallbackWorkDir string) (string, error) {
	workDir := opts.WorkDir
	if workDir == "" {
		workDir = fallbackWorkDir
	}
	if workDir == "" {
		var err error
		workDir, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	if e.workspace == nil {
		return workDir, nil
	}
	opts.WorkDir = workDir
	res, err := e.workspace.InitializeWorkspace(ctx, opts)
	if err != nil {
		return "", fmt.Errorf("initialize workspace: %w", err)
	}
	if res != nil && res.WorkDir != "" {
		return res.WorkDir, nil
	}
	return workDir, nil
}

func (rt *channelRuntime) prepareWorkspace(ctx context.Context) (string, error) {
	if rt == nil {
		return "", fmt.Errorf("channel runtime is nil")
	}
	opts := rt.workspace
	if opts.WorkDir == "" {
		opts.WorkDir = rt.workDir
	}
	return rt.engine().initializeWorkspace(ctx, opts, rt.workDir)
}

func (rt *channelRuntime) engine() *Engine {
	return rt.owner
}
