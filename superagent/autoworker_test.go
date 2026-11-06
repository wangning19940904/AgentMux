package superagent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
	h.agent.AutoWorkers, h.agent.AutoWorkerDir = true, workDir
	h.store.agents[h.agent.ID] = h.agent
}

func TestRouterLaunchesAWorkerNoOneConfigured(t *testing.T) {
	h := newHarness(t)
	h.enableAutoWorkers(t.TempDir())
	h.router.answer = func(core.InvocationRequest) (string, error) { return acceptAnswer("auto:dsh"), nil }
	h.ingest("alice", "github:o/r#1", "first job", true)
	h.ingest("alice", "github:o/r#2", "second job", true)
	h.settle()

	if !strings.Contains(h.router.prompts[0], `"id": "auto:dsh"`) {
		t.Fatalf("the roster must offer a new worker:\n%s", h.router.prompts[0])
	}
	if h.workers.callCount() != 2 {
		t.Fatalf("worker calls = %d", h.workers.callCount())
	}
	tickets := h.store.allTickets()
	id := autoWorkerAgentID(h.agent.ID, "dsh")
	if tickets[0].Worker.AgentID != id || tickets[1].Worker.AgentID != id || tickets[0].Worker.ID != "auto:dsh" {
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
		return `{"decision":"accept","worker_id":"auto:dsh","title":"x","approval_mode":"yolo","default_approval_mode":"yolo","work_dir":"/etc","env":{"A":"b"},"host_id":"host-1"}`, nil
	}
	h.ingest("alice", "github:o/r#1", "work", true)
	h.settle()
	worker, _ := h.store.GetAgentInstance(context.Background(), autoWorkerAgentID(h.agent.ID, "dsh"))
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
	h.router.answer = func(core.InvocationRequest) (string, error) { return acceptAnswer("auto:dsh"), nil }
	h.ingest("alice", "github:o/r#1", "work", true)
	h.settle()
	worker, _ := h.store.GetAgentInstance(context.Background(), autoWorkerAgentID(h.agent.ID, "dsh"))
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
	h.router.answer = func(core.InvocationRequest) (string, error) { return acceptAnswer("auto:dsh"), nil }
	h.ingest("alice", "github:o/r#1", "work", true)
	h.settle()
	if strings.Contains(h.router.prompts[0], `"id": "auto`) {
		t.Fatalf("the roster must not offer a new worker:\n%s", h.router.prompts[0])
	}
	if h.workers.callCount() != 0 || len(h.store.allTickets()) != 0 {
		t.Fatal("a refused launch must not run work")
	}
	if worker, _ := h.store.GetAgentInstance(context.Background(), autoWorkerAgentID(h.agent.ID, "dsh")); worker != nil {
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
	h.router.answer = func(core.InvocationRequest) (string, error) { return acceptAnswer("auto:dsh"), nil }
	h.ingest("alice", "github:o/r#1", "work", true)
	h.settle()
	if worker, _ := h.store.GetAgentInstance(context.Background(), autoWorkerAgentID(h.agent.ID, "dsh")); worker != nil || h.workers.callCount() != 0 {
		t.Fatalf("worker = %+v, calls = %d", worker, h.workers.callCount())
	}
}

func TestConcurrencyLimitAppliesToAutoWorkers(t *testing.T) {
	h := newHarness(t)
	h.enableAutoWorkers(t.TempDir())
	h.agent.MaxConcurrency = 1
	h.store.agents[h.agent.ID] = h.agent
	h.router.answer = func(core.InvocationRequest) (string, error) { return acceptAnswer("auto:dsh"), nil }
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
	if saved.Name != "Super Agent (dsh)" || !slices.Equal(saved.WorkerRuntimes, []string{"dsh"}) || saved.AutoWorkerApprovalMode != "" || saved.Mode != core.SuperAgentModeObserve {
		t.Fatalf("saved = %+v", saved)
	}
	for name, agent := range map[string]core.SuperAgent{
		"manual approval": {RouterAgentID: "router", AutoWorkers: true, AutoWorkerApprovalMode: core.ApprovalModeManual},
		"plan approval":   {RouterAgentID: "router", AutoWorkers: true, AutoWorkerApprovalMode: core.ApprovalModePlan},
		"reserved id":     {RouterAgentID: "router", AutoWorkers: true, Workers: []core.SuperAgentWorker{{ID: "auto", AgentID: "coder"}}},
		"reserved prefix": {RouterAgentID: "router", AutoWorkers: true, Workers: []core.SuperAgentWorker{{ID: "auto:dsh", AgentID: "coder"}}},
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
	if _, err := h.service.Save(ctx, core.SuperAgent{RouterAgentID: "router", AutoWorkers: true, AutoWorkerDir: "/no/such/directory"}); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("err = %v", err)
	}
}

func TestFrameworkIsEnoughToCreateASuperAgent(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	saved, err := h.service.Save(ctx, core.SuperAgent{RouterRuntime: "codex", AutoWorkers: true, Enabled: true, Mode: core.SuperAgentModeAct})
	if err != nil {
		t.Fatal(err)
	}
	router, _ := h.store.GetAgentInstance(ctx, saved.RouterAgentID)
	if router == nil || !IsRouterAgent(*router) || IsManagedAgent(*router) || router.RuntimeID != "codex" || router.DefaultApprovalMode != core.ApprovalModePlan || router.WorkDir == "" {
		t.Fatalf("router = %+v", router)
	}
	if saved.Name != "Super Agent (codex)" || !slices.Equal(saved.WorkerRuntimes, []string{"codex"}) {
		t.Fatalf("saved = %+v", saved)
	}
	stored, _ := h.store.GetSuperAgent(ctx, saved.ID)
	h.agent = *stored
	h.source = core.SuperAgentSource{ID: "gh", Kind: core.SuperAgentSourceGitHub, Enabled: true, AllowedActors: []string{"alice"}}
	h.router.answer = func(core.InvocationRequest) (string, error) { return acceptAnswer("auto:codex"), nil }
	h.ingest("alice", "github:o/r#1", "work", true)
	h.settle()
	worker, _ := h.store.GetAgentInstance(ctx, autoWorkerAgentID(saved.ID, "codex"))
	if worker == nil || worker.RuntimeID != "codex" || worker.DefaultApprovalMode != core.ApprovalModeAutoEdit || worker.WorkDir == router.WorkDir || !strings.Contains(worker.WorkDir, saved.ID) {
		t.Fatalf("workers must not share the router's scratch directory: %+v", worker)
	}
	if info, err := os.Stat(worker.WorkDir); err != nil || !info.IsDir() {
		t.Fatalf("work dir: %v", err)
	}
	if h.workers.callCount() != 1 {
		t.Fatalf("calls = %d", h.workers.callCount())
	}
}

func TestManagedRouterIsSharedAndKeepsOperatorChanges(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	first, err := h.service.Save(ctx, core.SuperAgent{RouterRuntime: "codex", AutoWorkers: true})
	if err != nil {
		t.Fatal(err)
	}
	router, _ := h.store.GetAgentInstance(ctx, first.RouterAgentID)
	router.DefaultModel = "chosen-on-the-agents-page"
	if err := h.store.UpsertAgentInstance(ctx, router); err != nil {
		t.Fatal(err)
	}
	second, err := h.service.Save(ctx, core.SuperAgent{RouterRuntime: "codex", AutoWorkers: true})
	if err != nil {
		t.Fatal(err)
	}
	router, _ = h.store.GetAgentInstance(ctx, second.RouterAgentID)
	if first.RouterAgentID != second.RouterAgentID || router.DefaultModel != "chosen-on-the-agents-page" {
		t.Fatalf("router = %+v", router)
	}
	if err := h.service.Delete(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if router, _ = h.store.GetAgentInstance(ctx, second.RouterAgentID); router == nil {
		t.Fatal("another Super Agent still routes with it")
	}
}

func TestFrameworkRouterRefusals(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.service.runtimeAvailable = func(id string) bool { return id != "gemini" }
	if _, err := h.service.Save(ctx, core.SuperAgent{RouterRuntime: "gemini", AutoWorkers: true}); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Errorf("err = %v", err)
	}
	if _, err := h.service.Save(ctx, core.SuperAgent{RouterRuntime: "kimi", AutoWorkers: true}); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Errorf("err = %v", err)
	}
	if router, _ := h.store.GetAgentInstance(ctx, managedRouterID("kimi")); router != nil {
		t.Errorf("router = %+v", router)
	}
	if _, err := h.service.Save(ctx, core.SuperAgent{AutoWorkers: true}); err == nil {
		t.Error("expected an error without a framework or router")
	}
	if _, err := h.service.Save(ctx, core.SuperAgent{RouterRuntime: "codex", AutoWorkers: true, AutoWorkerApprovalMode: core.ApprovalModeManual}); err == nil {
		t.Error("manual approval must be refused")
	}
}

