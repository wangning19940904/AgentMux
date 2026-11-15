package superagent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
)

func TestDefaultCostTier(t *testing.T) {
	for _, tc := range []struct{ runtime, model, want string }{
		{"claudecode", "", core.CostTierHigh},
		{"claudecode", "claude-haiku-4-5", core.CostTierLow},
		{"claudecode", "claude-sonnet-5", core.CostTierMedium},
		{"codex", "", core.CostTierMedium},
		{"codex", "gpt-6-mini", core.CostTierLow},
		{"codex", "gpt-6-pro", core.CostTierHigh},
		{"gemini", "gemini-3-flash", core.CostTierLow},
		{"dsh", "deepseek-v4", core.CostTierMedium},
	} {
		if got := defaultCostTier(tc.runtime, tc.model); got != tc.want {
			t.Errorf("%s/%s = %s, want %s", tc.runtime, tc.model, got, tc.want)
		}
	}
}

const planAnswer = "Here is a plan.\n\n<agentmux-action>\n" +
	`{"action":"plan","title":"Fix and document","text":"Two steps.","tickets":[` +
	`{"ref":"a","worker_id":"auto:dsh","title":"Fix it","instructions":"fix"},` +
	`{"ref":"b","worker_id":"local","title":"Document it","instructions":"docs","depends_on":["a"]}]}` +
	"\n</agentmux-action>"

// withPlanApproval turns plan approval on for the harness's agent.
func (h *harness) withPlanApproval(mode string) {
	h.t.Helper()
	h.enableAutoWorkers(h.t.TempDir())
	h.store.mu.Lock()
	h.agent.PlanApproval = mode
	h.store.agents[h.agent.ID] = h.agent
	h.store.mu.Unlock()
}

func TestPlanApprovalOffStartsAtOnceAndShowsTiers(t *testing.T) {
	h := newHarness(t)
	h.enableAutoWorkers(h.t.TempDir())
	h.router.answer = func(core.InvocationRequest) (string, error) { return acceptAnswer("auto:dsh"), nil }
	h.ingest("alice", "github:o/r#1", "fix it", true)
	h.settle()
	if tickets := h.store.allTickets(); len(tickets) != 1 {
		t.Fatalf("tickets = %+v", tickets)
	}
	prompt := h.router.prompts[0]
	if !strings.Contains(prompt, `"cost_tier": "medium"`) || strings.Contains(prompt, "PLANS:") {
		t.Fatalf("the roster shows tiers, and plans are explained only when approval is on:\n%s", prompt)
	}
}

