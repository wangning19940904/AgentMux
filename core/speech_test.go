package core

import (
	"context"
	"log/slog"
	"testing"
)

type recordingTextReplyStream struct {
	updates []string
}

func (s *recordingTextReplyStream) Update(_ context.Context, text string, _, _ bool) error {
	s.updates = append(s.updates, text)
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
