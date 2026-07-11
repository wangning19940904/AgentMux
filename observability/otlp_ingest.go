package observability

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/agentnexus/agentnexus/core"
)

// HandleOTLPTraces accepts the OTLP/HTTP JSON encoding used by the private
// Claude subprocess configuration. Protobuf is intentionally not advertised;
// launched children are pinned to http/json so the local receiver stays small.
func (s *IngestService) HandleOTLPTraces(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if contentType := strings.ToLower(r.Header.Get("Content-Type")); contentType != "" && !strings.Contains(contentType, "json") {
		http.Error(w, "only OTLP/HTTP JSON is supported", http.StatusUnsupportedMediaType)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, (64<<20)+1))
	if err != nil || len(body) > 64<<20 {
		http.Error(w, "invalid OTLP payload", http.StatusBadRequest)
		return
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	var request map[string]any
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid OTLP JSON", http.StatusBadRequest)
		return
	}
	if err := s.ingestOTLPTraceRequest(r, request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{}`))
}

func (s *IngestService) authorized(r *http.Request) bool {
	provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if provided == "" {
		provided = r.Header.Get("X-AgentNexus-Token")
	}
	return s.token != "" && subtle.ConstantTimeCompare([]byte(provided), []byte(s.token)) == 1
}

func (s *IngestService) ingestOTLPTraceRequest(r *http.Request, request map[string]any) error {
	resourceSpans := otlpSlice(request["resourceSpans"])
	for _, resourceItem := range resourceSpans {
		resourceSpan := otlpMap(resourceItem)
		resource := otlpMap(resourceSpan["resource"])
		resourceAttrs := otlpAttributes(resource["attributes"])
		scopeSpans := append(otlpSlice(resourceSpan["scopeSpans"]), otlpSlice(resourceSpan["instrumentationLibrarySpans"])...)
		for _, scopeItem := range scopeSpans {
			scope := otlpMap(scopeItem)
			for _, spanItem := range otlpSlice(scope["spans"]) {
				if err := s.ingestOTLPSpan(r, resourceAttrs, otlpMap(spanItem)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (s *IngestService) ingestOTLPSpan(r *http.Request, resource, span map[string]any) error {
	attrs := make(map[string]any, len(resource)+16)
	for key, value := range resource {
		attrs[key] = value
	}
	for key, value := range otlpAttributes(span["attributes"]) {
		attrs[key] = value
	}
	runtimeID := firstOTLPAttribute(attrs, "agentnexus.runtime", "service.name")
	sessionID := firstOTLPAttribute(attrs, "agentnexus.session_id", "session.id", "session_id", "gen_ai.conversation.id", "conversation.id", "thread.id", "thread_id")
	correlation := s.traceForSession(runtimeID, sessionID)
	incomingTraceID := otlpStringValue(span["traceId"])
	traceID := otlpAttrString(attrs, "agentnexus.parent_trace_id")
	if traceID == "" {
		traceID = correlation.traceID
	}
	if traceID == "" {
		traceID = incomingTraceID
	}
	s.registerTraceAlias(incomingTraceID, traceID)
	spanID := otlpStringValue(span["spanId"])
	if traceID == "" || spanID == "" {
		return fmt.Errorf("OTLP span is missing traceId or spanId")
	}
	parentID := otlpStringValue(span["parentSpanId"])
	if parentID == "" {
		parentID = otlpAttrString(attrs, "agentnexus.parent_span_id")
	}
	if parentID == "" {
		parentID = correlation.parentSpanID
	}
	name := otlpStringValue(span["name"])
	kind := otlpObservationKind(name, attrs)
	started := otlpUnixNano(span["startTimeUnixNano"])
	ended := otlpUnixNano(span["endTimeUnixNano"])
	if started.IsZero() {
		started = time.Now().UTC()
	}
	if ended.IsZero() || ended.Before(started) {
		ended = started
	}
	source := "otel"
	if runtimeID != "" {
		source += "." + strings.ToLower(runtimeID)
	}
	status := core.ObservationStatusOK
	if otlpStatusError(span["status"]) || !otlpAttrBoolDefault(attrs, "success", true) {
		status = core.ObservationStatusError
	}
	model := otlpObservationModel(kind, attrs, ended.Sub(started))
	tool := otlpObservationTool(kind, attrs, ended.Sub(started))
	usage := otlpObservationUsage(attrs, runtimeID)
	safeAttrs, content := splitOTLPAttributes(attrs, otlpSlice(span["events"]))
	base := core.ObservationEnvelope{
		TraceID: traceID, SpanID: spanID, ParentSpanID: parentID, Kind: kind, Name: name,
		AgentID:   firstOTLPAttribute(attrs, "agentnexus.agent_id", "agent_id"),
		RuntimeID: runtimeID, SessionID: sessionID,
		TurnID: firstNonBlank(firstOTLPAttribute(attrs, "agentnexus.turn_id", "turn.id", "turn_id"), correlation.turnID),
		Source: source, Provenance: []string{"native_otel", "otlp_http_json"},
		Quality: core.ObservationQualityComplete, Model: model, Tool: tool, Attributes: safeAttrs,
	}
	start := base
	start.EventID = "obs_" + stableHex("otel:start:"+incomingTraceID+":"+spanID, 16)
	start.Time = started
	start.DedupeKey = "otel:" + incomingTraceID + ":" + spanID + ":start"
	start.Lifecycle = core.ObservationLifecycleStart
	start.Status = core.ObservationStatusRunning
	if err := s.bus.Publish(r.Context(), start); err != nil {
		return err
	}
	end := base
	end.EventID = "obs_" + stableHex("otel:end:"+incomingTraceID+":"+spanID, 16)
	end.Time = ended
	end.DedupeKey = "otel:" + incomingTraceID + ":" + spanID + ":end"
	end.Lifecycle = core.ObservationLifecycleEnd
	end.Status = status
	end.Usage = usage
	end.Content = content
	if status == core.ObservationStatusError {
		end.Error = &core.ObservationError{Code: "native_otel_span_failed", Message: "Native runtime span failed"}
	}
	return s.bus.Publish(r.Context(), end)
}

func otlpObservationKind(name string, attrs map[string]any) string {
	value := strings.ToLower(firstNonBlank(name, otlpAttrString(attrs, "span.type")))
	switch {
	case strings.Contains(value, "llm_request"), strings.Contains(value, "model"):
		return "model.request"
	case strings.Contains(value, "tool"):
		return "tool.call"
	case strings.Contains(value, "hook"):
		return "hook.run"
	case strings.Contains(value, "permission"), strings.Contains(value, "decision"):
		return "permission"
	case strings.Contains(value, "compact"):
		return "compaction"
	case strings.Contains(value, "subagent"):
		return "subagent.run"
	default:
		return "agent.turn"
	}
}

func otlpObservationModel(kind string, attrs map[string]any, duration time.Duration) *core.ObservationModel {
	if kind != "model.request" {
		return nil
	}
	return &core.ObservationModel{
		Provider:     firstOTLPAttribute(attrs, "gen_ai.system", "provider"),
		Requested:    firstOTLPAttribute(attrs, "gen_ai.request.model", "model"),
		Resolved:     firstOTLPAttribute(attrs, "gen_ai.response.model", "model"),
		RequestID:    firstOTLPAttribute(attrs, "gen_ai.response.id", "request_id", "client_request_id"),
		Attempt:      int(otlpAttrInt(attrs, "attempt")),
		FinishReason: firstOTLPAttribute(attrs, "stop_reason", "finish_reason"),
		TTFTMillis:   otlpAttrInt(attrs, "ttft_ms"), DurationMillis: firstPositiveOTLP(otlpAttrInt(attrs, "duration_ms"), duration.Milliseconds()),
	}
}

func otlpObservationTool(kind string, attrs map[string]any, duration time.Duration) *core.ObservationTool {
	if kind != "tool.call" {
		return nil
	}
	return &core.ObservationTool{
		Name:           firstOTLPAttribute(attrs, "tool_name", "tool.name", "gen_ai.tool.name"),
		CallID:         firstOTLPAttribute(attrs, "tool_use_id", "gen_ai.tool.call.id"),
		DurationMillis: firstPositiveOTLP(otlpAttrInt(attrs, "duration_ms"), duration.Milliseconds()),
		InputBytes:     otlpAttrInt(attrs, "tool_input_size_bytes"), OutputBytes: otlpAttrInt(attrs, "tool_result_size_bytes"),
	}
}

func otlpObservationUsage(attrs map[string]any, runtimeID string) *core.ObservationUsage {
	usage := &core.ObservationUsage{
		InputTokens:      otlpAttrInt(attrs, "input_tokens", "gen_ai.usage.input_tokens"),
		OutputTokens:     otlpAttrInt(attrs, "output_tokens", "gen_ai.usage.output_tokens"),
		CacheReadTokens:  otlpAttrInt(attrs, "cache_read_tokens", "cached_input_tokens", "gen_ai.usage.cached_input_tokens"),
		CacheWriteTokens: otlpAttrInt(attrs, "cache_creation_tokens", "cache_write_tokens"),
		ReasoningTokens:  otlpAttrInt(attrs, "reasoning_tokens", "reasoning_output_tokens"),
		CostUSD:          otlpAttrFloat(attrs, "cost_usd"), Cumulative: true,
	}
	// Codex/OpenAI totals include cached input inside input_tokens; Claude's
	// native telemetry reports cache buckets alongside uncached input.
	if strings.Contains(strings.ToLower(runtimeID), "codex") && usage.CacheReadTokens > 0 {
		usage.InputTokens -= usage.CacheReadTokens
		if usage.InputTokens < 0 {
			usage.InputTokens = 0
		}
	}
	usage.TotalTokens = otlpAttrInt(attrs, "total_tokens", "gen_ai.usage.total_tokens")
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.InputTokens + usage.OutputTokens + usage.CacheReadTokens + usage.CacheWriteTokens
	}
	if usage.TotalTokens == 0 && usage.CacheReadTokens == 0 && usage.CacheWriteTokens == 0 {
		return nil
	}
	return usage
}

func splitOTLPAttributes(attrs map[string]any, events []any) (map[string]any, *core.ObservationContent) {
	safe := map[string]any{}
	content := map[string]any{}
	for key, value := range attrs {
		lower := strings.ToLower(key)
		switch {
		case otlpHiddenReasoningKey(lower):
			continue
		case otlpContentKey(lower):
			content[key] = value
		case otlpSensitiveAttributeKey(lower):
			continue
		default:
			safe[key] = value
		}
	}
	for _, eventValue := range events {
		event := otlpMap(eventValue)
		if otlpHiddenReasoningKey(strings.ToLower(otlpStringValue(event["name"]))) {
			continue
		}
		eventAttrs := otlpAttributes(event["attributes"])
		public := map[string]any{"name": otlpStringValue(event["name"])}
		for key, value := range eventAttrs {
			lower := strings.ToLower(key)
			if !otlpHiddenReasoningKey(lower) && !otlpSensitiveAttributeKey(lower) {
				public[key] = value
			}
		}
		if len(public) > 1 {
			content["event:"+otlpStringValue(event["name"])] = public
		}
	}
	if len(content) == 0 {
		return safe, nil
	}
	raw, _ := json.Marshal(content)
	return safe, &core.ObservationContent{ContentType: "application/json", Data: raw}
}

func otlpContentKey(key string) bool {
	for _, marker := range []string{"prompt", "response.model_output", "assistant_response", "tool_input", "tool.output", "tool_output", "tool_result", "tool_parameters", "full_command", "file_path", "body"} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return false
}

func otlpHiddenReasoningKey(key string) bool {
	return strings.Contains(key, "chain_of_thought") || strings.Contains(key, "reasoning") || strings.Contains(key, "thinking") || strings.Contains(key, "thought")
}

func otlpSensitiveAttributeKey(key string) bool {
	return strings.Contains(key, "authorization") || strings.Contains(key, "cookie") || strings.Contains(key, "api_key") ||
		strings.Contains(key, "apikey") || strings.Contains(key, "secret") || strings.Contains(key, "password") ||
		strings.Contains(key, "email") || strings.Contains(key, "raw_api")
}

func otlpAttributes(value any) map[string]any {
	result := map[string]any{}
	for _, item := range otlpSlice(value) {
		attribute := otlpMap(item)
		key := otlpStringValue(attribute["key"])
		if key != "" {
			result[key] = decodeOTLPAnyValue(attribute["value"])
		}
	}
	return result
}

func decodeOTLPAnyValue(value any) any {
	object := otlpMap(value)
	for _, key := range []string{"stringValue", "intValue", "doubleValue", "boolValue", "bytesValue"} {
		if item, ok := object[key]; ok {
			return item
		}
	}
	if array := otlpMap(object["arrayValue"]); array != nil {
		values := otlpSlice(array["values"])
		result := make([]any, 0, len(values))
		for _, item := range values {
			result = append(result, decodeOTLPAnyValue(item))
		}
		return result
	}
	return value
}

func otlpStatusError(value any) bool {
	status := otlpMap(value)
	code := strings.ToLower(otlpStringValue(status["code"]))
	return code == "2" || strings.Contains(code, "error")
}

func otlpUnixNano(value any) time.Time {
	nanos, _ := strconv.ParseInt(otlpStringValue(value), 10, 64)
	if nanos <= 0 {
		return time.Time{}
	}
	return time.Unix(0, nanos).UTC()
}

func firstOTLPAttribute(attrs map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := otlpAttrString(attrs, key); value != "" {
			return value
		}
	}
	return ""
}

func otlpAttrString(attrs map[string]any, key string) string { return otlpStringValue(attrs[key]) }
func otlpAttrInt(attrs map[string]any, keys ...string) int64 {
	for _, key := range keys {
		if value := otlpStringValue(attrs[key]); value != "" {
			parsed, _ := strconv.ParseInt(value, 10, 64)
			return parsed
		}
	}
	return 0
}
func otlpAttrFloat(attrs map[string]any, key string) float64 {
	parsed, _ := strconv.ParseFloat(otlpStringValue(attrs[key]), 64)
	return parsed
}
func otlpAttrBoolDefault(attrs map[string]any, key string, fallback bool) bool {
	value := otlpStringValue(attrs[key])
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	return err == nil && parsed
}
func otlpStringValue(value any) string {
	switch item := value.(type) {
	case string:
		return item
	case json.Number:
		return item.String()
	case float64:
		return strconv.FormatFloat(item, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(item)
	default:
		return ""
	}
}
func otlpMap(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}
func otlpSlice(value any) []any {
	result, _ := value.([]any)
	return result
}
func firstPositiveOTLP(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}
