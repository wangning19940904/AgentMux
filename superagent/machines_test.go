package superagent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
)

// machineStub is a WorkerRunner that also reaches other machines.
type machineStub struct {
	*workerStub
	mu        sync.Mutex
	names     map[string]string
	runtimes  map[string][]string
	offline   map[string]bool
	launches  []core.HostedWorkerRequest
	launchOn  []string
	releases  []string
	launchErr error
	steered   []string
	steerErr  error
}

func (m *machineStub) MachineName(hostID string) (string, bool) {
	name, ok := m.names[hostID]
	return name, ok
}

func (m *machineStub) Runtimes(_ context.Context, hostID string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.offline[hostID] {
		return nil, errors.New("device is offline")
	}
	return m.runtimes[hostID], nil
}

func (m *machineStub) Launch(_ context.Context, hostID string, request core.HostedWorkerRequest) (core.HostedWorker, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.launchErr != nil {
		return core.HostedWorker{}, m.launchErr
	}
	m.launches, m.launchOn = append(m.launches, request), append(m.launchOn, hostID)
	return core.HostedWorker{AgentID: hostedWorkerAgentID(request.OwnerID, request.RuntimeID), Name: request.OwnerName + " worker"}, nil
}

func (m *machineStub) Release(_ context.Context, hostID, ownerID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.releases = append(m.releases, hostID+"/"+ownerID)
	return nil
}

func (m *machineStub) Steer(_ context.Context, worker core.SuperAgentWorker, _, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.steered = append(m.steered, worker.HostID+": "+text)
	return m.steerErr
}

// withMachines gives the harness's agent auto workers and one other machine.
func (h *harness) withMachines() *machineStub {
	h.t.Helper()
	h.enableAutoWorkers(h.t.TempDir())
	stub := &machineStub{
		workerStub: h.workers,
		names:      map[string]string{"dev_1": "studio", "dev_2": "laptop"},
		runtimes:   map[string][]string{"dev_1": {"dsh", "codex"}, "dev_2": {"codex"}},
		offline:    map[string]bool{},
	}
	h.service.workers = stub
	h.store.mu.Lock()
	h.agent.WorkerMachines = []core.SuperAgentMachine{{HostID: "dev_1", Dir: "~/code", Capabilities: "Has the GPU."}}
	h.agent.AutoWorkerApprovalMode = core.ApprovalModeYolo
	h.store.agents[h.agent.ID] = h.agent
	h.store.mu.Unlock()
	return stub
}

func TestRouterLaunchesAWorkerOnAnotherMachine(t *testing.T) {
	h := newHarness(t)
	machines := h.withMachines()
	h.router.answer = func(core.InvocationRequest) (string, error) { return acceptAnswer("auto:dsh@dev_1"), nil }
	h.ingest("alice", "github:o/r#1", "train it", true)
	h.settle()

	prompt := h.router.prompts[0]
	for _, want := range []string{`"id": "auto:dsh@dev_1"`, `"location": "machine studio"`, "Has the GPU.", "auto:<framework>@<machine>"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("the roster must offer the machine (%s):\n%s", want, prompt)
		}
	}
	if len(machines.launches) != 1 || machines.launchOn[0] != "dev_1" {
		t.Fatalf("launches = %+v on %v", machines.launches, machines.launchOn)
	}
	// What the worker may do is the operator's, sent as the operator set it.
	if got := machines.launches[0]; got.OwnerID != h.agent.ID || got.RuntimeID != "dsh" || got.Dir != "~/code" || got.ApprovalMode != core.ApprovalModeYolo {
		t.Fatalf("launch = %+v", got)
	}
	tickets := h.store.allTickets()
	if len(tickets) != 1 || tickets[0].Status != core.SuperAgentTicketReady {
		t.Fatalf("tickets = %+v", tickets)
	}
	worker := tickets[0].Worker
	if worker.HostID != "dev_1" || worker.AgentID != hostedWorkerAgentID(h.agent.ID, "dsh") || worker.ID != "auto:dsh@dev_1" {
		t.Fatalf("worker = %+v", worker)
	}
	if h.workers.callCount() != 1 || h.workers.calls[0].worker.HostID != "dev_1" {
		t.Fatalf("the turn must run on the machine: %+v", h.workers.calls)
	}
	// Nothing is created here for a worker that lives elsewhere.
	if local, _ := h.store.GetAgentInstance(context.Background(), worker.AgentID); local != nil {
		t.Fatalf("a local agent was created: %+v", local)
	}
}

