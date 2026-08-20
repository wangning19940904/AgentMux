package core

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

func TestDirectChannelTurnIsSingleFlightPerConversation(t *testing.T) {
	rt := &channelRuntime{directTurns: map[string]*directChannelTurn{}}
	ctx, cancel := context.WithCancel(context.Background())
	turn, ok := rt.beginDirectTurn("chat-1", "user-1", cancel)
	if !ok || turn == nil {
		t.Fatal("first direct turn was not accepted")
	}
	_, second := rt.beginDirectTurn("chat-1", "user-1", func() {})
	if second {
		t.Fatal("overlapping direct turn was accepted")
	}
	if _, other := rt.beginDirectTurn("chat-2", "user-2", func() {}); !other {
		t.Fatal("independent conversation was blocked")
	}
	rt.finishDirectTurn("chat-1", turn)
	if _, next := rt.beginDirectTurn("chat-1", "user-1", func() {}); !next {
		t.Fatal("conversation remained blocked after the turn finished")
	}
	select {
	case <-ctx.Done():
		t.Fatal("finishing a turn must not cancel its context")
	default:
	}
}

func TestCancelDirectTurnForResetWaitsForCompletion(t *testing.T) {
	rt := &channelRuntime{directTurns: map[string]*directChannelTurn{}}
	ctx, cancel := context.WithCancel(context.Background())
	turn, ok := rt.beginDirectTurn("chat-1", "user-1", cancel)
	if !ok {
		t.Fatal("direct turn was not accepted")
	}
	finished := make(chan struct{})
	go func() {
		<-ctx.Done()
		rt.finishDirectTurn("chat-1", turn)
		close(finished)
	}()
	rt.cancelDirectTurnForReset(context.Background(), "chat-1")
	select {
	case <-finished:
	default:
		t.Fatal("conversation reset returned before the cancelled turn finished")
	}
}

func TestChannelTurnTimeoutPrefersGenericAndFallsBackToLegacy(t *testing.T) {
	generic := Channel{Config: map[string]string{
		ChannelConfigTurnTimeout:      "7",
		ChannelConfigCodexTurnTimeout: "9",
	}}
	if got := ChannelTurnTimeout(generic); got.Minutes() != 7 {
		t.Fatalf("generic timeout = %s", got)
	}
	legacy := Channel{Config: map[string]string{ChannelConfigCodexTurnTimeout: "9"}}
	if got := ChannelTurnTimeout(legacy); got.Minutes() != 9 {
		t.Fatalf("legacy timeout = %s", got)
	}
}

