// Package cliagent provides a reusable subprocess-based agent adapter for
// coding CLIs that support a non-interactive "print one turn as JSON/stream"
// mode (Codex, Cursor, Gemini, Qoder, OpenCode, iFlow, Kimi). Each concrete
// agent registers itself by supplying a Spec describing its binary, args and
// output parsing.
package cliagent

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// LineMapper maps one line of a CLI's streamed output to a core.Event (or nil
// to skip).
type LineMapper func(line []byte) *core.Event

// EventMapper converts one structured CLI output line into zero or more
// events. Native streaming formats sometimes carry both tool lifecycle state
// and user-visible output in the same frame, which cannot be represented by a
// LineMapper's single return value.
type EventMapper func(line []byte) []*core.Event

// ModelCatalog is the account-scoped model directory returned by a CLI.
// DefaultModel is optional; Models should contain the exact values accepted by
// the CLI's per-turn model flag.
type ModelCatalog struct {
	Models       []string
	DefaultModel string
}

// ModelCatalogParser converts a CLI model-list response into a catalog.
type ModelCatalogParser func(output []byte) (ModelCatalog, error)

// Spec describes how to drive a coding CLI for a single turn.
type Spec struct {
	Name   string
	Binary string
	// Args returns the argv (excluding the binary) for a turn carrying prompt.
	Args func(prompt, systemPrompt, model, approvalMode string) []string
	// Mapper turns a streamed output line into an event.
	Mapper LineMapper
	// EventMapper is the multi-event counterpart to Mapper. When both are set,
	// EventMapper takes precedence.
	EventMapper EventMapper
	// NewStderrMapper creates a per-turn mapper for the CLI process's stderr.
	// It is intentionally a factory so adapters can accumulate multiline
	// diagnostics (for example a device-auth URL followed by a verification
	// code) without sharing state across concurrent sessions.
	NewStderrMapper func() LineMapper
	// FinalFromLast, when set, treats the last non-empty output as the final
	// answer if the CLI does not emit an explicit result event.
	FinalFromLast bool
	// SupportsModel reports whether this CLI accepts a per-turn model flag.
	SupportsModel bool
	// ApprovalModes enumerates only policies this exact transport can enforce.
	ApprovalModes []string
	// DefaultApprovalMode is the safe adapter default when Agent configuration
	// does not provide one.
	DefaultApprovalMode string
	// ApprovalEnv optionally maps a policy to per-process environment overrides.
	ApprovalEnv func(mode string) map[string]string
	// ModelCatalogArgs and ParseModelCatalog enable live account model discovery.
	// Discovery runs with the same binary, work directory and environment as a
	// normal turn. The last successful result is cached on the Agent instance.
	ModelCatalogArgs  []string
	ParseModelCatalog ModelCatalogParser
	// ModelForSettings converts the transport-neutral runtime settings into the
	// exact value passed to the CLI's model flag.
	ModelForSettings func(core.RuntimeSettings) string
	// ReasoningEfforts and ServiceTiers enumerate settings that this CLI can
	// encode into its native model/request syntax when Provider metadata does
	// not supply a more specific catalog.
	ReasoningEfforts []string
	ServiceTiers     []string
}

// Agent is a generic CLI agent built from a Spec.
type Agent struct {
	spec                      Spec
	systemPrompt              string
	defaultModel              string
	supportedModels           []string
	defaultReasoningEffort    string
	supportedReasoningEfforts []string
	defaultServiceTier        string
	supportedServiceTiers     []string
	defaultApprovalMode       string
	supportedApprovalModes    []string
	env                       map[string]string

	catalogMu        sync.Mutex
	catalog          ModelCatalog
	catalogRefreshAt time.Time
}

const (
	modelCatalogSuccessTTL = 10 * time.Minute
	modelCatalogFailureTTL = time.Minute
	modelCatalogTimeout    = 15 * time.Second
)

