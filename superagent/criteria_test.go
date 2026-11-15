package superagent

import (
	"strings"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
)

func acceptWithCriteria(criteria string) string {
	return "on it\n<agentmux-action>{\"action\":\"accept\",\"worker_id\":\"local\",\"title\":\"Fix rounding\",\"instructions\":\"Use decimals.\"" + criteria + "}</agentmux-action>"
}

func TestAcceptParsesCriteria(t *testing.T) {
	decision, err := parseRouterDecision(acceptWithCriteria(`,"done_when":["tests pass","no float in pricing/"],"expect_minutes":25`))
	if err != nil {
		t.Fatal(err)
	}
	action := decision.Actions[0]
	if len(action.DoneWhen) != 2 || action.DoneWhen[1] != "no float in pricing/" || action.ExpectMinutes != 25 {
		t.Fatalf("action = %+v", action)
	}
	if _, err := parseRouterDecision(acceptWithCriteria(`,"expect_minutes":"soon"`)); err == nil {
		t.Fatal("an expected time that is not a number must be refused")
	}
}

func TestCheckCriteriaRefusesMalformedValues(t *testing.T) {
	seven := []string{"a", "b", "c", "d", "e", "f", "g"}
	for name, test := range map[string]struct {
		doneWhen []string
		expect   int
		want     string
	}{
		"too many":        {seven, 0, "at most 6"},
		"empty":           {[]string{"tests pass", "  "}, 0, "criterion 2 is empty"},
		"too long":        {[]string{strings.Repeat("x", maxCriterionRunes+1)}, 0, "longer than"},
		"several lines":   {[]string{"tests pass\nand lint"}, 0, "one line each"},
		"repeated":        {[]string{"Tests pass", "tests pass"}, 0, "repeats"},
		"negative":        {nil, -5, "expect_minutes"},
		"too long a turn": {nil, maxExpectMinutes + 1, "expect_minutes"},
	} {
		err := checkCriteria(test.doneWhen, test.expect)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: err = %v, want %q", name, err, test.want)
		}
	}
	if err := checkCriteria([]string{"a", "b", "c", "d", "e", "f"}, maxExpectMinutes); err != nil {
		t.Fatalf("six criteria and the longest turn are allowed: %v", err)
	}
}

func TestPreflightRefusesBadCriteriaAndStartsNothing(t *testing.T) {
	h := newHarness(t)
	h.router.answer = func(core.InvocationRequest) (string, error) {
		return acceptWithCriteria(`,"done_when":["a","b","c","d","e","f","g"]`), nil
	}
	h.ingest("alice", "github:o/r#1", "fix rounding", true)
	h.settle()
	if len(h.store.allTickets()) != 0 || len(h.replies.sent()) != 0 {
		t.Fatal("refused criteria must start nothing and say nothing")
	}
	if trigger := h.disposition(0); !strings.Contains(trigger.Reason, "at most 6") {
		t.Fatalf("the refusal must be on the record: %+v", trigger)
	}
}

func TestCriteriaReachTheTicketTheWorkerAndTheBrain(t *testing.T) {
	h := newHarness(t)
	h.router.answer = func(core.InvocationRequest) (string, error) {
		return acceptWithCriteria(`,"done_when":[" tests pass ","no float in pricing/"],"expect_minutes":25`), nil
	}
	h.ingest("alice", "github:o/r#1", "fix rounding", true)
	h.settle()
	tickets := h.store.allTickets()
	if len(tickets) != 1 {
		t.Fatalf("tickets = %+v", tickets)
	}
	ticket := tickets[0]
	if len(ticket.DoneWhen) != 2 || ticket.DoneWhen[0] != "tests pass" || ticket.ExpectMinutes != 25 || ticket.Instructions != "Use decimals." {
		t.Fatalf("ticket = %+v", ticket)
	}
	input := h.workers.calls[0].input
	for _, want := range []string{criteriaEvidenceAsk, "1. tests pass\n2. no float in pricing/", "about 25 minutes"} {
		if !strings.Contains(input, want) {
			t.Fatalf("worker input lacks %q:\n%s", want, input)
		}
	}
	record := readTicketRecord(t, h.ticketFolder(ticket))
	if len(record.DoneWhen) != 2 || record.ExpectMinutes != 25 {
		t.Fatalf("ticket.json = %+v", record)
	}
	h.ingest("alice", "github:o/r#1", "how is it going?", true)
	h.settle()
	prompt := h.router.prompts[len(h.router.prompts)-1]
	for _, want := range []string{`"done_when": [`, `"tests pass"`, `"expect_minutes": 25`} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("the brain's view lacks %q:\n%s", want, prompt)
		}
	}
}

func TestAcceptWithoutCriteriaIsUnchanged(t *testing.T) {
	h := newHarness(t)
	h.ingest("alice", "github:o/r#1", "fix it", true)
	h.settle()
	ticket := h.store.allTickets()[0]
	if ticket.DoneWhen != nil || ticket.ExpectMinutes != 0 {
		t.Fatalf("ticket = %+v", ticket)
	}
	input := h.workers.calls[0].input
	if strings.Contains(input, "DONE WHEN") || strings.Contains(input, "expects this to take") {
		t.Fatalf("a ticket without criteria must not mention them:\n%s", input)
	}
	h.ingest("alice", "github:o/r#1", "status?", true)
	h.settle()
	if prompt := h.router.prompts[len(h.router.prompts)-1]; strings.Contains(prompt, "done_when\": [") || strings.Contains(prompt, "expect_minutes\": ") {
		t.Fatalf("the brain's view must not show criteria a ticket lacks:\n%s", prompt)
	}
}
