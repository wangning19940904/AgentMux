package superagent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func (h *harness) setSupervision(mode string) {
	h.t.Helper()
	h.agent.Supervision = mode
	if err := h.store.UpsertSuperAgent(context.Background(), h.agent); err != nil {
		h.t.Fatal(err)
	}
}

// blockWorkers keeps every worker's turn running until the test ends or the
// returned function is called.
func (h *harness) blockWorkers() func() {
	release := make(chan struct{})
	h.workers.run = func(ctx context.Context, _ workerCall) (string, error) {
		select {
		case <-release:
			return "done", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	released := false
	done := func() {
		if !released {
			released = true
			close(release)
		}
	}
	h.t.Cleanup(done)
	return done
}

// waitRunning runs passes until the thread's ticket's worker is running and
// no router turn is still finishing, so the test may move the clock.
func (h *harness) waitRunning() core.SuperAgentTicket {
	h.t.Helper()
	for range 2000 {
		h.service.Pass(context.Background())
		h.service.mu.Lock()
		evaluating := len(h.service.evaluating)
		h.service.mu.Unlock()
		if running, _ := h.store.ListSuperAgentTicketsByStatus(context.Background(), core.SuperAgentTicketRunning); len(running) > 0 && evaluating == 0 && h.workers.callCount() > 0 {
			return running[0]
		}
		time.Sleep(time.Millisecond)
	}
	h.t.Fatal("no ticket started running")
	return core.SuperAgentTicket{}
}

func (h *harness) supervisionEvents() []core.SuperAgentTrigger {
	h.store.mu.Lock()
	defer h.store.mu.Unlock()
	var out []core.SuperAgentTrigger
	for _, trigger := range h.store.triggers {
		if trigger.Kind == core.SuperAgentTriggerSupervision {
			out = append(out, trigger)
		}
	}
	return out
}

func (h *harness) ticket(id string) core.SuperAgentTicket {
	h.t.Helper()
	ticket, err := h.store.GetSuperAgentTicket(context.Background(), id)
	if err != nil || ticket == nil {
		h.t.Fatalf("ticket %s: %v", id, err)
	}
	return *ticket
}

// orphan stores a Ticket marked running that nothing runs, as a daemon that
// lost track of it would leave.
func (h *harness) orphan(id, thread string) {
	h.t.Helper()
	now := h.clock.UTC()
	ticket := core.SuperAgentTicket{
		ID: id, SuperAgentID: h.agent.ID, SourceID: h.source.ID, SourceKind: h.source.Kind, ThreadKey: thread,
		Title: "Ghost " + id, Worker: h.agent.Workers[0], ConversationID: "superagent-" + id,
		Status: core.SuperAgentTicketRunning, Reply: json.RawMessage(`{"repo":"o/r","number":9}`),
		CreatedAt: now, StartedAt: now, UpdatedAt: now,
	}
	if err := h.store.UpsertSuperAgentTicket(context.Background(), ticket); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) supervise() {
	h.t.Helper()
	if err := h.service.supervise(context.Background()); err != nil {
		h.t.Fatal(err)
	}
}

func TestStalledTicketWakesTheBrainOncePerStall(t *testing.T) {
	h := newHarness(t)
	h.setSupervision(core.SuperAgentSupervisionAct)
	release := h.blockWorkers()
	h.router.answer = func(core.InvocationRequest) (string, error) {
		return acceptWithCriteria(`,"expect_minutes":40`), nil
	}
	h.ingest("alice", "github:o/r#1", "fix rounding", true)
	ticket := h.waitRunning()

	// expect_minutes 40 allows 60 minutes of silence.
	h.advance(59 * time.Minute)
	h.supervise()
	if len(h.ticket(ticket.ID).Signals) != 0 || len(h.supervisionEvents()) != 0 {
		t.Fatal("work within its limit is not stalled")
	}
	h.advance(2 * time.Minute)
	h.supervise()
	signals := h.ticket(ticket.ID).Signals
	if len(signals) != 1 || signals[0].Kind != core.SuperAgentSignalStalled || signals[0].WokeAt.IsZero() || !signals[0].ClearedAt.IsZero() {
		t.Fatalf("signals = %+v", signals)
	}
	events := h.supervisionEvents()
	if len(events) != 1 || events[0].ThreadKey != "github:o/r#1" || events[0].TicketID != ticket.ID {
		t.Fatalf("events = %+v", events)
	}
	for _, want := range []string{ticket.ID, "is stalled", "longer than the 1h limit", "you expected about 40m", "no model looked"} {
		if !strings.Contains(events[0].Text, want) {
			t.Fatalf("event lacks %q:\n%s", want, events[0].Text)
		}
	}
	if !h.ticket(ticket.ID).UpdatedAt.Equal(ticket.UpdatedAt) {
		t.Fatal("recording a signal must not move the ticket's updated time")
	}

	// The same stall is told once, however long it lasts.
	h.advance(30 * time.Minute)
	h.supervise()
	h.supervise()
	if got := len(h.supervisionEvents()); got != 1 {
		t.Fatalf("events = %d", got)
	}

	// Progress ends the stall; a later silence is a new one.
	h.service.progressRecorder(ticket)(core.InvocationStreamEvent{ToolName: "Bash", ToolCallID: "call-1", ToolInput: "go test ./..."})
	h.supervise()
	if signals := h.ticket(ticket.ID).Signals; signals[0].ClearedAt.IsZero() {
		t.Fatalf("progress must end the stall: %+v", signals)
	}
	h.advance(61 * time.Minute)
	h.supervise()
	signals = h.ticket(ticket.ID).Signals
	if len(signals) != 2 || !signals[1].ClearedAt.IsZero() || !strings.Contains(signals[1].Detail, "its last step: Bash: go test ./...") {
		t.Fatalf("signals = %+v", signals)
	}
	if got := len(h.supervisionEvents()); got != 2 {
		t.Fatalf("a new stall wakes the brain again: events = %d", got)
	}

	// Whatever stops the ticket running ends its signals.
	release()
	for range 2000 {
		if h.ticket(ticket.ID).Status == core.SuperAgentTicketReady {
			break
		}
		time.Sleep(time.Millisecond)
	}
	for _, signal := range h.ticket(ticket.ID).Signals {
		if signal.ClearedAt.IsZero() {
			t.Fatalf("a ready ticket has an open signal: %+v", signal)
		}
	}
}

func TestTheBrainSeesSignalsAndAnswersOnTheThread(t *testing.T) {
	h := newHarness(t)
	h.setSupervision(core.SuperAgentSupervisionAct)
	h.orphan("tkt_ghost", "github:o/r#9")
	h.supervise()
	h.advance(orphanGrace)
	h.supervise()
	tickets, _ := h.store.ListSuperAgentTickets(context.Background(), h.agent.ID, 10)
	view := h.service.routerView(context.Background(), h.agent, core.SuperAgentTrigger{ThreadKey: "github:o/r#9"}, tickets)
	if len(view.Tickets) != 1 || len(view.Tickets[0].Signals) != 1 || !strings.Contains(view.Tickets[0].Signals[0], "orphaned: marked running, but nothing on this machine is running it") {
		t.Fatalf("view = %+v", view.Tickets)
	}
	h.router.answer = func(core.InvocationRequest) (string, error) { return "", nil }
	h.settle()
	if len(h.router.prompts) != 1 || !strings.Contains(h.router.prompts[0], `"kind": "supervision"`) || !strings.Contains(h.router.prompts[0], `"ticket_id": "tkt_ghost"`) {
		t.Fatalf("prompts = %v", h.router.prompts)
	}
	if len(h.replies.sent()) != 0 {
		t.Fatalf("an empty answer to a signal sends nothing: %v", h.replies.sent())
	}
}

func TestOrphanedTicketIsToldOnceAcrossARestart(t *testing.T) {
	h := newHarness(t)
	h.setSupervision(core.SuperAgentSupervisionAct)
	h.orphan("tkt_ghost", "github:o/r#9")
	h.supervise()
	h.advance(30 * time.Second)
	h.supervise()
	if len(h.supervisionEvents()) != 0 {
		t.Fatal("a ticket is not orphaned before the grace period")
	}
	h.advance(31 * time.Second)
	h.supervise()
	if got := len(h.supervisionEvents()); got != 1 {
		t.Fatalf("events = %d", got)
	}

	// A new daemon on the same store suspects the ticket afresh, but the
	// signal it finds is the one already told.
	restarted := New(Options{
		Store: h.store, Invoker: h.router, Workers: h.workers, Replies: h.replies,
		Now: func() time.Time { return *h.clock }, StateDir: t.TempDir(),
	})
	for range 3 {
		if err := restarted.supervise(context.Background()); err != nil {
			t.Fatal(err)
		}
		h.advance(supervisionCooldown)
	}
	if got := len(h.supervisionEvents()); got != 1 {
		t.Fatalf("a restart must not repeat a signal: events = %d", got)
	}
	if signals := h.ticket("tkt_ghost").Signals; len(signals) != 1 || !signals[0].ClearedAt.IsZero() {
		t.Fatalf("signals = %+v", signals)
	}
}

func TestSignalsOnAThreadAreMergedAndRateLimited(t *testing.T) {
	h := newHarness(t)
	h.setSupervision(core.SuperAgentSupervisionAct)
	h.orphan("tkt_a", "github:o/r#9")
	h.orphan("tkt_b", "github:o/r#9")
	h.orphan("tkt_other", "github:o/r#10")
	h.supervise()
	h.advance(orphanGrace)
	h.supervise()
	events := h.supervisionEvents()
	if len(events) != 2 {
		t.Fatalf("one event per thread: %+v", events)
	}
	for _, event := range events {
		if event.ThreadKey == "github:o/r#9" && (!strings.Contains(event.Text, "tkt_a") || !strings.Contains(event.Text, "tkt_b")) {
			t.Fatalf("the thread's signals come together:\n%s", event.Text)
		}
	}

	h.orphan("tkt_c", "github:o/r#9")
	h.supervise()
	h.advance(orphanGrace)
	h.supervise()
	if got := len(h.supervisionEvents()); got != 2 {
		t.Fatalf("a signal within the cooldown waits: events = %d", got)
	}
	if signals := h.ticket("tkt_c").Signals; len(signals) != 1 || !signals[0].WokeAt.IsZero() {
		t.Fatalf("the waiting signal is recorded: %+v", signals)
	}
	h.advance(supervisionCooldown)
	h.supervise()
	events = h.supervisionEvents()
	if len(events) != 3 || !strings.Contains(events[2].Text, "tkt_c") || strings.Contains(events[2].Text, "tkt_a") {
		t.Fatalf("after the cooldown only the new signal is sent: %+v", events)
	}
}

func TestUnreachableMachineOrphansARemoteTicket(t *testing.T) {
	h := newHarness(t)
	h.setSupervision(core.SuperAgentSupervisionAct)
	h.orphan("tkt_remote", "github:o/r#9")
	ticket := h.ticket("tkt_remote")
	ticket.Worker = h.agent.Workers[1]
	if err := h.store.UpsertSuperAgentTicket(context.Background(), ticket); err != nil {
		t.Fatal(err)
	}
	// This daemon runs the turn; its machine stops answering.
	h.service.mu.Lock()
	h.service.running[h.agent.ID+"|tkt_remote"] = func() {}
	h.service.turnStarts["tkt_remote"] = h.clock.UTC()
	h.service.mu.Unlock()
	t.Cleanup(func() {
		h.service.mu.Lock()
		delete(h.service.running, h.agent.ID+"|tkt_remote")
		h.service.mu.Unlock()
	})
	h.workers.mu.Lock()
	h.workers.down = map[string]bool{"host-1": true}
	h.workers.mu.Unlock()
	h.supervise()
	h.advance(orphanGrace)
	h.supervise()
	signals := h.ticket("tkt_remote").Signals
	if len(signals) != 1 || signals[0].Kind != core.SuperAgentSignalOrphaned || !strings.Contains(signals[0].Detail, "cannot be reached: tunnel closed") {
		t.Fatalf("signals = %+v", signals)
	}
	h.workers.mu.Lock()
	h.workers.down = nil
	h.workers.mu.Unlock()
	h.supervise()
	if signals := h.ticket("tkt_remote").Signals; signals[0].ClearedAt.IsZero() {
		t.Fatalf("a machine that answers again ends the signal: %+v", signals)
	}
}

func TestObserveModeRecordsSignalsWithoutWakingAnything(t *testing.T) {
	h := newHarness(t)
	h.orphan("tkt_ghost", "github:o/r#9")
	h.supervise()
	h.advance(orphanGrace)
	h.supervise()
	signals := h.ticket("tkt_ghost").Signals
	if len(signals) != 1 || !signals[0].WokeAt.IsZero() {
		t.Fatalf("observe mode records the signal: %+v", signals)
	}
	if len(h.supervisionEvents()) != 0 {
		t.Fatal("observe mode wakes nothing")
	}
	tickets, _ := h.store.ListSuperAgentTickets(context.Background(), h.agent.ID, 10)
	if view := h.service.routerView(context.Background(), h.agent, core.SuperAgentTrigger{ThreadKey: "github:o/r#9"}, tickets); len(view.Tickets[0].Signals) != 0 {
		t.Fatalf("the brain is not shown signals in observe mode: %+v", view.Tickets[0])
	}

	// Switched to act, the signal that still holds is told.
	h.setSupervision(core.SuperAgentSupervisionAct)
	h.supervise()
	if len(h.supervisionEvents()) != 1 {
		t.Fatal("act mode tells a signal that still holds")
	}
}

func TestSupervisionOffRecordsNothing(t *testing.T) {
	h := newHarness(t)
	h.setSupervision(core.SuperAgentSupervisionOff)
	h.orphan("tkt_ghost", "github:o/r#9")
	h.supervise()
	h.advance(time.Hour)
	h.supervise()
	if len(h.ticket("tkt_ghost").Signals) != 0 || len(h.supervisionEvents()) != 0 {
		t.Fatal("supervision off records nothing")
	}
}

func TestSupervisionSettingIsChecked(t *testing.T) {
	for value, want := range map[string]string{"": "", "Observe": "", " act ": "act", "off": "off"} {
		agent := core.SuperAgent{Supervision: value}
		if err := normalizeSupervision(&agent); err != nil || agent.Supervision != want {
			t.Fatalf("%q: %q, %v", value, agent.Supervision, err)
		}
	}
	if err := normalizeSupervision(&core.SuperAgent{Supervision: "loud"}); err == nil {
		t.Fatal("an unknown mode must be refused")
	}
}

func TestStallLimit(t *testing.T) {
	for expect, want := range map[int]time.Duration{0: 20 * time.Minute, 10: 20 * time.Minute, 40: time.Hour, 120: 3 * time.Hour} {
		if got := stallLimit(expect); got != want {
			t.Fatalf("stallLimit(%d) = %s, want %s", expect, got, want)
		}
	}
}
