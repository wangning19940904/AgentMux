package observability

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func (t *TranscriptTailer) parseCodexLine(item transcriptFile, line []byte, offset int64, fallback time.Time, state *transcriptCursorState) ([]core.ObservationEnvelope, string, time.Time) {
	var raw map[string]any
	if json.Unmarshal(line, &raw) != nil {
		return nil, "", fallback.UTC()
	}
	timestamp := parseTranscriptTime(mapString(raw, "timestamp"), fallback)
	typeName := mapString(raw, "type")
	payload := mapObject(raw, "payload")
	payloadType := mapString(payload, "type")
	messageID := firstNonBlank(mapString(payload, "id", "turn_id", "call_id", "client_id"), mapString(raw, "id"))
	if messageID == "" {
		messageID = stableHex(string(line), 16)
	}
	if typeName == "session_meta" {
		state.SessionID = firstNonBlank(mapString(payload, "id", "session_id"), state.SessionID, strings.TrimSuffix(filepath.Base(item.Path), ".jsonl"))
		state.Cwd = firstNonBlank(mapString(payload, "cwd"), state.Cwd)
		state.Model = firstNonBlank(mapString(payload, "model"), state.Model)
		return nil, messageID, timestamp
	}
	if model := firstNonBlank(mapString(payload, "model"), mapString(raw, "model")); model != "" {
		state.Model = model
	}
	if state.SessionID == "" {
		state.SessionID = strings.TrimSuffix(filepath.Base(item.Path), ".jsonl")
	}
	if (typeName == "event_msg" && payloadType == "task_started") || typeName == "turn_context" {
		turnID := firstNonBlank(mapString(payload, "turn_id"), messageID)
		return []core.ObservationEnvelope{t.startCodexTurn(item, state, turnID, messageID, timestamp, offset)}, messageID, timestamp
	}
	if state.TraceID == "" {
		turnID := firstNonBlank(mapString(payload, "turn_id"), "backfill-"+state.SessionID)
		t.startCodexTurn(item, state, turnID, messageID, timestamp, offset)
	}
	if tokenCount := mapObject(raw, "token_count"); tokenCount != nil {
		return t.codexTokenNumbersEvent(item, parseCodexTokens(tokenCount), codexTokenNumbers{}, messageID, messageID, timestamp, offset, state), messageID, timestamp
	}
	if typeName == "event_msg" && payloadType == "token_count" {
		return t.codexTokenEvent(item, payload, messageID, timestamp, offset, state), messageID, timestamp
	}
	if typeName == "event_msg" && payloadType == "user_message" {
		content := firstNonNil(payload["message"], payload["text"])
		if content == nil {
			return nil, messageID, timestamp
		}
		envelope := t.baseTranscriptEnvelope(item, state, messageID, timestamp, offset, stableHex("codex:input:"+state.SessionID+":"+messageID, 8), state.RootSpan, "agent.input", "Codex input", core.ObservationLifecycleEvent, core.ObservationStatusOK)
		envelope.Content = jsonObservationContent(content)
		return []core.ObservationEnvelope{envelope}, messageID, timestamp
	}
	if typeName == "event_msg" && (payloadType == "task_complete" || payloadType == "task_completed" || payloadType == "turn_completed") {
		status := core.ObservationStatusOK
		if mapString(payload, "status") == "failed" {
			status = core.ObservationStatusError
		}
		envelope := t.baseTranscriptEnvelope(item, state, messageID, timestamp, offset, state.RootSpan, "", "agent.turn", "Codex turn", core.ObservationLifecycleEnd, status)
		if response := firstNonNil(payload["last_agent_message"], payload["message"]); response != nil {
			envelope.Content = jsonObservationContent(response)
		}
		return []core.ObservationEnvelope{envelope}, messageID, timestamp
	}
	if typeName != "response_item" {
		return nil, messageID, timestamp
	}
	return t.codexResponseItemEvents(item, payload, messageID, timestamp, offset, state), messageID, timestamp
}

