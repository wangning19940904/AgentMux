package superagent

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// holder stores a Ticket that holds dir on this machine.
func (h *harness) holder(id, agentID, dir, status string) core.SuperAgentTicket {
	h.t.Helper()
	ticket := core.SuperAgentTicket{
		ID: id, SuperAgentID: agentID, Title: "Busy work", Status: status, ThreadKey: "other",
		Worker:    core.SuperAgentWorker{ID: "auto:dsh", AgentID: "saw"},
		Workspace: &core.SuperAgentTicketWorkspace{Dir: cleanDir(dir, true), HeldSince: h.clock.Add(-time.Minute)},
		CreatedAt: *h.clock, UpdatedAt: *h.clock,
	}
	if err := h.store.UpsertSuperAgentTicket(context.Background(), ticket); err != nil {
		h.t.Fatal(err)
	}
	return ticket
}

func accept(worker string, extra ...func(*routerAction)) routerAction {
	action := routerAction{Action: actionAccept, WorkerID: worker, Title: "More work"}
	for _, change := range extra {
		change(&action)
	}
	return action
}

func (h *harness) preflightAccepts(actions ...routerAction) error {
	h.t.Helper()
	agent := h.service.withWorkerRuntimes(context.Background(), h.agent)
	return h.service.preflight(context.Background(), agent, core.SuperAgentTrigger{ThreadKey: "t"}, routerDecision{Actions: actions})
}

func TestAcceptForAHeldDirectoryIsRefusedNamingTheHolder(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	h.enableAutoWorkers(dir)
	h.holder("tkt_busy", h.agent.ID, dir, core.SuperAgentTicketRunning)

	err := h.preflightAccepts(accept("auto:dsh"))
	if err == nil || !strings.Contains(err.Error(), "in use by ticket tkt_busy (\"Busy work\")") || !strings.Contains(err.Error(), "depends_on") {
		t.Fatalf("err = %v", err)
	}
	// Waiting for the holder is allowed, directly or through another ticket.
	if err := h.preflightAccepts(accept("auto:dsh", func(a *routerAction) { a.DependsOn = []string{"tkt_busy"} })); err != nil {
		t.Fatal(err)
	}
	if err := h.store.UpsertSuperAgentTicket(context.Background(), core.SuperAgentTicket{ID: "tkt_next", SuperAgentID: h.agent.ID, Status: core.SuperAgentTicketQueued, DependsOn: []string{"tkt_busy"}}); err != nil {
		t.Fatal(err)
	}
	if err := h.preflightAccepts(accept("auto:dsh", func(a *routerAction) { a.DependsOn = []string{"tkt_next"} })); err != nil {
		t.Fatal(err)
	}
	// Another agent's ticket on this machine holds it too.
	h.store.mu.Lock()
	delete(h.store.tickets, "tkt_busy")
	h.store.mu.Unlock()
	h.holder("tkt_theirs", "someone-else", dir, core.SuperAgentTicketQueued)
	if err := h.preflightAccepts(accept("auto:dsh")); err == nil || !strings.Contains(err.Error(), "of agent someone-else") {
		t.Fatalf("err = %v", err)
	}
	// A finished ticket holds nothing.
	ticket, _ := h.store.GetSuperAgentTicket(context.Background(), "tkt_theirs")
	ticket.Status = core.SuperAgentTicketReady
	releaseWorkspace(ticket)
	_ = h.store.UpsertSuperAgentTicket(context.Background(), *ticket)
	if err := h.preflightAccepts(accept("auto:dsh")); err != nil {
		t.Fatal(err)
	}
}

func TestAcceptsInOneBlockCannotShareADirectory(t *testing.T) {
	h := newHarness(t)
	h.enableAutoWorkers(t.TempDir())
	err := h.preflightAccepts(accept("auto:dsh", func(a *routerAction) { a.Ref = "a" }), accept("auto:dsh"))
	if err == nil || !strings.Contains(err.Error(), "the accept with ref a") {
		t.Fatalf("err = %v", err)
	}
	if err := h.preflightAccepts(accept("auto:dsh", func(a *routerAction) { a.Ref = "a" }), accept("auto:dsh", func(a *routerAction) { a.DependsOn = []string{"a"} })); err != nil {
		t.Fatal(err)
	}
}

func TestIsolatedWorkersNeverConflict(t *testing.T) {
	h := newHarness(t)
	repository := t.TempDir()
	if output, err := exec.Command("git", "-C", repository, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git: %v %s", err, output)
	}
	h.enableAutoWorkers(repository)
	h.ingest("alice", "github:o/r#1", "first", true)
	h.ingest("alice", "github:o/r#2", "second", true)
	h.router.answer = func(core.InvocationRequest) (string, error) { return acceptAnswer("auto:dsh"), nil }
	h.settle()
	tickets := h.store.allTickets()
	if len(tickets) != 2 {
		t.Fatalf("both accepts must start: %+v", tickets)
	}
	for _, ticket := range tickets {
		if ticket.Workspace == nil || !ticket.Workspace.Isolated || ticket.Workspace.Dir != cleanDir(repository, true) {
			t.Fatalf("workspace = %+v", ticket.Workspace)
		}
	}
}