func TestRouterCannotLaunchOnAMachineTheOperatorDidNotList(t *testing.T) {
	h := newHarness(t)
	machines := h.withMachines()
	h.router.answer = func(core.InvocationRequest) (string, error) { return acceptAnswer("auto:dsh@dev_2"), nil }
	h.ingest("alice", "github:o/r#1", "work", true)
	h.settle()
	if len(machines.launches) != 0 || len(h.store.allTickets()) != 0 || h.workers.callCount() != 0 {
		t.Fatalf("launches = %+v tickets = %+v", machines.launches, h.store.allTickets())
	}
}

func TestRosterShowsWhatAMachineCanRunNow(t *testing.T) {
	h := newHarness(t)
	machines := h.withMachines()
	h.store.mu.Lock()
	h.agent.WorkerRuntimes = []string{"dsh", "kimi"}
	h.agent.WorkerMachines = append(h.agent.WorkerMachines, core.SuperAgentMachine{HostID: "dev_2"})
	h.store.agents[h.agent.ID] = h.agent
	h.store.mu.Unlock()
	machines.offline["dev_2"] = true

	statuses := h.service.machineStatuses(context.Background(), h.agent)
	if len(statuses) != 2 || !statuses[0].Available || statuses[0].Name != "studio" || statuses[1].Available || !strings.Contains(statuses[1].Detail, "offline") {
		t.Fatalf("statuses = %+v", statuses)
	}
	var prompt strings.Builder
	writeRouterContext(&prompt, h.agent, nil, routerContext{Machines: statuses})
	text := prompt.String()
	entry := func(id string) string {
		_, rest, found := strings.Cut(text, `"id": "`+id+`"`)
		if !found {
			t.Fatalf("no roster entry %s:\n%s", id, text)
		}
		body, _, _ := strings.Cut(rest, "}")
		return body
	}
	if !strings.Contains(entry("auto:dsh@dev_1"), `"available": true`) {
		t.Fatalf("dsh on studio must be available:\n%s", text)
	}
	if body := entry("auto:kimi@dev_1"); !strings.Contains(body, `"available": false`) || !strings.Contains(body, "kimi is not installed on studio") {
		t.Fatalf("kimi on studio: %s", body)
	}
	if body := entry("auto:dsh@dev_2"); !strings.Contains(body, `"available": false`) || !strings.Contains(body, "offline") {
		t.Fatalf("dsh on the offline laptop: %s", body)
	}
}

func TestAFailedLaunchLeavesNoTicketRunning(t *testing.T) {
	h := newHarness(t)
	machines := h.withMachines()
	machines.launchErr = ErrMachineOutdated
	h.router.answer = func(core.InvocationRequest) (string, error) { return acceptAnswer("auto:dsh@dev_1"), nil }
	h.ingest("alice", "github:o/r#1", "work", true)
	h.settle()
	if h.workers.callCount() != 0 {
		t.Fatalf("worker calls = %d", h.workers.callCount())
	}
	for _, ticket := range h.store.allTickets() {
		if ticket.Status == core.SuperAgentTicketRunning || ticket.Status == core.SuperAgentTicketQueued {
			t.Fatalf("ticket = %+v", ticket)
		}
	}
}

