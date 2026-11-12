package superagent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// brainHarness is an agent made from an ordinary Agent bound to channel c1.
type brainHarness struct {
	*harness
	msg *core.Message
}

func newBrainHarness(t *testing.T, workers bool) *brainHarness {
	t.Helper()
	clock := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	h := &harness{
		t: t, store: newMemoryStore(), workers: &workerStub{}, replies: &replyStub{},
		router: &routerStub{answer: func(core.InvocationRequest) (string, error) { return "", nil }},
		clock:  &clock,
	}
	h.router.updates = h.relay
	h.store.local["coder"] = core.AgentInstance{ID: "coder", Name: "Coder", RuntimeID: "codex", Enabled: true}
	h.store.channels = []core.Channel{{ID: "c1", Name: "Team bot", Type: "feishu", AgentID: "coder", Enabled: true}}
	h.service = New(Options{
		Store: h.store, Invoker: h.router, Workers: h.workers, Replies: h.replies,
		Now: func() time.Time { return *h.clock }, StateDir: t.TempDir(),
	})
	t.Cleanup(h.service.wg.Wait)
	if _, err := h.service.UnifyAgents(context.Background()); err != nil {
		t.Fatal(err)
	}
	if workers {
		agent, _ := h.store.GetSuperAgent(context.Background(), "coder")
		agent.Workers = []core.SuperAgentWorker{{ID: "local", AgentID: "yolo", Capabilities: "builds things"}}
		if _, err := h.service.Save(context.Background(), *agent); err != nil {
			t.Fatal(err)
		}
	}
	stored, _ := h.store.GetSuperAgent(context.Background(), "coder")
	h.agent = *stored
	return &brainHarness{harness: h, msg: &core.Message{
		ID: "om_1", SourceMessageID: "om_1", ChannelID: "c1", ChatID: "oc_1", ChatType: "group",
		UserID: "ou_alice", UserName: "Alice", Text: "please fix the login bug", ConversationKey: "chat:oc_1",
	}}
}

func TestAChatOnlyAgentGetsNoBrief(t *testing.T) {
	h := newBrainHarness(t, false)
	ctx := context.Background()
	if brief := h.service.BrainBrief(ctx, "coder", h.msg); brief != "" {
		t.Fatalf("a migrated agent with no workers was briefed:\n%s", brief)
	}
	h.service.BrainFinished(ctx, "coder", h.msg, acceptAnswer("local"))
	if len(h.store.triggers) != 0 || len(h.store.allTickets()) != 0 {
		t.Fatal("a chat-only agent acted on an action block")
	}
}

func TestBrainBriefListsTheRosterAndTickets(t *testing.T) {
	h := newBrainHarness(t, true)
	brief := h.service.BrainBrief(context.Background(), "coder", h.msg)
	for _, want := range []string{`You are "Coder"`, `"id": "local"`, "builds things", "OPEN TICKETS", "MEMORY INDEX", actionOpen} {
		if !strings.Contains(brief, want) {
			t.Fatalf("brief lacks %q:\n%s", want, brief)
		}
	}
	if strings.Contains(brief, "EVENT ") {
		t.Fatal("the chat message itself follows the brief; the brief must not hold an event")
	}
	disabled := h.agent
	disabled.Enabled = false
	_ = h.store.UpsertSuperAgent(context.Background(), disabled)
	if brief := h.service.BrainBrief(context.Background(), "coder", h.msg); brief != "" {
		t.Fatal("a disabled agent was briefed")
	}
}