func TestTicketHoldsItsDirectoryWhileItRuns(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	h.enableAutoWorkers(dir)
	h.router.answer = func(core.InvocationRequest) (string, error) { return acceptAnswer("auto:dsh"), nil }
	held := false
	h.workers.run = func(context.Context, workerCall) (string, error) {
		tickets := h.store.allTickets()
		held = len(tickets) == 1 && holdsWorkspace(tickets[0])
		view := h.service.routerView(context.Background(), h.agent, core.SuperAgentTrigger{ThreadKey: "x"}, tickets)
		var prompt strings.Builder
		writeRouterContext(&prompt, h.agent, view.Statuses, view)
		if !strings.Contains(prompt.String(), "DIRECTORIES IN USE") || !strings.Contains(prompt.String(), `"dir": "`+cleanDir(dir, true)+`"`) || view.Tickets[0].Holds == "" {
			held = false
		}
		activity, err := h.service.Activity(context.Background(), h.agent.ID)
		if err != nil || len(activity.WorkspaceClaims) != 1 || activity.WorkspaceClaims[0].Machine != "this machine" {
			held = false
		}
		return "done", nil
	}
	h.ingest("alice", "github:o/r#1", "fix it", true)
	h.settle()
	if !held {
		t.Fatal("a running ticket must hold its directory, and the brain and Console must see it")
	}
	ticket := h.store.allTickets()[0]
	if ticket.Status != core.SuperAgentTicketReady || ticket.Workspace == nil || ticket.Workspace.Isolated || !ticket.Workspace.HeldSince.IsZero() {
		t.Fatalf("a ready ticket must release its directory: %+v %+v", ticket.Status, ticket.Workspace)
	}
}

func TestQueuedTicketWaitsForAHeldDirectoryAndClaimsSurviveARestart(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	h.enableAutoWorkers(dir)
	h.holder("tkt_busy", h.agent.ID, dir, core.SuperAgentTicketRunning)
	waiting := core.SuperAgentTicket{
		ID: "tkt_wait", SuperAgentID: h.agent.ID, Title: "Waits", Status: core.SuperAgentTicketQueued, ThreadKey: "t",
		Worker: core.SuperAgentWorker{ID: "local", AgentID: "coder"}, ConversationID: "c", Inputs: []string{"go"},
		Workspace: &core.SuperAgentTicketWorkspace{Dir: cleanDir(dir, true)}, CreatedAt: *h.clock, UpdatedAt: *h.clock,
	}
	if err := h.store.UpsertSuperAgentTicket(context.Background(), waiting); err != nil {
		t.Fatal(err)
	}
	// The daemon restarts: the holder is queued again and still holds.
	restarted := New(Options{Store: h.store, Invoker: h.router, Workers: h.workers, Replies: h.replies, StateDir: h.service.state, Now: func() time.Time { return *h.clock }})
	h.workers.run = func(ctx context.Context, call workerCall) (string, error) {
		if call.conv == "c" {
			return "waited", nil
		}
		<-ctx.Done()
		return "", ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := restarted.Start(ctx); err != nil {
		t.Fatal(err)
	}
	busy, _ := h.store.GetSuperAgentTicket(context.Background(), "tkt_busy")
	if !holdsWorkspace(*busy) {
		t.Fatalf("a recovered ticket keeps its claim: %+v", busy)
	}
	h.service = restarted
	if err := h.preflightAccepts(accept("auto:dsh")); err == nil || !strings.Contains(err.Error(), "tkt_busy") {
		t.Fatalf("err = %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		restarted.Pass(ctx)
		time.Sleep(5 * time.Millisecond)
	}
	h.workers.mu.Lock()
	for _, call := range h.workers.calls {
		if call.conv == "c" {
			t.Error("a ticket must not start in a directory another holds")
		}
	}
	h.workers.mu.Unlock()
	if err := restarted.StopTicket(context.Background(), "tkt_busy"); err != nil {
		t.Fatal(err)
	}
	ready := func() bool {
		got, _ := h.store.GetSuperAgentTicket(context.Background(), "tkt_wait")
		return got.Status == core.SuperAgentTicketReady
	}
	for deadline = time.Now().Add(5 * time.Second); time.Now().Before(deadline) && !ready(); {
		restarted.Pass(ctx)
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	restarted.Stop()
	if got, _ := h.store.GetSuperAgentTicket(context.Background(), "tkt_wait"); got.Status != core.SuperAgentTicketReady {
		t.Fatalf("the waiting ticket must run once the directory is free: %+v", got)
	}
}

func TestOneMachineReachedTwoWaysIsOneClaim(t *testing.T) {
	h := newHarness(t)
	h.service.machineUIDs = func(hostID string) string {
		return map[string]string{"dev_1": "uid-a", "dev_2": "uid-a", "": "uid-self"}[hostID]
	}
	a := h.service.workspaceKey(core.SuperAgentTicketWorkspace{HostID: "dev_1", Dir: "~/code"})
	b := h.service.workspaceKey(core.SuperAgentTicketWorkspace{HostID: "dev_2", Dir: "~/code"})
	local := h.service.workspaceKey(core.SuperAgentTicketWorkspace{Dir: "~/code"})
	if a != b || a == local {
		t.Fatalf("keys: %q %q %q", a, b, local)
	}
}
