package superagent

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/wangning19940904/AgentMux/core"
)

// autoWorkerID is the worker a router names to have a new one launched. It is
// not a roster entry, so normalizeWorkers keeps it reserved.
const autoWorkerID = "auto"

const autoWorkerAgentSource = "super_agent"

const autoWorkerGuidance = "Launches a new worker on this machine with its own session and directory. Choose it when no worker above suits the work."

func autoWorkerAgentID(superAgentID string) string { return "saw_" + superAgentID }

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
// advance. A decision can ask for one, but everything that determines what it
// may do comes from the operator: the runtime, provider and environment of the
// router Agent, and the Super Agent's directory and approval mode. Tools and
// channels are not inherited. Each Ticket has its own conversation, and so its
// own session and, in a git repository, its own worktree. MaxConcurrency bounds
// how many run at once.
func (s *Service) launchAutoWorker(ctx context.Context, agent core.SuperAgent) (core.SuperAgentWorker, error) {
	if !agent.AutoWorkers {
		return core.SuperAgentWorker{}, errors.New("router chose to launch a worker, which this Super Agent does not allow")
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
	id := autoWorkerAgentID(agent.ID)
	now := s.now().UTC()
	createdAt := now
	existing, err := s.store.GetAgentInstance(ctx, id)
	if err != nil {
		return core.SuperAgentWorker{}, err
	}
	if existing != nil {
		createdAt = existing.CreatedAt
	}
	name := agent.Name + " auto worker"
	instance := core.AgentInstance{
		ID: id, Name: name, RuntimeID: router.RuntimeID, SessionBackend: router.SessionBackend,
		WorkDir: workDir, WorkspaceMode: autoWorkerWorkspaceMode(ctx, workDir),
		ProviderTool: router.ProviderTool, ProviderID: router.ProviderID, ProviderName: router.ProviderName,
		DefaultModel: router.DefaultModel, DefaultReasoningEffort: router.DefaultReasoningEffort, DefaultServiceTier: router.DefaultServiceTier,
		DefaultApprovalMode: firstNonEmpty(agent.AutoWorkerApprovalMode, defaultAutoWorkerApproval(router.RuntimeID)),
		Env:                 router.Env, Enabled: true, Source: autoWorkerAgentSource,
		OwnerTenantID: router.OwnerTenantID, OwnerTenantName: router.OwnerTenantName, Visibility: router.Visibility,
		CreatedAt: createdAt, UpdatedAt: now,
	}
	if err := s.store.UpsertAgentInstance(ctx, &instance); err != nil {
		return core.SuperAgentWorker{}, err
	}
	return core.SuperAgentWorker{ID: autoWorkerID, Label: name, AgentID: id}, nil
}

// autoWorkerWorkspaceMode isolates parallel Tickets wherever git allows it.
func autoWorkerWorkspaceMode(ctx context.Context, workDir string) string {
	output, err := exec.CommandContext(ctx, "git", "-C", workDir, "rev-parse", "--is-inside-work-tree").Output()
	if err == nil && strings.TrimSpace(string(output)) == "true" {
		return "worktree"
	}
	return "shared"
}