// New builds an Agent from a Spec and config map.
func New(spec Spec, cfg map[string]any) *Agent {
	a := &Agent{spec: spec}
	if v, ok := cfg["system_prompt"].(string); ok {
		a.systemPrompt = v
	}
	configured := core.RuntimeSettingsSelectionFromConfig(cfg)
	defaults := configured.DefaultRuntimeSettings()
	capabilities := configured.RuntimeSettingsCapabilities()
	if spec.SupportsModel {
		a.defaultModel = defaults.Model
		a.supportedModels = runtimeOptionValues(capabilities.Models)
	}
	a.defaultReasoningEffort = defaults.ReasoningEffort
	a.supportedReasoningEfforts = runtimeOptionValues(capabilities.ReasoningEfforts)
	if len(a.supportedReasoningEfforts) == 0 {
		a.supportedReasoningEfforts = append([]string(nil), spec.ReasoningEfforts...)
	}
	a.defaultServiceTier = defaults.ServiceTier
	a.supportedServiceTiers = runtimeOptionValues(capabilities.ServiceTiers)
	if len(a.supportedServiceTiers) == 0 {
		a.supportedServiceTiers = append([]string(nil), spec.ServiceTiers...)
	}
	a.defaultApprovalMode = spec.DefaultApprovalMode
	if defaults.ApprovalMode != "" {
		a.defaultApprovalMode = defaults.ApprovalMode
	}
	a.supportedApprovalModes = mergeValues(runtimeOptionValues(capabilities.ApprovalModes), spec.ApprovalModes)
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
	catalog := a.discoverModelCatalog(ctx, workDir)
	defaultModel := a.defaultModel
	if defaultModel == "" {
		defaultModel = catalog.DefaultModel
	}
	models := mergeValues([]string{defaultModel}, a.supportedModels, catalog.Models)
	settings := core.NewRuntimeSettingsSelection(core.RuntimeSettings{
		Model: defaultModel, ReasoningEffort: a.defaultReasoningEffort,
		ServiceTier: a.defaultServiceTier, ApprovalMode: a.defaultApprovalMode,
	}, core.RuntimeSettingsCapabilities{
		Models:           core.RuntimeOptions(models),
		ReasoningEfforts: core.RuntimeOptions(a.supportedReasoningEfforts),
		ServiceTiers:     core.RuntimeOptions(a.supportedServiceTiers),
		ApprovalModes:    core.RuntimeOptions(a.supportedApprovalModes),
	})
	return &session{agent: a, workDir: workDir, id: a.spec.Name + "-" + randID(), settings: settings}, nil
}

func (a *Agent) discoverModelCatalog(ctx context.Context, workDir string) ModelCatalog {
	if a == nil || len(a.spec.ModelCatalogArgs) == 0 || a.spec.ParseModelCatalog == nil {
		return ModelCatalog{}
	}

	a.catalogMu.Lock()
	defer a.catalogMu.Unlock()
	if time.Now().Before(a.catalogRefreshAt) {
		return copyModelCatalog(a.catalog)
	}

	refreshAt := time.Now().Add(modelCatalogFailureTTL)
	discoveryCtx, cancel := context.WithTimeout(ctx, modelCatalogTimeout)
	defer cancel()
	bin := a.spec.Binary
	if path, err := exec.LookPath(bin); err == nil {
		bin = path
	}
	cmd := exec.CommandContext(discoveryCtx, bin, append([]string(nil), a.spec.ModelCatalogArgs...)...)
	cmd.Dir = workDir
	cmd.Env = overrideEnv(buildEnv(a.env), map[string]string{"NO_COLOR": "1", "TERM": "dumb"})
	output, err := cmd.CombinedOutput()
	if err == nil {
		var catalog ModelCatalog
		catalog, err = a.spec.ParseModelCatalog(output)
		if err == nil {
			a.catalog = normalizeModelCatalog(catalog)
			refreshAt = time.Now().Add(modelCatalogSuccessTTL)
		}
	}
	a.catalogRefreshAt = refreshAt
	return copyModelCatalog(a.catalog)
}

func normalizeModelCatalog(catalog ModelCatalog) ModelCatalog {
	catalog.DefaultModel = strings.TrimSpace(catalog.DefaultModel)
	catalog.Models = mergeValues([]string{catalog.DefaultModel}, catalog.Models)
	return catalog
}

func copyModelCatalog(catalog ModelCatalog) ModelCatalog {
	return ModelCatalog{Models: append([]string(nil), catalog.Models...), DefaultModel: catalog.DefaultModel}
}

func runtimeOptionValues(options []core.RuntimeOption) []string {
	values := make([]string, 0, len(options))
	for _, option := range options {
		values = append(values, option.Value)
	}
	return values
}

func mergeValues(groups ...[]string) []string {
	seen := map[string]bool{}
	var values []string
	for _, group := range groups {
		for _, value := range group {
			value = strings.TrimSpace(value)
			if value == "" || seen[value] {
				continue
			}
			seen[value] = true
			values = append(values, value)
		}
	}
	return values
}

// ListSessions returns no persistent sessions for CLI agents.
func (a *Agent) ListSessions(ctx context.Context) ([]string, error) { return nil, nil }

// Stop is a no-op.
func (a *Agent) Stop(ctx context.Context) error { return nil }
