package superagent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/framework/health"
)

// healthStub is this machine's framework health.
type healthStub struct {
	mu       sync.Mutex
	records  map[string]health.Record
	failures []string
	checks   bool
}

func (h *healthStub) Status(_ context.Context, kind string) (health.Record, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if record, ok := h.records[kind]; ok {
		return record, nil
	}
	return health.Record{Kind: kind, State: health.StateUnknown}, nil
}

func (h *healthStub) RecordFailure(_ context.Context, kind, text string) (health.Record, bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failures = append(h.failures, kind+": "+text)
	return health.Record{}, true, nil
}

func (h *healthStub) Settings(context.Context) (health.Settings, error) {
	return health.Settings{Enabled: h.checks}, nil
}

// machineHealthStub is a machineStub whose machines report health records.
type machineHealthStub struct {
	*machineStub
	records map[string][]health.Record
}

func (m *machineHealthStub) Health(_ context.Context, hostID string) ([]health.Record, error) {
	return m.records[hostID], nil
}

func acceptWith(worker, policy string) string {
	return fmt.Sprintf("on it\n\n<agentmux-action>\n{\"action\":\"accept\",\"worker_id\":%q,\"machine_policy\":%q,\"title\":\"Fix it\",\"instructions\":\"look closely\"}\n</agentmux-action>", worker, policy)
}

// withRuntimes gives the harness's agent auto workers of two frameworks and
// this machine's health.
func (h *harness) withRuntimes(records map[string]health.Record) *healthStub {
	h.t.Helper()
	h.enableAutoWorkers(h.t.TempDir())
	h.store.mu.Lock()
	h.agent.WorkerRuntimes = []string{"dsh", "codex"}
	h.store.agents[h.agent.ID] = h.agent
	h.store.mu.Unlock()
	stub := &healthStub{records: records}
	h.service.healthRecords = stub
	return stub
}

func TestFixedPolicyRefusesAFailingFramework(t *testing.T) {
	h := newHarness(t)
	h.withRuntimes(map[string]health.Record{
		"dsh": {Kind: "dsh", State: health.StateUsageLimit, Reason: "usage limit reached"},
	})
	answers := []string{acceptAnswer("auto:dsh"), acceptAnswer("auto:codex")}
	h.router.answer = func(core.InvocationRequest) (string, error) {
		answer := answers[0]
		if len(answers) > 1 {
			answers = answers[1:]
		}
		return answer, nil
	}
	h.ingest("alice", "github:o/r#1", "fix it", true)
	h.settle()
	if tickets := h.store.allTickets(); len(tickets) != 0 {
		t.Fatalf("a usage-limited framework must not start a ticket: %+v", tickets)
	}
	prompt := h.router.prompts[0]
	for _, want := range []string{`"health": "usage-limited: usage limit reached"`, "PLACEMENT:", "machine_policy"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("the roster must mark the framework (%s):\n%s", want, prompt)
		}
	}
	*h.clock = h.clock.Add(retryDelay + time.Second)
	h.settle()
	if len(h.router.prompts) < 2 || !strings.Contains(h.router.prompts[1], "dsh on this machine is usage-limited") {
		t.Fatalf("the brain must hear why: %v", h.router.prompts)
	}
	tickets := h.store.allTickets()
	if len(tickets) != 1 || tickets[0].Worker.ID != "auto:codex" || tickets[0].Placement != nil {
		t.Fatalf("the brain's second choice must run as asked: %+v", tickets)
	}
}

func TestUnknownHealthNeverHoldsWorkBack(t *testing.T) {
	h := newHarness(t)
	h.withRuntimes(map[string]health.Record{"dsh": {Kind: "dsh", State: health.StateUnknown, Reason: "not checked yet"}})
	h.router.answer = func(core.InvocationRequest) (string, error) { return acceptAnswer("auto:dsh"), nil }
	h.ingest("alice", "github:o/r#1", "fix it", true)
	h.settle()
	if tickets := h.store.allTickets(); len(tickets) != 1 || tickets[0].Status != core.SuperAgentTicketReady {
		t.Fatalf("tickets = %+v", tickets)
	}
	if prompt := h.router.prompts[0]; strings.Contains(prompt, `"health": `) {
		t.Fatalf("unknown health must not be marked:\n%s", prompt)
	}
}

