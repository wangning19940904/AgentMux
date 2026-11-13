package superagent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/mailbox"
)

type sentMail struct{ from, to, text string }

// mailStub is a mailbox that records what agents send.
type mailStub struct {
	mu      sync.Mutex
	parties []core.MailParty
	sent    []sentMail
	err     error
}

func (m *mailStub) Send(_ context.Context, from, to, text string) (core.MailMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return core.MailMessage{}, m.err
	}
	m.sent = append(m.sent, sentMail{from, to, text})
	return core.MailMessage{From: from, To: to, Text: text}, nil
}

func (m *mailStub) Reachable(context.Context, string) ([]core.MailParty, error) {
	return m.parties, nil
}

func (m *mailStub) all() []sentMail {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]sentMail(nil), m.sent...)
}

func (h *harness) withMailbox(parties ...core.MailParty) *mailStub {
	box := &mailStub{parties: parties}
	h.service.SetMailbox(box)
	return box
}

func TestMailArrivesAsAnEventAndTheReplyGoesBackToItsSender(t *testing.T) {
	h := newHarness(t)
	box := h.withMailbox()
	h.router.answer = func(core.InvocationRequest) (string, error) { return "The build is green.", nil }
	message := core.MailMessage{ID: "mail_1", From: "agent:planner@hub", FromName: "Planner", To: "agent:" + h.agent.ID, Text: "Is the build green?"}
	ctx := context.Background()
	if err := h.service.ReceiveMail(ctx, h.agent.ID, message); err != nil {
		t.Fatal(err)
	}
	// The hub may deliver a message twice; the agent hears it once.
	if err := h.service.ReceiveMail(ctx, h.agent.ID, message); err != nil {
		t.Fatal(err)
	}
	h.settle()

	if len(h.router.prompts) != 1 {
		t.Fatalf("the router was asked %d times", len(h.router.prompts))
	}
	for _, want := range []string{`"source": "mailbox"`, `"actor": "Planner"`, `"actor_id": "agent:planner@hub"`, "direct messages with agent:planner@hub", "Is the build green?"} {
		if !strings.Contains(h.router.prompts[0], want) {
			t.Fatalf("the event must show %s:\n%s", want, h.router.prompts[0])
		}
	}
	sent := box.all()
	if len(sent) != 1 || sent[0] != (sentMail{"agent:" + h.agent.ID, "agent:planner@hub", "The build is green."}) {
		t.Fatalf("sent = %+v", sent)
	}
	if got := h.disposition(0); got.Disposition != core.SuperAgentDispositionAnswered || got.SourceKind != core.SuperAgentSourceMailbox {
		t.Fatalf("trigger = %+v", got)
	}
	// Nothing went out through the chat and GitHub senders.
	if len(h.replies.texts) != 0 {
		t.Fatalf("replies = %+v", h.replies.texts)
	}
}

func TestWorkStartedByMailReportsBackByMail(t *testing.T) {
	h := newHarness(t)
	box := h.withMailbox()
	h.workers.run = func(context.Context, workerCall) (string, error) { return "fixed in abc123", nil }
	if err := h.service.ReceiveMail(context.Background(), h.agent.ID, core.MailMessage{
		ID: "mail_2", From: "person:alice", FromName: "Alice", Text: "Please fix the login bug.",
	}); err != nil {
		t.Fatal(err)
	}
	h.settle()

	tickets := h.store.allTickets()
	if len(tickets) != 1 || tickets[0].Status != core.SuperAgentTicketReady || tickets[0].ThreadKey != "mail:person:alice" || tickets[0].SourceID != mailSourceID {
		t.Fatalf("tickets = %+v", tickets)
	}
	sent := box.all()
	if len(sent) != 2 || sent[0].to != "person:alice" || sent[0].text != "on it" || sent[1].to != "person:alice" || !strings.Contains(sent[1].text, "fixed in abc123") {
		t.Fatalf("sent = %+v", sent)
	}
}

