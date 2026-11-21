package superagent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
)

// withProfiles gives the harness's agent a strong and a cheap profile of its
// worker framework.
func (h *harness) withProfiles() {
	h.t.Helper()
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	h.agent.WorkerRuntimes = []string{"dsh"}
	h.agent.WorkerProfiles = []core.SuperAgentWorkerProfile{
		{ID: "deep", RuntimeID: "dsh", Model: "big-opus", ReasoningEffort: "high", Use: "hard debugging and design"},
		{ID: "fast", RuntimeID: "dsh", ReasoningEffort: "low", Use: "translation and lookups", CostTier: core.CostTierLow},
	}
	h.store.agents[h.agent.ID] = h.agent
}

func TestRouterSeesProfilesAndLaunchesThem(t *testing.T) {
	h := newHarness(t)
	h.enableAutoWorkers(t.TempDir())
	h.withProfiles()
	h.router.answer = func(core.InvocationRequest) (string, error) { return acceptAnswer("auto:dsh/deep"), nil }
	h.ingest("alice", "github:o/r#1", "find the race", true)
	h.settle()

	prompt := h.router.prompts[0]
	for _, want := range []string{`"id": "auto:dsh"`, `"id": "auto:dsh/deep"`, `"model": "big-opus"`, `"effort": "high"`, `"use": "hard debugging and design"`, `"id": "auto:dsh/fast"`, "PROFILES: ", `"cost_tier": "high"`, `"cost_tier": "low"`} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("the roster must show the profiles (%s):\n%s", want, prompt)
		}
	}
	tickets := h.store.allTickets()
	if len(tickets) != 1 {
		t.Fatalf("tickets = %+v", tickets)
	}
	worker := tickets[0].Worker
	if worker.ID != "auto:dsh/deep" || worker.AgentID != autoWorkerAgentID(h.agent.ID, "dsh")+"+deep" {
		t.Fatalf("worker = %+v", worker)
	}
	instance, _ := h.store.GetAgentInstance(context.Background(), worker.AgentID)
	if instance == nil || instance.DefaultModel != "big-opus" || instance.DefaultReasoningEffort != "high" {
		t.Fatalf("the profile's settings must reach the worker: %+v", instance)
	}
	// The rest is what the plain worker of the router's framework has.
	if instance.Env["DEEPSEEK_API_KEY"] != "from-router" || instance.RuntimeID != "dsh" {
		t.Fatalf("instance = %+v", instance)
	}
}

func TestProfileLeavesUnsetSettingsToThePlainWorker(t *testing.T) {
	h := newHarness(t)
	h.enableAutoWorkers(t.TempDir())
	h.withProfiles()
	stored, _ := h.store.GetSuperAgent(context.Background(), h.agent.ID)
	fast, err := h.service.launchAutoWorker(context.Background(), *stored, "dsh", "fast")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := h.service.launchAutoWorker(context.Background(), *stored, "dsh", "")
	if err != nil {
		t.Fatal(err)
	}
	if fast.AgentID == plain.AgentID {
		t.Fatal("each profile needs its own Agent, so a Ticket keeps its model")
	}
	instance, _ := h.store.GetAgentInstance(context.Background(), fast.AgentID)
	if instance.DefaultModel != "model-1" || instance.DefaultReasoningEffort != "low" {
		t.Fatalf("fast keeps the router's model at low effort: %+v", instance)
	}
	if _, err := h.service.launchAutoWorker(context.Background(), *stored, "dsh", "nope"); err == nil || !strings.Contains(err.Error(), `no profile "nope"`) {
		t.Fatalf("an unknown profile: err = %v", err)
	}
	if err := h.service.deleteAutoWorkers(context.Background(), h.agent.ID); err != nil {
		t.Fatal(err)
	}
	if gone, _ := h.store.GetAgentInstance(context.Background(), fast.AgentID); gone != nil {
		t.Fatal("deleting the agent's workers must remove its profiles' too")
	}
}