func TestFlexiblePolicyMovesTheTicket(t *testing.T) {
	h := newHarness(t)
	h.withRuntimes(map[string]health.Record{"dsh": {Kind: "dsh", State: health.StateFailing, Reason: "not signed in"}})
	h.router.answer = func(core.InvocationRequest) (string, error) { return acceptWith("auto:dsh", "flexible"), nil }
	trigger := h.ingest("alice", "github:o/r#1", "fix it", true)
	h.settle()
	tickets := h.store.allTickets()
	if len(tickets) != 1 || tickets[0].Worker.ID != "auto:codex" {
		t.Fatalf("the ticket must move to the healthy framework: %+v", tickets)
	}
	moved := tickets[0].Placement
	if moved == nil || moved.RequestedWorkerID != "auto:dsh" || moved.WorkerID != "auto:codex" || moved.Machine != "this machine" || !strings.Contains(moved.Reason, "not signed in") {
		t.Fatalf("placement = %+v", moved)
	}
	stored, _ := h.store.GetSuperAgentTicket(context.Background(), tickets[0].ID)
	if stored.Placement == nil {
		t.Fatal("the move must be stored on the ticket")
	}
	for _, item := range h.store.triggers {
		if item.ID == trigger.ID && !strings.Contains(item.Reason, "moved "+tickets[0].ID+" from auto:dsh to auto:codex") {
			t.Fatalf("the trigger must record the move: %q", item.Reason)
		}
	}
	// The brain sees where it went with its next event.
	view := h.service.routerView(context.Background(), h.agent, trigger, tickets)
	if len(view.Tickets) != 1 || !strings.Contains(view.Tickets[0].Moved, "from auto:dsh to auto:codex on this machine") {
		t.Fatalf("view = %+v", view.Tickets)
	}
}

func TestFlexiblePolicyRefusesWhenNothingCanTakeIt(t *testing.T) {
	h := newHarness(t)
	h.withRuntimes(map[string]health.Record{
		"dsh":   {Kind: "dsh", State: health.StateFailing, Reason: "cli missing"},
		"codex": {Kind: "codex", State: health.StateUsageLimit},
	})
	h.store.mu.Lock()
	h.agent.Workers = nil
	h.store.agents[h.agent.ID] = h.agent
	h.store.mu.Unlock()
	err := h.service.preflight(context.Background(), h.agent, core.SuperAgentTrigger{ThreadKey: "t"}, routerDecision{Actions: []routerAction{{Action: actionAccept, WorkerID: "auto:dsh", MachinePolicy: "flexible"}}})
	if err == nil || !strings.Contains(err.Error(), "no other place") || !strings.Contains(err.Error(), "auto:codex (codex on this machine is usage-limited)") {
		t.Fatalf("err = %v", err)
	}
	if err := validateDecision(routerDecision{Actions: []routerAction{{Action: actionAccept, WorkerID: "auto:dsh", MachinePolicy: "anywhere"}}}); err == nil {
		t.Fatal("an unknown machine_policy must be refused")
	}
}

