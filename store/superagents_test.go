package store

import (
	"context"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func TestSuperAgentPersistence(t *testing.T) {
	st := openPostgresIntegrationStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 3, 1, 2, 3, 0, time.UTC)
	agent := core.SuperAgent{
		ID: "sa", Name: "keeper", RouterAgentID: "router", Mode: core.SuperAgentModeAct, Enabled: true,
		Workers:   []core.SuperAgentWorker{{ID: "w", HostID: "h", AgentID: "a"}},
		Sources:   []core.SuperAgentSource{{ID: "src", Kind: core.SuperAgentSourceEmail, Enabled: true, AllowedActors: []string{"a@b.c"}, Config: map[string]string{"password": "pw"}}},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := st.UpsertSuperAgent(ctx, agent); err != nil {
		t.Fatal(err)
	}
	agent.Name = "renamed"
	if err := st.UpsertSuperAgent(ctx, agent); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetSuperAgent(ctx, "sa")
	if err != nil || got == nil || got.Name != "renamed" || got.Workers[0].HostID != "h" || got.Sources[0].Config["password"] != "pw" {
		t.Fatalf("agent=%+v err=%v", got, err)
	}
	if missing, err := st.GetSuperAgent(ctx, "nope"); missing != nil || err != nil {
		t.Fatalf("missing=%+v err=%v", missing, err)
	}

	trigger := core.SuperAgentTrigger{
		ID: "t1", SuperAgentID: "sa", SourceID: "src", SourceKind: core.SuperAgentSourceEmail, DedupeKey: "d1", ThreadKey: "thread",
		Disposition: core.SuperAgentDispositionPending, Text: "hello", Reply: []byte(`{"to":"a@b.c"}`), ReceivedAt: now, UpdatedAt: now,
	}
	inserted, err := st.InsertSuperAgentTrigger(ctx, trigger)
	if err != nil || !inserted {
		t.Fatalf("inserted=%v err=%v", inserted, err)
	}
	duplicate := trigger
	duplicate.ID = "t2"
	inserted, err = st.InsertSuperAgentTrigger(ctx, duplicate)
	if err != nil || inserted {
		t.Fatalf("a redelivered event must not store twice: inserted=%v err=%v", inserted, err)
	}
	pending, err := st.ListPendingSuperAgentTriggers(ctx, 10)
	if err != nil || len(pending) != 1 || string(pending[0].Reply) != `{"to":"a@b.c"}` {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	trigger.Disposition = core.SuperAgentDispositionAccepted
	if err := st.UpdateSuperAgentTrigger(ctx, trigger); err != nil {
		t.Fatal(err)
	}
	if pending, _ := st.ListPendingSuperAgentTriggers(ctx, 10); len(pending) != 0 {
		t.Fatalf("pending=%+v", pending)
	}
	if listed, err := st.ListSuperAgentTriggers(ctx, "sa", 10); err != nil || len(listed) != 1 || listed[0].Disposition != core.SuperAgentDispositionAccepted {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}

	ticket := core.SuperAgentTicket{
		ID: "k1", SuperAgentID: "sa", ThreadKey: "thread", Status: core.SuperAgentTicketQueued, Title: "work",
		Worker: agent.Workers[0], ConversationID: "c", Inputs: []string{"do it"}, CreatedAt: now, UpdatedAt: now,
	}
	if err := st.UpsertSuperAgentTicket(ctx, ticket); err != nil {
		t.Fatal(err)
	}
	ticket.Status = core.SuperAgentTicketRunning
	if err := st.UpsertSuperAgentTicket(ctx, ticket); err != nil {
		t.Fatal(err)
	}
	loaded, err := st.GetSuperAgentTicket(ctx, "k1")
	if err != nil || loaded == nil || loaded.Status != core.SuperAgentTicketRunning || loaded.Inputs[0] != "do it" || loaded.Worker.HostID != "h" {
		t.Fatalf("ticket=%+v err=%v", loaded, err)
	}
	if running, err := st.ListSuperAgentTicketsByStatus(ctx, core.SuperAgentTicketQueued, core.SuperAgentTicketRunning); err != nil || len(running) != 1 {
		t.Fatalf("running=%+v err=%v", running, err)
	}
	if queued, _ := st.ListSuperAgentTicketsByStatus(ctx, core.SuperAgentTicketQueued); len(queued) != 0 {
		t.Fatalf("queued=%+v", queued)
	}
	if thread, err := st.ListSuperAgentThreadTickets(ctx, "sa", "thread"); err != nil || len(thread) != 1 {
		t.Fatalf("thread=%+v err=%v", thread, err)
	}
	if other, _ := st.ListSuperAgentThreadTickets(ctx, "sa", "other"); len(other) != 0 {
		t.Fatalf("other=%+v", other)
	}
	if listed, err := st.ListSuperAgentTickets(ctx, "sa", 10); err != nil || len(listed) != 1 {
		t.Fatalf("tickets=%+v err=%v", listed, err)
	}

	if state, err := st.GetSuperAgentSourceState(ctx, "src"); err != nil || state != "" {
		t.Fatalf("state=%q err=%v", state, err)
	}
	for _, position := range []string{"1", "2"} {
		if err := st.SetSuperAgentSourceState(ctx, "sa", "src", position); err != nil {
			t.Fatal(err)
		}
	}
	if state, _ := st.GetSuperAgentSourceState(ctx, "src"); state != "2" {
		t.Fatalf("state=%q", state)
	}

	if err := st.DeleteSuperAgent(ctx, "sa"); err != nil {
		t.Fatal(err)
	}
	if listed, _ := st.ListSuperAgents(ctx); len(listed) != 0 {
		t.Fatalf("agents=%+v", listed)
	}
	if listed, _ := st.ListSuperAgentTickets(ctx, "sa", 10); len(listed) != 0 {
		t.Fatalf("tickets=%+v", listed)
	}
	if listed, _ := st.ListSuperAgentTriggers(ctx, "sa", 10); len(listed) != 0 {
		t.Fatalf("triggers=%+v", listed)
	}
	if state, _ := st.GetSuperAgentSourceState(ctx, "src"); state != "" {
		t.Fatalf("a new Source must not inherit a read position: %q", state)
	}
}

// A database can already hold a different migration at a neighboring version.
// The super_agents tables must still appear, or the daemon cannot start.
func TestSuperAgentMigrationRunsOnDatabaseWithVersion18Applied(t *testing.T) {
	st := openPostgresIntegrationStore(t)
	ctx := context.Background()
	for _, statement := range []string{
		`DROP TABLE super_agents, super_agent_triggers, super_agent_tickets, super_agent_source_state`,
		`DELETE FROM schema_migrations WHERE name='super_agents'`,
		`INSERT INTO schema_migrations (version,name) VALUES (18,'usage_request_identity') ON CONFLICT (version) DO NOTHING`,
	} {
		if _, err := st.db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.migratePostgres(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ListSuperAgents(ctx); err != nil {
		t.Fatalf("super_agents must exist after migrating: %v", err)
	}
}
