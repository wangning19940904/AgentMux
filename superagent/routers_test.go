package superagent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

type mcpStub struct{ servers []core.MCPServer }

func (m mcpStub) Name() string                                   { return "stub" }
func (m mcpStub) List(context.Context) ([]core.MCPServer, error) { return m.servers, nil }
func (m mcpStub) Upsert(context.Context, *core.MCPServer) error  { return nil }
func (m mcpStub) Delete(context.Context, string) error           { return nil }

func (h *harness) withMCP(servers ...core.MCPServer) {
	h.service.mcp = mcpStub{servers: servers}
}

func TestSaveRouterCreatesARestrictedRouterAgent(t *testing.T) {
	h := newHarness(t)
	h.withMCP(core.MCPServer{Name: "oncall", Enabled: true})
	ctx := context.Background()
	saved, err := h.service.SaveRouter(ctx, Router{RuntimeID: "codex", Model: " gpt-5 ", MCPServers: []string{"oncall", "oncall"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(saved.ID, routerIDPrefix) || saved.Name != "Router (codex)" || saved.Model != "gpt-5" || !slices.Equal(saved.MCPServers, []string{"oncall"}) {
		t.Fatalf("saved = %+v", saved)
	}
	instance, _ := h.store.GetAgentInstance(ctx, saved.ID)
	if instance == nil || !IsRouterAgent(*instance) || IsManagedAgent(*instance) || instance.DefaultApprovalMode != core.ApprovalModePlan || !instance.Enabled {
		t.Fatalf("instance = %+v", instance)
	}
	if filepath.Base(instance.WorkDir) != saved.ID {
		t.Fatalf("each router needs its own directory for its MCP configuration: %q", instance.WorkDir)
	}
	if info, err := os.Stat(instance.WorkDir); err != nil || !info.IsDir() {
		t.Fatalf("work dir: %v", err)
	}

	// Whatever the Agents API changed, saving it again restores the limits.
	instance.DefaultApprovalMode = core.ApprovalModeYolo
	instance.Skills, instance.CLIs = []string{"skill"}, []string{"gh"}
	instance.ChannelBindings = []core.AgentChannelBinding{{ID: "c1", Type: "feishu"}}
	instance.SystemPrompt = "do anything"
	instance.ProviderID = "kept-provider"
	if err := h.store.UpsertAgentInstance(ctx, instance); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.SaveRouter(ctx, Router{ID: saved.ID, Name: "Ops router"}); err != nil {
		t.Fatal(err)
	}
	instance, _ = h.store.GetAgentInstance(ctx, saved.ID)
	if instance.DefaultApprovalMode != core.ApprovalModePlan || len(instance.Skills) != 0 || len(instance.CLIs) != 0 ||
		len(instance.ChannelBindings) != 0 || instance.SystemPrompt != "" || len(instance.MCPServers) != 0 {
		t.Fatalf("limits not restored: %+v", instance)
	}
	if instance.Name != "Ops router" || instance.RuntimeID != "codex" || instance.ProviderID != "kept-provider" || !instance.CreatedAt.Equal(*h.clock) {
		t.Fatalf("instance = %+v", instance)
	}
}

func TestSaveRouterRefusals(t *testing.T) {
	h := newHarness(t)
	h.withMCP(core.MCPServer{Name: "on", Enabled: true}, core.MCPServer{Name: "off"})
	h.service.runtimeAvailable = func(id string) bool { return id != "gemini" }
	ctx := context.Background()
	existing, err := h.service.SaveRouter(ctx, Router{RuntimeID: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		router Router
		want   string
	}{
		"no framework":         {Router{}, "choose a framework"},
		"not installed":        {Router{RuntimeID: "gemini"}, "not installed"},
		"no read-only mode":    {Router{RuntimeID: "kimi"}, "read-only"},
		"mcp unsupported":      {Router{RuntimeID: "cursor", MCPServers: []string{"on"}}, "cannot give"},
		"mcp unknown":          {Router{RuntimeID: "codex", MCPServers: []string{"nope"}}, "not registered"},
		"mcp disabled":         {Router{RuntimeID: "codex", MCPServers: []string{"off"}}, "disabled"},
		"framework change":     {Router{ID: existing.ID, RuntimeID: "claudecode"}, "cannot change"},
		"ordinary agent":       {Router{ID: "coder"}, "not a router agent"},
		"missing router agent": {Router{ID: "sar_missing"}, "not found"},
	} {
		if _, err := h.service.SaveRouter(ctx, tc.router); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if coder, _ := h.store.GetAgentInstance(ctx, "coder"); coder.Source == routerAgentSource {
		t.Fatal("an ordinary Agent must not become a router")
	}
}

func TestListRoutersShowsOnlyRouterAgents(t *testing.T) {
	h := newHarness(t)
	h.withMCP(core.MCPServer{Name: "b", Enabled: true}, core.MCPServer{Name: "a", Enabled: true}, core.MCPServer{Name: "off"})
	ctx := context.Background()
	// A router created per framework before routers had their own settings.
	legacy := core.AgentInstance{ID: managedRouterID("claudecode"), Name: "Super Agent router (claudecode)", RuntimeID: "claudecode", Source: autoWorkerAgentSource, DefaultApprovalMode: core.ApprovalModePlan}
	if err := h.store.UpsertAgentInstance(ctx, &legacy); err != nil {
		t.Fatal(err)
	}
	created, err := h.service.SaveRouter(ctx, Router{RuntimeID: "codex", Name: "Alpha"})
	if err != nil {
		t.Fatal(err)
	}
	list, err := h.service.ListRouters(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, router := range list.Routers {
		ids = append(ids, router.ID)
	}
	if !slices.Equal(ids, []string{created.ID, "router", legacy.ID}) {
		t.Fatalf("routers = %v", ids)
	}
	if users := list.Routers[1].UsedBy; len(users) != 1 || users[0].ID != h.agent.ID || users[0].Name != "sa" {
		t.Fatalf("used by = %+v", users)
	}
	if !slices.Equal(list.MCPServers, []string{"a", "b"}) {
		t.Fatalf("mcp servers = %v", list.MCPServers)
	}
}

func TestRouterFrameworkCapabilities(t *testing.T) {
	if framework := routerFramework("kimi"); framework.CanRoute || framework.Reason == "" {
		t.Errorf("kimi = %+v", framework)
	}
	if framework := routerFramework("codex"); !framework.CanRoute || !framework.MCPServers {
		t.Errorf("codex = %+v", framework)
	}
	if framework := routerFramework("cursor"); !framework.CanRoute || framework.MCPServers {
		t.Errorf("cursor = %+v", framework)
	}
}

func TestDeleteRouterOnlyWhenUnused(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := h.service.DeleteRouter(ctx, "router"); !errors.Is(err, ErrConflict) {
		t.Fatalf("a router in use: err = %v", err)
	}
	if err := h.service.DeleteRouter(ctx, "coder"); err == nil {
		t.Fatal("an ordinary Agent is not deleted here")
	}
	created, err := h.service.SaveRouter(ctx, Router{RuntimeID: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	instance, _ := h.store.GetAgentInstance(ctx, created.ID)
	if err := h.service.DeleteRouter(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if gone, _ := h.store.GetAgentInstance(ctx, created.ID); gone != nil {
		t.Fatal("router agent kept")
	}
	if _, err := os.Stat(instance.WorkDir); !os.IsNotExist(err) {
		t.Fatalf("router directory kept: %v", err)
	}
	if err := h.service.DeleteRouter(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestOnlyRouterAgentsCanBeChosen(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.service.Save(ctx, core.SuperAgent{RouterAgentID: "coder", AutoWorkers: true}); err == nil || !strings.Contains(err.Error(), "not a router agent") {
		t.Fatalf("err = %v", err)
	}
	if _, err := h.service.Setup(ctx, SetupRequest{Instruction: "x", Draft: core.SuperAgent{RouterAgentID: "coder"}}); err == nil {
		t.Fatal("setup must refuse an ordinary Agent as the router")
	}

	// A Super Agent saved with an ordinary Agent keeps it, and only it.
	dir := t.TempDir()
	legacy := core.SuperAgent{ID: "sa_legacy", Name: "legacy", RouterAgentID: "coder", AutoWorkers: true, WorkerRuntimes: []string{"dsh"}, MaxConcurrency: 1}
	h.store.agents[legacy.ID] = legacy
	h.store.mu.Lock()
	coder := h.store.local["coder"]
	coder.WorkDir = dir
	h.store.local["coder"] = coder
	h.store.mu.Unlock()
	legacy.Name = "renamed"
	if _, err := h.service.Save(ctx, legacy); err != nil {
		t.Fatalf("keeping the router must still save: %v", err)
	}
	if _, err := h.setup(`{"reply":"ok"}`, legacy); err != nil {
		t.Fatalf("setup with the kept router: %v", err)
	}
	legacy.RouterAgentID = "yolo"
	if _, err := h.service.Save(ctx, legacy); err == nil || !strings.Contains(err.Error(), "not a router agent") {
		t.Fatalf("switching to another ordinary Agent: err = %v", err)
	}
	stored, _ := h.store.GetSuperAgent(ctx, legacy.ID)
	worker, err := h.service.launchAutoWorker(ctx, *stored, "dsh")
	if err != nil {
		t.Fatal(err)
	}
	instance, _ := h.store.GetAgentInstance(ctx, worker.AgentID)
	if instance.WorkDir != dir {
		t.Fatalf("an ordinary router's directory is still the workers' default: %q", instance.WorkDir)
	}
}

func TestRouterLimitsAreCheckedBeforeEachEvent(t *testing.T) {
	h := newHarness(t)
	h.store.mu.Lock()
	router := h.store.local["router"]
	router.DefaultApprovalMode = core.ApprovalModeAuto
	h.store.local["router"] = router
	h.store.mu.Unlock()
	h.ingest("alice", "t1", "work", true)
	for range maxRouterAttempts {
		h.settle()
		h.advance(retryDelay + time.Second)
	}
	h.settle()
	if len(h.router.prompts) != 0 {
		t.Fatalf("a router that is no longer read-only must not see the event: %d calls", len(h.router.prompts))
	}
	if trigger := h.disposition(0); trigger.Disposition != core.SuperAgentDispositionFailed || !strings.Contains(trigger.Reason, "plan mode") {
		t.Fatalf("trigger = %+v", trigger)
	}
}