func TestBrainActionsStartATicketThatReportsToTheChat(t *testing.T) {
	h := newBrainHarness(t, true)
	h.workers.run = func(context.Context, workerCall) (string, error) { return "fixed on branch fix-login", nil }
	ctx := context.Background()
	h.service.BrainFinished(ctx, "coder", h.msg, acceptAnswer("local"))
	tickets := h.store.allTickets()
	if len(tickets) != 1 {
		t.Fatalf("tickets = %+v", tickets)
	}
	ticket := tickets[0]
	if ticket.SuperAgentID != "coder" || ticket.SourceKind != core.SuperAgentSourceChannel || ticket.SourceID != "channel:c1" || ticket.Worker.AgentID != "yolo" {
		t.Fatalf("ticket = %+v", ticket)
	}
	if !strings.Contains(ticket.Inputs[0], "please fix the login bug") {
		t.Fatalf("the worker was not given the request: %s", ticket.Inputs[0])
	}
	if sent := h.replies.sent(); len(sent) != 0 {
		t.Fatalf("the chat already has the brain's reply, but AgentMux sent %q", sent)
	}
	h.settle()
	target := `{"channel_id":"c1","chat_id":"oc_1","message_id":"om_1"}`
	if got := h.replies.sentTo(target); len(got) != 1 || got[0] != "fixed on branch fix-login" {
		t.Fatalf("report to the chat = %q (all: %q, targets %q)", got, h.replies.sent(), h.replies.targets)
	}
	if h.replies.sources[0] != "channel:c1" {
		t.Fatalf("report went through %q", h.replies.sources[0])
	}

	// A redelivered message does not start the work twice.
	h.service.BrainFinished(ctx, "coder", h.msg, acceptAnswer("local"))
	if len(h.store.allTickets()) != 1 {
		t.Fatal("a redelivered message started a second ticket")
	}
	// The next turn on the chat sees the ticket.
	next := *h.msg
	next.ID, next.SourceMessageID, next.Text = "om_2", "om_2", "how is it going?"
	if brief := h.service.BrainBrief(ctx, "coder", &next); !strings.Contains(brief, ticket.ID) || !strings.Contains(brief, `"thread": "this thread"`) {
		t.Fatalf("the next brief does not list the ticket on this thread:\n%s", brief)
	}
}

func TestBrainActionsAreCheckedBeforeAnythingRuns(t *testing.T) {
	h := newBrainHarness(t, true)
	h.service.BrainFinished(context.Background(), "coder", h.msg, acceptAnswer("nobody"))
	if len(h.store.allTickets()) != 0 {
		t.Fatal("an unknown worker started a ticket")
	}
	sent := h.replies.sent()
	if len(sent) != 1 || !strings.Contains(sent[0], "not on the roster") {
		t.Fatalf("the chat was not told: %q", sent)
	}
	if trigger := h.store.triggers[0]; trigger.Disposition != core.SuperAgentDispositionFailed {
		t.Fatalf("trigger = %+v", trigger)
	}
}

func TestObservingBrainRecordsActionsOnly(t *testing.T) {
	h := newBrainHarness(t, true)
	agent := h.agent
	agent.Mode = core.SuperAgentModeObserve
	_ = h.store.UpsertSuperAgent(context.Background(), agent)
	if brief := h.service.BrainBrief(context.Background(), "coder", h.msg); !strings.Contains(brief, "OBSERVE MODE") {
		t.Fatal("an observing brain was not told")
	}
	h.service.BrainFinished(context.Background(), "coder", h.msg, acceptAnswer("local"))
	if len(h.store.allTickets()) != 0 || len(h.replies.sent()) != 0 {
		t.Fatal("an observing agent acted")
	}
	if trigger := h.store.triggers[0]; trigger.Disposition != core.SuperAgentDispositionObserved || !strings.Contains(trigger.Reason, "would accept") {
		t.Fatalf("trigger = %+v", trigger)
	}
}

func TestPlainAnswersRecordNothing(t *testing.T) {
	h := newBrainHarness(t, true)
	h.service.BrainFinished(context.Background(), "coder", h.msg, `{"action":"accept","worker_id":"local"}`)
	if len(h.store.triggers) != 0 {
		t.Fatal("an answer without an action block was read as one")
	}
}
