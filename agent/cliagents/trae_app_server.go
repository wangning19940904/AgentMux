package cliagents

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"

	"github.com/wangning19940904/AgentMux/agent/cliagent"
	"github.com/wangning19940904/AgentMux/agent/internal/runner"
	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/internal/traeauth"
)

// TRAE shares the v2 thread/turn protocol but owns its executable, account,
// telemetry identity and approval catalog. The legacy adapter is only used
// when the binary explicitly reports that app-server is not a command.
type traeAppAgent struct {
	*codexAgent
	legacy     *cliagent.Agent
	probeMu    sync.Mutex
	probed     bool
	legacyOnly bool
}

func newTraeAppAgent(cfg map[string]any, legacy *cliagent.Agent) *traeAppAgent {
	a := newCodexAgent(cfg)
	a.runtimeID, a.binary, a.ensureAuth = "traecli", "traecli", traeauth.Ensure
	a.supportedApprovalModes = core.ApprovalModeValuesForRuntime("traecli")
	a.desktopMode, a.desktopThreadID = false, ""
	return &traeAppAgent{codexAgent: a, legacy: legacy}
}

func (a *traeAppAgent) useLegacy(ctx context.Context) bool {
	a.probeMu.Lock()
	defer a.probeMu.Unlock()
	if a.probed {
		return a.legacyOnly
	}
	cmd := exec.CommandContext(ctx, a.binary, "app-server", "--help")
	cmd.Env = runner.BuildEnv(a.env)
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return false
	}
	detail := strings.ToLower(string(output))
	a.legacyOnly = err != nil && (strings.Contains(detail, "unrecognized subcommand") || strings.Contains(detail, "unknown command"))
	a.probed = true
	return a.legacyOnly
}
func (a *traeAppAgent) StartSession(ctx context.Context, dir string) (core.AgentSession, error) {
	if a.useLegacy(ctx) {
		return a.legacy.StartSession(ctx, dir)
	}
	return a.codexAgent.StartSession(ctx, dir)
}
func (a *traeAppAgent) StartSessionResume(ctx context.Context, dir, id string) (core.AgentSession, error) {
	if a.useLegacy(ctx) {
		return a.legacy.StartSessionResume(ctx, dir, id)
	}
	return a.codexAgent.StartSessionResume(ctx, dir, id)
}
func (a *traeAppAgent) RuntimeSettingsCatalog(ctx context.Context, dir string) (core.RuntimeSettings, core.RuntimeSettingsCapabilities, error) {
	if a.useLegacy(ctx) {
		return a.legacy.RuntimeSettingsCatalog(ctx, dir)
	}
	return a.codexAgent.RuntimeSettingsCatalog(ctx, dir)
}
func (a *traeAppAgent) OpenNativeThread(_ context.Context, id string) (bool, string, error) {
	return false, "traecli resume " + id, nil
}

func classifySteerError(err error) error {
	var rpc *appRPCError
	if errors.As(err, &rpc) {
		message := strings.ToLower(rpc.message)
		if rpc.code == -32601 || rpc.code == -32600 || rpc.code == -32602 || strings.Contains(message, "no active turn") || strings.Contains(message, "not steerable") || strings.Contains(message, "expected turn") || strings.Contains(message, "turn id mismatch") {
			return &core.SteerRejectedError{Reason: err.Error()}
		}
	}
	return err
}

func (a *traeAppAgent) CodexControlCapability() core.CodexControlCapability {
	a.probeMu.Lock()
	legacy := a.legacyOnly
	a.probeMu.Unlock()
	if legacy {
		return core.CodexControlCapability{State: "unavailable", Error: "当前 TRAE 版本仅支持排队，请升级以使用调整方向"}
	}
	return a.codexAgent.CodexControlCapability()
}