func TestRouterCannotNameAProfileTheOperatorDidNotSet(t *testing.T) {
	h := newHarness(t)
	h.enableAutoWorkers(t.TempDir())
	h.withProfiles()
	h.router.answer = func(core.InvocationRequest) (string, error) { return acceptAnswer("auto:dsh/opus-max"), nil }
	h.ingest("alice", "github:o/r#1", "work", true)
	h.settle()
	if len(h.store.allTickets()) != 0 || h.workers.callCount() != 0 {
		t.Fatalf("tickets = %+v", h.store.allTickets())
	}
}

func TestProfileOnAnotherMachine(t *testing.T) {
	h := newHarness(t)
	machines := h.withMachines()
	h.withProfiles()
	h.router.answer = func(core.InvocationRequest) (string, error) { return acceptAnswer("auto:dsh/deep@dev_1"), nil }
	h.ingest("alice", "github:o/r#1", "train it", true)
	h.settle()

	if !strings.Contains(h.router.prompts[0], `"id": "auto:dsh/deep@dev_1"`) {
		t.Fatalf("the machine must offer the profile:\n%s", h.router.prompts[0])
	}
	if len(machines.launches) != 1 {
		t.Fatalf("launches = %+v", machines.launches)
	}
	if got := machines.launches[0]; got.Profile != "deep" || got.Model != "big-opus" || got.ReasoningEffort != "high" || got.RuntimeID != "dsh" {
		t.Fatalf("launch = %+v", got)
	}
	tickets := h.store.allTickets()
	if len(tickets) != 1 || tickets[0].Worker.ID != "auto:dsh/deep@dev_1" || tickets[0].Worker.AgentID != hostedWorkerAgentID(h.agent.ID, "dsh", "")+"+deep" {
		t.Fatalf("tickets = %+v", tickets)
	}
}

func TestAMachineTooOldForProfilesIsNotUsedSilently(t *testing.T) {
	h := newHarness(t)
	machines := h.withMachines()
	machines.noProfiles = true
	h.withProfiles()
	stored, _ := h.store.GetSuperAgent(context.Background(), h.agent.ID)
	_, _, err := h.service.launchMachineWorkerMode(context.Background(), *stored, "dsh", "deep", "dev_1")
	if err == nil || !strings.Contains(err.Error(), "too old for worker profiles") || !strings.Contains(err.Error(), "auto:dsh@dev_1") {
		t.Fatalf("err = %v", err)
	}
}

