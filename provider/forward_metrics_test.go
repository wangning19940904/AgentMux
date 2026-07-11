package provider

import (
	"net/http/httptest"
	"testing"
)

func TestParseProxyResponseMetricsJSON(t *testing.T) {
	raw := []byte(`{"id":"resp_1","status":"completed","usage":{"input_tokens":120,"output_tokens":30,"input_tokens_details":{"cached_tokens":80}}}`)
	got := parseProxyResponseMetrics(protoResponses, raw, false)
	if got.inputTokens != 40 || got.outputTokens != 30 || got.cacheReadTokens != 80 {
		t.Fatalf("metrics = %#v", got)
	}
	if got.finishReason != "completed" || !got.streamComplete {
		t.Fatalf("completion = reason %q complete=%t", got.finishReason, got.streamComplete)
	}
}

func TestParseProxyResponseMetricsAnthropicStream(t *testing.T) {
	raw := []byte("event: message_start\n" +
		`data: {"type":"message_start","message":{"usage":{"input_tokens":42,"cache_read_input_tokens":12}}}` + "\n\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":9}}` + "\n\n" +
		`data: {"type":"message_stop"}` + "\n\n")
	got := parseProxyResponseMetrics(protoAnthropic, raw, true)
	if got.inputTokens != 42 || got.outputTokens != 9 || got.cacheReadTokens != 12 {
		t.Fatalf("metrics = %#v", got)
	}
	if got.finishReason != "end_turn" || !got.streamComplete {
		t.Fatalf("completion = reason %q complete=%t", got.finishReason, got.streamComplete)
	}
}

func TestProxyRequestIdentityUsesTraceparent(t *testing.T) {
	r := httptest.NewRequest("POST", "http://localhost/v1/responses", nil)
	r.Header.Set("x-request-id", "request-1")
	r.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	got := proxyRequestIdentityFrom(r)
	if got.RequestID != "request-1" || got.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("identity = %#v", got)
	}
}
