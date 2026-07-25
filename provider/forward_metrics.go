package provider

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

const proxyCaptureLimit = 2 << 20

type proxyRequestIdentity struct {
	RequestID    string
	TraceID      string
	ParentSpanID string
}

type proxyAttemptResult struct {
	OK               bool
	Retryable        bool
	StatusCode       int
	Upstream         string
	Err              error
	StartedAt        time.Time
	TTFT             time.Duration
	Duration         time.Duration
	RequestBytes     int64
	ResponseBytes    int64
	StreamComplete   bool
	FinishReason     string
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	ResponseBody     []byte
}

// proxyCaptureReader records timing and a bounded response prefix while the
// original response continues streaming without buffering or backpressure.
type proxyCaptureReader struct {
	r       io.Reader
	start   time.Time
	first   time.Time
	bytes   int64
	capture bytes.Buffer
}

func (r *proxyCaptureReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 {
		if r.first.IsZero() {
			r.first = time.Now()
		}
		r.bytes += int64(n)
		remaining := proxyCaptureLimit - r.capture.Len()
		if remaining > 0 {
			if n < remaining {
				remaining = n
			}
			_, _ = r.capture.Write(p[:remaining])
		}
	}
	return n, err
}

func (r *proxyCaptureReader) apply(result *proxyAttemptResult, proto string, stream bool) {
	if result == nil {
		return
	}
	if !r.first.IsZero() {
		result.TTFT = r.first.Sub(r.start)
	}
	result.ResponseBytes = r.bytes
	result.ResponseBody = append([]byte(nil), r.capture.Bytes()...)
	metrics := parseProxyResponseMetrics(proto, result.ResponseBody, stream)
	result.InputTokens = metrics.inputTokens
	result.OutputTokens = metrics.outputTokens
	result.CacheReadTokens = metrics.cacheReadTokens
	result.CacheWriteTokens = metrics.cacheWriteTokens
	result.FinishReason = metrics.finishReason
	result.StreamComplete = !stream || metrics.streamComplete
}

type proxyResponseMetrics struct {
	inputTokens      int64
	outputTokens     int64
	cacheReadTokens  int64
	cacheWriteTokens int64
	finishReason     string
	streamComplete   bool
}

func parseProxyResponseMetrics(proto string, raw []byte, stream bool) proxyResponseMetrics {
	var out proxyResponseMetrics
	objects := proxyResponseObjects(raw, stream)
	for _, object := range objects {
		for _, usage := range proxyUsageMaps(object) {
			out.inputTokens = max64(out.inputTokens,
				int64Value(usage["input_tokens"]), int64Value(usage["prompt_tokens"]))
			out.outputTokens = max64(out.outputTokens,
				int64Value(usage["output_tokens"]), int64Value(usage["completion_tokens"]))
			out.cacheReadTokens = max64(out.cacheReadTokens,
				int64Value(usage["cache_read_input_tokens"]),
				int64Value(nestedAnyMap(usage, "input_tokens_details")["cached_tokens"]))
			out.cacheWriteTokens = max64(out.cacheWriteTokens,
				int64Value(usage["cache_creation_input_tokens"]))
		}
		if reason := proxyFinishReason(object); reason != "" {
			out.finishReason = reason
		}
		if proxyObjectCompleted(object) {
			out.streamComplete = true
		}
	}
	text := string(raw)
	if strings.Contains(text, "data: [DONE]") || strings.Contains(text, `"type":"message_stop"`) ||
		strings.Contains(text, `"type": "message_stop"`) || strings.Contains(text, `"type":"response.completed"`) ||
		strings.Contains(text, `"type": "response.completed"`) {
		out.streamComplete = true
	}
	// OpenAI reports cached tokens as a subset of input_tokens, while
	// Anthropic reports cache-read/cache-write tokens beside uncached input.
	// Normalize to AgentMux's mutually exclusive billing buckets.
	if (proto == protoResponses || proto == protoOpenAIChat) && out.cacheReadTokens > 0 {
		out.inputTokens -= out.cacheReadTokens
		if out.inputTokens < 0 {
			out.inputTokens = 0
		}
	}
	return out
}

