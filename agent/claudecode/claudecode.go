// Package claudecode implements the Claude Code agent adapter. It spawns the
// `claude` CLI as a subprocess and drives it in stream-json mode, mapping its
// output to core.Event.
package claudecode

import (
	"context"
	"os/exec"

	"github.com/agentnexus/agentnexus/core"
)

func init() {
	core.RegisterAgent("claudecode", func(cfg map[string]any) (core.Agent, error) {
		a := &Agent{}
		if v, ok := cfg["system_prompt"].(string); ok {
			a.systemPrompt = v
		}
		if env, ok := cfg["env"].(map[string]string); ok {
			a.env = env
		}
		return a, nil
	})
}

// Agent is the Claude Code adapter.
type Agent struct {
	systemPrompt string
	env          map[string]string
}

// Name returns the registered name.
func (a *Agent) Name() string { return "claudecode" }

// StartSession spawns a new Claude Code session in workDir.
func (a *Agent) StartSession(ctx context.Context, workDir string) (core.AgentSession, error) {
	return newSession(a, workDir)
}

// ListSessions is not yet backed by persistent state; returns empty.
func (a *Agent) ListSessions(ctx context.Context) ([]string, error) {
	return nil, nil
}

// Stop is a no-op at the agent level (sessions own their processes).
func (a *Agent) Stop(ctx context.Context) error { return nil }

// claudeBinary resolves the claude CLI path.
func claudeBinary() string {
	if p, err := exec.LookPath("claude"); err == nil {
		return p
	}
	return "claude"
}
