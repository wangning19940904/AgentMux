package superagent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func followUpAnswer(reply, when, note string) string {
	return fmt.Sprintf("%s\n\n<agentmux-action>\n{\"action\":\"follow_up\",%s,\"note\":%q,\"reason\":\"waiting\"}\n</agentmux-action>", reply, when, note)
}

func isTimerEvent(prompt string) bool {
	return strings.Contains(prompt, `"kind": "`+core.SuperAgentTriggerTimer+`"`)
}

// followUpRouter sets a follow-up on every event and answers timer events
// with onTimer.
func (h *harness) followUpRouter(when, note, onTimer string) {
	h.router.answer = func(req core.InvocationRequest) (string, error) {
		if isTimerEvent(req.Input) {
			return onTimer, nil
		}
		return followUpAnswer("I will check back", when, note), nil
	}
}

func (h *harness) timers(status string) []core.SuperAgentTimer {
	return h.store.timersWhere(func(timer core.SuperAgentTimer) bool { return timer.Status == status })
}

func (h *harness) timerEvents() []core.SuperAgentTrigger {
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	var out []core.SuperAgentTrigger
	for _, trigger := range h.store.triggers {
		if trigger.Kind == core.SuperAgentTriggerTimer {
			out = append(out, trigger)
		}
	}
	return out
}

func TestFollowUpWakesTheRouterOnItsThread(t *testing.T) {
	h := newHarness(t)
	h.followUpRouter(`"in":"18h"`, "Check whether Li approved the migration.", "Li approved it.")
	h.ingest("alice", "github:o/r#1", "ask Li about the migration", true)
	h.settle()

	active := h.timers(core.SuperAgentTimerActive)
	if len(active) != 1 {
		t.Fatalf("timers = %+v", h.store.timers)
	}
	timer := active[0]
	if timer.ThreadKey != "github:o/r#1" || timer.SourceID != "gh" || !timer.DueAt.Equal(h.clock.Add(18*time.Hour)) || timer.Kind != core.SuperAgentTimerFollowUp {
		t.Fatalf("timer = %+v", timer)
	}
	if event := h.disposition(0); event.Disposition != core.SuperAgentDispositionAnswered || !strings.Contains(event.Reason, "set follow-up "+timer.ID) || timer.SetBy != event.ID {
		t.Fatalf("event = %+v", event)
	}
	if !strings.Contains(h.router.prompts[0], "NOW: 2026-11-03T00:00:00Z") {
		t.Fatalf("the router was not told the time:\n%s", h.router.prompts[0])
	}

	h.advance(17 * time.Hour)
	h.settle()
	if len(h.timerEvents()) != 0 {
		t.Fatal("the follow-up fired early")
	}
	// An event before it is due shows the router what is still set.
	h.ingest("alice", "github:o/r#1", "any news?", true)
	h.settle()
	if prompt := h.router.prompts[1]; !strings.Contains(prompt, "TIMERS ON THIS THREAD") || !strings.Contains(prompt, timer.ID) {
		t.Fatalf("the router was not shown its timer:\n%s", prompt)
	}

	h.advance(time.Hour)
	h.settle()
	events := h.timerEvents()
	if len(events) != 1 || events[0].ThreadKey != "github:o/r#1" || events[0].Disposition != core.SuperAgentDispositionAnswered {
		t.Fatalf("timer events = %+v", events)
	}
	prompt := h.router.prompts[len(h.router.prompts)-1]
	if !isTimerEvent(prompt) || !strings.Contains(prompt, "Check whether Li approved the migration.") || strings.Contains(prompt, "late)") {
		t.Fatalf("timer prompt:\n%s", prompt)
	}
	if conversations := h.router.conversations; conversations[len(conversations)-1] != conversations[0] {
		t.Fatalf("the follow-up ran on another conversation: %v", conversations)
	}
	if sent := h.replies.sentTo(`{"repo":"o/r","number":1}`); !slices.Contains(sent, "Li approved it.") {
		t.Fatalf("replies = %q", sent)
	}
	done, _ := h.store.GetSuperAgentTimer(context.Background(), timer.ID)
	if done.Status != core.SuperAgentTimerDone || done.FiredAt.IsZero() {
		t.Fatalf("timer after firing = %+v", done)
	}
}

