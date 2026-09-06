package core

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type countingSteerSession struct {
	*remoteInteractiveTestSession
	calls   int
	entered chan struct{}
	release chan struct{}
}

func TestQueuedTurnsExecuteInReceiveOrder(t *testing.T) {
	e := NewEngine(nil, NewHookRunner(nil, nil))
	rt, _ := newRemoteControlTestRuntime(e)
	session := &remoteBlockingTestSession{started: make(chan string, 3), release: make(chan struct{})}
	rt.agent = &remoteControlTestAgent{session: session}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt.runCtx = ctx
	message := func(text string) *Message {
		return &Message{ChannelID: rt.channel.ID, ChatID: "one", ConversationKey: "chat:one", UserID: "member", Text: text}
	}
	first := message("first")
	e.handleChannelMessage(ctx, first, eventData(first))
	select {
	case <-session.started:
	case <-time.After(time.Second):
		t.Fatal("first task did not start")
	}
	for _, text := range []string{"second", "third"} {
		msg := message(text)
		e.handleChannelMessage(ctx, msg, eventData(msg))
	}
	for _, want := range []string{"second", "third"} {
		session.release <- struct{}{}
		select {
		case got := <-session.started:
			if !strings.Contains(got, want) {
				t.Fatalf("got %s want %s", got, want)
			}
		case <-time.After(time.Second):
			t.Fatal("queue did not advance")
		}
	}
	session.release <- struct{}{}
	waitFor(t, "queue drained", func() bool {
		rt.controlMu.Lock()
		defer rt.controlMu.Unlock()
		state := rt.controlStateLocked("chat:one")
		return state.active == nil && len(state.queue) == 0
	})
}

