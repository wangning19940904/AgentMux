package core

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

type recordingTextReplyStream struct {
	updates []string
	done    []bool
	failed  []bool
}

func (s *recordingTextReplyStream) Update(_ context.Context, text string, done, failed bool) error {
	s.updates = append(s.updates, text)
	s.done = append(s.done, done)
	s.failed = append(s.failed, failed)
	return nil
}

func (s *recordingTextReplyStream) Close(context.Context) error { return nil }

type recordingSpeechReply struct {
	texts []string
	done  []bool
}

func (s *recordingSpeechReply) Update(_ context.Context, text string, done bool) error {
	s.texts = append(s.texts, text)
	s.done = append(s.done, done)
	return nil
}

func (s *recordingSpeechReply) Close(context.Context) error { return nil }

func TestDriveReplyStreamSpeaksOnlyAssistantAnswer(t *testing.T) {
	engine := NewEngine(slog.Default(), nil)
	rendered := &recordingTextReplyStream{}
	spoken := &recordingSpeechReply{}
	events := make(chan *Event, 4)
	events <- &Event{Type: EventThinking, Text: "checking private details"}
	events <- &Event{Type: EventToolUse, ToolCallID: "tool-1", ToolName: "Read", ToolInput: "secret.txt"}
	events <- &Event{Type: EventOutput, Text: "会议结论"}
	events <- &Event{Type: EventFinal, Text: "会议结论：通过。"}
	close(events)

	engine.driveReplyStream(context.Background(), nil, rendered, spoken, events, nil)

	if len(spoken.texts) != 3 {
		t.Fatalf("speech updates = %v", spoken.texts)
	}
	if spoken.texts[0] != "会议结论" ||
		spoken.texts[1] != "会议结论：通过。" ||
		spoken.texts[2] != "会议结论：通过。" ||
		spoken.done[0] || spoken.done[1] || !spoken.done[2] {
		t.Fatalf("speech text=%v done=%v", spoken.texts, spoken.done)
	}
	for _, text := range spoken.texts {
		if text == "checking private details" || text == "secret.txt" {
			t.Fatalf("non-answer content was spoken: %q", text)
		}
	}
}

func TestDriveReplyStreamPreservesCompletedCursorAnswerOnTailClose(t *testing.T) {
	engine := NewEngine(slog.Default(), nil)
	rendered := &recordingTextReplyStream{}
	events := make(chan *Event, 5)
	events <- &Event{Type: EventToolUse, ToolCallID: "install", ToolName: "Shell", ToolInput: "agentbuddy skill add"}
	events <- &Event{Type: EventToolUse, ToolCallID: "install", ToolResult: "exit 0", Status: "completed"}
	events <- &Event{Type: EventOutput, Text: "spacex-meego-requirements v1.0.3 已安装完成。", Metadata: map[string]string{"runtime": "cursor"}}
	events <- &Event{Type: EventOutput, Text: "好的。", Metadata: map[string]string{"runtime": "cursor"}}
	// Cursor's stream can end without a structured result frame. In that case
	// the subprocess runner, rather than the native mapper, produces this event.
	events <- &Event{Type: EventError, Err: fmt.Errorf("RetriableError: WritableIterable is closed (exit status 1)"), Metadata: map[string]string{"runtime": "cursor", "transport": "process"}}
	close(events)

	engine.driveReplyStream(context.Background(), nil, rendered, nil, events, nil)

	last := len(rendered.updates) - 1
	if last < 0 || !rendered.done[last] || rendered.failed[last] {
		t.Fatalf("final stream state: updates=%v done=%v failed=%v", rendered.updates, rendered.done, rendered.failed)
	}
	if !strings.Contains(rendered.updates[last], "已安装完成") || strings.Contains(rendered.updates[last], "WritableIterable") || rendered.updates[last] == "好的。" {
		t.Fatalf("completed answer was not preserved: %q", rendered.updates[last])
	}
}

func TestDriveReplyStreamKeepsRealCursorFailures(t *testing.T) {
	tests := []struct {
		name   string
		events []*Event
	}{
		{
			name: "close before post-tool answer",
			events: []*Event{
				{Type: EventOutput, Text: "开始安装", Metadata: map[string]string{"runtime": "cursor"}},
				{Type: EventToolUse, ToolCallID: "install", ToolName: "Shell"},
				{Type: EventToolUse, ToolCallID: "install", ToolResult: "exit 0"},
				{Type: EventError, Err: fmt.Errorf("WritableIterable is closed"), Metadata: map[string]string{"runtime": "cursor"}},
			},
		},
		{
			name: "failed tool",
			events: []*Event{
				{Type: EventToolUse, ToolCallID: "install", ToolName: "Shell"},
				{Type: EventToolUse, ToolCallID: "install", ToolResult: "exit 1", Err: fmt.Errorf("exit 1")},
				{Type: EventOutput, Text: "安装失败", Metadata: map[string]string{"runtime": "cursor"}},
				{Type: EventError, Err: fmt.Errorf("WritableIterable is closed"), Metadata: map[string]string{"runtime": "cursor"}},
			},
		},
		{
			name: "other cursor error",
			events: []*Event{
				{Type: EventToolUse, ToolCallID: "install", ToolName: "Shell"},
				{Type: EventToolUse, ToolCallID: "install", ToolResult: "exit 0"},
				{Type: EventOutput, Text: "安装完成", Metadata: map[string]string{"runtime": "cursor"}},
				{Type: EventError, Err: fmt.Errorf("upstream request failed"), Metadata: map[string]string{"runtime": "cursor"}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := NewEngine(slog.Default(), nil)
			rendered := &recordingTextReplyStream{}
			events := make(chan *Event, len(tt.events))
			for _, event := range tt.events {
				events <- event
			}
			close(events)

			engine.driveReplyStream(context.Background(), nil, rendered, nil, events, nil)

			last := len(rendered.updates) - 1
			if last < 0 || !rendered.done[last] || !rendered.failed[last] {
				t.Fatalf("real failure was suppressed: updates=%v done=%v failed=%v", rendered.updates, rendered.done, rendered.failed)
			}
			if !strings.Contains(rendered.updates[last], "error:") {
				t.Fatalf("failure text missing from %q", rendered.updates[last])
			}
		})
	}
}