func TestOfflineMachineIsRefusedOrLeftForAnother(t *testing.T) {
	h := newHarness(t)
	machines := h.withMachines()
	stub := &machineHealthStub{machineStub: machines, records: map[string][]health.Record{}}
	h.service.workers = stub
	machines.offline["dev_1"] = true
	h.agent = h.service.withWorkerRuntimes(context.Background(), h.agent)
	decision := func(policy string) routerDecision {
		return routerDecision{Actions: []routerAction{{Action: actionAccept, WorkerID: "auto:dsh@dev_1", MachinePolicy: policy}}}
	}
	err := h.service.preflight(context.Background(), h.agent, core.SuperAgentTrigger{ThreadKey: "t"}, decision(""))
	if err == nil || !strings.Contains(err.Error(), "machine studio is offline") {
		t.Fatalf("err = %v", err)
	}
	flexible := decision("flexible")
	if err := h.service.preflight(context.Background(), h.agent, core.SuperAgentTrigger{ThreadKey: "t"}, flexible); err != nil {
		t.Fatal(err)
	}
	// The same framework on this machine is closest.
	if placed := flexible.Actions[0].placed; placed == nil || placed.moved == nil || placed.moved.WorkerID != "auto:dsh" {
		t.Fatalf("placed = %+v", placed)
	}

	// A machine's own record marks its framework.
	machines.offline["dev_1"] = false
	stub.records["dev_1"] = []health.Record{{Kind: "dsh", State: health.StateFailing, Reason: "model rejected"}}
	h.service.placement = placementCache{}
	err = h.service.preflight(context.Background(), h.agent, core.SuperAgentTrigger{ThreadKey: "t"}, decision(""))
	if err == nil || !strings.Contains(err.Error(), "dsh on studio is failing: model rejected") {
		t.Fatalf("err = %v", err)
	}
	view := h.service.routerView(context.Background(), h.agent, core.SuperAgentTrigger{ThreadKey: "t"}, nil)
	var prompt strings.Builder
	writeRouterContext(&prompt, h.agent, view.Statuses, view)
	if !strings.Contains(prompt.String(), `"health": "failing: model rejected"`) {
		t.Fatalf("roster:\n%s", prompt.String())
	}
	found := false
	for _, record := range view.Health {
		found = found || (record.HostID == "dev_1" && record.Runtime == "dsh" && record.State == "failing")
	}
	if !found {
		t.Fatalf("health = %+v", view.Health)
	}
}

func TestFailedRunsFeedFrameworkHealth(t *testing.T) {
	h := newHarness(t)
	records := h.withRuntimes(nil)
	h.router.answer = func(core.InvocationRequest) (string, error) { return acceptAnswer("auto:dsh"), nil }
	h.workers.run = func(context.Context, workerCall) (string, error) {
		return "", fmt.Errorf("Not logged in · Please run /login")
	}
	h.ingest("alice", "github:o/r#1", "fix it", true)
	h.settle()
	if len(records.failures) != 1 || !strings.HasPrefix(records.failures[0], "dsh: ") {
		t.Fatalf("failures = %v", records.failures)
	}

	// With periodic checks on, the engine already learns from the run.
	records.checks = true
	records.failures = nil
	h.ingest("alice", "github:o/r#2", "fix it", true)
	h.settle()
	if len(records.failures) != 0 {
		t.Fatalf("failures = %v", records.failures)
	}

	// A failure of the work itself says nothing about the framework.
	records.checks = false
	h.workers.run = func(context.Context, workerCall) (string, error) { return "", fmt.Errorf("tests failed") }
	h.ingest("alice", "github:o/r#3", "fix it", true)
	h.settle()
	if len(records.failures) != 0 {
		t.Fatalf("failures = %v", records.failures)
	}

	// On another machine the failure marks the framework here.
	h.service.noteRunFailure(context.Background(), h.agent, core.SuperAgentWorker{ID: "auto:codex@dev_1", HostID: "dev_1"}, fmt.Errorf("You've hit your usage limit"))
	h.service.workers = &machineHealthStub{machineStub: &machineStub{workerStub: h.workers, runtimes: map[string][]string{"dev_1": {"codex"}}}}
	if record := h.service.runtimeHealth(context.Background(), "dev_1", "codex"); record.State != health.StateUsageLimit {
		t.Fatalf("record = %+v", record)
	}
}
