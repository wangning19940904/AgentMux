package superagent

import (
	"context"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func newUnifiedService(t *testing.T, store *memoryStore) *Service {
	t.Helper()
	clock := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	return New(Options{
		Store: store, Invoker: &routerStub{answer: func(core.InvocationRequest) (string, error) { return "", nil }},
		Workers: &workerStub{}, Replies: &replyStub{}, Now: func() time.Time { return clock }, StateDir: t.TempDir(),
	})
}

func TestUnifyAgentsGivesEveryAgentARecordOnce(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	store.local["coder"] = core.AgentInstance{ID: "coder", Name: "Coder", RuntimeID: "codex", WorkDir: "/repo", Enabled: true, CreatedAt: created}
	store.local["off"] = core.AgentInstance{ID: "off", Name: "Off", RuntimeID: "claudecode", Enabled: false}
	store.local["saw_x_codex"] = core.AgentInstance{ID: "saw_x_codex", Name: "worker", Source: autoWorkerAgentSource}
	// A Super Agent saved before router agents existed routes with "yolo":
	// that Agent already is a brain.
	store.agents["sa_legacy"] = core.SuperAgent{ID: "sa_legacy", Name: "Legacy", RouterAgentID: "yolo", Mode: core.SuperAgentModeAct, Enabled: true}
	service := newUnifiedService(t, store)

	count, err := service.UnifyAgents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("created %d records, want 2 (coder, off)", count)
	}
	coder, _ := store.GetSuperAgent(ctx, "coder")
	if coder == nil {
		t.Fatal("coder has no agent record")
	}
	if coder.RouterAgentID != "coder" || coder.Name != "Coder" || coder.Mode != core.SuperAgentModeAct || !coder.Enabled {
		t.Fatalf("coder record = %+v", coder)
	}
	if !coder.CreatedAt.Equal(created) || canStartWorkers(*coder) || len(coder.Sources) != 0 {
		t.Fatalf("coder record should keep its creation time and start no workers: %+v", coder)
	}
	if off, _ := store.GetSuperAgent(ctx, "off"); off == nil || off.Enabled {
		t.Fatalf("a disabled agent should stay disabled: %+v", off)
	}
	for _, id := range []string{"router", "yolo", "saw_x_codex"} {
		if agent, _ := store.GetSuperAgent(ctx, id); agent != nil {
			t.Fatalf("%s should get no record of its own", id)
		}
	}
	if instance, _ := store.GetAgentInstance(ctx, "coder"); instance.WorkDir != "/repo" || instance.RuntimeID != "codex" {
		t.Fatalf("the brain changed: %+v", instance)
	}

	before, _ := store.ListSuperAgents(ctx)
	again, err := service.UnifyAgents(ctx)
	if err != nil || again != 0 {
		t.Fatalf("second run created %d, err %v", again, err)
	}
	after, _ := store.ListSuperAgents(ctx)
	if len(after) != len(before) {
		t.Fatalf("second run changed the records: %d then %d", len(before), len(after))
	}
}

func TestUnifyAgentsLeavesATakenIDAlone(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	store.agents["coder"] = core.SuperAgent{ID: "coder", Name: "Other", RouterAgentID: "router", Enabled: true}
	service := newUnifiedService(t, store)
	if _, err := service.UnifyAgents(ctx); err != nil {
		t.Fatal(err)
	}
	if agent, _ := store.GetSuperAgent(ctx, "coder"); agent.Name != "Other" || agent.RouterAgentID != "router" {
		t.Fatalf("an existing record was replaced: %+v", agent)
	}
}

func TestStartGivesAgentsTheirRecords(t *testing.T) {
	store := newMemoryStore()
	service := newUnifiedService(t, store)
	if err := service.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Stop)
	if agent, _ := store.GetSuperAgent(context.Background(), "coder"); agent == nil || agent.RouterAgentID != "coder" {
		t.Fatalf("coder record = %+v", agent)
	}
}