func TestTimerFiresOnceWhenTheDaemonStopsBetweenItsWrites(t *testing.T) {
	h := newHarness(t)
	h.followUpRouter(`"in":"1h"`, "look again", "")
	h.ingest("alice", "github:o/r#1", "work", true)
	h.settle()
	timer := h.timers(core.SuperAgentTimerActive)[0]

	h.advance(time.Hour)
	ctx := context.Background()
	if fired, err := h.service.fireTimer(ctx, timer.ID, h.service.now()); err != nil || !fired {
		t.Fatalf("fired=%v err=%v", fired, err)
	}
	// The Trigger was stored, but the daemon stopped before the timer was
	// marked done.
	_ = h.store.UpsertSuperAgentTimer(ctx, timer)
	h.settle()
	if events := h.timerEvents(); len(events) != 1 {
		t.Fatalf("timer events = %+v", events)
	}
	if len(h.timers(core.SuperAgentTimerDone)) != 1 {
		t.Fatalf("timers = %+v", h.store.timers)
	}
}

func TestADisabledAgentsTimersWaitAndThenSayTheyAreLate(t *testing.T) {
	h := newHarness(t)
	h.followUpRouter(`"in":"1h"`, "look again", "")
	h.ingest("alice", "github:o/r#1", "work", true)
	h.settle()

	disabled := h.agent
	disabled.Enabled = false
	_ = h.store.UpsertSuperAgent(context.Background(), disabled)
	h.advance(4 * time.Hour)
	h.settle()
	if len(h.timerEvents()) != 0 {
		t.Fatal("a disabled agent's timer fired")
	}

	_ = h.store.UpsertSuperAgent(context.Background(), h.agent)
	h.settle()
	events := h.timerEvents()
	if len(events) != 1 || !strings.Contains(events[0].Text, "3h00m late") {
		t.Fatalf("timer events = %+v", events)
	}
}

func TestFollowUpTimes(t *testing.T) {
	now := time.Date(2026, 11, 3, 9, 0, 0, 0, time.FixedZone("CST", 8*3600))
	for _, tc := range []struct {
		in, at string
		want   time.Duration
	}{
		{in: "30m", want: 30 * time.Minute},
		{in: "18h", want: 18 * time.Hour},
		{in: "3d", want: 72 * time.Hour},
		{in: "1d12h", want: 36 * time.Hour},
		{in: " 2 H ", want: 2 * time.Hour},
		{at: "2026-11-04T09:00:00+08:00", want: 24 * time.Hour},
		{at: "2026-11-03T02:00:00Z", want: time.Hour},
		{at: "2026-11-03T10:30", want: 90 * time.Minute},
	} {
		due, err := followUpDue(routerAction{In: tc.in, At: tc.at}, now)
		if err != nil || !due.Equal(now.Add(tc.want)) {
			t.Errorf("in=%q at=%q: due=%v err=%v, want %v", tc.in, tc.at, due, err, now.Add(tc.want))
		}
	}
	for _, tc := range []routerAction{
		{},
		{In: "1h", At: "2026-11-04T09:00:00+08:00"},
		{In: "10s"},
		{In: "soon"},
		{In: "91d"},
		{At: "2026-11-03T08:00:00+08:00"},
		{At: "tomorrow"},
	} {
		if due, err := followUpDue(tc, now); err == nil {
			t.Errorf("in=%q at=%q was accepted as %v", tc.In, tc.At, due)
		}
	}
	if err := validateDecision(routerDecision{Actions: []routerAction{{Action: actionFollowUp, In: "1h"}}}); err == nil {
		t.Error("a follow-up without a note was accepted")
	}
}

func TestFollowUpLimitsAreCheckedBeforeAnythingIsCarriedOut(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	trigger := core.SuperAgentTrigger{ID: "sat_1", ThreadKey: "github:o/r#1", SourceID: "gh"}
	followUp := routerAction{Action: actionFollowUp, In: "1h", Note: "look"}
	var first core.SuperAgentTimer
	for index := range maxThreadFollowUps {
		timer, err := h.service.setFollowUp(ctx, h.agent, trigger, followUp, "")
		if err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			first = timer
		}
	}
	if err := h.service.preflight(ctx, h.agent, trigger, routerDecision{Actions: []routerAction{followUp}}); err == nil || !strings.Contains(err.Error(), "most allowed") {
		t.Fatalf("a sixth follow-up on one thread: %v", err)
	}
	if _, err := h.service.setFollowUp(ctx, h.agent, trigger, followUp, ""); err == nil {
		t.Fatal("a sixth follow-up was stored")
	}
	// Cancelling one in the same block makes room.
	swap := routerDecision{Actions: []routerAction{{Action: actionCancelTimer, TimerID: first.ID}, followUp}}
	if err := h.service.preflight(ctx, h.agent, trigger, swap); err != nil {
		t.Fatalf("cancel then set: %v", err)
	}
	// Another thread has room of its own, but may not cancel this thread's.
	elsewhere := core.SuperAgentTrigger{ID: "sat_2", ThreadKey: "github:o/r#2", SourceID: "gh"}
	if err := h.service.preflight(ctx, h.agent, elsewhere, routerDecision{Actions: []routerAction{followUp}}); err != nil {
		t.Fatalf("a follow-up on another thread: %v", err)
	}
	theirs := routerDecision{Actions: []routerAction{{Action: actionCancelTimer, TimerID: first.ID}}}
	if err := h.service.preflight(ctx, h.agent, elsewhere, theirs); err == nil {
		t.Fatal("a thread cancelled another thread's timer")
	}
	if err := h.service.preflight(ctx, h.agent, trigger, routerDecision{Actions: []routerAction{{Action: actionFollowUp, In: "10s", Note: "look"}}}); err == nil {
		t.Fatal("a follow-up ten seconds away passed")
	}
}

