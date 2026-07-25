package observability

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
)

func TestInsightEngineToolFailureThresholdIsAdvisory(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "insights.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Minute)
	for i := 0; i < 5; i++ {
		traceID := core.NewObservationTraceID()
		rootID := core.NewObservationSpanID()
		status := core.ObservationStatusOK
		if i == 0 {
			status = core.ObservationStatusError
		}
		for _, envelope := range []core.ObservationEnvelope{
			{EventID: core.NewObservationEventID(), Time: base.Add(time.Duration(i) * time.Second), TraceID: traceID, SpanID: rootID,
				Kind: "agent.turn", Lifecycle: core.ObservationLifecycleStart, AgentID: "agent-a", Status: core.ObservationStatusRunning},
			{EventID: core.NewObservationEventID(), Time: base.Add(time.Duration(i)*time.Second + time.Millisecond), TraceID: traceID,
				SpanID: "tool-span-" + traceID[:8], ParentSpanID: rootID, Kind: "tool.call", Name: "bash",
				Lifecycle: core.ObservationLifecycleEnd, AgentID: "agent-a", Status: status,
				Tool: &core.ObservationTool{Name: "bash", CallID: "call", OutputBytes: 4000}},
			{EventID: core.NewObservationEventID(), Time: base.Add(time.Duration(i)*time.Second + 2*time.Millisecond), TraceID: traceID, SpanID: rootID,
				Kind: "agent.turn", Lifecycle: core.ObservationLifecycleEnd, AgentID: "agent-a", Status: status},
		} {
			if err := st.RecordObservation(ctx, envelope); err != nil {
				t.Fatal(err)
			}
		}
	}
	engine := NewInsightEngine(st)
	insights, err := engine.Run(ctx, base.Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	var failure *store.ObservationInsight
	for i := range insights {
		if insights[i].RuleID == "tool_failure_rate" {
			failure = &insights[i]
			break
		}
	}
	if failure == nil {
		t.Fatal("expected tool failure insight at 20% threshold")
	}
	if failure.SampleSize != 5 || !failure.OnlySuggestion || len(failure.RelatedTraceIDs) == 0 {
		t.Fatalf("insight = %#v", failure)
	}
	if _, err := engine.Run(ctx, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	resolved, err := st.ListObservationInsights(ctx, store.ObservationInsightFilter{Status: "resolved", Limit: 10})
	if err != nil || len(resolved) == 0 || resolved[0].ID != failure.ID {
		t.Fatalf("stale insight was not resolved: %+v, err=%v", resolved, err)
	}
}

func TestInsightEngineDeduplicatesStableRequestAcrossTraces(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "insights.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Minute)

	recordInsightModelSpan(t, st, base, core.NewObservationTraceID(), "transcript", "req-shared", core.ObservationStatusError)
	recordInsightModelSpan(t, st, base.Add(time.Second), core.NewObservationTraceID(), "codex.otel", "req-shared", core.ObservationStatusError)

	insights, err := NewInsightEngine(st).Run(ctx, base.Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	failure := findInsight(insights, "agent_error_rate")
	if failure == nil {
		t.Fatal("expected model error insight")
	}
	if failure.SampleSize != 1 {
		t.Fatalf("duplicate request counted across traces: sample size = %d, want 1", failure.SampleSize)
	}
}

func TestInsightEngineKeepsUniqueLowerPriorityRequestInSameTurn(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "insights.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Minute)
	traceID := core.NewObservationTraceID()

	// The high-priority OTel request must not cause the unique transcript
	// request from the same turn to be discarded as a whole-source batch.
	recordInsightModelSpan(t, st, base, traceID, "codex.otel", "req-first", core.ObservationStatusOK)
	recordInsightModelSpan(t, st, base.Add(time.Millisecond), traceID, "transcript", "req-second", core.ObservationStatusError)

	insights, err := NewInsightEngine(st).Run(ctx, base.Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	failure := findInsight(insights, "agent_error_rate")
	if failure == nil {
		t.Fatal("expected unique lower-priority request to contribute its error")
	}
	if failure.SampleSize != 2 {
		t.Fatalf("model request sample size = %d, want 2", failure.SampleSize)
	}
}

func TestInsightEngineDeduplicatesToolCallBySessionAndCallID(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "insights.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Minute)

	for index, source := range []string{"transcript", "codex.otel"} {
		envelope := core.ObservationEnvelope{
			EventID: core.NewObservationEventID(), Time: base.Add(time.Duration(index) * time.Second), TraceID: core.NewObservationTraceID(), SpanID: core.NewObservationSpanID(),
			Kind: "tool.call", Lifecycle: core.ObservationLifecycleEnd, AgentID: "agent-a", RuntimeID: "codex",
			SessionID: "session-a", Source: source, Status: core.ObservationStatusError,
			Tool: &core.ObservationTool{Name: "bash", CallID: "call-shared"},
		}
		if err := st.RecordObservation(ctx, envelope); err != nil {
			t.Fatal(err)
		}
	}

	insights, err := NewInsightEngine(st).Run(ctx, base.Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	failure := findInsight(insights, "tool_failure_rate")
	if failure == nil {
		t.Fatal("expected tool failure insight")
	}
	if failure.SampleSize != 1 {
		t.Fatalf("duplicate tool call counted across traces: sample size = %d, want 1", failure.SampleSize)
	}
}

func recordInsightModelSpan(t *testing.T, st *store.Store, at time.Time, traceID, source, requestID, status string) {
	t.Helper()
	endedAt := at.Add(10 * time.Millisecond)
	runtimeID := "codex"
	if strings.Contains(source, "otel") {
		runtimeID = "codex_cli"
	}
	envelope := core.ObservationEnvelope{
		EventID: core.NewObservationEventID(), Time: endedAt, TraceID: traceID, SpanID: core.NewObservationSpanID(),
		Kind: "model.request", Lifecycle: core.ObservationLifecycleEnd, AgentID: "agent-a", RuntimeID: runtimeID,
		SessionID: "session-a", TurnID: "turn-a", Source: source, Status: status,
		Model: &core.ObservationModel{RequestID: requestID, Attempt: 1, Resolved: "gpt-test"},
		Usage: &core.ObservationUsage{InputTokens: 100, OutputTokens: 10},
	}
	if err := st.RecordObservation(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
}

func findInsight(insights []store.ObservationInsight, ruleID string) *store.ObservationInsight {
	for i := range insights {
		if insights[i].RuleID == ruleID {
			return &insights[i]
		}
	}
	return nil
}

func TestPercentile95(t *testing.T) {
	values := []int64{1, 2, 3, 4, 100}
	if got := percentile95(values); got != 100 {
		t.Fatalf("p95 = %d", got)
	}
}