func proxyResponseObjects(raw []byte, stream bool) []map[string]any {
	var out []map[string]any
	if !stream {
		var object map[string]any
		if json.Unmarshal(raw, &object) == nil && object != nil {
			out = append(out, object)
		}
		return out
	}
	for _, line := range bytes.Split(raw, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		line = bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(line) == 0 || bytes.Equal(line, []byte("[DONE]")) {
			continue
		}
		var object map[string]any
		if json.Unmarshal(line, &object) == nil && object != nil {
			out = append(out, object)
		}
	}
	return out
}

func proxyUsageMaps(object map[string]any) []map[string]any {
	var out []map[string]any
	if usage := nestedAnyMap(object, "usage"); usage != nil {
		out = append(out, usage)
	}
	for _, key := range []string{"message", "response", "delta"} {
		if usage := nestedAnyMap(nestedAnyMap(object, key), "usage"); usage != nil {
			out = append(out, usage)
		}
	}
	return out
}

func proxyFinishReason(object map[string]any) string {
	for _, key := range []string{"stop_reason", "finish_reason", "status"} {
		if value := stringValue(object[key]); value != "" && value != "in_progress" {
			return value
		}
	}
	for _, key := range []string{"message", "response", "delta"} {
		nested := nestedAnyMap(object, key)
		for _, reasonKey := range []string{"stop_reason", "finish_reason", "status"} {
			if value := stringValue(nested[reasonKey]); value != "" && value != "in_progress" {
				return value
			}
		}
	}
	if choices, ok := object["choices"].([]any); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]any); ok {
			return stringValue(choice["finish_reason"])
		}
	}
	return ""
}

func proxyObjectCompleted(object map[string]any) bool {
	if typ := stringValue(object["type"]); typ == "message_stop" || typ == "response.completed" {
		return true
	}
	if status := stringValue(object["status"]); status == "completed" || status == "failed" || status == "incomplete" {
		return true
	}
	response := nestedAnyMap(object, "response")
	status := stringValue(response["status"])
	return status == "completed" || status == "failed" || status == "incomplete"
}

func nestedAnyMap(object map[string]any, key string) map[string]any {
	if object == nil {
		return nil
	}
	nested, _ := object[key].(map[string]any)
	return nested
}

func int64Value(value any) int64 {
	switch v := value.(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	case json.Number:
		n, _ := v.Int64()
		return n
	default:
		return 0
	}
}

func max64(values ...int64) int64 {
	var max int64
	for _, value := range values {
		if value > max {
			max = value
		}
	}
	return max
}

func proxyRequestIdentityFrom(r *http.Request) proxyRequestIdentity {
	requestID := firstTraceValue(r.Header.Get("x-request-id"), r.Header.Get("request-id"))
	if requestID == "" {
		requestID = randomProxyID("preq-")
	}
	traceID := firstTraceValue(r.Header.Get("x-agentmux-trace-id"), traceIDFromTraceparent(r.Header.Get("traceparent")))
	if traceID == "" {
		traceID = randomProxyTraceID()
	}
	return proxyRequestIdentity{RequestID: requestID, TraceID: traceID, ParentSpanID: spanIDFromTraceparent(r.Header.Get("traceparent"))}
}

func randomProxyTraceID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err == nil {
		return hex.EncodeToString(buf)
	}
	digest := sha256.Sum256([]byte(randomProxyID("trace")))
	return hex.EncodeToString(digest[:16])
}

func traceIDFromTraceparent(value string) string {
	parts := strings.Split(strings.TrimSpace(value), "-")
	if len(parts) >= 4 && len(parts[1]) == 32 {
		return parts[1]
	}
	return ""
}

func spanIDFromTraceparent(value string) string {
	parts := strings.Split(strings.TrimSpace(value), "-")
	if len(parts) >= 4 && len(parts[2]) == 16 {
		return parts[2]
	}
	return ""
}

func randomProxyID(prefix string) string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err == nil {
		return prefix + hex.EncodeToString(buf)
	}
	return prefix + time.Now().UTC().Format("20060102150405.000000000")
}
