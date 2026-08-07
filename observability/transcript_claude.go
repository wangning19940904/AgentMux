package observability

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func claudeTranscriptClass(path string) string {
	normalized := filepath.ToSlash(path)
	if strings.Contains(normalized, "/subagents/workflows/") {
		if strings.EqualFold(filepath.Base(path), "journal.jsonl") {
			return "workflow"
		}
		return "workflow_subagent"
	}
	if strings.Contains(normalized, "/subagents/") {
		return "subagent"
	}
	return "main"
}

func (t *TranscriptTailer) parseClaudeLine(item transcriptFile, line []byte, offset int64, fallback time.Time, state *transcriptCursorState) ([]core.ObservationEnvelope, string, time.Time) {
	var raw map[string]any
	if json.Unmarshal(line, &raw) != nil {
		return nil, "", fallback.UTC()
	}
	timestamp := parseTranscriptTime(mapString(raw, "timestamp"), fallback)
	typeName := mapString(raw, "type")
	messageID := firstNonBlank(mapString(raw, "uuid", "key"), mapString(mapObject(raw, "message"), "id"))
	if messageID == "" {
		messageID = stableHex(string(line), 16)
	}
	if value := mapString(raw, "sessionId", "session_id"); value != "" {
		state.SessionID = value
	}
	if value := mapString(raw, "cwd"); value != "" {
		state.Cwd = value
	}
	if value := mapString(raw, "agentId", "agent_id"); value != "" {
		state.AgentID = value
	}
	if item.Class == "workflow" {
		return t.claudeWorkflowEvents(item, raw, messageID, timestamp, offset, state), messageID, timestamp
	}
	message := mapObject(raw, "message")
	role := firstNonBlank(mapString(message, "role"), typeName)
	if model := mapString(message, "model"); model != "" {
		state.Model = model
	}
	if typeName == "user" && !claudeOnlyToolResults(message["content"]) {
		turnID := firstNonBlank(mapString(raw, "promptId"), messageID)
		state.TurnID = turnID
		state.TraceID = stableHex("claude:trace:"+state.SessionID+":"+turnID+":"+state.AgentID, 16)
		state.RootSpan = stableHex("claude:root:"+state.TraceID, 8)
		kind, name := "agent.turn", "Claude turn"
		if item.Class != "main" {
			kind, name = "subagent.run", "Claude subagent"
		}
		envelope := t.baseTranscriptEnvelope(item, state, messageID, timestamp, offset, state.RootSpan, "", kind, name, core.ObservationLifecycleStart, core.ObservationStatusRunning)
		if content := claudePublicContent(message["content"], false); content != nil {
			envelope.Content = jsonObservationContent(content)
		}
		return []core.ObservationEnvelope{envelope}, messageID, timestamp
	}
	if state.TraceID == "" {
		state.TurnID = firstNonBlank(mapString(raw, "promptId"), "backfill-"+state.SessionID)
		state.TraceID = stableHex("claude:trace:"+state.SessionID+":"+state.TurnID+":"+state.AgentID, 16)
		state.RootSpan = stableHex("claude:root:"+state.TraceID, 8)
	}
	if typeName == "assistant" || role == "assistant" {
		return t.claudeAssistantEvents(item, raw, message, messageID, timestamp, offset, state), messageID, timestamp
	}
	if typeName == "user" {
		return t.claudeToolResultEvents(item, message, messageID, timestamp, offset, state), messageID, timestamp
	}
	return nil, messageID, timestamp
}

func (t *TranscriptTailer) claudeWorkflowEvents(item transcriptFile, raw map[string]any, messageID string, timestamp time.Time, offset int64, state *transcriptCursorState) []core.ObservationEnvelope {
	workflowID := filepath.Base(filepath.Dir(item.Path))
	agentID := mapString(raw, "agentId", "agent_id")
	state.AgentID = firstNonBlank(agentID, state.AgentID)
	state.SessionID = firstNonBlank(state.SessionID, workflowID)
	state.TurnID = workflowID
	state.TraceID = stableHex("claude:workflow:"+workflowID+":"+agentID, 16)
	state.RootSpan = stableHex("claude:workflow-root:"+state.TraceID, 8)
	lifecycle, status := core.ObservationLifecycleEvent, core.ObservationStatusUnset
	switch mapString(raw, "type") {
	case "started":
		lifecycle, status = core.ObservationLifecycleStart, core.ObservationStatusRunning
	case "result":
		lifecycle, status = core.ObservationLifecycleEnd, core.ObservationStatusOK
	case "failed", "error":
		lifecycle, status = core.ObservationLifecycleEnd, core.ObservationStatusError
	}
	envelope := t.baseTranscriptEnvelope(item, state, messageID, timestamp, offset, state.RootSpan, "", "subagent.run", "Claude workflow subagent", lifecycle, status)
	if lifecycle == core.ObservationLifecycleEnd {
		if result := raw["result"]; result != nil {
			envelope.Content = jsonObservationContent(result)
		}
	}
	return []core.ObservationEnvelope{envelope}
}

