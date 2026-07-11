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
		if settings := core.RuntimeSettingsSelectionFromConfig(cfg); settings != nil {
			defaults := settings.DefaultRuntimeSettings()
			capabilities := settings.RuntimeSettingsCapabilities()
			a.defaultModel = defaults.Model
			a.defaultReasoningEffort = defaults.ReasoningEffort
			for _, option := range capabilities.Models {
				a.supportedModels = append(a.supportedModels, option.Value)
			}
			for _, option := range capabilities.ReasoningEfforts {
				a.supportedReasoningEfforts = append(a.supportedReasoningEfforts, option.Value)
			}
		}
		if len(a.supportedReasoningEfforts) == 0 {
			a.supportedReasoningEfforts = []string{"low", "medium", "high", "max"}
		}
		if env, ok := cfg["env"].(map[string]string); ok {
			a.env = env
		}
		return a, nil
	})
}

// Agent is the Claude Code adapter.
type Agent struct {
	systemPrompt              string
	defaultModel              string
	defaultReasoningEffort    string
	supportedModels           []string
	supportedReasoningEfforts []string
	env                       map[string]string
}

// Name returns the registered name.
func (a *Agent) Name() string { return "claudecode" }

// StartSession spawns a new Claude Code session in workDir.
func (a *Agent) StartSession(ctx context.Context, workDir string) (core.AgentSession, error) {
	return newSession(a, workDir)
}

// StartSessionResume spawns a session that resumes the given claude-native
// session id, restoring prior context. Empty resumeID behaves like
// StartSession. Implements core.ResumableAgent.
func (a *Agent) StartSessionResume(ctx context.Context, workDir, resumeID string) (core.AgentSession, error) {
	return newSessionResume(a, workDir, resumeID)
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
