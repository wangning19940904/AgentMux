package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/config"
	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/hookrelay"
	"github.com/wangning19940904/AgentMux/store"
)

func TestOTLPExporterDefaultsToMetadataAndRequiresPerExporterContentOptIn(t *testing.T) {
	st, err := store.OpenLegacySQLite(filepath.Join(t.TempDir(), "obs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	recorder, err := store.NewObservationRecorder(st, store.ObservationRecorderOptions{
		CaptureContent: true, MasterKey: bytes.Repeat([]byte{9}, 32), KnownSecrets: []string{"secret-key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var bodies [][]byte
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	defer collector.Close()
	exporters := []config.ObservabilityExporterConfig{
		{Name: "metadata", Type: "otlp_http", Protocol: "http/json", Enabled: true, Endpoint: collector.URL},
		{Name: "content", Type: "otlp_http", Protocol: "http/json", Enabled: true, Endpoint: collector.URL, IncludeContent: true},
	}
	service := NewExporterService(nil, st, recorder, exporters)
	pipeline := NewPipeline(recorder, service)
	envelope := core.ObservationEnvelope{
		TraceID: core.NewObservationTraceID(), SpanID: core.NewObservationSpanID(), Kind: "agent.turn",
		Lifecycle: core.ObservationLifecycleEnd, Status: core.ObservationStatusOK,
		Content: &core.ObservationContent{ContentType: "application/json", Data: []byte(`{"prompt":"hello world","Authorization":"Bearer secret-key"}`)},
	}
	if err := pipeline.Observe(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	for _, exporter := range exporters {
		if err := service.flush(context.Background(), exporter); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("collector requests = %d", len(bodies))
	}
	var metadataBody, contentBody []byte
	for _, body := range bodies {
		if bytes.Contains(body, []byte("agentmux.content")) {
			contentBody = body
		} else {
			metadataBody = body
		}
	}
	if len(metadataBody) == 0 || bytes.Contains(metadataBody, []byte("hello world")) || bytes.Contains(metadataBody, []byte("secret-key")) {
		t.Fatalf("metadata-only OTLP body leaked content: %s", metadataBody)
	}
	if !bytes.Contains(contentBody, []byte("hello world")) || !bytes.Contains(contentBody, []byte("[REDACTED]")) || bytes.Contains(contentBody, []byte("secret-key")) {
		t.Fatalf("opted-in OTLP body was not safely redacted: %s", contentBody)
	}
}

func TestOTLPExporterFallsBackToMetadataAfterContentExpires(t *testing.T) {
	st, err := store.OpenLegacySQLite(filepath.Join(t.TempDir(), "expired.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	recorder, err := store.NewObservationRecorder(st, store.ObservationRecorderOptions{
		CaptureContent: true, MasterKey: bytes.Repeat([]byte{8}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	var body []byte
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer collector.Close()
	service := NewExporterService(nil, st, recorder, nil)
	item := store.ObservationExportItem{Envelope: core.ObservationEnvelope{
		TraceID: core.NewObservationTraceID(), SpanID: core.NewObservationSpanID(), Kind: "agent.turn",
		PayloadRef: &core.ObservationPayloadRef{ID: "expired-payload"},
	}}
	if err := service.exportOne(context.Background(), config.ObservabilityExporterConfig{
		Name: "content", Endpoint: collector.URL, IncludeContent: true,
	}, item); err != nil {
		t.Fatal(err)
	}
	if len(body) == 0 || bytes.Contains(body, []byte("agentmux.content")) {
		t.Fatalf("expired payload should export metadata only: %s", body)
	}
}

func TestNativeHookSocketAndEncryptedSpoolReachObservationBus(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "amux-ingest-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	bus := core.NewObservationBus()
	received := make(chan core.ObservationEnvelope, 4)
	bus.Subscribe("capture", func(_ context.Context, envelope core.ObservationEnvelope) error {
		received <- envelope
		return nil
	})
	service := NewIngestService(nil, bus, home, "token")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := service.Start(ctx); err != nil {
		t.Fatal(err)
	}
	opts := hookrelay.DefaultOptions(home)
	opts.Source = "claude"
	payload := `{"hook_event_name":"UserPromptSubmit","session_id":"session-1","prompt":"hello"}`
	if delivery, err := hookrelay.Relay(context.Background(), strings.NewReader(payload), opts); err != nil || !delivery.Socket {
		t.Fatalf("socket delivery = %+v, %v", delivery, err)
	}
	select {
	case envelope := <-received:
		if envelope.Kind != "agent.turn" || envelope.Source != "hook.claude" || envelope.Content == nil {
			t.Fatalf("socket envelope = %+v", envelope)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for hook socket ingest")
	}
	cancel()
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(opts.SocketPath); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("ingest socket did not close")
		}
		time.Sleep(10 * time.Millisecond)
	}
	spooled, err := hookrelay.Relay(context.Background(), strings.NewReader(`{"hook_event_name":"SessionEnd","session_id":"session-1"}`), opts)
	if err != nil || !spooled.Spooled {
		t.Fatalf("spool delivery = %+v, %v", spooled, err)
	}
	service2 := NewIngestService(nil, bus, home, "token")
	if err := service2.ConsumeSpool(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case envelope := <-received:
		if envelope.Kind != "agent.session" || envelope.Lifecycle != core.ObservationLifecycleEnd {
			t.Fatalf("spool envelope = %+v", envelope)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for encrypted spool ingest")
	}
}

func TestOTLPJSONIngestJoinsParentTraceAndNeverKeepsHiddenReasoning(t *testing.T) {
	bus := core.NewObservationBus()
	var envelopes []core.ObservationEnvelope
	bus.Subscribe("capture", func(_ context.Context, envelope core.ObservationEnvelope) error {
		envelopes = append(envelopes, envelope)
		return nil
	})
	service := NewIngestService(nil, bus, t.TempDir(), "otel-token")
	attributes := func(values map[string]any) []map[string]any {
		out := make([]map[string]any, 0, len(values))
		for key, value := range values {
			encoded := map[string]any{"stringValue": value}
			if _, ok := value.(int); ok {
				encoded = map[string]any{"intValue": value}
			}
			out = append(out, map[string]any{"key": key, "value": encoded})
		}
		return out
	}
	payload := map[string]any{"resourceSpans": []any{map[string]any{
		"resource": map[string]any{"attributes": attributes(map[string]any{
			"agentmux.parent_trace_id": "11111111111111111111111111111111",
			"agentmux.parent_span_id":  "2222222222222222", "agentmux.runtime": "claude",
			"agentmux.session_id": "session-1", "agentmux.turn_id": "turn-1",
		})},
		"scopeSpans": []any{map[string]any{"spans": []any{map[string]any{
			"traceId": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "spanId": "bbbbbbbbbbbbbbbb", "name": "claude_code.llm_request",
			"startTimeUnixNano": "1783764000000000000", "endTimeUnixNano": "1783764001000000000",
			"attributes": attributes(map[string]any{
				"model": "claude-sonnet", "request_id": "request-1", "attempt": 1,
				"input_tokens": 100, "output_tokens": 20, "prompt": "public prompt",
				"thinking_content": "private chain of thought", "api_key": "must-not-survive",
			}),
		}}}},
	}}}
	raw, _ := json.Marshal(payload)
	request := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(raw))
	request.Header.Set("Authorization", "Bearer otel-token")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	service.HandleOTLPTraces(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("OTLP response = %d: %s", response.Code, response.Body.String())
	}
	if len(envelopes) != 2 {
		t.Fatalf("envelopes = %+v", envelopes)
	}
	end := envelopes[1]
	if end.TraceID != "11111111111111111111111111111111" || end.ParentSpanID != "2222222222222222" || end.Kind != "model.request" {
		t.Fatalf("correlation = %+v", end)
	}
	if end.Usage == nil || end.Usage.InputTokens != 100 || end.Model == nil || end.Model.RequestID != "request-1" {
		t.Fatalf("model usage = %+v", end)
	}
	if end.Content == nil || !bytes.Contains(end.Content.Data, []byte("public prompt")) ||
		bytes.Contains(end.Content.Data, []byte("private chain of thought")) || bytes.Contains(end.Content.Data, []byte("must-not-survive")) {
		t.Fatalf("content filtering = %s", end.Content.Data)
	}
	if _, ok := end.Attributes["prompt"]; ok {
		t.Fatal("prompt must not be persisted as plaintext attributes")
	}
}

func TestOTLPUsageNormalizesRuntimeCacheSemantics(t *testing.T) {
	attrs := map[string]any{
		"input_tokens": json.Number("100"), "output_tokens": json.Number("10"), "cached_input_tokens": json.Number("20"),
	}
	codex := otlpObservationUsage(attrs, "codex")
	if codex == nil || codex.InputTokens != 80 || codex.CacheReadTokens != 20 || codex.OutputTokens != 10 || codex.TotalTokens != 110 {
		t.Fatalf("codex usage = %+v", codex)
	}
	claude := otlpObservationUsage(attrs, "claude")
	if claude == nil || claude.InputTokens != 100 || claude.CacheReadTokens != 20 || claude.TotalTokens != 130 {
		t.Fatalf("claude usage = %+v", claude)
	}
}

func TestOTLPLongLivedRuntimeJoinsCurrentSessionTurn(t *testing.T) {
	bus := core.NewObservationBus()
	service := NewIngestService(nil, bus, t.TempDir(), "otel-token")
	var received []core.ObservationEnvelope
	bus.Subscribe("capture", func(_ context.Context, envelope core.ObservationEnvelope) error {
		received = append(received, envelope)
		return nil
	})
	for _, envelope := range []core.ObservationEnvelope{
		{TraceID: "11111111111111111111111111111111", SpanID: "2222222222222222", Kind: "agent.turn", Lifecycle: core.ObservationLifecycleStart, Source: "agentmux.internal", RuntimeID: "codex", SessionID: "thread-1", TurnID: "turn-1"},
		{TraceID: "11111111111111111111111111111111", SpanID: "3333333333333333", ParentSpanID: "2222222222222222", Kind: "agent.run", Lifecycle: core.ObservationLifecycleStart, Source: "agentmux.internal", RuntimeID: "codex", SessionID: "thread-1", TurnID: "turn-1"},
	} {
		if err := service.ObserveCorrelation(context.Background(), envelope); err != nil {
			t.Fatal(err)
		}
	}
	payload := `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"codex_cli"}},{"key":"session.id","value":{"stringValue":"thread-1"}}]},"scopeSpans":[{"spans":[{"traceId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","spanId":"bbbbbbbbbbbbbbbb","name":"codex.model","startTimeUnixNano":"1783764000000000000","endTimeUnixNano":"1783764001000000000"}]}]}]}`
	request := httptest.NewRequest(http.MethodPost, "/v1/traces", strings.NewReader(payload))
	request.Header.Set("Authorization", "Bearer otel-token")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	service.HandleOTLPTraces(response, request)
	if response.Code != http.StatusOK || len(received) != 2 {
		t.Fatalf("response=%d spans=%+v body=%s", response.Code, received, response.Body.String())
	}
	if received[1].TraceID != "11111111111111111111111111111111" || received[1].ParentSpanID != "3333333333333333" || received[1].TurnID != "turn-1" {
		t.Fatalf("correlation = %+v", received[1])
	}
}

func TestNativeHookJoinsCurrentAgentMuxTurn(t *testing.T) {
	service := NewIngestService(nil, core.NewObservationBus(), t.TempDir(), "token")
	for _, envelope := range []core.ObservationEnvelope{
		{TraceID: "11111111111111111111111111111111", SpanID: "2222222222222222", Kind: "agent.turn", Source: "agentmux.internal", RuntimeID: "claude", SessionID: "session-1", TurnID: "turn-1"},
		{TraceID: "11111111111111111111111111111111", SpanID: "3333333333333333", Kind: "agent.run", Source: "agentmux.internal", RuntimeID: "claude", SessionID: "session-1", TurnID: "turn-1"},
	} {
		if err := service.ObserveCorrelation(context.Background(), envelope); err != nil {
			t.Fatal(err)
		}
	}
	envelope, err := service.hookEnvelope(hookrelay.Message{
		Version: 1, Source: "claude", ReceivedAt: time.Now().UTC(),
		Payload: json.RawMessage(`{"hook_event_name":"UserPromptSubmit","session_id":"session-1","prompt":"hello"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if envelope.TraceID != "11111111111111111111111111111111" || envelope.ParentSpanID != "3333333333333333" || envelope.TurnID != "turn-1" || envelope.Kind != "hook.run" {
		t.Fatalf("hook correlation = %+v", envelope)
	}
}

func TestVersionedEnvelopeHTTPIngestCarriesEphemeralContent(t *testing.T) {
	bus := core.NewObservationBus()
	received := make(chan core.ObservationEnvelope, 1)
	bus.Subscribe("capture", func(_ context.Context, envelope core.ObservationEnvelope) error {
		received <- envelope
		return nil
	})
	service := NewIngestService(nil, bus, t.TempDir(), "ingest-token")
	wire := `{"version":"v1","trace_id":"11111111111111111111111111111111","span_id":"2222222222222222","kind":"agent.turn","content_type":"application/json","content":{"prompt":"hello"}}`
	request := httptest.NewRequest(http.MethodPost, "/ingest", strings.NewReader(wire))
	request.Header.Set("Authorization", "Bearer ingest-token")
	response := httptest.NewRecorder()
	service.HandleHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("response = %d: %s", response.Code, response.Body.String())
	}
	envelope := <-received
	if envelope.Content == nil || !bytes.Contains(envelope.Content.Data, []byte(`"prompt":"hello"`)) {
		t.Fatalf("envelope content = %+v", envelope.Content)
	}
}
