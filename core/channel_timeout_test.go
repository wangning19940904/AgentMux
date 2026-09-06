package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDirectTimeoutIsInterruptedWithoutAgentErrorEvent(t *testing.T) {
	store := &deliveryControlStore{}
	engine := NewEngine(nil, NewHookRunner(nil, nil))
	engine.channelControl = store
	rt := &channelRuntime{owner: engine, channel: Channel{ID: "timeout-channel"}}
	engine.channels[rt.channel.ID] = rt
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	turn, ok := rt.beginDirectTurn(ctx, "chat:timeout", "user", cancel)
	if !ok {
		t.Fatal("could not begin turn")
	}
	task := ChannelTask{ID: "timeout-task", ChannelID: rt.channel.ID, ConversationKey: "chat:timeout", Status: ChannelTaskRunning}
	if !rt.attachDirectTask("chat:timeout", turn, task, &Message{ChannelID: rt.channel.ID, ConversationKey: "chat:timeout"}) {
		t.Fatal("could not attach task")
	}
	rt.finishDirectTurn("chat:timeout", turn)
	got := store.lastTask()
	if got.Status != ChannelTaskInterrupted || got.Error != context.DeadlineExceeded.Error() || got.FinishedAt.IsZero() {
		t.Fatalf("timed-out task was not interrupted: %+v", got)
	}
}

func TestTimeoutCardDoesNotAssumeInteractiveShell(t *testing.T) {
	for _, retrying := range []bool{false, true} {
		engine := NewEngine(nil, NewHookRunner(nil, nil))
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		events := make(chan *Event, 1)
		if retrying {
			events <- &Event{Type: EventModelResponse, Err: errors.New("connection failed"), Metadata: map[string]string{"will_retry": "true"}}
		}
		close(events)
		stream := &finalContextReplyStream{}
		engine.driveReplyStream(ctx, nil, stream, nil, events, nil)
		cancel()
		if !stream.done || !stream.failed || stream.ctxErr != nil || !strings.Contains(stream.text, "超时") || strings.Contains(stream.text, "request_user_input") || strings.Contains(stream.text, "扫码") {
			t.Fatalf("timeout card=%+v", stream)
		}
		if retrying && !strings.Contains(stream.text, "模型服务请求持续失败") {
			t.Fatalf("missing model failure context: %s", stream.text)
		}
	}
}

func TestModelRetryIsProgressAndCanRecover(t *testing.T) {
	engine := NewEngine(nil, NewHookRunner(nil, nil))
	events := make(chan *Event, 2)
	events <- &Event{Type: EventModelResponse, Err: errors.New("connection failed"), Metadata: map[string]string{"will_retry": "true"}}
	events <- &Event{Type: EventFinal, Text: "Recovered"}
	close(events)
	stream := &recordingTextReplyStream{}
	engine.driveReplyStream(context.Background(), nil, stream, nil, events, nil)
	if len(stream.updates) < 2 || !strings.Contains(stream.updates[0], "正在重试") || stream.failed[len(stream.failed)-1] || !strings.Contains(stream.updates[len(stream.updates)-1], "Recovered") {
		t.Fatalf("retry recovery: %+v", stream)
	}
	if strings.Contains(stream.updates[len(stream.updates)-1], "正在重试") {
		t.Fatal("recovered answer retained stale retry progress")
	}
}