func TestHostWorkerKeepsOneWorkerPerProfile(t *testing.T) {
	store := newMemoryStore()
	service := New(Options{Store: store, StateDir: t.TempDir(), RuntimeAvailable: func(id string) bool { return id == "dsh" }})
	ctx := context.Background()
	plain, err := service.HostWorker(ctx, core.HostedWorkerRequest{OwnerID: "sa_1", RuntimeID: "dsh"})
	if err != nil {
		t.Fatal(err)
	}
	deep, err := service.HostWorker(ctx, core.HostedWorkerRequest{OwnerID: "sa_1", RuntimeID: "dsh", Profile: "deep", Model: "big-opus", ReasoningEffort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if deep.AgentID == plain.AgentID || deep.Profile != "deep" || plain.Profile != "" {
		t.Fatalf("plain = %+v deep = %+v", plain, deep)
	}
	instance, _ := store.GetAgentInstance(ctx, deep.AgentID)
	if instance.DefaultModel != "big-opus" || instance.DefaultReasoningEffort != "high" {
		t.Fatalf("instance = %+v", instance)
	}
	for name, request := range map[string]core.HostedWorkerRequest{
		"need a profile": {OwnerID: "sa_1", RuntimeID: "dsh", Model: "big-opus"},
		"profile id":     {OwnerID: "sa_1", RuntimeID: "dsh", Profile: "../x", Model: "m"},
		"model must be":  {OwnerID: "sa_1", RuntimeID: "dsh", Profile: "deep", Model: "two words"},
	} {
		if _, err := service.HostWorker(ctx, request); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), name) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	removed, err := service.ReleaseHostedWorkers(ctx, "sa_1")
	if err != nil || removed != 2 {
		t.Fatalf("releasing must remove the profile's worker too: removed = %d err = %v", removed, err)
	}
}

func TestSaveChecksProfiles(t *testing.T) {
	agent := core.SuperAgent{AutoWorkers: true, WorkerRuntimes: []string{"dsh"}}
	for name, profiles := range map[string][]core.SuperAgentWorkerProfile{
		"not one of this agent's worker frameworks": {{ID: "deep", RuntimeID: "codex", Model: "m"}},
		"two profiles named":                        {{ID: "deep", RuntimeID: "dsh", Model: "a"}, {ID: "Deep", RuntimeID: "dsh", Model: "b"}},
		"neither a model nor a reasoning effort":    {{ID: "deep", RuntimeID: "dsh"}},
		"profile id":                                {{ID: "deep/er", RuntimeID: "dsh", Model: "m"}},
		"cost tier":                                 {{ID: "deep", RuntimeID: "dsh", Model: "m", CostTier: "huge"}},
	} {
		next := agent
		next.WorkerProfiles = profiles
		if err := normalizeWorkerProfiles(&next); err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	next := agent
	next.WorkerProfiles = []core.SuperAgentWorkerProfile{{ID: " Fast ", RuntimeID: "dsh", ReasoningEffort: "low", CostTier: "LOW"}}
	if err := normalizeWorkerProfiles(&next); err != nil || next.WorkerProfiles[0].ID != "fast" || next.WorkerProfiles[0].CostTier != "low" {
		t.Fatalf("profiles = %+v err = %v", next.WorkerProfiles, err)
	}
	next.AutoWorkers = false
	if err := normalizeWorkerProfiles(&next); err != nil || next.WorkerProfiles != nil {
		t.Fatal("an agent without auto workers has no profiles")
	}
}

func TestFlexiblePlacementKeepsTheProfile(t *testing.T) {
	h := newHarness(t)
	h.withMachines()
	h.withProfiles()
	stored, _ := h.store.GetSuperAgent(context.Background(), h.agent.ID)
	busy := func(place workerPlace) string {
		if place.HostID == "" && place.Place == "" {
			return "busy"
		}
		return ""
	}
	placed, err := h.service.place(context.Background(), *stored, routerAction{WorkerID: "auto:dsh/deep", MachinePolicy: core.MachinePolicyFlexible}, busy)
	if err != nil {
		t.Fatal(err)
	}
	if placed.place.Profile != "deep" || placed.place.WorkerID == "auto:dsh/deep" {
		t.Fatalf("moved to %+v", placed.place)
	}
}

func TestParseAutoWorker(t *testing.T) {
	for id, want := range map[string][3]string{
		"auto:dsh":             {"dsh", "", ""},
		"auto:dsh@dev_1":       {"dsh", "", "dev_1"},
		"auto:dsh/deep":        {"dsh", "deep", ""},
		"auto:dsh/deep@dev_1":  {"dsh", "deep", "dev_1"},
		"auto:dsh/deep@a.b-c_": {"dsh", "deep", "a.b-c_"},
	} {
		runtime, profile, path, ok := parseAutoWorker(id)
		if !ok || [3]string{runtime, profile, path} != want {
			t.Fatalf("%s: %q %q %q", id, runtime, profile, path)
		}
		if got := autoRosterID(runtime, profile, path); got != id {
			t.Fatalf("round trip %s = %s", id, got)
		}
	}
}

func TestProfileEffortMovesItsTier(t *testing.T) {
	for _, c := range []struct{ tier, effort, want string }{
		{core.CostTierMedium, "xhigh", core.CostTierHigh},
		{core.CostTierMedium, "low", core.CostTierLow},
		{core.CostTierHigh, "high", core.CostTierHigh},
		{core.CostTierLow, "minimal", core.CostTierLow},
		{core.CostTierMedium, "medium", core.CostTierMedium},
		{core.CostTierHigh, "", core.CostTierHigh},
	} {
		if got := effortTier(c.tier, c.effort); got != c.want {
			t.Fatalf("%s at %q = %s, want %s", c.tier, c.effort, got, c.want)
		}
	}
}