func TestSaveChecksWorkerMachines(t *testing.T) {
	h := newHarness(t)
	machines := h.withMachines()
	ctx := context.Background()
	save := func(list ...core.SuperAgentMachine) error {
		agent := h.agent
		agent.WorkerMachines = list
		_, err := h.service.Save(ctx, agent)
		return err
	}
	for name, list := range map[string][]core.SuperAgentMachine{
		"not connected": {{HostID: "dev_9"}},
		"listed twice":  {{HostID: "dev_1"}, {HostID: "dev_1"}},
		"absolute path": {{HostID: "dev_1", Dir: "code"}},
		"must name":     {{HostID: " "}},
	} {
		if err := save(list...); err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	if err := save(core.SuperAgentMachine{HostID: " dev_1 ", Dir: "~/code"}, core.SuperAgentMachine{HostID: "dev_2", Dir: "/srv/work"}); err != nil {
		t.Fatal(err)
	}
	stored, _ := h.store.GetSuperAgent(ctx, h.agent.ID)
	if len(stored.WorkerMachines) != 2 || stored.WorkerMachines[0].HostID != "dev_1" {
		t.Fatalf("machines = %+v", stored.WorkerMachines)
	}
	// Taking a machine off the list has it drop this agent's workers.
	h.agent = *stored
	if err := save(core.SuperAgentMachine{HostID: "dev_2", Dir: "/srv/work"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(machines.releases, []string{"dev_1/" + h.agent.ID}) {
		t.Fatalf("releases = %v", machines.releases)
	}
	// Without auto workers there is nothing to launch anywhere.
	stored, _ = h.store.GetSuperAgent(ctx, h.agent.ID)
	off := *stored
	off.AutoWorkers = false
	if _, err := h.service.Save(ctx, off); err != nil {
		t.Fatal(err)
	}
	stored, _ = h.store.GetSuperAgent(ctx, h.agent.ID)
	if len(stored.WorkerMachines) != 0 {
		t.Fatalf("machines = %+v", stored.WorkerMachines)
	}
	if !slices.Contains(machines.releases, "dev_2/"+h.agent.ID) {
		t.Fatalf("releases = %v", machines.releases)
	}
}

func TestDeletingAnAgentReleasesItsWorkersElsewhere(t *testing.T) {
	h := newHarness(t)
	machines := h.withMachines()
	if err := h.service.Delete(context.Background(), h.agent.ID); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(machines.releases, []string{"dev_1/" + h.agent.ID}) {
		t.Fatalf("releases = %v", machines.releases)
	}
}

func TestParseAutoWorkerID(t *testing.T) {
	for id, want := range map[string][3]string{
		"auto:codex":       {"codex", "", "ok"},
		"auto:codex@dev_1": {"codex", "dev_1", "ok"},
		"auto:":            {"", "", ""},
		"auto:@dev_1":      {"", "dev_1", ""},
		"wrk_1":            {"", "", ""},
	} {
		runtimeID, hostID, ok := parseAutoWorkerID(id)
		if runtimeID != want[0] || hostID != want[1] || ok != (want[2] == "ok") {
			t.Errorf("parseAutoWorkerID(%q) = %q, %q, %v", id, runtimeID, hostID, ok)
		}
	}
}

// The machine that hosts a worker decides what it may be.
func TestHostWorkerAppliesThisMachinesChecks(t *testing.T) {
	store := newMemoryStore()
	state := t.TempDir()
	service := New(Options{Store: store, StateDir: state, RuntimeAvailable: func(id string) bool { return id == "dsh" }})
	ctx := context.Background()
	dir := t.TempDir()

	hosted, err := service.HostWorker(ctx, core.HostedWorkerRequest{OwnerID: "sa_1", OwnerName: "Builder", RuntimeID: "dsh", Dir: dir, ApprovalMode: core.ApprovalModeYolo})
	if err != nil {
		t.Fatal(err)
	}
	if hosted.AgentID != "sawr_sa_1_dsh" || hosted.WorkDir != dir || hosted.WorkspaceMode != "shared" {
		t.Fatalf("hosted = %+v", hosted)
	}
	instance, _ := store.GetAgentInstance(ctx, hosted.AgentID)
	if instance == nil || !IsManagedAgent(*instance) || isAgentRecord(*instance) || !instance.Enabled || instance.DefaultApprovalMode != core.ApprovalModeYolo {
		t.Fatalf("instance = %+v", instance)
	}
	if len(instance.Env) != 0 || len(instance.MCPServers) != 0 {
		t.Fatalf("a hosted worker uses this machine's own settings: %+v", instance)
	}

	// With no directory the worker gets one of its own under this machine's state.
	hosted, err = service.HostWorker(ctx, core.HostedWorkerRequest{OwnerID: "sa_2", RuntimeID: "dsh"})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(state, "hosted-workers", "sa_2", "work"); hosted.WorkDir != want {
		t.Fatalf("work dir = %q, want %q", hosted.WorkDir, want)
	}
	if info, err := os.Stat(hosted.WorkDir); err != nil || !info.IsDir() {
		t.Fatalf("work dir not created: %v", err)
	}
	if instance, _ := store.GetAgentInstance(ctx, hosted.AgentID); instance.DefaultApprovalMode != core.ApprovalModeAutoEdit {
		t.Fatalf("default approval = %q", instance.DefaultApprovalMode)
	}

	for name, request := range map[string]core.HostedWorkerRequest{
		"not installed":  {OwnerID: "sa_1", RuntimeID: "codex"},
		"does not exist": {OwnerID: "sa_1", RuntimeID: "dsh", Dir: filepath.Join(dir, "missing")},
		"absolute path":  {OwnerID: "sa_1", RuntimeID: "dsh", Dir: "relative/dir"},
		"unattended":     {OwnerID: "sa_1", RuntimeID: "dsh", ApprovalMode: core.ApprovalModeManual},
		"owner id":       {OwnerID: "../etc", RuntimeID: "dsh"},
	} {
		if _, err := service.HostWorker(ctx, request); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), name) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}

	// An agent of this machine's own is never taken over.
	own := core.AgentInstance{ID: "sawr_sa_3_dsh", Name: "mine", RuntimeID: "dsh", Enabled: true}
	_ = store.UpsertAgentInstance(ctx, &own)
	if _, err := service.HostWorker(ctx, core.HostedWorkerRequest{OwnerID: "sa_3", RuntimeID: "dsh"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v", err)
	}

	removed, err := service.ReleaseHostedWorkers(ctx, "sa_1")
	if err != nil || removed != 1 {
		t.Fatalf("removed = %d err = %v", removed, err)
	}
	if gone, _ := store.GetAgentInstance(ctx, "sawr_sa_1_dsh"); gone != nil {
		t.Fatal("the worker must be removed")
	}
	if kept, _ := store.GetAgentInstance(ctx, "sawr_sa_2_dsh"); kept == nil {
		t.Fatal("another agent's worker must stay")
	}
	if removed, _ := service.ReleaseHostedWorkers(ctx, "sa_3"); removed != 0 {
		t.Fatal("this machine's own agent must stay")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("releasing a worker must not remove its files")
	}
}

func TestTicketsListsEveryAgentNewestFirst(t *testing.T) {
	h := newHarness(t)
	machines := h.withMachines()
	_ = machines
	answers := []string{acceptAnswer("local"), acceptAnswer("auto:dsh@dev_1")}
	h.router.answer = func(core.InvocationRequest) (string, error) {
		answer := answers[0]
		if len(answers) > 1 {
			answers = answers[1:]
		}
		return answer, nil
	}
	h.workers.run = func(context.Context, workerCall) (string, error) { return strings.Repeat("x", 5000), nil }
	h.ingest("alice", "github:o/r#1", "first", true)
	h.settle()
	*h.clock = h.clock.Add(1)
	h.ingest("alice", "github:o/r#2", "second", true)
	h.settle()

	tickets, err := h.service.Tickets(context.Background(), 0)
	if err != nil || len(tickets) != 2 {
		t.Fatalf("tickets = %+v err = %v", tickets, err)
	}
	for _, ticket := range tickets {
		if ticket.SuperAgentName != "sa" || len([]rune(ticket.Output)) > ticketSummaryOutput+20 || ticket.Reply != nil || ticket.Inputs != nil {
			t.Fatalf("summary = %+v", ticket)
		}
	}
	byMachine := map[string]string{}
	for _, ticket := range tickets {
		byMachine[ticket.Worker.ID] = ticket.MachineName
	}
	if byMachine["auto:dsh@dev_1"] != "studio" || byMachine["local"] != "" {
		t.Fatalf("machines = %v", byMachine)
	}
	if one, _ := h.service.Tickets(context.Background(), 1); len(one) != 1 {
		t.Fatalf("limit ignored: %d", len(one))
	}
}
