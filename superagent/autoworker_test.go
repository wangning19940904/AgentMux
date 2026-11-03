package superagent

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func (h *harness) enableAutoWorkers(workDir string) {
	h.t.Helper()
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	router := h.store.local["router"]
	router.WorkDir = workDir
	router.RuntimeID, router.ProviderID, router.DefaultModel = "dsh", "provider-1", "model-1"
	router.Env = map[string]string{"DEEPSEEK_API_KEY": "from-router"}
	router.MCPServers, router.Skills = []string{"mcp-1"}, []string{"skill-1"}
	h.store.local["router"] = router
	h.agent.AutoWorkers = true
	h.store.agents[h.agent.ID] = h.agent
}

func TestRouterLaunchesAWorkerNoOneConfigured(t *testing.T) {
	h := newHarness(t)
	h.enableAutoWorkers(t.TempDir())
	h.router.answer = func(core.InvocationRequest) (string, error) { return acceptAnswer("auto"), nil }
	h.ingest("alice", "github:o/r#1", "first job", true)
	h.ingest("alice", "github:o/r#2", "second job", true)
	h.settle()

	if !strings.Contains(h.router.prompts[0], `"id": "auto"`) {
		t.Fatalf("the roster must offer a new worker:\n%s", h.router.prompts[0])
	}
	if h.workers.callCount() != 2 {
		t.Fatalf("worker calls = %d", h.workers.callCount())
	}
	tickets := h.store.allTickets()
	id := autoWorkerAgentID(h.agent.ID)
	if tickets[0].Worker.AgentID != id || tickets[1].Worker.AgentID != id || tickets[0].Worker.ID != autoWorkerID {
		t.Fatalf("tickets = %+v", tickets)
	}
	if tickets[0].ConversationID == tickets[1].ConversationID {
		t.Fatal("each Ticket needs its own session and directory")
	}
	worker, _ := h.store.GetAgentInstance(context.Background(), id)
	if worker == nil || !IsManagedAgent(*worker) || !worker.Enabled {
		t.Fatalf("worker = %+v", worker)
	}
	if worker.RuntimeID != "dsh" || worker.ProviderID != "provider-1" || worker.DefaultModel != "model-1" || worker.Env["DEEPSEEK_API_KEY"] != "from-router" {
		t.Fatalf("the worker must run like the router: %+v", worker)
	}
	if worker.DefaultApprovalMode != core.ApprovalModeAutoEdit {
		t.Fatalf("approval mode = %q", worker.DefaultApprovalMode)
	}
	if len(worker.MCPServers) != 0 || len(worker.Skills) != 0 || len(worker.ChannelBindings) != 0 {
		t.Fatalf("tools and channels must not be inherited: %+v", worker)
	}
}

func TestRouterDecisionCannotSetWhatAnAutoWorkerMayDo(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	h.enableAutoWorkers(dir)
	h.router.answer = func(core.InvocationRequest) (string, error) {
		return `{"decision":"accept","worker_id":"auto","title":"x","approval_mode":"yolo","default_approval_mode":"yolo","work_dir":"/etc","env":{"A":"b"},"host_id":"host-1"}`, nil
	}
	h.ingest("alice", "github:o/r#1", "work", true)
	h.settle()
	worker, _ := h.store.GetAgentInstance(context.Background(), autoWorkerAgentID(h.agent.ID))
	if worker.DefaultApprovalMode != core.ApprovalModeAutoEdit || worker.WorkDir != dir || worker.Env["A"] != "" {
		t.Fatalf("worker = %+v", worker)
	}
	if ticket := h.store.allTickets()[0]; ticket.Worker.HostID != "" {
		t.Fatalf("ticket = %+v", ticket)
	}
}

func TestAutoWorkerUsesOperatorLimits(t *testing.T) {
	h := newHarness(t)
	h.enableAutoWorkers(t.TempDir())
	dir := t.TempDir()
	h.agent.AutoWorkerDir, h.agent.AutoWorkerApprovalMode = dir, core.ApprovalModeYolo
	h.store.agents[h.agent.ID] = h.agent
	h.router.answer = func(core.InvocationRequest) (string, error) { return acceptAnswer("auto"), nil }
	h.ingest("alice", "github:o/r#1", "work", true)
	h.settle()
	worker, _ := h.store.GetAgentInstance(context.Background(), autoWorkerAgentID(h.agent.ID))
	if worker.WorkDir != dir || worker.DefaultApprovalMode != core.ApprovalModeYolo {
		t.Fatalf("worker = %+v", worker)
	}
}

