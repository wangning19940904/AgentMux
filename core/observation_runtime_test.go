package core

import (
	"context"
	"strings"
	"sync"
	"testing"
)

type observationScriptSession struct {
	events      []*Event
	traceparent string
}

func (s *observationScriptSession) ID() string { return "session-observed" }
func (s *observationScriptSession) Send(ctx context.Context, _ string) (<-chan *Event, error) {
	s.traceparent = ObservationTraceparent(ctx)
	out := make(chan *Event, len(s.events))
	for _, event := range s.events {
		out <- event
	}
	close(out)
	return out, nil
}
func (s *observationScriptSession) RespondPermission(context.Context, bool) error { return nil }
func (s *observationScriptSession) Close(context.Context) error                   { return nil }

func TestObserveSendCorrelatesToolsUsageCostAndTraceparent(t *testing.T) {
	engine := NewEngine(nil, NewHookRunner(nil, nil))
	bus := NewObservationBus()
	engine.SetObservationBus(bus)
	var mu sync.Mutex
	var envelopes []ObservationEnvelope
	bus.Subscribe("capture", func(_ context.Context, envelope ObservationEnvelope) error {
		mu.Lock()
		envelopes = append(envelopes, envelope)
		mu.Unlock()
		return nil
	})
	var records []UsageRecord
	engine.SetUsageSink(func(_ context.Context, record UsageRecord) (float64, error) {
		records = append(records, record)
		return 1.25, nil
	})
	session := &observationScriptSession{events: []*Event{
		{Type: EventModelRequest, EventID: "model-start", Status: "in_progress", Usage: &TurnUsage{RequestID: "req-1", Attempt: 1, RequestedModel: "gpt-x"}},
		{Type: EventToolUse, EventID: "tool-a-start", ToolCallID: "call-a", ToolName: "Read", ToolInputRaw: `{"path":"a"}`, Status: "in_progress"},
		{Type: EventToolUse, EventID: "tool-b-start", ToolCallID: "call-b", ToolName: "Bash", ToolInputRaw: `{"command":"true"}`, Status: "in_progress"},
		{Type: EventToolUse, EventID: "tool-b-end", ToolCallID: "call-b", ToolName: "Bash", ToolResultRaw: `{"ok":true}`, Status: "completed"},
		{Type: EventToolUse, EventID: "tool-a-end", ToolCallID: "call-a", ToolName: "Read", ToolResultRaw: `{"text":"a"}`, Status: "completed"},
		{Type: EventModelResponse, EventID: "usage-1", Status: "in_progress", Usage: &TurnUsage{RequestID: "req-1", Attempt: 1, ResolvedModel: "gpt-x", InputTokens: 100, OutputTokens: 10, TotalTokens: 110, Cumulative: true}},
		{Type: EventModelResponse, EventID: "usage-2", Status: "completed", Usage: &TurnUsage{RequestID: "req-1", Attempt: 1, ResolvedModel: "gpt-x", InputTokens: 140, OutputTokens: 20, TotalTokens: 160, Cumulative: true}},
		{Type: EventFinal, EventID: "final", Text: "done", Final: true, Status: "completed"},
	}}
	data := map[string]string{"agent_id": "agent-1", "runtime_id": "codex"}
	stream, err := engine.observeSend(context.Background(), session, "hello", data)
	if err != nil {
		t.Fatal(err)
	}
	for range stream {
	}
	if parts := strings.Split(session.traceparent, "-"); len(parts) != 4 || parts[0] != "00" || len(parts[1]) != 32 || len(parts[2]) != 16 {
		t.Fatalf("traceparent = %q", session.traceparent)
	}
	if len(records) != 1 {
		t.Fatalf("usage records = %d, want one terminal request", len(records))
	}
	if records[0].InputTokens != 140 || records[0].OutputTokens != 20 || records[0].RequestID != "req-1:attempt:1" {
		t.Fatalf("usage record = %+v", records[0])
	}
	mu.Lock()
	defer mu.Unlock()
	toolSpans := map[string]string{}
	toolEnds := map[string]string{}
	var rootEnd *ObservationEnvelope
	for index := range envelopes {
		envelope := &envelopes[index]
		if envelope.TraceID != data["trace_id"] {
			t.Fatalf("trace id = %q, want %q", envelope.TraceID, data["trace_id"])
		}
		if envelope.Kind == "tool.call" && envelope.Tool != nil {
			if envelope.Lifecycle == ObservationLifecycleStart {
				toolSpans[envelope.Tool.CallID] = envelope.SpanID
			}
			if envelope.Lifecycle == ObservationLifecycleEnd {
				toolEnds[envelope.Tool.CallID] = envelope.SpanID
			}
		}
		if envelope.Kind == "agent.turn" && envelope.Lifecycle == ObservationLifecycleEnd {
			rootEnd = envelope
		}
	}
	for _, callID := range []string{"call-a", "call-b"} {
		if toolSpans[callID] == "" || toolSpans[callID] != toolEnds[callID] {
			t.Fatalf("tool %s spans: start=%q end=%q", callID, toolSpans[callID], toolEnds[callID])
		}
	}
	if rootEnd == nil || rootEnd.Usage == nil || rootEnd.Usage.TotalTokens != 160 || rootEnd.Usage.CostUSD != 1.25 {
		t.Fatalf("root end = %+v", rootEnd)
	}
	if rootEnd.Quality != ObservationQualityComplete {
		t.Fatalf("root quality = %q", rootEnd.Quality)
	}
}

func TestObserveSendRetainsLateToolInputAfterOutOfOrderResult(t *testing.T) {
	engine := NewEngine(nil, NewHookRunner(nil, nil))
	bus := NewObservationBus()
	engine.SetObservationBus(bus)
	var envelopes []ObservationEnvelope
	bus.Subscribe("capture", func(_ context.Context, envelope ObservationEnvelope) error {
		envelopes = append(envelopes, envelope)
		return nil
	})
	session := &observationScriptSession{events: []*Event{
		{Type: EventToolUse, EventID: "late-result", ToolCallID: "call-late", ToolResultRaw: `{"ok":true}`, Status: "completed"},
		{Type: EventToolUse, EventID: "late-start", ToolCallID: "call-late", ToolName: "Read", ToolInputRaw: `{"path":"late"}`, Status: "in_progress"},
		{Type: EventFinal, EventID: "done", Text: "done", Status: "completed"},
	}}
	stream, err := engine.observeSend(context.Background(), session, "hello", map[string]string{"runtime_id": "codex"})
	if err != nil {
		t.Fatal(err)
	}
	for range stream {
	}
	var spanID string
	var sawResult, sawInput bool
	for _, envelope := range envelopes {
		if envelope.Kind != "tool.call" || envelope.Tool == nil || envelope.Tool.CallID != "call-late" {
			continue
		}
		if spanID == "" {
			spanID = envelope.SpanID
		} else if envelope.SpanID != spanID {
			t.Fatalf("out-of-order tool split across spans: %q and %q", spanID, envelope.SpanID)
		}
		if envelope.Lifecycle == ObservationLifecycleEnd && envelope.Content != nil && strings.Contains(string(envelope.Content.Data), "ok") {
			sawResult = true
		}
		if envelope.Content != nil && strings.Contains(string(envelope.Content.Data), "late") {
			sawInput = true
		}
	}
	if !sawResult || !sawInput {
		t.Fatalf("late tool payloads missing: result=%t input=%t envelopes=%+v", sawResult, sawInput, envelopes)
	}
}
