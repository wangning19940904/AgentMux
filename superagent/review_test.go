package superagent

import (
	"context"
	"strings"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
)

func reviewAnswer(reply, ticketID, verdict, rest string) string {
	return reply + "\n<agentmux-action>{\"action\":\"review\",\"ticket_id\":\"" + ticketID + "\",\"verdict\":\"" + verdict + "\"" + rest + "}</agentmux-action>"
}

func TestReviewActionIsChecked(t *testing.T) {
	for name, test := range map[string]struct {
		rest string
		want string
	}{
		"no ticket":           {`"verdict":"accepted"`, "no ticket_id"},
		"unknown verdict":     {`"ticket_id":"tkt_1","verdict":"great"`, `not "great"`},
		"needs_user, no note": {`"ticket_id":"tkt_1","verdict":"needs_user"`, "needs a note"},
		"rework, no gaps":     {`"ticket_id":"tkt_1","verdict":"rework","checks":[{"criterion":"tests pass","met":true}]`, "needs the gaps"},
		"accepted, unmet":     {`"ticket_id":"tkt_1","verdict":"accepted","checks":[{"criterion":"tests pass","met":false}]`, `cannot be "accepted"`},
		"check, no criterion": {`"ticket_id":"tkt_1","verdict":"accepted","checks":[{"met":true}]`, "names no criterion"},
		"too many checks":     {`"ticket_id":"tkt_1","verdict":"accepted","checks":[` + strings.Repeat(`{"criterion":"x","met":true},`, maxReviewChecks) + `{"criterion":"y","met":true}]`, "at most 12"},
	} {
		_, err := parseRouterDecision("<agentmux-action>{\"action\":\"review\"," + test.rest + "}</agentmux-action>")
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: err = %v, want %q", name, err, test.want)
		}
	}
	decision, err := parseRouterDecision(reviewAnswer("", "tkt_1", "Rework", `,"checks":[{"criterion":"tests pass","met":false,"evidence":"2 fail"}],"note":"also lint"`))
	if err != nil {
		t.Fatal(err)
	}
	if action := decision.Actions[0]; reviewVerdict(action) != core.SuperAgentReviewRework || len(action.Checks) != 1 || action.Checks[0].Evidence != "2 fail" || action.Note != "also lint" {
		t.Fatalf("action = %+v", action)
	}
}

// reviewRouter accepts work with criteria and answers each ticket update
// with the next of verdicts.
func (h *harness) reviewRouter(verdicts ...string) {
	h.router.answer = func(core.InvocationRequest) (string, error) {
		return acceptWithCriteria(`,"done_when":["tests pass","no float in pricing/"]`), nil
	}
	h.router.updates = func(req core.InvocationRequest) (string, error) {
		match := updatedTicket.FindStringSubmatch(req.Input)
		if match == nil || len(verdicts) == 0 {
			return "", nil
		}
		verdict := verdicts[0]
		verdicts = verdicts[1:]
		switch verdict {
		case core.SuperAgentReviewRework:
			return reviewAnswer("Sent it back.", match[1], verdict, `,"checks":[{"criterion":"tests pass","met":false,"evidence":"pricing_test fails"},{"criterion":"no float in pricing/","met":true,"evidence":"grep finds none"}]`), nil
		case core.SuperAgentReviewNeedsUser:
			return reviewAnswer("", match[1], verdict, `,"note":"round per line or per invoice?"`), nil
		default:
			return reviewAnswer("Rounding is fixed.", match[1], verdict, `,"checks":[{"criterion":"tests pass","met":true,"evidence":"go test ok"},{"criterion":"no float in pricing/","met":true,"evidence":"grep finds none"}]`), nil
		}
	}
}

func TestReworkGoesBackToTheSameWorkerAtMostTwice(t *testing.T) {
	h := newHarness(t)
	h.reviewRouter(core.SuperAgentReviewRework, core.SuperAgentReviewRework, core.SuperAgentReviewRework)
	h.ingest("alice", "github:o/r#1", "fix rounding", true)
	h.settle()

	if got := h.workers.callCount(); got != 3 {
		t.Fatalf("the first turn and two reworks run; worker calls = %d", got)
	}
	calls := h.workers.calls
	if calls[1].conv != calls[0].conv || calls[2].conv != calls[0].conv {
		t.Fatalf("rework continues the ticket's conversation: %+v", calls)
	}
	for _, want := range []string{"REWORK (round 1 of 2)", "- Not met: tests pass (pricing_test fails)"} {
		if !strings.Contains(calls[1].input, want) {
			t.Fatalf("rework input lacks %q:\n%s", want, calls[1].input)
		}
	}
	if strings.Contains(calls[1].input, "no float in pricing/") {
		t.Fatalf("a met criterion is not a gap:\n%s", calls[1].input)
	}
	if !strings.Contains(calls[2].input, "REWORK (round 2 of 2)") {
		t.Fatalf("second rework input:\n%s", calls[2].input)
	}
	if len(h.router.updatePrompts) != 3 || !strings.Contains(h.router.updatePrompts[2], "Rework rounds used: 2 of 2") {
		t.Fatalf("the brain is told how many rounds are used: %v", h.router.updatePrompts)
	}

	ticket := h.store.allTickets()[0]
	if ticket.Status != core.SuperAgentTicketReady || ticket.ReworkRounds != 2 || len(ticket.Reviews) != 3 {
		t.Fatalf("ticket = %+v", ticket)
	}
	third := ticket.Reviews[2]
	if third.Verdict != core.SuperAgentReviewRework || third.Refused == "" || third.Round != 0 {
		t.Fatalf("the third rework is recorded as refused: %+v", third)
	}
	sent := h.replies.sent()
	last := sent[len(sent)-1]
	if !strings.Contains(last, "“Fix rounding” was sent back 2 times and still falls short, so it needs a decision from you.") || !strings.Contains(last, "tests pass") {
		t.Fatalf("the sender is told the ticket needs a decision: %v", sent)
	}
	if record := readTicketRecord(t, h.ticketFolder(ticket)); len(record.Reviews) != 3 || record.ReworkRounds != 2 {
		t.Fatalf("ticket.json = %+v", record)
	}
	tickets, _ := h.store.ListSuperAgentTickets(context.Background(), h.agent.ID, 10)
	view := h.service.routerView(context.Background(), h.agent, core.SuperAgentTrigger{ThreadKey: "github:o/r#1"}, tickets)
	if !strings.Contains(view.Tickets[0].Review, "rework refused") {
		t.Fatalf("the brain sees the refusal: %+v", view.Tickets[0])
	}
}

