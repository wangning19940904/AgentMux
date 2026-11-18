package superagent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func TestWorkShowsRequestsTicketsAndFollowUpsAcrossAgents(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	now := *h.clock
	agentID := h.agent.ID
	reply := json.RawMessage(`{"chat_id":"oc_secret"}`)

	for _, trigger := range []core.SuperAgentTrigger{
		{ID: "tr_req", DedupeKey: "1", SuperAgentID: agentID, ThreadKey: "chat:1", Actor: "ou_alice", ActorName: "Alice", Text: "please fix the build", Disposition: core.SuperAgentDispositionAccepted, ReceivedAt: now.Add(-time.Hour)},
		{ID: "tr_first", DedupeKey: "2", SuperAgentID: agentID, ThreadKey: "chat:2", ActorName: "Bob", Text: "deploy it", Disposition: core.SuperAgentDispositionPending, ReceivedAt: now.Add(-2 * time.Minute)},
		{ID: "tr_next", DedupeKey: "3", SuperAgentID: agentID, ThreadKey: "chat:2", ActorName: "Bob", Text: "and tell Li", Disposition: core.SuperAgentDispositionPending, ReceivedAt: now.Add(-time.Minute)},
	} {
		if _, err := h.store.InsertSuperAgentTrigger(ctx, trigger); err != nil {
			t.Fatal(err)
		}
	}
	for _, ticket := range []core.SuperAgentTicket{
		{ID: "tk_running", SuperAgentID: agentID, TriggerID: "tr_req", Title: "Fix the build", Status: core.SuperAgentTicketRunning, Reply: reply, Worker: core.SuperAgentWorker{ID: "local", AgentID: "coder"}, CreatedAt: now.Add(-48 * time.Hour), UpdatedAt: now.Add(-48 * time.Hour)},
		{ID: "tk_done", SuperAgentID: agentID, Title: "Answer Bob", Status: core.SuperAgentTicketReady, CreatedAt: now.Add(-2 * time.Hour), FinishedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour)},
		{ID: "tk_old", SuperAgentID: agentID, Title: "Long done", Status: core.SuperAgentTicketReady, CreatedAt: now.Add(-72 * time.Hour), FinishedAt: now.Add(-48 * time.Hour), UpdatedAt: now.Add(-48 * time.Hour)},
		{ID: "tk_orphan", SuperAgentID: "sa_gone", Title: "From a deleted agent", Status: core.SuperAgentTicketQueued, CreatedAt: now},
	} {
		if err := h.store.UpsertSuperAgentTicket(ctx, ticket); err != nil {
			t.Fatal(err)
		}
	}
	for _, timer := range []core.SuperAgentTimer{
		{ID: "tm_later", SuperAgentID: agentID, Status: core.SuperAgentTimerActive, ThreadKey: "chat:1", Note: "check again", DueAt: now.Add(3 * time.Hour), Reply: reply, UpdatedAt: now},
		{ID: "tm_soon", SuperAgentID: agentID, Status: core.SuperAgentTimerActive, ThreadKey: "chat:1", TicketID: "tk_running", Note: "is it merged?", DueAt: now.Add(time.Hour), UpdatedAt: now},
		{ID: "tm_fired", SuperAgentID: agentID, Status: core.SuperAgentTimerDone, ThreadKey: "chat:1", Note: "fired", DueAt: now.Add(-time.Hour), FiredAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour)},
	} {
		if err := h.store.UpsertSuperAgentTimer(ctx, timer); err != nil {
			t.Fatal(err)
		}
	}
	// The router is reading the oldest event on chat:2.
	h.service.mu.Lock()
	h.service.evaluating[agentID+"|chat:2"] = true
	h.service.mu.Unlock()
	defer func() {
		h.service.mu.Lock()
		delete(h.service.evaluating, agentID+"|chat:2")
		h.service.mu.Unlock()
	}()

	work, err := h.service.Work(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if len(work.Routing) != 2 || work.Routing[0].ID != "tr_first" || !work.Routing[0].Routing || work.Routing[1].Routing || work.Routing[0].SuperAgentName != "sa" || work.Routing[0].ActorName != "Bob" {
		t.Fatalf("routing = %+v", work.Routing)
	}

	var ids []string
	for _, ticket := range work.Tickets {
		ids = append(ids, ticket.ID)
	}
	if len(ids) != 2 || ids[0] != "tk_running" || ids[1] != "tk_done" {
		t.Fatalf("tickets = %v: a running ticket comes first however old, a finished one stays a day, a deleted agent's is left out", ids)
	}
	running := work.Tickets[0]
	if running.Request != "please fix the build" || running.RequesterName != "Alice" || running.SuperAgentName != "sa" || running.Reply != nil {
		t.Fatalf("running = %+v", running)
	}

	var timers []string
	for _, timer := range work.Timers {
		timers = append(timers, timer.ID)
		if timer.Reply != nil || timer.SuperAgentName != "sa" {
			t.Fatalf("timer = %+v", timer)
		}
	}
	if len(timers) != 3 || timers[0] != "tm_soon" || timers[1] != "tm_later" || timers[2] != "tm_fired" {
		t.Fatalf("timers = %v: set ones first, soonest due first", timers)
	}
	if work.Timers[0].TicketTitle != "Fix the build" {
		t.Fatalf("timer ticket = %q", work.Timers[0].TicketTitle)
	}
}