func TestAutoWorkerIsolatesTicketsInAGitRepository(t *testing.T) {
	repository, plain := t.TempDir(), t.TempDir()
	if output, err := exec.Command("git", "-C", repository, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	ctx := context.Background()
	if mode := autoWorkerWorkspaceMode(ctx, repository); mode != "worktree" {
		t.Fatalf("repository mode = %q", mode)
	}
	if mode := autoWorkerWorkspaceMode(ctx, plain); mode != "shared" {
		t.Fatalf("plain directory mode = %q", mode)
	}
}

func TestAutoWorkersOffRefusesTheLaunch(t *testing.T) {
	h := newHarness(t)
	h.router.answer = func(core.InvocationRequest) (string, error) { return acceptAnswer("auto"), nil }
	h.ingest("alice", "github:o/r#1", "work", true)
	h.settle()
	if strings.Contains(h.router.prompts[0], `"id": "auto"`) {
		t.Fatalf("the roster must not offer a new worker:\n%s", h.router.prompts[0])
	}
	if h.workers.callCount() != 0 || len(h.store.allTickets()) != 0 {
		t.Fatal("a refused launch must not run work")
	}
	if worker, _ := h.store.GetAgentInstance(context.Background(), autoWorkerAgentID(h.agent.ID)); worker != nil {
		t.Fatalf("worker = %+v", worker)
	}
	if trigger := h.disposition(0); trigger.Disposition != core.SuperAgentDispositionFailed {
		t.Fatalf("trigger = %+v", trigger)
	}
	if len(h.replies.sent()) != 1 {
		t.Fatalf("replies = %v", h.replies.sent())
	}
}

func TestObserveModeLaunchesNothing(t *testing.T) {
	h := newHarness(t)
	h.enableAutoWorkers(t.TempDir())
	h.agent.Mode = core.SuperAgentModeObserve
	h.store.agents[h.agent.ID] = h.agent
	h.router.answer = func(core.InvocationRequest) (string, error) { return acceptAnswer("auto"), nil }
	h.ingest("alice", "github:o/r#1", "work", true)
	h.settle()
	if worker, _ := h.store.GetAgentInstance(context.Background(), autoWorkerAgentID(h.agent.ID)); worker != nil || h.workers.callCount() != 0 {
		t.Fatalf("worker = %+v, calls = %d", worker, h.workers.callCount())
	}
}

func TestConcurrencyLimitAppliesToAutoWorkers(t *testing.T) {
	h := newHarness(t)
	h.enableAutoWorkers(t.TempDir())
	h.agent.MaxConcurrency = 1
	h.store.agents[h.agent.ID] = h.agent
	h.router.answer = func(core.InvocationRequest) (string, error) { return acceptAnswer("auto"), nil }
	release := make(chan struct{})
	h.workers.run = func(context.Context, workerCall) (string, error) {
		<-release
		return "done", nil
	}
	h.ingest("alice", "github:o/r#1", "one", true)
	h.ingest("alice", "github:o/r#2", "two", true)
	ctx := context.Background()
	for range 2000 {
		h.service.Pass(ctx)
		if tickets := h.store.allTickets(); len(tickets) == 2 && h.workers.callCount() == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	h.service.Pass(ctx)
	if tickets := h.store.allTickets(); len(tickets) != 2 || h.workers.callCount() != 1 {
		t.Errorf("tickets = %d, calls = %d", len(tickets), h.workers.callCount())
	}
	close(release)
	h.settle()
	if h.workers.callCount() != 2 {
		t.Fatalf("calls = %d", h.workers.callCount())
	}
}

func TestSaveAutoWorkers(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.enableAutoWorkers(t.TempDir())
	saved, err := h.service.Save(ctx, core.SuperAgent{RouterAgentID: "router", AutoWorkers: true})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Name != "router" || saved.AutoWorkerApprovalMode != core.ApprovalModeAutoEdit || saved.Mode != core.SuperAgentModeObserve {
		t.Fatalf("saved = %+v", saved)
	}
	for name, agent := range map[string]core.SuperAgent{
		"manual approval": {RouterAgentID: "router", AutoWorkers: true, AutoWorkerApprovalMode: core.ApprovalModeManual},
		"plan approval":   {RouterAgentID: "router", AutoWorkers: true, AutoWorkerApprovalMode: core.ApprovalModePlan},
		"reserved id":     {RouterAgentID: "router", AutoWorkers: true, Workers: []core.SuperAgentWorker{{ID: "auto", AgentID: "coder"}}},
		"no workers":      {RouterAgentID: "router"},
	} {
		if _, err := h.service.Save(ctx, agent); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
	h.store.mu.Lock()
	router := h.store.local["router"]
	router.WorkDir = ""
	h.store.local["router"] = router
	h.store.mu.Unlock()
	if _, err := h.service.Save(ctx, core.SuperAgent{RouterAgentID: "router", AutoWorkers: true}); err == nil || !strings.Contains(err.Error(), "working directory") {
		t.Fatalf("err = %v", err)
	}
}

func TestDeleteRemovesTheManagedWorker(t *testing.T) {
	h := newHarness(t)
	h.enableAutoWorkers(t.TempDir())
	h.router.answer = func(core.InvocationRequest) (string, error) { return acceptAnswer("auto"), nil }
	h.ingest("alice", "github:o/r#1", "work", true)
	h.settle()
	ctx := context.Background()
	id := autoWorkerAgentID(h.agent.ID)
	if worker, _ := h.store.GetAgentInstance(ctx, id); worker == nil {
		t.Fatal("expected a managed worker")
	}
	if err := h.service.Delete(ctx, h.agent.ID); err != nil {
		t.Fatal(err)
	}
	if worker, _ := h.store.GetAgentInstance(ctx, id); worker != nil {
		t.Fatalf("worker = %+v", worker)
	}
}
