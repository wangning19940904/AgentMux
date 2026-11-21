package superagent

import (
	"context"
	"strings"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
)

// sentID is the id of the message the agent posted with text.
func sentID(t *testing.T, h *harness, text string) (string, sentRecord) {
	t.Helper()
	records, err := h.service.loadSent(context.Background(), h.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.Text == text {
			return record.MessageID, record
		}
	}
	t.Fatalf("no sent message %q in %+v", text, records)
	return "", sentRecord{}
}

func lastPrompt(h *harness) string {
	h.router.mu.Lock()
	defer h.router.mu.Unlock()
	return h.router.prompts[len(h.router.prompts)-1]
}

func ticketTitled(t *testing.T, h *harness, title string) core.SuperAgentTicket {
	t.Helper()
	for _, ticket := range h.store.allTickets() {
		if ticket.Title == title {
			return ticket
		}
	}
	t.Fatalf("no ticket %q", title)
	return core.SuperAgentTicket{}
}

func acceptTitled(reply, title string) func(core.InvocationRequest) (string, error) {
	return actions(reply, `{"action":"accept","worker_id":"local","title":"`+title+`","instructions":"do it"}`)
}

// A person who replies to the agent's report in a chat where it reports on
// several pieces of work means the work that report was about. The router is
// told which, rather than left to guess from "this one".
func TestAReplyToTheAgentsMessageSaysWhichWorkItIsAbout(t *testing.T) {
	h := newHarness(t)
	h.service.directory = directoryStub{}
	h = refeishu(t, h, "oc_team")
	ctx := context.Background()

	h.router.answer = acceptTitled("Researching OMA.", "OMA research")
	if _, err := h.service.InterceptMessage(ctx, groupMessage("m1", "ou_alice", "research the OMA move", true)); err != nil {
		t.Fatal(err)
	}
	h.settle()
	h.router.answer = acceptTitled("Fixing WebMCP.", "WebMCP fix")
	if _, err := h.service.InterceptMessage(ctx, groupMessage("m2", "ou_alice", "fix WebMCP", true)); err != nil {
		t.Fatal(err)
	}
	h.settle()
	oma := ticketTitled(t, h, "OMA research")
	id, record := sentID(t, h, "Researching OMA.")
	if record.About.Thread != oma.ThreadKey || !strings.Contains(strings.Join(record.About.Tickets, ","), oma.ID) {
		t.Fatalf("the reply is about the OMA ticket: %+v", record)
	}

	// No @mention: replying to the agent's own message addresses it.
	h.router.answer = func(core.InvocationRequest) (string, error) { return "", nil }
	reply := &core.Message{
		ID: "m3", SourceMessageID: "m3", ChannelID: "ch1", ChatID: "oc_team", ChatType: "group",
		UserID: "ou_alice", Text: "I'll do this one myself, stop following it", ParentID: id, RootID: id,
	}
	if claimed, err := h.service.InterceptMessage(ctx, reply); err != nil || !claimed {
		t.Fatalf("claimed=%v err=%v", claimed, err)
	}
	h.settle()

	prompt := lastPrompt(h)
	for _, want := range []string{
		`"replying_to": {`, `"your_message": "Researching OMA."`, `"ticket_ids": [`, oma.ID,
		`"replied_to": true`, `mean the work that message was about: ` + oma.ID,
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt lacks %q:\n%s", want, prompt)
		}
	}
	var stored *core.SuperAgentTrigger
	for _, trigger := range h.store.triggers {
		if trigger.DedupeKey == "m3" {
			stored = &trigger
		}
	}
	if stored == nil || !stored.Addressed || stored.InReplyTo == nil || stored.InReplyTo.MessageID != id {
		t.Fatalf("stored trigger = %+v", stored)
	}

	// A reply to a message the agent did not send carries nothing.
	other := *reply
	other.ID, other.SourceMessageID, other.ParentID, other.RootID, other.MentionedBot = "m4", "m4", "om_someone_else", "om_someone_else", true
	if _, err := h.service.InterceptMessage(ctx, &other); err != nil {
		t.Fatal(err)
	}
	h.settle()
	if prompt := lastPrompt(h); strings.Contains(prompt, "replying_to") {
		t.Fatalf("no replying_to for someone else's message:\n%s", prompt)
	}
}

// Work stopped on one thread is shown, with why, to the router on every other
// thread, and the ticket's own files say it stopped, so no thread reports it
// as current work.
func TestStoppedWorkIsSharedWithEveryThread(t *testing.T) {
	h := newHarness(t)
	h.service.directory = directoryStub{}
	h = refeishu(t, h, "oc_team")
	ctx := context.Background()

	h.router.answer = acceptTitled("Measuring the savings.", "Savings check")
	if _, err := h.service.InterceptMessage(ctx, groupMessage("m1", "ou_alice", "check the savings", true)); err != nil {
		t.Fatal(err)
	}
	h.settle()
	savings := ticketTitled(t, h, "Savings check")

	h.router.answer = actions("Handed back to you.", `{"action":"stop","ticket_id":"`+savings.ID+`","reason":"Alice does it herself"}`)
	if _, err := h.service.InterceptMessage(ctx, groupMessage("m2", "ou_alice", "leave the savings check to me", true)); err != nil {
		t.Fatal(err)
	}
	h.settle()
	stopped, _ := h.store.GetSuperAgentTicket(ctx, savings.ID)
	if stopped.Status != core.SuperAgentTicketStopped || !strings.Contains(stopped.StopReason, "Alice does it herself") || !strings.Contains(stopped.StopReason, "leave the savings check to me") {
		t.Fatalf("stopped ticket = %+v", stopped)
	}
	if result := readTicketFile(t, h.service, *stopped, ticketResultFile); !strings.Contains(result, "STOPPED: Alice does it herself") {
		t.Fatalf("result.md = %q", result)
	}
	if record := readTicketFile(t, h.service, *stopped, ticketRecordFile); !strings.Contains(record, `"stop_reason": "Alice does it herself`) {
		t.Fatalf("ticket.json = %s", record)
	}

	h.router.answer = func(core.InvocationRequest) (string, error) { return "", nil }
	if _, err := h.service.InterceptMessage(ctx, groupMessage("m3", "ou_alice", "what are you working on?", true)); err != nil {
		t.Fatal(err)
	}
	h.settle()
	prompt := lastPrompt(h)
	section := prompt[strings.Index(prompt, "STOPPED LATELY"):]
	for _, want := range []string{`"id": "` + savings.ID + `"`, `"title": "Savings check"`, "Alice does it herself"} {
		if !strings.Contains(section, want) {
			t.Fatalf("STOPPED LATELY lacks %q:\n%s", want, prompt)
		}
	}
	open := prompt[strings.Index(prompt, "OPEN TICKETS"):strings.Index(prompt, "STOPPED LATELY")]
	if strings.Contains(open, savings.ID) {
		t.Fatalf("stopped work is not an open ticket:\n%s", open)
	}
}