func TestRouterWritesToTheAgentsAndPeopleItMayReach(t *testing.T) {
	h := newHarness(t)
	box := h.withMailbox(
		core.MailParty{Address: "agent:reviewer@dev_9", Kind: core.MailPartyAgent, Name: "Reviewer", Description: "Reviews Go changes.", Machine: "studio", Online: true},
		core.MailParty{Address: "person:alice", Kind: core.MailPartyPerson, Name: "Alice", Online: true},
	)
	h.router.answer = func(core.InvocationRequest) (string, error) {
		return "Asked the reviewer.\n<agentmux-action>\n" +
			`[{"action":"post","to":"agent:reviewer@dev_9","text":"Can you review PR 12 in o/r?"},{"action":"post","to":"person:alice","text":"Review requested."}]` +
			"\n</agentmux-action>", nil
	}
	h.ingest("alice", "github:o/r#1", "get this reviewed", true)
	h.settle()

	prompt := h.router.prompts[0]
	for _, want := range []string{`"to": "agent:reviewer@dev_9"`, "Agent on studio: a direct message", "Reviews Go changes.", `"to": "person:alice"`, "OTHER AGENTS AND PEOPLE IN AGENTMUX"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("the router must be shown %s:\n%s", want, prompt)
		}
	}
	sent := box.all()
	from := "agent:" + h.agent.ID
	if len(sent) != 2 || sent[0] != (sentMail{from, "agent:reviewer@dev_9", "Can you review PR 12 in o/r?"}) || sent[1] != (sentMail{from, "person:alice", "Review requested."}) {
		t.Fatalf("sent = %+v", sent)
	}
	// The reply itself still goes to the thread the request came from.
	if len(h.replies.texts) != 1 || h.replies.texts[0] != "Asked the reviewer." {
		t.Fatalf("replies = %+v", h.replies.texts)
	}
}

func TestRouterCannotWriteToSomeoneItMayNotReach(t *testing.T) {
	h := newHarness(t)
	box := h.withMailbox(core.MailParty{Address: "person:alice", Kind: core.MailPartyPerson, Name: "Alice"})
	h.router.answer = func(core.InvocationRequest) (string, error) {
		return "<agentmux-action>\n" + `{"action":"post","to":"agent:stranger","text":"hello"}` + "\n</agentmux-action>", nil
	}
	h.ingest("alice", "github:o/r#1", "say hello to the stranger", true)
	h.exhaustRetries()
	if sent := box.all(); len(sent) != 0 {
		t.Fatalf("sent = %+v", sent)
	}
	if len(h.router.prompts) < 2 || !strings.Contains(h.router.prompts[1], "it is not one of the agents or people in PLACES") {
		t.Fatalf("the router must be told why: %v", h.router.prompts)
	}
}

func TestMailForAMissingOrDisabledAgentIsRefused(t *testing.T) {
	h := newHarness(t)
	h.withMailbox()
	ctx := context.Background()
	message := core.MailMessage{ID: "mail_3", From: "person:alice", Text: "hi"}
	if err := h.service.ReceiveMail(ctx, "nobody", message); !mailbox.IsRefusal(err) {
		t.Fatalf("missing agent: %v", err)
	}
	h.agent.Enabled = false
	if err := h.store.UpsertSuperAgent(ctx, h.agent); err != nil {
		t.Fatal(err)
	}
	if err := h.service.ReceiveMail(ctx, h.agent.ID, message); !mailbox.IsRefusal(err) {
		t.Fatalf("disabled agent: %v", err)
	}
	if len(h.store.triggers) != 0 {
		t.Fatalf("triggers = %+v", h.store.triggers)
	}
}

func TestAnAnswerByMailIsListedForTheThreadThatAsked(t *testing.T) {
	h := newHarness(t)
	box := h.withMailbox(core.MailParty{Address: "agent:reviewer", Kind: core.MailPartyAgent, Name: "Reviewer", Online: true})
	h.router.answer = func(core.InvocationRequest) (string, error) {
		return "<agentmux-action>\n" + `{"action":"post","to":"agent:reviewer","text":"Is PR 12 safe to merge?"}` + "\n</agentmux-action>", nil
	}
	h.ingest("alice", "github:o/r#1", "can we merge?", true)
	h.settle()
	if len(box.all()) != 1 {
		t.Fatalf("sent = %+v", box.all())
	}
	h.router.answer = func(core.InvocationRequest) (string, error) { return "", nil }
	if err := h.service.ReceiveMail(context.Background(), h.agent.ID, core.MailMessage{ID: "mail_4", From: "agent:reviewer", FromName: "Reviewer", Text: "Yes, merge it."}); err != nil {
		t.Fatal(err)
	}
	h.settle()
	prompt := h.router.prompts[len(h.router.prompts)-1]
	if !strings.Contains(prompt, "QUESTIONS YOU ASKED HERE") || !strings.Contains(prompt, "Is PR 12 safe to merge?") || !strings.Contains(prompt, "github:o/r#1") {
		t.Fatalf("the answer must be tied to the thread that asked:\n%s", prompt)
	}
}
