package superagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wangning19940904/AgentMux/core"
)

func managedRouterID(runtimeID string) string { return "sar_" + runtimeID }

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

// resolveRouter sets RouterAgentID from the chosen framework, creating the
// router Agent when this framework has none yet, and validates the result.
func (s *Service) resolveRouter(ctx context.Context, agent *core.SuperAgent) (*core.AgentInstance, error) {
	agent.RouterRuntime = strings.TrimSpace(agent.RouterRuntime)
	if agent.RouterRuntime != "" {
		id, err := s.ensureManagedRouter(ctx, agent.RouterRuntime)
		if err != nil {
			return nil, err
		}
		agent.RouterAgentID = id
	}
	return s.validateRouter(ctx, agent.RouterAgentID)
}

// ensureManagedRouter keeps one read-only router Agent per framework. It holds
// nothing specific to a Super Agent, since each one's guidance travels in the
// prompt and each Trigger has its own conversation, so an abandoned form leaves
// nothing to clean up. An existing one is kept as is, which lets the operator
// choose its provider or model on the Agents page.
func (s *Service) ensureManagedRouter(ctx context.Context, runtimeID string) (string, error) {
	if s.runtimeAvailable != nil && !s.runtimeAvailable(runtimeID) {
		return "", fmt.Errorf("agent framework %q is not installed on this machine", runtimeID)
	}
	if modes := core.ApprovalModeValuesForRuntime(runtimeID); len(modes) > 0 && !core.ApprovalModeSupported(runtimeID, core.ApprovalModePlan) {
		return "", fmt.Errorf("the router reads untrusted events, so it needs a read-only mode, which %s does not have; choose another framework", runtimeID)
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
		DefaultApprovalMode: core.ApprovalModePlan, Enabled: true, Source: autoWorkerAgentSource,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.store.UpsertAgentInstance(ctx, &instance); err != nil {
		return "", err
	}
	return id, nil
}

// defaultAutoWorkerApproval is the least permissive unattended mode the
// framework has, since frameworks differ in which modes they offer.
func defaultAutoWorkerApproval(runtimeID string) string {
	if core.ApprovalModeSupported(runtimeID, core.ApprovalModeAuto) && !core.ApprovalModeSupported(runtimeID, core.ApprovalModeAutoEdit) {
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

// autoWorkerDir prefers the operator's choice, then a router Agent's own
// directory. A router created from a framework only has a scratch directory, so
// without a choice the workers get one of their own.
func (s *Service) autoWorkerDir(agent core.SuperAgent, router core.AgentInstance) (string, error) {
	if dir := strings.TrimSpace(agent.AutoWorkerDir); dir != "" {
		return expandHome(dir)
	}
	if !IsManagedAgent(router) && strings.TrimSpace(router.WorkDir) != "" {
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