func TestAcceptedReviewClosesTheTicketAsReady(t *testing.T) {
	h := newHarness(t)
	h.reviewRouter(core.SuperAgentReviewAccepted)
	h.ingest("alice", "github:o/r#1", "fix rounding", true)
	h.settle()
	ticket := h.store.allTickets()[0]
	if ticket.Status != core.SuperAgentTicketReady || len(ticket.Reviews) != 1 || ticket.Reviews[0].Verdict != core.SuperAgentReviewAccepted || len(ticket.Reviews[0].Checks) != 2 {
		t.Fatalf("ticket = %+v", ticket)
	}
	if h.workers.callCount() != 1 {
		t.Fatal("an accepted ticket does not run again")
	}
	if sent := h.replies.sent(); sent[len(sent)-1] != "Rounding is fixed." {
		t.Fatalf("replies = %v", sent)
	}
}

func TestNeedsUserTellsTheSender(t *testing.T) {
	h := newHarness(t)
	h.reviewRouter(core.SuperAgentReviewNeedsUser)
	h.ingest("alice", "github:o/r#1", "fix rounding", true)
	h.settle()
	sent := h.replies.sent()
	if sent[len(sent)-1] != "“Fix rounding” needs a decision from you: round per line or per invoice?" {
		t.Fatalf("replies = %v", sent)
	}
	ticket := h.store.allTickets()[0]
	if len(ticket.Reviews) != 1 || ticket.Reviews[0].Verdict != core.SuperAgentReviewNeedsUser || ticket.Status != core.SuperAgentTicketReady {
		t.Fatalf("ticket = %+v", ticket)
	}
}

func TestReviewNamesOnlyAFinishedTicketOnTheEventsThread(t *testing.T) {
	h := newHarness(t)
	h.ingest("alice", "github:o/r#1", "fix rounding", true)
	h.settle()
	ticket := h.store.allTickets()[0]

	h.router.answer = func(core.InvocationRequest) (string, error) {
		return reviewAnswer("", ticket.ID, "accepted", ""), nil
	}
	h.ingest("alice", "github:o/r#2", "looks good to me", true)
	h.settle()
	if trigger := h.disposition(1); !strings.Contains(trigger.Reason, "is not on this event's thread") {
		t.Fatalf("a review from another thread is refused: %+v", trigger)
	}

	running := ticket
	running.Status = core.SuperAgentTicketRunning
	running.ID = "tkt_running"
	if err := h.store.UpsertSuperAgentTicket(context.Background(), running); err != nil {
		t.Fatal(err)
	}
	decision, _ := parseRouterDecision(reviewAnswer("", "tkt_running", "accepted", ""))
	trigger := core.SuperAgentTrigger{ThreadKey: "github:o/r#1"}
	if err := h.service.preflight(context.Background(), h.agent, trigger, decision); err == nil || !strings.Contains(err.Error(), "is running") {
		t.Fatalf("a running ticket cannot be reviewed: %v", err)
	}
	twice, _ := parseRouterDecision("<agentmux-action>[" +
		`{"action":"review","ticket_id":"` + ticket.ID + `","verdict":"accepted"},` +
		`{"action":"review","ticket_id":"` + ticket.ID + `","verdict":"needs_user","note":"x"}]</agentmux-action>`)
	if err := h.service.preflight(context.Background(), h.agent, trigger, twice); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("one review per ticket per answer: %v", err)
	}
}

func TestTicketUpdateWithoutCriteriaAsksForNoReview(t *testing.T) {
	h := newHarness(t)
	h.ingest("alice", "github:o/r#1", "fix it", true)
	h.settle()
	if strings.Contains(h.router.updatePrompts[0], "review action") {
		t.Fatalf("a ticket without criteria is reported as before:\n%s", h.router.updatePrompts[0])
	}
}