func (s *countingSteerSession) Steer(ctx context.Context, text string) error {
	s.calls++
	if s.entered != nil {
		close(s.entered)
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.steerErr
}
func queueFixture(t *testing.T, steerErr error) (*Engine, *channelRuntime, *channelControlState, *countingSteerSession, *Message) {
	t.Helper()
	e := NewEngine(nil, NewHookRunner(nil, nil))
	rt, _ := newRemoteControlTestRuntime(e)
	session := &countingSteerSession{remoteInteractiveTestSession: &remoteInteractiveTestSession{fakeSession: &fakeSession{id: "one"}, steerErr: steerErr}}
	state := rt.controlStateLocked("chat:one")
	state.active = &runtimeChannelTask{task: ChannelTask{ID: "active", ControllerID: "member", ConversationKey: "chat:one"}, session: session}
	msg := &Message{ID: "m2", ChannelID: rt.channel.ID, ChatID: "one", ChatType: "p2p", ConversationKey: "chat:one", UserID: "member", Text: "next"}
	return e, rt, state, session, msg
}

func TestQueueRequiresExplicitSteerAndBindsOriginalTask(t *testing.T) {
	e, rt, state, s, msg := queueFixture(t, nil)
	e.handleChannelMessage(context.Background(), msg, eventData(msg))
	if s.calls != 0 || len(state.queue) != 1 {
		t.Fatal("follow-up did not stay queued")
	}
	item := state.queue[0]
	action := ChannelTaskAction{TaskID: item.task.ID, Action: ChannelTaskActionSteer, Nonce: item.task.ControlNonce}
	callback := *msg
	callback.InteractionMessageID = "card"
	wrong := action
	wrong.Nonce = "wrong"
	if e.controlQueuedTask(context.Background(), rt, &callback, wrong) == nil {
		t.Fatal("bad nonce accepted")
	}
	if err := e.controlQueuedTask(context.Background(), rt, &callback, action); err != nil {
		t.Fatal(err)
	}
	if s.calls != 1 || len(state.queue) != 0 || item.task.Status != ChannelTaskSteered || item.task.TargetTaskID != "active" {
		t.Fatalf("steer=%d task=%+v", s.calls, item.task)
	}
	if e.controlQueuedTask(context.Background(), rt, &callback, action) == nil || s.calls != 1 {
		t.Fatal("duplicate click steered twice")
	}
}
func TestQueueSteerRejectionAndUnknownOutcome(t *testing.T) {
	for _, tc := range []struct {
		name      string
		err       error
		status    ChannelTaskStatus
		remaining int
	}{{"rejected", &SteerRejectedError{Reason: "no active turn"}, ChannelTaskQueued, 1}, {"unknown", errors.New("connection lost"), ChannelTaskSteerUnknown, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			e, rt, state, _, msg := queueFixture(t, tc.err)
			item, err := e.enqueueRemoteTask(context.Background(), rt, msg, msg.Text)
			if err != nil {
				t.Fatal(err)
			}
			if e.controlQueuedTask(context.Background(), rt, msg, ChannelTaskAction{TaskID: item.task.ID, Action: ChannelTaskActionSteer}) == nil {
				t.Fatal("expected failure")
			}
			if item.task.Status != tc.status || len(state.queue) != tc.remaining {
				t.Fatalf("status=%s remaining=%d", item.task.Status, len(state.queue))
			}
		})
	}
}
func TestQueuePermissionsAndStaleTarget(t *testing.T) {
	e, rt, state, s, msg := queueFixture(t, nil)
	item, _ := e.enqueueRemoteTask(context.Background(), rt, msg, msg.Text)
	other := *msg
	other.UserID = "other"
	if e.controlQueuedTask(context.Background(), rt, &other, ChannelTaskAction{TaskID: item.task.ID, Action: ChannelTaskActionCancel}) == nil {
		t.Fatal("other user cancelled")
	}
	state.active = &runtimeChannelTask{task: ChannelTask{ID: "new-task", ControllerID: "member"}, session: s}
	if e.controlQueuedTask(context.Background(), rt, msg, ChannelTaskAction{TaskID: item.task.ID, Action: ChannelTaskActionSteer}) == nil || s.calls != 0 {
		t.Fatal("stale steer reached next task")
	}
	if err := e.controlQueuedTask(context.Background(), rt, msg, ChannelTaskAction{TaskID: item.task.ID, Action: ChannelTaskActionCancel}); err != nil {
		t.Fatal(err)
	}
	if item.task.Status != ChannelTaskCancelled || len(state.queue) != 0 {
		t.Fatal("cancel failed")
	}
}
func TestQueueCompletingDuringSteerDoesNotStartAnotherTurn(t *testing.T) {
	e, rt, state, s, msg := queueFixture(t, nil)
	s.entered = make(chan struct{})
	s.release = make(chan struct{})
	item, _ := e.enqueueRemoteTask(context.Background(), rt, msg, msg.Text)
	result := make(chan error, 1)
	go func() {
		result <- e.controlQueuedTask(context.Background(), rt, msg, ChannelTaskAction{TaskID: item.task.ID, Action: ChannelTaskActionSteer})
	}()
	<-s.entered
	rt.controlMu.Lock()
	active := state.active
	rt.controlMu.Unlock()
	active.msg = msg
	e.finishRemoteTask(rt, active, ChannelTaskSucceeded, "")
	rt.controlMu.Lock()
	if !state.steering || state.active != nil {
		t.Fatal("steer did not hold admission")
	}
	rt.controlMu.Unlock()
	close(s.release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if len(state.queue) != 0 {
		t.Fatal("steered message remained queued")
	}
}

type parallelTurnAgent struct {
	started chan string
	release chan struct{}
	mu      sync.Mutex
	starts  int
}

func (a *parallelTurnAgent) Name() string { return "test-parallel" }
func (a *parallelTurnAgent) StartSession(context.Context, string) (AgentSession, error) {
	a.mu.Lock()
	a.starts++
	a.mu.Unlock()
	return &remoteBlockingTestSession{started: a.started, release: a.release}, nil
}
func (a *parallelTurnAgent) ListSessions(context.Context) ([]string, error) { return nil, nil }
func (a *parallelTurnAgent) Stop(context.Context) error                     { return nil }
func TestIndependentChannelConversationsRunInSameDirectory(t *testing.T) {
	e := NewEngine(nil, NewHookRunner(nil, nil))
	rt, _ := newRemoteControlTestRuntime(e)
	a := &parallelTurnAgent{started: make(chan string, 3), release: make(chan struct{})}
	rt.agent = a
	rt.workDir = t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt.runCtx = ctx
	var wg sync.WaitGroup
	for _, key := range []string{"root:first", "root:second"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			m := &Message{ChannelID: rt.channel.ID, ChatID: "group", ConversationKey: key, UserID: "member", Text: key}
			e.handleChannelMessage(ctx, m, eventData(m))
		}(key)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-a.started:
		case <-time.After(2 * time.Second):
			t.Fatal("independent conversation was serialized by directory")
		}
	}
	close(a.release)
	wg.Wait()
	waitFor(t, "parallel tasks completed", func() bool {
		rt.controlMu.Lock()
		defer rt.controlMu.Unlock()
		return rt.controlTasks["root:first"].active == nil && rt.controlTasks["root:second"].active == nil
	})
}