func (t *TranscriptTailer) startCodexTurn(item transcriptFile, state *transcriptCursorState, turnID, messageID string, timestamp time.Time, offset int64) core.ObservationEnvelope {
	state.TurnID = turnID
	state.TraceID = stableHex("codex:trace:"+state.SessionID+":"+turnID, 16)
	state.RootSpan = stableHex("codex:root:"+state.TraceID, 8)
	return t.baseTranscriptEnvelope(item, state, messageID, timestamp, offset, state.RootSpan, "", "agent.turn", "Codex turn", core.ObservationLifecycleStart, core.ObservationStatusRunning)
}

func (t *TranscriptTailer) codexTokenEvent(item transcriptFile, payload map[string]any, messageID string, timestamp time.Time, offset int64, state *transcriptCursorState) []core.ObservationEnvelope {
	info := mapObject(payload, "info")
	current := parseCodexTokens(mapObject(info, "total_token_usage"))
	if current.zero() {
		current = parseCodexTokens(mapObject(payload, "token_count"))
	}
	if current.zero() {
		return nil
	}
	return t.codexTokenNumbersEvent(item, current, parseCodexTokens(mapObject(info, "last_token_usage")), mapString(payload, "request_id"), messageID, timestamp, offset, state)
}

func (t *TranscriptTailer) codexTokenNumbersEvent(item transcriptFile, current, last codexTokenNumbers, requestID, messageID string, timestamp time.Time, offset int64, state *transcriptCursorState) []core.ObservationEnvelope {
	if current.zero() {
		return nil
	}
	delta, reset := current.delta(state.Previous, state.HaveUsage)
	if reset && !last.zero() {
		delta = last
	}
	state.Previous, state.HaveUsage = current, true
	if delta.zero() {
		return nil
	}
	// token_count is emitted after each request and contains session-cumulative
	// totals. Give each non-zero delta its own request span; using one span per
	// turn would cause the recorder's per-span MAX aggregation to drop deltas.
	requestID = firstNonBlank(requestID, messageID)
	spanID := stableHex("codex:model:"+state.SessionID+":"+requestID, 8)
	// token_count is emitted after one model request and its delta is final for
	// that request. Close the synthetic span so realtime Usage materialization
	// includes transcript-only Codex sessions.
	envelope := t.baseTranscriptEnvelope(item, state, messageID, timestamp, offset, spanID, state.RootSpan, "model.request", "Codex model request", core.ObservationLifecycleEnd, core.ObservationStatusOK)
	envelope.Model = &core.ObservationModel{Provider: "openai", Resolved: state.Model, RequestID: requestID}
	envelope.Usage = delta.observationUsage()
	return []core.ObservationEnvelope{envelope}
}