func TestEnsureAgentCreatesAndFollowsItsBrain(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	service := newUnifiedService(t, store)
	instance := core.AgentInstance{ID: "agent_new", Name: "New", RuntimeID: "codex", Enabled: true}
	store.local[instance.ID] = instance
	agent, err := service.EnsureAgent(ctx, instance)
	if err != nil || agent == nil || agent.ID != "agent_new" || agent.RouterAgentID != "agent_new" || agent.Mode != core.SuperAgentModeAct {
		t.Fatalf("EnsureAgent = %+v, %v", agent, err)
	}
	instance.Name, instance.Enabled = "Renamed", false
	if _, err := service.EnsureAgent(ctx, instance); err != nil {
		t.Fatal(err)
	}
	if stored, _ := store.GetSuperAgent(ctx, "agent_new"); stored.Name != "Renamed" || stored.Enabled {
		t.Fatalf("the agent did not follow its brain: %+v", stored)
	}
	// A router agent belongs to its Super Agent and gets no record.
	if agent, err := service.EnsureAgent(ctx, store.local["router"]); agent != nil || err != nil {
		t.Fatalf("router agent got %+v, %v", agent, err)
	}
	// An Agent a Super Agent already routes with stays that agent's brain.
	store.agents["sa_legacy"] = core.SuperAgent{ID: "sa_legacy", Name: "Legacy", RouterAgentID: "yolo", Enabled: true}
	if agent, err := service.EnsureAgent(ctx, store.local["yolo"]); err != nil || agent == nil || agent.ID != "sa_legacy" {
		t.Fatalf("yolo resolved to %+v, %v", agent, err)
	}
	if stored, _ := store.GetSuperAgent(ctx, "sa_legacy"); stored.Name != "Legacy" {
		t.Fatalf("a Super Agent took its router's name: %+v", stored)
	}
}

func TestChannelSourcesComeFromBindings(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	store.channels = []core.Channel{
		{ID: "c1", Name: "Team bot", Type: "feishu", AgentID: "coder", Enabled: true},
		{ID: "c2", Name: "Other", Type: "slack", AgentID: "yolo", Enabled: true},
	}
	service := newUnifiedService(t, store)
	if _, err := service.UnifyAgents(ctx); err != nil {
		t.Fatal(err)
	}
	agents, err := service.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var coder core.SuperAgent
	for _, agent := range agents {
		if agent.ID == "coder" {
			coder = agent
		}
	}
	if len(coder.Sources) != 1 || coder.Sources[0].ID != "channel:c1" || coder.Sources[0].Kind != core.SuperAgentSourceChannel || coder.Sources[0].Config["channel_id"] != "c1" {
		t.Fatalf("coder sources = %+v", coder.Sources)
	}

	// Saving the agent with its channel source sent back stores no copy, and
	// stays in act mode with no allowed list to fill in.
	coder.AutoWorkers, coder.WorkerRuntimes = true, []string{"codex"}
	saved, err := service.Save(ctx, coder)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Sources) != 1 || saved.Mode != core.SuperAgentModeAct {
		t.Fatalf("saved = %+v", saved)
	}
	if stored, _ := store.GetSuperAgent(ctx, "coder"); len(stored.Sources) != 0 {
		t.Fatalf("a channel source was stored: %+v", stored.Sources)
	}
	if instance, _ := store.GetAgentInstance(ctx, "coder"); instance.WorkDir != "" && instance.WorkspaceMode == "shared" {
		t.Fatalf("an agent's own brain moved into its home: %+v", instance)
	}
}

func TestSavingAnAgentRenamesItsOwnBrain(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	service := newUnifiedService(t, store)
	if _, err := service.UnifyAgents(ctx); err != nil {
		t.Fatal(err)
	}
	agent, _ := store.GetSuperAgent(ctx, "coder")
	agent.Name, agent.Enabled = "Builder", false
	if _, err := service.Save(ctx, *agent); err != nil {
		t.Fatal(err)
	}
	if instance, _ := store.GetAgentInstance(ctx, "coder"); instance.Name != "Builder" || instance.Enabled {
		t.Fatalf("brain = %+v", instance)
	}
}

func TestDeletingAnAgentDeletesItsOwnBrainOnly(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	store.agents["sa_legacy"] = core.SuperAgent{ID: "sa_legacy", Name: "Legacy", RouterAgentID: "yolo", Enabled: true}
	service := newUnifiedService(t, store)
	if _, err := service.UnifyAgents(ctx); err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(ctx, "coder"); err != nil {
		t.Fatal(err)
	}
	if instance, _ := store.GetAgentInstance(ctx, "coder"); instance != nil {
		t.Fatal("the agent's own brain was kept")
	}
	if err := service.Delete(ctx, "sa_legacy"); err != nil {
		t.Fatal(err)
	}
	if instance, _ := store.GetAgentInstance(ctx, "yolo"); instance == nil {
		t.Fatal("an ordinary Agent a Super Agent routed with was deleted")
	}
}
