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

func TestWorkLinksTicketsEventsAndFollowUpsIntoChains(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	now := *h.clock
	agentID := h.agent.ID

	for _, trigger := range []core.SuperAgentTrigger{
		{ID: "tr_req", DedupeKey: "1", SuperAgentID: agentID, ThreadKey: "chat:1", ActorName: "Alice", Text: "ship the fix", Disposition: core.SuperAgentDispositionAccepted, TicketID: "tk_root", ReceivedAt: now.Add(-50 * time.Hour)},
		// The root's update started a second Ticket: its ticket id moved to
		// the new Ticket, and only the dedupe key still names the root.
		{ID: "tr_update", DedupeKey: "ticket:tk_root:abc", SuperAgentID: agentID, ThreadKey: "chat:1", Kind: core.SuperAgentTriggerTicketUpdate, Text: "Ticket tk_root finished", Disposition: core.SuperAgentDispositionAccepted, TicketID: "tk_next", ReceivedAt: now.Add(-3 * time.Hour)},
		// A due follow-up about the second Ticket started a third.
		{ID: "tr_timer", DedupeKey: "timer:tm_fired:1", SuperAgentID: agentID, ThreadKey: "chat:1", Kind: core.SuperAgentTriggerTimer, SubjectTicketID: "tk_next", TimerID: "tm_fired", Disposition: core.SuperAgentDispositionAccepted, TicketID: "tk_third", ReceivedAt: now.Add(-2 * time.Hour)},
		// A later message, and the second Ticket's result, went to Tickets
		// that were already there.
		{ID: "tr_more", DedupeKey: "2", SuperAgentID: agentID, ThreadKey: "chat:9", ActorName: "Bob", Text: "also update the docs", Disposition: core.SuperAgentDispositionFollowUp, TicketID: "tk_next", ReceivedAt: now.Add(-90 * time.Minute)},
		{ID: "tr_handoff", DedupeKey: "ticket:tk_next:def", SuperAgentID: agentID, ThreadKey: "chat:1", Kind: core.SuperAgentTriggerTicketUpdate, SubjectTicketID: "tk_next", Disposition: core.SuperAgentDispositionFollowUp, TicketID: "tk_third", ReceivedAt: now.Add(-time.Hour)},
		// A Ticket's own update sent back to it is a rework, not a delivery.
		{ID: "tr_rework", DedupeKey: "ticket:tk_next:ghi", SuperAgentID: agentID, ThreadKey: "chat:1", Kind: core.SuperAgentTriggerTicketUpdate, Disposition: core.SuperAgentDispositionFollowUp, TicketID: "tk_next", ReceivedAt: now.Add(-30 * time.Minute)},
		{ID: "tr_set", DedupeKey: "3", SuperAgentID: agentID, ThreadKey: "chat:1", ActorName: "Alice", Text: "remind me", Disposition: core.SuperAgentDispositionAnswered, TicketID: "tk_third", ReceivedAt: now.Add(-20 * time.Minute)},
	} {
		if _, err := h.store.InsertSuperAgentTrigger(ctx, trigger); err != nil {
			t.Fatal(err)
		}
	}
	for _, ticket := range []core.SuperAgentTicket{
		// Finished two days ago: on the page only because the chain starts here.
		{ID: "tk_root", SuperAgentID: agentID, TriggerID: "tr_req", Title: "Fix it", Status: core.SuperAgentTicketReady, CreatedAt: now.Add(-50 * time.Hour), FinishedAt: now.Add(-49 * time.Hour), UpdatedAt: now.Add(-49 * time.Hour)},
		{ID: "tk_next", SuperAgentID: agentID, TriggerID: "tr_update", Title: "Verify it", Status: core.SuperAgentTicketRunning, CreatedAt: now.Add(-3 * time.Hour), UpdatedAt: now},
		{ID: "tk_third", SuperAgentID: agentID, TriggerID: "tr_timer", Title: "Release it", Status: core.SuperAgentTicketQueued, DependsOn: []string{"tk_next"}, CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now},
		{ID: "tk_unrelated", SuperAgentID: agentID, Title: "Long done", Status: core.SuperAgentTicketReady, CreatedAt: now.Add(-72 * time.Hour), FinishedAt: now.Add(-48 * time.Hour), UpdatedAt: now.Add(-48 * time.Hour)},
	} {
		if err := h.store.UpsertSuperAgentTicket(ctx, ticket); err != nil {
			t.Fatal(err)
		}
	}
	for _, timer := range []core.SuperAgentTimer{
		{ID: "tm_fired", SuperAgentID: agentID, Status: core.SuperAgentTimerDone, ThreadKey: "chat:1", TicketID: "tk_next", Note: "is it verified?", DueAt: now.Add(-2 * time.Hour), FiredAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-2 * time.Hour)},
		{ID: "tm_loose", SuperAgentID: agentID, Status: core.SuperAgentTimerActive, ThreadKey: "chat:1", SetBy: "tr_set", Note: "remind Alice", DueAt: now.Add(time.Hour), UpdatedAt: now},
	} {
		if err := h.store.UpsertSuperAgentTimer(ctx, timer); err != nil {
			t.Fatal(err)
		}
	}

	work, err := h.service.Work(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]TicketSummary{}
	for _, ticket := range work.Tickets {
		byID[ticket.ID] = ticket
	}
	if _, shown := byID["tk_unrelated"]; shown || len(byID) != 3 {
		t.Fatalf("tickets = %v: an old Ticket shows only when shown work came from it", byID)
	}
	if root := byID["tk_root"]; root.OriginKind != "" || root.OriginTicketID != "" || root.Request != "ship the fix" {
		t.Fatalf("root = %+v", root)
	}
	if next := byID["tk_next"]; next.OriginKind != core.SuperAgentTriggerTicketUpdate || next.OriginTicketID != "tk_root" || next.OriginTimerID != "" {
		t.Fatalf("next = %+v: an update stored before events named their subject is read from its dedupe key", next)
	}
	if third := byID["tk_third"]; third.OriginKind != core.SuperAgentTriggerTimer || third.OriginTicketID != "tk_next" || third.OriginTimerID != "tm_fired" {
		t.Fatalf("third = %+v", third)
	}

	if len(work.Events) != 2 || work.Events[0].ID != "tr_more" || work.Events[1].ID != "tr_handoff" {
		t.Fatalf("events = %+v: a rework and an event that started a Ticket are left out", work.Events)
	}
	if more := work.Events[0]; more.TicketID != "tk_next" || more.FromTicketID != "" || more.ActorName != "Bob" || more.Text != "also update the docs" {
		t.Fatalf("message = %+v", more)
	}
	if handoff := work.Events[1]; handoff.TicketID != "tk_third" || handoff.FromTicketID != "tk_next" {
		t.Fatalf("handoff = %+v", handoff)
	}

	for _, timer := range work.Timers {
		switch timer.ID {
		case "tm_fired":
			if timer.TicketTitle != "Verify it" || timer.FromTicketID != "" {
				t.Fatalf("fired = %+v", timer)
			}
		case "tm_loose":
			if timer.FromTicketID != "tk_third" {
				t.Fatalf("loose = %+v: a follow-up that names no Ticket comes from the one its event concerned", timer)
			}
		}
	}
}
