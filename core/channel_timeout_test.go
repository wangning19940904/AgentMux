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
		if retrying && !strings.Contains(stream.text, "模型服务请求失败，重试尚未恢复") {
			t.Fatalf("missing model failure context: %s", stream.text)
		}
		if !retrying && strings.Contains(stream.text, "网络") {
			t.Fatalf("timeout without model failure blamed the network: %s", stream.text)
		}
		if !strings.Contains(stream.text, "不会回滚") || !strings.Contains(stream.text, "先核实结果") {
			t.Fatalf("timeout lost external operation guidance: %s", stream.text)
		}
	}
}

func TestTimeoutCardShowsEffectiveChannelLimitAfterModelRecovery(t *testing.T) {
	for _, tc := range []struct {
		config map[string]string
		limit  string
	}{
		{map[string]string{ChannelConfigTurnTimeout: "60", ChannelConfigCodexTurnTimeout: "20"}, "60"},
		{map[string]string{ChannelConfigTurnTimeout: "35"}, "35"},
		{map[string]string{ChannelConfigCodexTurnTimeout: "90"}, "90"},
		{nil, "60"},
	} {
		engine := NewEngine(nil, NewHookRunner(nil, nil))
		engine.channels["channel-timeout"] = &channelRuntime{channel: Channel{ID: "channel-timeout", Config: tc.config}}
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		events := make(chan *Event, 4)
		events <- &Event{Type: EventModelResponse, Err: errors.New("connection failed"), Metadata: map[string]string{"will_retry": "true"}}
		events <- &Event{Type: EventModelResponse}
		events <- &Event{Type: EventToolUse, ToolCallID: "check", ToolName: "exec_command", ToolInput: "check deployment"}
		events <- &Event{Type: EventToolUse, ToolCallID: "check", ToolResult: "deployment finished"}
		close(events)
		stream := &finalContextReplyStream{}
		engine.driveReplyStream(ctx, nil, stream, nil, events, map[string]string{"channel_id": "channel-timeout"})
		cancel()
		if !stream.done || !stream.failed || stream.ctxErr != nil || !strings.Contains(stream.text, tc.limit+" 分钟总时限") {
			t.Fatalf("timeout did not show effective limit: %+v", stream)
		}
		if strings.Contains(stream.text, "网络") || strings.Contains(stream.text, "重试尚未恢复") {
			t.Fatalf("timeout retained a recovered model error: %s", stream.text)
		}
		if !strings.Contains(stream.text, "deployment finished") {
			t.Fatalf("timeout discarded completed tool progress: %s", stream.text)
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