func TestPlanApprovalProposesApprovesAndRemembers(t *testing.T) {
	h := newHarness(t)
	h.withPlanApproval(core.PlanApprovalAboveLow)
	answers := []string{acceptAnswer("auto:dsh"), planAnswer}
	h.router.answer = func(core.InvocationRequest) (string, error) {
		answer := answers[0]
		if len(answers) > 1 {
			answers = answers[1:]
		}
		return answer, nil
	}
	h.ingest("alice", "github:o/r#1", "fix it", true)
	h.settle()
	*h.clock = h.clock.Add(retryDelay + 1)
	h.settle()
	if len(h.router.prompts) < 2 || !strings.Contains(h.router.prompts[1], `needs the requester's approval before medium-tier work starts: propose it with a "plan" action`) {
		t.Fatalf("the brain must hear that the accept needs a plan:\n%v", h.router.prompts)
	}
	if !strings.Contains(h.router.prompts[0], "PLANS: This agent needs the requester's approval before medium- or high-tier work starts") {
		t.Fatalf("prompt:\n%s", h.router.prompts[0])
	}
	if tickets := h.store.allTickets(); len(tickets) != 0 {
		t.Fatalf("nothing starts before approval: %+v", tickets)
	}
	plans, err := h.service.Plans(context.Background(), h.agent.ID)
	if err != nil || len(plans) != 1 {
		t.Fatalf("plans = %+v, %v", plans, err)
	}
	plan := plans[0]
	if plan.Status != core.SuperAgentPlanPending || plan.Requester != "alice" || plan.Tier != core.CostTierMedium || len(plan.Tickets) != 2 || plan.Tickets[0].Where != "this machine" || plan.Trigger != nil {
		t.Fatalf("plan = %+v", plan)
	}
	sent := strings.Join(h.replies.sent(), "\n")
	if !strings.Contains(sent, "Proposed plan “Fix and document” (medium tier)") || !strings.Contains(sent, "2. Document it: local on this machine, medium tier, after a") {
		t.Fatalf("the thread must read the plan:\n%s", sent)
	}

	approved, err := h.service.ApprovePlan(context.Background(), h.agent.ID, plan.ID, true, "Ops")
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != core.SuperAgentPlanApproved || len(approved.TicketIDs) != 2 || approved.DecidedBy != "Ops" {
		t.Fatalf("approved = %+v", approved)
	}
	second, _ := h.store.GetSuperAgentTicket(context.Background(), approved.TicketIDs[1])
	if second == nil || len(second.DependsOn) != 1 || second.DependsOn[0] != approved.TicketIDs[0] || second.Actor != "alice" {
		t.Fatalf("refs must resolve to the started tickets: %+v", second)
	}
	if _, err := h.service.ApprovePlan(context.Background(), h.agent.ID, plan.ID, false, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("a decided plan cannot be approved again: %v", err)
	}
	trust, _ := h.service.Trust(context.Background(), h.agent.ID)
	if len(trust) != 1 || trust[0].Requester != "alice" || trust[0].Tier != core.CostTierMedium || trust[0].ApprovedBy != "Ops" {
		t.Fatalf("trust = %+v", trust)
	}
	h.settle()

	// Remembered: alice's next medium-tier work starts at once, bob's does not.
	h.router.answer = func(core.InvocationRequest) (string, error) { return acceptAnswer("auto:dsh"), nil }
	h.ingest("alice", "github:o/r#2", "again", true)
	h.settle()
	if tickets := h.store.allTickets(); len(tickets) != 3 {
		t.Fatalf("tickets = %d", len(tickets))
	}
	agent := h.service.withWorkerRuntimes(context.Background(), h.agent)
	err = h.service.preflight(context.Background(), agent, core.SuperAgentTrigger{Actor: "bob", ThreadKey: "t"}, routerDecision{Actions: []routerAction{{Action: actionAccept, WorkerID: "auto:dsh"}}})
	if err == nil || !strings.Contains(err.Error(), "medium-tier") {
		t.Fatalf("err = %v", err)
	}
	// Revoked, alice needs a plan again.
	if err := h.service.RevokeTrust(context.Background(), h.agent.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := h.service.RevokeTrust(context.Background(), h.agent.ID, "alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	err = h.service.preflight(context.Background(), agent, core.SuperAgentTrigger{Actor: "alice", ThreadKey: "t"}, routerDecision{Actions: []routerAction{{Action: actionAccept, WorkerID: "auto:dsh"}}})
	if err == nil {
		t.Fatal("a revoked approval must not start work")
	}
}

func TestTrustedPlanStartsWithoutACard(t *testing.T) {
	h := newHarness(t)
	h.withPlanApproval(core.PlanApprovalAlways)
	if err := h.service.remember(context.Background(), h.agent.ID, "alice", "Alice", core.CostTierHigh, "Ops"); err != nil {
		t.Fatal(err)
	}
	h.router.answer = func(core.InvocationRequest) (string, error) { return planAnswer, nil }
	h.ingest("alice", "github:o/r#1", "fix it", true)
	h.settle()
	if tickets := h.store.allTickets(); len(tickets) != 2 {
		t.Fatalf("tickets = %+v", tickets)
	}
	plans, _ := h.service.Plans(context.Background(), h.agent.ID)
	if len(plans) != 1 || plans[0].Status != core.SuperAgentPlanApproved || plans[0].DecidedBy != trustDecidedBy {
		t.Fatalf("plans = %+v", plans)
	}
	if sent := strings.Join(h.replies.sent(), "\n"); !strings.Contains(sent, "Started plan “Fix and document” (2 tickets)") {
		t.Fatalf("sent:\n%s", sent)
	}
}

func TestDecliningAPlanTellsTheBrain(t *testing.T) {
	h := newHarness(t)
	h.withPlanApproval(core.PlanApprovalAlways)
	answers := []string{planAnswer, "Understood, nothing started."}
	h.router.answer = func(core.InvocationRequest) (string, error) {
		answer := answers[0]
		if len(answers) > 1 {
			answers = answers[1:]
		}
		return answer, nil
	}
	h.ingest("alice", "github:o/r#1", "fix it", true)
	h.settle()
	plans, _ := h.service.Plans(context.Background(), h.agent.ID)
	if len(plans) != 1 {
		t.Fatalf("plans = %+v", plans)
	}
	if !strings.Contains(h.router.prompts[len(h.router.prompts)-1], "PLANS") {
		t.Fatal("prompt must explain plans")
	}
	declined, err := h.service.DeclinePlan(context.Background(), h.agent.ID, plans[0].ID, "too expensive", "Ops")
	if err != nil || declined.Status != core.SuperAgentPlanDeclined {
		t.Fatalf("declined = %+v, %v", declined, err)
	}
	h.settle()
	last := h.router.prompts[len(h.router.prompts)-1]
	if !strings.Contains(last, `"kind": "plan"`) || !strings.Contains(last, "too expensive") || !strings.Contains(last, "nothing was started") {
		t.Fatalf("the brain must hear the decline:\n%s", last)
	}
	if len(h.store.allTickets()) != 0 {
		t.Fatal("a declined plan starts nothing")
	}
	if sent := h.replies.sent(); sent[len(sent)-1] != "Understood, nothing started." {
		t.Fatalf("sent = %v", sent)
	}
}

func TestSaveChecksPlanApprovalAndCostTiers(t *testing.T) {
	h := newHarness(t)
	agent := h.agent
	agent.PlanApproval = "sometimes"
	if _, err := h.service.Save(context.Background(), agent); err == nil || !strings.Contains(err.Error(), "plan_approval") {
		t.Fatalf("err = %v", err)
	}
	agent.PlanApproval = "Above_Medium"
	agent.Workers[0].CostTier = "huge"
	if _, err := h.service.Save(context.Background(), agent); err == nil || !strings.Contains(err.Error(), "cost tier") {
		t.Fatalf("err = %v", err)
	}
	agent.Workers[0].CostTier = "LOW"
	saved, err := h.service.Save(context.Background(), agent)
	if err != nil {
		t.Fatal(err)
	}
	if saved.PlanApproval != core.PlanApprovalAboveMedium || saved.Workers[0].CostTier != core.CostTierLow {
		t.Fatalf("saved = %+v", saved)
	}
	tiers := h.service.rosterTiers(context.Background(), saved, nil)
	if tiers["local"] != core.CostTierLow {
		t.Fatalf("tiers = %v", tiers)
	}
	if err := validateDecision(routerDecision{Actions: []routerAction{{Action: actionPlan, Title: "x"}}}); err == nil {
		t.Fatal("a plan without tickets must be refused")
	}
}
