package server

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
	"github.com/wangning19940904/AgentMux/superagent"
)

// agentRecords keeps agent records in memory over a SQLite test store, which
// has no super_agents table; everything else is the real store.
type agentRecords struct {
	*store.Store
	mu     sync.Mutex
	agents map[string]core.SuperAgent
}

func (r *agentRecords) ListSuperAgents(context.Context) ([]core.SuperAgent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []core.SuperAgent{}
	for _, agent := range r.agents {
		out = append(out, agent)
	}
	return out, nil
}
func (r *agentRecords) GetSuperAgent(_ context.Context, id string) (*core.SuperAgent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if agent, ok := r.agents[id]; ok {
		return &agent, nil
	}
	return nil, nil
}
func (r *agentRecords) UpsertSuperAgent(_ context.Context, agent core.SuperAgent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents[agent.ID] = agent
	return nil
}
func (r *agentRecords) DeleteSuperAgent(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.agents, id)
	return nil
}

// Creating an agent through the stable agent-instances API makes a unified
// agent with the same id; the response keeps its published shape, and
// deleting the agent removes both.
func TestAgentInstancesCreateUnifiedAgents(t *testing.T) {
	addTestRuntimeToPath(t, "codex")
	s, st := newTestServer(t)
	ctx := context.Background()
	records := &agentRecords{Store: st, agents: map[string]core.SuperAgent{}}
	s.SetSuperAgents(superagent.New(superagent.Options{Store: records, StateDir: filepath.Join(t.TempDir(), "state")}), nil)

	rec := doJSON(t, s, http.MethodPost, "/api/v1/agent-instances", core.AgentInstance{
		Name: "Research", RuntimeID: "codex", WorkDir: "/repo", DefaultApprovalMode: core.ApprovalModeAutoEdit, Enabled: true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create: code = %d body = %s", rec.Code, rec.Body.String())
	}
	var saved core.AgentInstance
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.ID == "" || saved.Name != "Research" || saved.RuntimeID != "codex" {
		t.Fatalf("saved = %+v", saved)
	}
	agent, err := records.GetSuperAgent(ctx, saved.ID)
	if err != nil || agent == nil {
		t.Fatalf("no unified agent for %s: %v", saved.ID, err)
	}
	if agent.RouterAgentID != saved.ID || agent.Mode != core.SuperAgentModeAct || !agent.Enabled || agent.Name != "Research" {
		t.Fatalf("unified agent = %+v", agent)
	}

	// The unified list shows the agent with its bound channel as a source.
	if err := st.UpsertChannel(ctx, &core.Channel{ID: "ch1", Name: "Team bot", Type: "slack", AgentID: saved.ID, Enabled: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	rec = doJSON(t, s, http.MethodGet, "/api/v1/super-agents", nil)
	var agents []core.SuperAgent
	if err := json.Unmarshal(rec.Body.Bytes(), &agents); err != nil {
		t.Fatalf("list: %v (%s)", err, rec.Body.String())
	}
	if len(agents) != 1 || len(agents[0].Sources) != 1 || agents[0].Sources[0].Kind != core.SuperAgentSourceChannel {
		t.Fatalf("agents = %+v", agents)
	}

	// Renaming through the stable API renames the agent.
	saved.Name = "Researcher"
	if rec := doJSON(t, s, http.MethodPost, "/api/v1/agent-instances", saved); rec.Code != http.StatusOK {
		t.Fatalf("rename: code = %d body = %s", rec.Code, rec.Body.String())
	}
	if agent, _ := records.GetSuperAgent(ctx, saved.ID); agent.Name != "Researcher" {
		t.Fatalf("agent name = %q", agent.Name)
	}

	if rec := doJSON(t, s, http.MethodDelete, "/api/v1/agent-instances?id="+saved.ID, nil); rec.Code != http.StatusOK {
		t.Fatalf("delete: code = %d body = %s", rec.Code, rec.Body.String())
	}
	if agent, _ := records.GetSuperAgent(ctx, saved.ID); agent != nil {
		t.Fatal("deleting the agent left its unified record")
	}
	if instance, _ := st.GetAgentInstance(ctx, saved.ID); instance != nil {
		t.Fatal("deleting the agent left its brain")
	}
}