func TestStoppingATicketCancelsItsFollowUps(t *testing.T) {
	h := newHarness(t)
	h.router.answer = func(core.InvocationRequest) (string, error) {
		return `on it

<agentmux-action>
[{"action":"accept","ref":"a","worker_id":"local","title":"Fix it","instructions":"look closely"},
 {"action":"follow_up","in":"2d","note":"Is the fix merged?","ticket_id":"a"},
 {"action":"follow_up","in":"3d","note":"Tell Alice the release date."}]
</agentmux-action>`, nil
	}
	h.ingest("alice", "github:o/r#1", "please fix the build", true)
	h.settle()
	ticket := h.store.allTickets()[0]
	active := h.timers(core.SuperAgentTimerActive)
	if len(active) != 2 || active[0].TicketID != ticket.ID || active[1].TicketID != "" {
		t.Fatalf("timers = %+v", active)
	}
	if err := h.service.StopTicket(context.Background(), ticket.ID); err != nil {
		t.Fatal(err)
	}
	if cancelled := h.timers(core.SuperAgentTimerCancelled); len(cancelled) != 1 || cancelled[0].ID != active[0].ID {
		t.Fatalf("cancelled = %+v", cancelled)
	}
	if left := h.timers(core.SuperAgentTimerActive); len(left) != 1 || left[0].ID != active[1].ID {
		t.Fatalf("still set = %+v", left)
	}
}

func TestRouterCancelsATimerWhenTheAnswerCameFirst(t *testing.T) {
	h := newHarness(t)
	h.followUpRouter(`"at":"2026-11-04T09:00:00Z"`, "Ask Li again.", "")
	h.ingest("alice", "github:o/r#1", "ask Li", true)
	h.settle()
	timer := h.timers(core.SuperAgentTimerActive)[0]

	h.router.answer = func(core.InvocationRequest) (string, error) {
		return fmt.Sprintf("thanks\n\n<agentmux-action>\n{\"action\":\"cancel_timer\",\"timer_id\":%q}\n</agentmux-action>", timer.ID), nil
	}
	h.ingest("alice", "github:o/r#1", "Li said yes", true)
	h.settle()
	if cancelled := h.timers(core.SuperAgentTimerCancelled); len(cancelled) != 1 {
		t.Fatalf("timers = %+v", h.store.timers)
	}
	if event := h.disposition(1); !strings.Contains(event.Reason, "cancelled follow-up "+timer.ID) {
		t.Fatalf("event = %+v", event)
	}
	h.advance(48 * time.Hour)
	h.settle()
	if len(h.timerEvents()) != 0 {
		t.Fatal("a cancelled timer fired")
	}
	// Cancelling it again is not a mistake the router has to answer for.
	h.ingest("alice", "github:o/r#1", "and thanks", true)
	h.settle()
	if event := h.disposition(2); event.Disposition == core.SuperAgentDispositionFailed {
		t.Fatalf("event = %+v", event)
	}
}

func TestAFailedTimerEventReachesNoWorkerAndNoPerson(t *testing.T) {
	h := newHarness(t)
	h.router.answer = func(req core.InvocationRequest) (string, error) {
		if isTimerEvent(req.Input) {
			return "", errors.New("model unavailable")
		}
		return `on it

<agentmux-action>
[{"action":"accept","worker_id":"local","title":"Fix it","instructions":"look closely"},
 {"action":"follow_up","in":"1h","note":"Is it merged?"}]
</agentmux-action>`, nil
	}
	h.ingest("alice", "github:o/r#1", "please fix the build", true)
	h.settle()
	sent, calls := len(h.replies.sent()), h.workers.callCount()

	h.advance(time.Hour)
	h.exhaustRetries()
	events := h.timerEvents()
	if len(events) != 1 || events[0].Disposition != core.SuperAgentDispositionFailed {
		t.Fatalf("timer events = %+v", events)
	}
	if len(h.replies.sent()) != sent || h.workers.callCount() != calls {
		t.Fatalf("a failed follow-up reached someone: replies %q, worker calls %d", h.replies.sent(), h.workers.callCount())
	}
}

