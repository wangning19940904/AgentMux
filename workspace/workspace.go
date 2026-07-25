// Package workspace prepares per-agent working directories before execution.
package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/skills"
)

const initVersion = 1

// Initializer prepares work directories with AgentMux metadata and native
// agent directories.
type Initializer struct {
	SkillRoots []string
}

// New builds an Initializer using the default global skill roots.
func New(skillRoots ...string) *Initializer {
	if len(skillRoots) == 0 {
		skillRoots = skills.DefaultRoots()
	}
	return &Initializer{SkillRoots: skillRoots}
}

type workspaceFile struct {
	Version       int       `json:"version"`
	RuntimeID     string    `json:"runtime_id,omitempty"`
	AgentID       string    `json:"agent_id,omitempty"`
	Skills        []string  `json:"skills,omitempty"`
	MCPServers    []string  `json:"mcp_servers,omitempty"`
	InitializedAt time.Time `json:"initialized_at"`
}

// InitializeWorkspace ensures the directory and runtime-native scaffolding
// exist. It is idempotent and does not overwrite user prompt files.
func (i *Initializer) InitializeWorkspace(ctx context.Context, opts core.WorkspaceInitOptions) (*core.WorkspaceInitResult, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	workDir, err := normalizeWorkDir(opts.WorkDir)
	if err != nil {
		return nil, err
	}
	res := &core.WorkspaceInitResult{
		WorkDir:   workDir,
		RuntimeID: opts.RuntimeID,
		AgentID:   opts.AgentID,
	}
	if err := ensureDir(workDir, res); err != nil {
		return nil, err
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
		Version:       initVersion,
		RuntimeID:     opts.RuntimeID,
		AgentID:       opts.AgentID,
		Skills:        append([]string(nil), opts.Skills...),
		MCPServers:    append([]string(nil), opts.MCPServers...),
		InitializedAt: time.Now().UTC(),
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
