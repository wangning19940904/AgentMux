package superagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/wangning19940904/AgentMux/core"
)

func managedRouterID(runtimeID string) string { return routerIDPrefix + runtimeID }

func (s *Service) stateDir() (string, error) {
	if s.state != "" {
		return s.state, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".agentmux"), nil
}

// resolveRouter finds the router a Super Agent routes with and validates it.
// current is the router the Super Agent was saved with. A framework named
// instead of a router agent means the router agent the Super Agent already has
// on that framework or, when own is set, a new one: it gets an empty
// RouterAgentID, and Save creates it once everything else is valid. Setup,
// which saves nothing, uses the framework's default router agent instead.
func (s *Service) resolveRouter(ctx context.Context, agent *core.SuperAgent, current string, own bool) (*core.AgentInstance, error) {
	agent.RouterRuntime = strings.TrimSpace(agent.RouterRuntime)
	runtimeID := agent.RouterRuntime
	if runtimeID == "" {
		return s.validateRouter(ctx, agent.RouterAgentID, current)
	}
	if !own {
		id, err := s.ensureManagedRouter(ctx, runtimeID)
		if err != nil {
			return nil, err
		}
		agent.RouterAgentID = id
		return s.validateRouter(ctx, id, current)
	}
	if current != "" {
		router, err := s.store.GetAgentInstance(ctx, current)
		if err != nil {
			return nil, err
		}
		if router != nil && IsRouterAgent(*router) && router.RuntimeID == runtimeID {
			agent.RouterAgentID = current
			return s.validateRouter(ctx, current, current)
		}
	}
	if s.runtimeAvailable != nil && !s.runtimeAvailable(runtimeID) {
		return nil, fmt.Errorf("agent framework %q is not installed on this machine", runtimeID)
	}
	if err := checkRouterRuntime(runtimeID); err != nil {
		return nil, err
	}
	agent.RouterAgentID = ""
	return &core.AgentInstance{RuntimeID: runtimeID, DefaultApprovalMode: defaultRouterApproval(runtimeID), Source: routerAgentSource}, nil
}

// ensureManagedRouter keeps a default router agent per framework, for setting
// up a Super Agent that names a framework before it has a router of its own.
// An existing one is kept as is.
func (s *Service) ensureManagedRouter(ctx context.Context, runtimeID string) (string, error) {
	if s.runtimeAvailable != nil && !s.runtimeAvailable(runtimeID) {
		return "", fmt.Errorf("agent framework %q is not installed on this machine", runtimeID)
	}
	if err := checkRouterRuntime(runtimeID); err != nil {
		return "", err
	}
	id := managedRouterID(runtimeID)
	existing, err := s.store.GetAgentInstance(ctx, id)
	if err != nil {
		return "", err
	}
	if existing != nil {
		return id, nil
	}
	state, err := s.stateDir()
	if err != nil {
		return "", err
	}
	workDir := filepath.Join(state, "super-agents", "router")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return "", fmt.Errorf("create router directory: %w", err)
	}
	now := s.now().UTC()
	instance := core.AgentInstance{
		ID: id, Name: fmt.Sprintf("Super Agent router (%s)", runtimeID), RuntimeID: runtimeID,
		WorkDir: workDir, WorkspaceMode: "shared", SessionBackend: "structured",
		DefaultApprovalMode: setupRouterApproval(runtimeID), Enabled: true, Source: routerAgentSource,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.UpsertAgentInstance(ctx, &instance); err != nil {
		return "", err
	}
	return id, nil
}

// setupRouterApproval keeps the default router agent read-only where it can
// be: it serves the setup assistant until a Super Agent adopts it, and the
// operator chooses its mode then.
func setupRouterApproval(runtimeID string) string {
	if slices.Contains(routerApprovalModes(runtimeID), core.ApprovalModePlan) {
		return core.ApprovalModePlan
	}
	return defaultRouterApproval(runtimeID)
}

// withWorkerRuntimes gives a Super Agent saved before worker frameworks could be
// chosen the router's framework, which its auto workers used then.
func (s *Service) withWorkerRuntimes(ctx context.Context, agent core.SuperAgent) core.SuperAgent {
	if !agent.AutoWorkers || len(agent.WorkerRuntimes) > 0 {
		return agent
	}
	if router, err := s.store.GetAgentInstance(ctx, agent.RouterAgentID); err == nil && router != nil {
		agent.WorkerRuntimes = []string{router.RuntimeID}
	}
	return agent
}

// defaultAutoWorkerApproval is Auto, where the framework approves what looks
// safe and declines the rest, like the router. A framework without Auto gets
// Auto edit, since frameworks differ in which modes they offer.
func defaultAutoWorkerApproval(runtimeID string) string {
	if core.ApprovalModeSupported(runtimeID, core.ApprovalModeAuto) {
		return core.ApprovalModeAuto
	}
	return core.ApprovalModeAutoEdit
}

func expandHome(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~")), nil
}

func validateAutoWorkerDir(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	expanded, err := expandHome(path)
	if err != nil {
		return err
	}
	if info, err := os.Stat(expanded); err != nil || !info.IsDir() {
		return fmt.Errorf("auto worker directory %q does not exist on this machine", path)
	}
	return nil
}

// autoWorkerDir prefers the operator's choice, then the directory of an
// ordinary Agent kept as the router. A router agent only has a scratch
// directory, so without a choice the workers get one of their own.
func (s *Service) autoWorkerDir(agent core.SuperAgent, router core.AgentInstance) (string, error) {
	if dir := strings.TrimSpace(agent.AutoWorkerDir); dir != "" {
		return expandHome(dir)
	}
	if !IsManagedAgent(router) && !IsRouterAgent(router) && strings.TrimSpace(router.WorkDir) != "" {
		return router.WorkDir, nil
	}
	state, err := s.stateDir()
	if err != nil {
		return "", err
	}
	if agent.ID == "" {
		return "", errors.New("super agent has no id")
	}
	dir := filepath.Join(state, "super-agents", agent.ID, "work")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create auto worker directory: %w", err)
	}
	return dir, nil
}