func TestTheDailyBudgetHoldsTimersBack(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	now := h.clock.UTC()
	for index := range maxTimerEventsPerDay {
		_ = h.store.UpsertSuperAgentTimer(ctx, core.SuperAgentTimer{
			ID: fmt.Sprintf("stm_old_%d", index), SuperAgentID: h.agent.ID, Kind: core.SuperAgentTimerFollowUp,
			Status: core.SuperAgentTimerDone, ThreadKey: "github:o/r#9", DueAt: now, FiredAt: now, UpdatedAt: now,
		})
	}
	h.followUpRouter(`"in":"1h"`, "look again", "")
	h.ingest("alice", "github:o/r#1", "work", true)
	h.settle()

	h.advance(time.Hour)
	h.settle()
	if len(h.timerEvents()) != 0 {
		t.Fatal("a timer fired past the daily budget")
	}
	activity, err := h.service.Activity(ctx, h.agent.ID)
	if err != nil || !strings.Contains(activity.TimerWarning, "1 more are due") {
		t.Fatalf("warning=%q err=%v", activity.TimerWarning, err)
	}

	h.advance(timerBudgetWindow)
	h.settle()
	if len(h.timerEvents()) != 1 {
		t.Fatalf("timer events = %+v", h.timerEvents())
	}
}

func TestActivityListsTimersAndTheConsoleCancelsThem(t *testing.T) {
	h := newHarness(t)
	h.followUpRouter(`"in":"1h"`, "look again", "")
	h.ingest("alice", "github:o/r#1", "work", true)
	h.settle()
	ctx := context.Background()
	activity, err := h.service.Activity(ctx, h.agent.ID)
	if err != nil || len(activity.Timers) != 1 || activity.Timers[0].Status != core.SuperAgentTimerActive || activity.TimerWarning != "" {
		t.Fatalf("activity = %+v err=%v", activity, err)
	}
	id := activity.Timers[0].ID
	if err := h.service.CancelTimer(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := h.service.CancelTimer(ctx, id); !errors.Is(err, ErrConflict) {
		t.Fatalf("cancelling twice: %v", err)
	}
	if err := h.service.CancelTimer(ctx, "stm_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cancelling a missing timer: %v", err)
	}
	activity, _ = h.service.Activity(ctx, h.agent.ID)
	if len(activity.Timers) != 1 || activity.Timers[0].Status != core.SuperAgentTimerCancelled {
		t.Fatalf("activity timers = %+v", activity.Timers)
	}
}

func TestABrainSetsAFollowUpThatReportsToItsChat(t *testing.T) {
	h := newBrainHarness(t, true)
	ctx := context.Background()
	if brief := h.service.BrainBrief(ctx, "coder", h.msg); !strings.Contains(brief, "NOW: 2026-11-12T00:00:00Z") || !strings.Contains(brief, `"follow_up"`) {
		t.Fatalf("the brief does not explain follow-ups:\n%s", brief)
	}
	h.service.BrainFinished(ctx, "coder", h.msg, followUpAnswer("I will ask again tomorrow", `"in":"1d"`, "Ask Alice whether the login bug is back."))
	active := h.timers(core.SuperAgentTimerActive)
	if len(active) != 1 || active[0].SourceID != "channel:c1" || active[0].SuperAgentID != "coder" {
		t.Fatalf("timers = %+v", h.store.timers)
	}
	if brief := h.service.BrainBrief(ctx, "coder", h.msg); !strings.Contains(brief, "TIMERS ON THIS THREAD") {
		t.Fatalf("the next turn is not shown the timer:\n%s", brief)
	}

	h.router.answer = func(core.InvocationRequest) (string, error) { return "Alice, is the login bug back?", nil }
	h.advance(24 * time.Hour)
	h.settle()
	target := `{"channel_id":"c1","chat_id":"oc_1","message_id":"om_1"}`
	if got := h.replies.sentTo(target); len(got) != 1 || got[0] != "Alice, is the login bug back?" {
		t.Fatalf("follow-up in the chat = %q (all: %q)", got, h.replies.sent())
	}
}