func (t *TranscriptTailer) codexResponseItemEvents(item transcriptFile, payload map[string]any, messageID string, timestamp time.Time, offset int64, state *transcriptCursorState) []core.ObservationEnvelope {
	switch mapString(payload, "type") {
	case "function_call", "custom_tool_call", "computer_call":
		callID := firstNonBlank(mapString(payload, "call_id"), mapString(payload, "id"), messageID)
		name := firstNonBlank(mapString(payload, "name"), mapString(payload, "action"), "Codex tool")
		input := firstNonNil(payload["arguments"], payload["input"], payload["action"])
		encoded, _ := json.Marshal(input)
		envelope := t.baseTranscriptEnvelope(item, state, messageID, timestamp, offset, stableHex("codex:tool:"+state.SessionID+":"+callID, 8), state.RootSpan, "tool.call", name, core.ObservationLifecycleStart, core.ObservationStatusRunning)
		envelope.Tool = &core.ObservationTool{Name: name, CallID: callID, InputBytes: int64(len(encoded))}
		if input != nil {
			envelope.Content = jsonObservationContent(input)
		}
		return []core.ObservationEnvelope{envelope}
	case "function_call_output", "custom_tool_call_output", "computer_call_output":
		callID := firstNonBlank(mapString(payload, "call_id"), mapString(payload, "id"), messageID)
		output := firstNonNil(payload["output"], payload["result"])
		encoded, _ := json.Marshal(output)
		status := core.ObservationStatusOK
		if mapString(payload, "status") == "failed" {
			status = core.ObservationStatusError
		}
		envelope := t.baseTranscriptEnvelope(item, state, messageID, timestamp, offset, stableHex("codex:tool:"+state.SessionID+":"+callID, 8), state.RootSpan, "tool.call", "Codex tool", core.ObservationLifecycleEnd, status)
		envelope.Tool = &core.ObservationTool{CallID: callID, OutputBytes: int64(len(encoded))}
		if output != nil {
			envelope.Content = jsonObservationContent(output)
		}
		return []core.ObservationEnvelope{envelope}
	case "message":
		role := mapString(payload, "role")
		if role != "assistant" && role != "user" {
			return nil
		}
		content := codexPublicMessageContent(payload["content"])
		if content == nil {
			return nil
		}
		kind, name := "model.response", "Codex response"
		if role == "user" {
			kind, name = "agent.input", "Codex input"
		}
		envelope := t.baseTranscriptEnvelope(item, state, messageID, timestamp, offset, stableHex("codex:item:"+state.SessionID+":"+messageID, 8), state.RootSpan, kind, name, core.ObservationLifecycleEvent, core.ObservationStatusOK)
		envelope.Content = jsonObservationContent(content)
		return []core.ObservationEnvelope{envelope}
	case "reasoning":
		// Hidden chain-of-thought and encrypted_content are intentionally
		// ignored. Only the runtime's explicitly public summary is retained.
		summary := payload["summary"]
		if summary == nil {
			return nil
		}
		envelope := t.baseTranscriptEnvelope(item, state, messageID, timestamp, offset, stableHex("codex:reasoning-summary:"+state.SessionID+":"+messageID, 8), state.RootSpan, "model.reasoning_summary", "Codex reasoning summary", core.ObservationLifecycleEvent, core.ObservationStatusOK)
		envelope.Content = jsonObservationContent(summary)
		return []core.ObservationEnvelope{envelope}
	}
	return nil
}

type codexTokenNumbers struct {
	Input     int64 `json:"input_tokens"`
	Output    int64 `json:"output_tokens"`
	Cached    int64 `json:"cached_input_tokens"`
	Reasoning int64 `json:"reasoning_output_tokens"`
	Total     int64 `json:"total_tokens"`
}

func parseCodexTokens(raw map[string]any) codexTokenNumbers {
	return codexTokenNumbers{
		Input: mapInt64(raw, "input_tokens"), Output: mapInt64(raw, "output_tokens"),
		Cached: mapInt64(raw, "cached_input_tokens"), Reasoning: mapInt64(raw, "reasoning_output_tokens"),
		Total: mapInt64(raw, "total_tokens"),
	}
}

func (u codexTokenNumbers) zero() bool {
	return u.Input == 0 && u.Output == 0 && u.Cached == 0 && u.Reasoning == 0 && u.Total == 0
}

func (u codexTokenNumbers) delta(previous codexTokenNumbers, havePrevious bool) (codexTokenNumbers, bool) {
	if !havePrevious {
		return u, false
	}
	if u.Input < previous.Input || u.Output < previous.Output || u.Cached < previous.Cached || u.Reasoning < previous.Reasoning || u.Total < previous.Total {
		return u, true
	}
	return codexTokenNumbers{Input: u.Input - previous.Input, Output: u.Output - previous.Output, Cached: u.Cached - previous.Cached, Reasoning: u.Reasoning - previous.Reasoning, Total: u.Total - previous.Total}, false
}

func (u codexTokenNumbers) observationUsage() *core.ObservationUsage {
	input := u.Input - u.Cached
	if input < 0 {
		input = 0
	}
	total := u.Total
	if total == 0 {
		total = input + u.Cached + u.Output
	}
	return &core.ObservationUsage{InputTokens: input, OutputTokens: u.Output, CacheReadTokens: u.Cached, ReasoningTokens: u.Reasoning, TotalTokens: total}
}

func codexPublicMessageContent(value any) any {
	var result []any
	for _, block := range mapArray(value) {
		typeName := mapString(block, "type")
		if typeName == "input_text" || typeName == "output_text" || typeName == "text" {
			result = append(result, mapString(block, "text"))
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}
