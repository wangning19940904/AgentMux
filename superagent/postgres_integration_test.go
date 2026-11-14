package superagent

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
)

func openPostgresStore(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("AGENTMUX_TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("AGENTMUX_TEST_DATABASE_URL must be set in CI")
		}
		t.Skip("AGENTMUX_TEST_DATABASE_URL is not set")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("amux_unify_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE SCHEMA "` + schema + `"`); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	st, err := store.OpenPostgres(context.Background(), store.DatabaseConfig{URL: parsed.String()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = st.Close()
		_, _ = admin.Exec(`DROP SCHEMA "` + schema + `" CASCADE`)
		_ = admin.Close()
	})
	return st
}

// A database from before there was one kind of agent: two ordinary Agents,
// one bound to a channel, and a Super Agent with its router agent and a
// Ticket. Starting the service gives the ordinary Agents records, keeps the
// Super Agent as it was, and a second start changes nothing.
func TestPostgresStartupUnifiesExistingAgents(t *testing.T) {
	st := openPostgresStore(t)
	ctx := context.Background()
	created := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	for _, instance := range []core.AgentInstance{
		{ID: "agent_chat", Name: "Chat bot", RuntimeID: "codex", WorkDir: "/srv/repo", DefaultModel: "gpt-5", DefaultApprovalMode: core.ApprovalModeAutoEdit, Enabled: true, CreatedAt: created, UpdatedAt: created},
		{ID: "agent_idle", Name: "Idle", RuntimeID: "claudecode", Enabled: false, CreatedAt: created, UpdatedAt: created},
		{ID: "sar_legacy", Name: "Ops router", RuntimeID: "codex", Source: routerAgentSource, DefaultApprovalMode: core.ApprovalModePlan, Enabled: true, CreatedAt: created, UpdatedAt: created},
	} {
		if err := st.UpsertAgentInstance(ctx, &instance); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.UpsertChannel(ctx, &core.Channel{ID: "ch_team", Name: "Team", Type: "feishu", AgentID: "agent_chat", Enabled: true, CreatedAt: created, UpdatedAt: created}); err != nil {
		t.Fatal(err)
	}
	legacy := core.SuperAgent{
		ID: "sa_ops", Name: "Ops", RouterAgentID: "sar_legacy", MaxConcurrency: 2, Enabled: true,
		Workers:   []core.SuperAgentWorker{{ID: "w", AgentID: "agent_chat"}},
		Sources:   []core.SuperAgentSource{{ID: "gh", Kind: core.SuperAgentSourceGitHub, Enabled: true, AllowedActors: []string{"alice"}}},
		CreatedAt: created, UpdatedAt: created,
	}
	if err := st.UpsertSuperAgent(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	ticket := core.SuperAgentTicket{ID: "tkt_1", SuperAgentID: "sa_ops", ThreadKey: "github:o/r#1", Status: core.SuperAgentTicketReady, CreatedAt: created, UpdatedAt: created}
	if err := st.UpsertSuperAgentTicket(ctx, ticket); err != nil {
		t.Fatal(err)
	}

	start := func() {
		service := New(Options{Store: st, StateDir: t.TempDir()})
		if err := service.Start(ctx); err != nil {
			t.Fatal(err)
		}
		service.Stop()
	}
	start()
	agents, err := st.ListSuperAgents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]core.SuperAgent{}
	for _, agent := range agents {
		byID[agent.ID] = agent
	}
	if len(byID) != 3 {
		t.Fatalf("agents = %+v", agents)
	}
	chat := byID["agent_chat"]
	if chat.RouterAgentID != "agent_chat" || !chat.Enabled || len(chat.Sources) != 0 || canStartWorkers(chat) {
		t.Fatalf("chat agent = %+v", chat)
	}
	if idle := byID["agent_idle"]; idle.Enabled || idle.RouterAgentID != "agent_idle" {
		t.Fatalf("idle agent = %+v", idle)
	}
	ops := byID["sa_ops"]
	if ops.RouterAgentID != "sar_legacy" || len(ops.Sources) != 1 || len(ops.Workers) != 1 {
		t.Fatalf("the Super Agent changed: %+v", ops)
	}
	if kept, _ := st.GetSuperAgentTicket(ctx, "tkt_1"); kept == nil || kept.Status != core.SuperAgentTicketReady {
		t.Fatalf("ticket = %+v", kept)
	}
	brain, _ := st.GetAgentInstance(ctx, "agent_chat")
	if brain == nil || brain.WorkDir != "/srv/repo" || brain.DefaultModel != "gpt-5" || brain.DefaultApprovalMode != core.ApprovalModeAutoEdit || brain.RuntimeID != "codex" {
		t.Fatalf("brain = %+v", brain)
	}
	if channel, _ := st.GetChannel(ctx, "ch_team"); channel == nil || channel.AgentID != "agent_chat" {
		t.Fatalf("channel binding = %+v", channel)
	}
	listed, err := New(Options{Store: st, StateDir: t.TempDir()}).List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range listed {
		if agent.ID == "agent_chat" && (len(agent.Sources) != 1 || agent.Sources[0].ID != "channel:ch_team") {
			t.Fatalf("chat agent sources = %+v", agent.Sources)
		}
	}

	before := byID["agent_chat"].UpdatedAt
	start()
	again, _ := st.ListSuperAgents(ctx)
	if len(again) != 3 {
		t.Fatalf("second start: %d agents", len(again))
	}
	if chat, _ := st.GetSuperAgent(ctx, "agent_chat"); !chat.UpdatedAt.Equal(before) {
		t.Fatal("second start rewrote a migrated agent")
	}
}
