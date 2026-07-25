// Package cliagent provides a reusable subprocess-based agent adapter for
// coding CLIs that support a non-interactive "print one turn as JSON/stream"
// mode (Codex, Cursor, Gemini, Qoder, OpenCode, iFlow, Kimi). Each concrete
// agent registers itself by supplying a Spec describing its binary, args and
// output parsing.
package cliagent

import (
	"context"
	"os"

	"github.com/wangning19940904/AgentMux/core"
)

// LineMapper maps one line of a CLI's streamed output to a core.Event (or nil
// to skip).
type LineMapper func(line []byte) *core.Event

// Spec describes how to drive a coding CLI for a single turn.
type Spec struct {
	Name   string
	Binary string
	// Args returns the argv (excluding the binary) for a turn carrying prompt.
	Args func(prompt, systemPrompt, model string) []string
	// Mapper turns a streamed output line into an event.
	Mapper LineMapper
	// FinalFromLast, when set, treats the last non-empty output as the final
	// answer if the CLI does not emit an explicit result event.
	FinalFromLast bool
	// SupportsModel reports whether this CLI accepts a per-turn model flag.
	SupportsModel bool
}

// Agent is a generic CLI agent built from a Spec.
type Agent struct {
	spec            Spec
	systemPrompt    string
	defaultModel    string
	supportedModels []string
	env             map[string]string
}

// New builds an Agent from a Spec and config map.
func New(spec Spec, cfg map[string]any) *Agent {
	a := &Agent{spec: spec}
	if v, ok := cfg["system_prompt"].(string); ok {
		a.systemPrompt = v
	}
	if spec.SupportsModel {
		if model := core.ModelSelectionFromConfig(cfg); model != nil {
			a.defaultModel = model.DefaultModel()
			a.supportedModels = model.SupportedModels()
		}
	}
	if env, ok := cfg["env"].(map[string]string); ok {
		a.env = env
	}
	return a
}

// Name returns the agent name.
func (a *Agent) Name() string { return a.spec.Name }

// StartSession creates a new turn-based session.
func (a *Agent) StartSession(ctx context.Context, workDir string) (core.AgentSession, error) {
	if workDir == "" {
		workDir, _ = os.Getwd()
	}
	var model *core.ModelSelection
	if a.spec.SupportsModel {
		model = core.NewModelSelection(a.defaultModel, a.supportedModels)
	}
	return &session{agent: a, workDir: workDir, id: a.spec.Name + "-" + randID(), model: model}, nil
}

// ListSessions returns no persistent sessions for CLI agents.
func (a *Agent) ListSessions(ctx context.Context) ([]string, error) { return nil, nil }

// Stop is a no-op.
func (a *Agent) Stop(ctx context.Context) error { return nil }