func (t *TranscriptTailer) claudeAssistantEvents(item transcriptFile, raw, message map[string]any, messageID string, timestamp time.Time, offset int64, state *transcriptCursorState) []core.ObservationEnvelope {
	requestID := firstNonBlank(mapString(raw, "requestId"), mapString(message, "id"), messageID)
	spanID := stableHex("claude:model:"+state.SessionID+":"+requestID, 8)
	stopReason := mapString(message, "stop_reason")
	lifecycle, status := core.ObservationLifecycleEvent, core.ObservationStatusRunning
	if stopReason != "" {
		lifecycle, status = core.ObservationLifecycleEnd, core.ObservationStatusOK
	}
	modelEvent := t.baseTranscriptEnvelope(item, state, messageID+":model", timestamp, offset, spanID, state.RootSpan, "model.request", "Claude model request", lifecycle, status)
	modelEvent.Model = &core.ObservationModel{Provider: "anthropic", Resolved: state.Model, RequestID: requestID, FinishReason: stopReason}
	modelEvent.Usage = claudeObservationUsage(mapObject(message, "usage"))
	if content := claudePublicContent(message["content"], true); content != nil {
		modelEvent.Content = jsonObservationContent(content)
	}
	events := []core.ObservationEnvelope{modelEvent}
	for index, block := range mapArray(message["content"]) {
		if mapString(block, "type") != "tool_use" {
			continue
		}
		callID := mapString(block, "id")
		name := mapString(block, "name")
		tool := t.baseTranscriptEnvelope(item, state, messageID+fmt.Sprintf(":tool:%d", index), timestamp, offset, stableHex("claude:tool:"+state.SessionID+":"+callID, 8), state.RootSpan, "tool.call", name, core.ObservationLifecycleStart, core.ObservationStatusRunning)
		input := block["input"]
		encoded, _ := json.Marshal(input)
		tool.Tool = &core.ObservationTool{Name: name, CallID: callID, InputBytes: int64(len(encoded))}
		if input != nil {
			tool.Content = jsonObservationContent(input)
		}
		events = append(events, tool)
	}
	return events
}

func (t *TranscriptTailer) claudeToolResultEvents(item transcriptFile, message map[string]any, messageID string, timestamp time.Time, offset int64, state *transcriptCursorState) []core.ObservationEnvelope {
	var events []core.ObservationEnvelope
	for index, block := range mapArray(message["content"]) {
		if mapString(block, "type") != "tool_result" {
			continue
		}
		callID := mapString(block, "tool_use_id")
		status := core.ObservationStatusOK
		if value, _ := block["is_error"].(bool); value {
			status = core.ObservationStatusError
		}
		content := block["content"]
		encoded, _ := json.Marshal(content)
		envelope := t.baseTranscriptEnvelope(item, state, messageID+fmt.Sprintf(":tool_result:%d", index), timestamp, offset, stableHex("claude:tool:"+state.SessionID+":"+callID, 8), state.RootSpan, "tool.call", "Claude tool", core.ObservationLifecycleEnd, status)
		envelope.Tool = &core.ObservationTool{CallID: callID, OutputBytes: int64(len(encoded))}
		if content != nil {
			envelope.Content = jsonObservationContent(content)
		}
		events = append(events, envelope)
	}
	return events
}

func claudeObservationUsage(raw map[string]any) *core.ObservationUsage {
	if raw == nil {
		return nil
	}
	usage := &core.ObservationUsage{
		InputTokens: mapInt64(raw, "input_tokens"), OutputTokens: mapInt64(raw, "output_tokens"),
		CacheReadTokens: mapInt64(raw, "cache_read_input_tokens"), CacheWriteTokens: mapInt64(raw, "cache_creation_input_tokens"),
	}
	usage.TotalTokens = usage.InputTokens + usage.OutputTokens + usage.CacheReadTokens + usage.CacheWriteTokens
	if usage.TotalTokens == 0 {
		return nil
	}
	return usage
}

func claudeOnlyToolResults(value any) bool {
	blocks := mapArray(value)
	if len(blocks) == 0 {
		return false
	}
	for _, block := range blocks {
		if mapString(block, "type") != "tool_result" {
			return false
		}
	}
	return true
}

func claudePublicContent(value any, assistant bool) any {
	if text, ok := value.(string); ok {
		return text
	}
	var result []any
	for _, block := range mapArray(value) {
		typeName := mapString(block, "type")
		if typeName == "thinking" || typeName == "redacted_thinking" {
			continue
		}
		if assistant && typeName == "tool_use" {
			continue
		}
		if typeName == "text" {
			result = append(result, mapString(block, "text"))
		} else if !assistant && typeName != "tool_result" {
			result = append(result, block)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}