func TestDefaultApprovalFitsTheFramework(t *testing.T) {
	if mode := defaultAutoWorkerApproval("cursor"); mode != core.ApprovalModeAuto {
		t.Errorf("cursor = %q", mode)
	}
	if mode := defaultAutoWorkerApproval("codex"); mode != core.ApprovalModeAutoEdit {
		t.Errorf("codex = %q", mode)
	}
	h := newHarness(t)
	if _, err := h.service.Save(context.Background(), core.SuperAgent{RouterRuntime: "cursor", AutoWorkers: true, AutoWorkerApprovalMode: core.ApprovalModeAutoEdit}); err == nil {
		t.Fatal("a mode the framework lacks must be refused")
	}
}

func TestAutoWorkerDirExpandsHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	if got, err := expandHome("~/src/widgets"); err != nil || got != filepath.Join(home, "src", "widgets") {
		t.Fatalf("got %q, %v", got, err)
	}
	if got, _ := expandHome("/abs/path"); got != "/abs/path" {
		t.Fatalf("got %q", got)
	}
}

func TestDeleteRemovesTheManagedWorker(t *testing.T) {
	h := newHarness(t)
	h.enableAutoWorkers(t.TempDir())
	h.router.answer = func(core.InvocationRequest) (string, error) { return acceptAnswer("auto:dsh"), nil }
	h.ingest("alice", "github:o/r#1", "work", true)
	h.settle()
	ctx := context.Background()
	id := autoWorkerAgentID(h.agent.ID, "dsh")
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

func TestSaveWorkerFrameworks(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.service.runtimeAvailable = func(id string) bool { return id != "gemini" }
	// Kimi cannot route, since it has no read-only mode, but it can work.
	saved, err := h.service.Save(ctx, core.SuperAgent{RouterRuntime: "codex", AutoWorkers: true, WorkerRuntimes: []string{" kimi ", "cursor", "kimi"}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(saved.WorkerRuntimes, []string{"kimi", "cursor"}) || saved.AutoWorkerApprovalMode != "" {
		t.Fatalf("saved = %+v", saved)
	}
	if _, err := h.service.Save(ctx, core.SuperAgent{RouterRuntime: "codex", AutoWorkers: true, WorkerRuntimes: []string{"gemini"}}); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Errorf("err = %v", err)
	}
	if _, err := h.service.Save(ctx, core.SuperAgent{RouterRuntime: "codex", AutoWorkers: true, WorkerRuntimes: []string{"codex", "cursor"}, AutoWorkerApprovalMode: core.ApprovalModeAutoEdit}); err == nil || !strings.Contains(err.Error(), "cursor") {
		t.Errorf("a mode one worker framework lacks must be refused: %v", err)
	}
	off, err := h.service.Save(ctx, core.SuperAgent{RouterRuntime: "codex", WorkerRuntimes: []string{"kimi"}, Workers: []core.SuperAgentWorker{{AgentID: "coder"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(off.WorkerRuntimes) != 0 {
		t.Fatalf("worker frameworks must be cleared with auto workers off: %+v", off.WorkerRuntimes)
	}
}

func TestRouterChoosesAmongWorkerFrameworks(t *testing.T) {
	h := newHarness(t)
	h.enableAutoWorkers(t.TempDir())
	h.agent.WorkerRuntimes = []string{"dsh", "kimi"}
	h.store.agents[h.agent.ID] = h.agent
	h.router.answer = func(request core.InvocationRequest) (string, error) {
		if strings.Contains(request.Input, "use kimi") {
			return acceptAnswer("auto:kimi"), nil
		}
		return acceptAnswer("auto:codex"), nil
	}
	h.ingest("alice", "github:o/r#1", "use kimi", true)
	h.settle()
	h.ingest("alice", "github:o/r#2", "use codex", true)
	h.settle()

	for _, entry := range []string{`"id": "auto:dsh"`, `"id": "auto:kimi"`, "Launches a new kimi worker"} {
		if !strings.Contains(h.router.prompts[0], entry) {
			t.Fatalf("the roster must offer %s:\n%s", entry, h.router.prompts[0])
		}
	}
	tickets := h.store.allTickets()
	id := autoWorkerAgentID(h.agent.ID, "kimi")
	if len(tickets) != 1 || tickets[0].Worker.ID != "auto:kimi" || tickets[0].Worker.AgentID != id {
		t.Fatalf("tickets = %+v", tickets)
	}
	if trigger := h.disposition(1); trigger.Disposition != core.SuperAgentDispositionFailed {
		t.Fatalf("a framework that is not on the list must be refused: %+v", trigger)
	}
	ctx := context.Background()
	worker, _ := h.store.GetAgentInstance(ctx, id)
	if worker == nil || worker.RuntimeID != "kimi" || worker.DefaultApprovalMode != core.ApprovalModeAuto {
		t.Fatalf("worker = %+v", worker)
	}
	if worker.ProviderID != "" || worker.DefaultModel != "" || len(worker.Env) != 0 {
		t.Fatalf("the router's provider and environment belong to its framework: %+v", worker)
	}

	legacy := core.AgentInstance{ID: "saw_" + h.agent.ID, RuntimeID: "dsh", Source: autoWorkerAgentSource}
	other := core.AgentInstance{ID: autoWorkerAgentID("sa_other", "kimi"), RuntimeID: "kimi", Source: autoWorkerAgentSource}
	for _, instance := range []core.AgentInstance{legacy, other} {
		if err := h.store.UpsertAgentInstance(ctx, &instance); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.service.Delete(ctx, h.agent.ID); err != nil {
		t.Fatal(err)
	}
	for _, removed := range []string{id, legacy.ID} {
		if found, _ := h.store.GetAgentInstance(ctx, removed); found != nil {
			t.Errorf("%s was kept", removed)
		}
	}
	if found, _ := h.store.GetAgentInstance(ctx, other.ID); found == nil {
		t.Error("another Super Agent's worker was removed")
	}
}

func TestSuperAgentSavedBeforeWorkerFrameworksUsesTheRouters(t *testing.T) {
	h := newHarness(t)
	h.enableAutoWorkers(t.TempDir())
	agents, err := h.service.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 || !slices.Equal(agents[0].WorkerRuntimes, []string{"dsh"}) {
		t.Fatalf("agents = %+v", agents)
	}
}
