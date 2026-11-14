package superagent

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"

	"github.com/wangning19940904/AgentMux/core"
)

// autoWorkerID prefixes the workers a router names to have a new one launched,
// one per worker framework, as in "auto:codex". They are not roster entries, so
// normalizeWorkers keeps the prefix reserved.
const autoWorkerID = "auto"

const autoWorkerAgentSource = "super_agent"

const autoWorkerGuidance = "Launches a new %s worker on this machine with its own session and directory. Choose it when no worker above suits the work."

func autoWorkerRosterID(runtimeID string) string { return autoWorkerID + ":" + runtimeID }

func isAutoWorkerID(id string) bool {
	return id == autoWorkerID || strings.HasPrefix(id, autoWorkerID+":")
}

// autoWorkerAgentID names the Agent instance that runs a Super Agent's auto
// workers of one framework.
func autoWorkerAgentID(superAgentID, runtimeID string) string {
	return "saw_" + superAgentID + "_" + runtimeID
}

// IsManagedAgent reports whether a Super Agent owns this Agent instance.
func IsManagedAgent(agent core.AgentInstance) bool { return agent.Source == autoWorkerAgentSource }

func validateAutoWorkerApproval(mode string) error {
	switch mode {
	case core.ApprovalModeAutoEdit, core.ApprovalModeAuto, core.ApprovalModeYolo:
		return nil
	default:
		return fmt.Errorf("auto workers run unattended, so auto_worker_approval_mode must be %s, %s or %s", core.ApprovalModeAutoEdit, core.ApprovalModeAuto, core.ApprovalModeYolo)
	}
}

// launchAutoWorker gives a new Ticket a worker that was not configured in
// advance. A decision can ask for one of the worker frameworks, but everything
// that determines what it may do comes from the operator: the Super Agent's
// directory and approval mode, and for the router's own framework, the router
// Agent's provider and environment. Tools and channels are not inherited. Each
// Ticket has its own conversation, and so its own session and, in a git
// repository, its own worktree. MaxConcurrency bounds how many run at once.
func (s *Service) launchAutoWorker(ctx context.Context, agent core.SuperAgent, runtimeID string) (core.SuperAgentWorker, error) {
	if !agent.AutoWorkers {
		return core.SuperAgentWorker{}, errors.New("router chose to launch a worker, which this Super Agent does not allow")
	}
	if !slices.Contains(agent.WorkerRuntimes, runtimeID) {
		return core.SuperAgentWorker{}, fmt.Errorf("router chose to launch a %q worker, which is not one of this Super Agent's worker frameworks", runtimeID)
	}
	router, err := s.store.GetAgentInstance(ctx, agent.RouterAgentID)
	if err != nil {
		return core.SuperAgentWorker{}, err
	}
	if router == nil {
		return core.SuperAgentWorker{}, fmt.Errorf("router agent %q not found on this machine", agent.RouterAgentID)
	}
	workDir, err := s.autoWorkerDir(agent, *router)
	if err != nil {
		return core.SuperAgentWorker{}, err
	}
	id := autoWorkerAgentID(agent.ID, runtimeID)
	now := s.now().UTC()
	createdAt := now
	existing, err := s.store.GetAgentInstance(ctx, id)
	if err != nil {
		return core.SuperAgentWorker{}, err
	}
	if existing != nil {
		createdAt = existing.CreatedAt
	}
	name := fmt.Sprintf("%s auto worker (%s)", agent.Name, runtimeID)
	instance := core.AgentInstance{
		ID: id, Name: name, RuntimeID: runtimeID, SessionBackend: "structured",
		WorkDir: workDir, WorkspaceMode: autoWorkerWorkspaceMode(ctx, workDir),
		DefaultApprovalMode: firstNonEmpty(agent.AutoWorkerApprovalMode, defaultAutoWorkerApproval(runtimeID)),
		Enabled:             true, Source: autoWorkerAgentSource,
		CreatedAt: createdAt, UpdatedAt: now,
	}
	// The router's model and environment belong to its framework; another
	// framework uses its own defaults.
	if runtimeID == router.RuntimeID {
		instance.SessionBackend = router.SessionBackend
		instance.DefaultModel, instance.DefaultReasoningEffort, instance.DefaultServiceTier = router.DefaultModel, router.DefaultReasoningEffort, router.DefaultServiceTier
		instance.Env = router.Env
	}
	if err := s.store.UpsertAgentInstance(ctx, &instance); err != nil {
		return core.SuperAgentWorker{}, err
	}
	return core.SuperAgentWorker{ID: autoWorkerRosterID(runtimeID), Label: name, AgentID: id}, nil
}

// deleteAutoWorkers removes the Agent instances a Super Agent launched, including
// those of frameworks no longer on its list and the single instance used before
// worker frameworks could be chosen.
func (s *Service) deleteAutoWorkers(ctx context.Context, superAgentID string) error {
	instances, err := s.store.ListAgentInstances(ctx)
	if err != nil {
		return err
	}
	prefix := autoWorkerAgentID(superAgentID, "")
	for _, instance := range instances {
		runtimeID, found := strings.CutPrefix(instance.ID, prefix)
		owned := instance.ID == "saw_"+superAgentID || (found && runtimeID == instance.RuntimeID)
		if !IsManagedAgent(instance) || !owned {
			continue
		}
		if err := s.store.DeleteAgentInstance(ctx, instance.ID); err != nil {
			return err
		}
	}
	return nil
}

// autoWorkerWorkspaceMode isolates parallel Tickets wherever git allows it.
func autoWorkerWorkspaceMode(ctx context.Context, workDir string) string {
	output, err := exec.CommandContext(ctx, "git", "-C", workDir, "rev-parse", "--is-inside-work-tree").Output()
	if err == nil && strings.TrimSpace(string(output)) == "true" {
		return "worktree"
	}
	return "shared"
}