func TestFinalDeliveryRetriesAndPersistsDirectTaskState(t *testing.T) {
	store := &deliveryControlStore{}
	engine := NewEngine(slog.New(slog.NewTextHandler(io.Discard, nil)), NewHookRunner(nil, nil))
	engine.channelControl = store
	rt := &channelRuntime{
		owner:        engine,
		channel:      Channel{ID: "channel-delivery"},
		directTurns:  map[string]*directChannelTurn{},
		controlTasks: map[string]*channelControlState{},
	}
	engine.channels[rt.channel.ID] = rt

	_, cancel := context.WithCancel(context.Background())
	turn, ok := rt.beginDirectTurn("chat:one", "user", cancel)
	if !ok {
		t.Fatal("direct turn was not accepted")
	}
	task := ChannelTask{
		ID: "task-delivery", ChannelID: rt.channel.ID, ConversationKey: "chat:one",
		Status: ChannelTaskRunning, DeliveryKey: "turn:task-delivery", DeliveryStatus: ChannelDeliveryPending,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if !rt.attachDirectTask("chat:one", turn, task, &Message{ChannelID: rt.channel.ID, ConversationKey: "chat:one"}) {
		t.Fatal("direct task was not attached")
	}
	stream := &flakyReplyStream{failures: 2}
	data := withTaskData(map[string]string{"channel_id": rt.channel.ID, "conversation_key": "chat:one"}, task, "started")
	if err := engine.finalizeReplyStream(context.Background(), stream, "done", false, data); err != nil {
		t.Fatal(err)
	}
	if stream.attempts != 3 || turn.task.DeliveryAttempts != 3 || turn.task.DeliveryStatus != ChannelDeliverySent || turn.task.DeliveredAt.IsZero() {
		t.Fatalf("delivery state = attempts:%d task:%+v", stream.attempts, turn.task)
	}
	rt.finishDirectTurn("chat:one", turn)
	last := store.lastTask()
	if last.Status != ChannelTaskSucceeded || last.DeliveryStatus != ChannelDeliverySent {
		t.Fatalf("persisted task = %+v", last)
	}
}

func TestFinalDeliveryExhaustionFailsTask(t *testing.T) {
	store := &deliveryControlStore{}
	engine := NewEngine(slog.New(slog.NewTextHandler(io.Discard, nil)), NewHookRunner(nil, nil))
	engine.channelControl = store
	rt := &channelRuntime{
		owner: engine, channel: Channel{ID: "channel-failed"},
		directTurns: map[string]*directChannelTurn{}, controlTasks: map[string]*channelControlState{},
	}
	engine.channels[rt.channel.ID] = rt
	_, cancel := context.WithCancel(context.Background())
	turn, _ := rt.beginDirectTurn("chat:failed", "user", cancel)
	task := ChannelTask{ID: "task-failed", ChannelID: rt.channel.ID, ConversationKey: "chat:failed", Status: ChannelTaskRunning, DeliveryStatus: ChannelDeliveryPending}
	rt.attachDirectTask("chat:failed", turn, task, &Message{ChannelID: rt.channel.ID, ConversationKey: "chat:failed"})
	data := withTaskData(map[string]string{"channel_id": rt.channel.ID, "conversation_key": "chat:failed"}, task, "started")
	if err := engine.finalizeReplyStream(context.Background(), &flakyReplyStream{failures: 10}, "done", false, data); err == nil {
		t.Fatal("delivery exhaustion unexpectedly succeeded")
	}
	rt.finishDirectTurn("chat:failed", turn)
	last := store.lastTask()
	if last.Status != ChannelTaskFailed || last.DeliveryStatus != ChannelDeliveryFailed || last.DeliveryAttempts != 3 || last.DeliveryError == "" {
		t.Fatalf("failed delivery task = %+v", last)
	}
}

type flakyReplyStream struct {
	failures int
	attempts int
}

func (s *flakyReplyStream) Update(context.Context, string, bool, bool) error {
	s.attempts++
	if s.attempts <= s.failures {
		return errors.New("temporary delivery failure")
	}
	return nil
}
func (*flakyReplyStream) Close(context.Context) error { return nil }

type deliveryControlStore struct {
	mu    sync.Mutex
	tasks []ChannelTask
}

func (*deliveryControlStore) CreateChannelTask(context.Context, ChannelTask) error { return nil }
func (s *deliveryControlStore) UpdateChannelTask(_ context.Context, task ChannelTask) error {
	s.mu.Lock()
	s.tasks = append(s.tasks, task)
	s.mu.Unlock()
	return nil
}
func (*deliveryControlStore) GetChannelTask(context.Context, string) (*ChannelTask, error) {
	return nil, nil
}
func (*deliveryControlStore) ListChannelTasks(context.Context, string, string, bool) ([]ChannelTask, error) {
	return nil, nil
}
func (*deliveryControlStore) RecoverChannelTasks(context.Context, string) ([]ChannelTask, error) {
	return nil, nil
}
func (*deliveryControlStore) CreateChannelInteraction(context.Context, ChannelInteraction) error {
	return nil
}
func (*deliveryControlStore) UpdateChannelInteractionMessage(context.Context, string, string) error {
	return nil
}
func (*deliveryControlStore) ResolveChannelInteraction(context.Context, string, string, string, ChannelInteractionStatus) (bool, error) {
	return false, nil
}
func (*deliveryControlStore) GetChannelInteraction(context.Context, string) (*ChannelInteraction, error) {
	return nil, nil
}
func (*deliveryControlStore) ListChannelInteractions(context.Context, string, string, bool) ([]ChannelInteraction, error) {
	return nil, nil
}
func (s *deliveryControlStore) lastTask() ChannelTask {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.tasks) == 0 {
		return ChannelTask{}
	}
	return s.tasks[len(s.tasks)-1]
}
