package cliagents

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wangning19940904/AgentMux/core"
)

func traeSessionID(line []byte) string {
	var frame map[string]any
	if json.Unmarshal(line, &frame) != nil || traeString(frame["type"]) != "thread.started" {
		return ""
	}
	return traeString(frame["thread_id"])
}

func traeStreamEvents(line []byte) []*core.Event {
	var frame map[string]any
	if json.Unmarshal(line, &frame) != nil {
		return nil
	}
	frameType := traeString(frame["type"])
	switch frameType {
	case "turn.started":
		return []*core.Event{{
			Type: core.EventModelRequest, Status: "in_progress",
			Metadata: traeMetadata("started"),
		}}
	case "turn.completed":
		usageMap := traeObject(frame["usage"])
		usage := &core.TurnUsage{
			InputTokens:      traeInt64(usageMap["input_tokens"]),
			OutputTokens:     traeInt64(usageMap["output_tokens"]),
			CacheReadTokens:  traeInt64(usageMap["cached_input_tokens"]),
			CacheWriteTokens: traeInt64(usageMap["cache_creation_input_tokens"]),
			ReasoningTokens:  traeInt64(usageMap["reasoning_output_tokens"]),
		}
		usage.TotalTokens = usage.InputTokens + usage.OutputTokens
		return []*core.Event{{
			Type: core.EventModelResponse, Status: "completed", Usage: usage,
			Metadata: traeMetadata("completed"),
		}}
	case "turn.failed", "turn.error":
		message := traeErrorMessage(frame)
		if message == "" {
			message = "TRAE turn failed"
		}
		return []*core.Event{{Type: core.EventError, Err: fmt.Errorf("%s", message), Metadata: traeMetadata("failed")}}
	case "item.started", "item.updated", "item.completed":
		return traeItemEvents(frameType, traeObject(frame["item"]))
	default:
		return nil
	}
}

func traeItemEvents(frameType string, item map[string]any) []*core.Event {
	if len(item) == 0 {
		return nil
	}
	itemType := traeString(item["type"])
	itemID := traeString(item["id"])
	lifecycle := strings.TrimPrefix(frameType, "item.")
	switch itemType {
	case "agent_message":
		if frameType != "item.completed" {
			return nil
		}
		text := traeString(item["text"])
		if text == "" {
			return nil
		}
		return []*core.Event{{
			Type: core.EventOutput, ItemID: itemID, Text: text, Status: "completed",
			Metadata: traeMetadata("completed"),
		}}
	case "reasoning":
		// TRAE's JSONL format does not distinguish public reasoning summaries
		// from raw reasoning. Do not surface either as user-visible text.
		return nil
	case "error":
		message := traeString(item["message"])
		if message == "" {
			return nil
		}
		return []*core.Event{{
			Type: core.EventThinking, ItemID: itemID, Text: message, Status: "warning",
			Metadata: traeMetadata("warning"),
		}}
	case "command_execution":
		event := traeToolEvent(item, itemID, lifecycle, "Shell")
		event.ToolInput = traeString(item["command"])
		event.ToolResult = traeString(item["aggregated_output"])
		if frameType == "item.completed" {
			exitCode := traeInt64(item["exit_code"])
			if exitCode != 0 {
				event.Err = fmt.Errorf("command exited with code %d", exitCode)
			}
		}
		return []*core.Event{event}
	case "file_change":
		event := traeToolEvent(item, itemID, lifecycle, "FileChange")
		event.ToolInput = traeJSONSummary(item["changes"])
		if event.ToolInput == "" {
			event.ToolInput = traeString(item["path"])
		}
		return []*core.Event{event}
	case "mcp_tool_call":
		name := traeString(item["tool"])
		if name == "" {
			name = traeString(item["name"])
		}
		if server := traeString(item["server"]); server != "" {
			name = server + "/" + name
		}
		event := traeToolEvent(item, itemID, lifecycle, name)
		event.ToolInput = traeJSONSummary(item["arguments"])
		event.ToolResult = traeJSONSummary(item["result"])
		return []*core.Event{event}
	default:
		return nil
	}
}

func traeToolEvent(item map[string]any, itemID, lifecycle, name string) *core.Event {
	raw, _ := json.Marshal(item)
	status := lifecycle
	if lifecycle == "started" || lifecycle == "updated" {
		status = "in_progress"
	}
	return &core.Event{
		Type: core.EventToolUse, ItemID: itemID, ToolCallID: itemID,
		ToolName: name, ToolInputRaw: string(raw), Status: status,
		Metadata: traeMetadata(lifecycle),
	}
}

func traeErrorMessage(frame map[string]any) string {
	if message := traeString(frame["message"]); message != "" {
		return message
	}
	errorMap := traeObject(frame["error"])
	return traeString(errorMap["message"])
}

func traeMetadata(lifecycle string) map[string]string {
	return map[string]string{
		"runtime": "traecli", "transport": "trae-jsonl",
		"coverage": "native", "lifecycle": lifecycle,
	}
}

func traeObject(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

func traeString(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func traeInt64(value any) int64 {
	switch number := value.(type) {
	case float64:
		return int64(number)
	case json.Number:
		parsed, _ := number.Int64()
		return parsed
	default:
		return 0
	}
}

func traeJSONSummary(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(encoded)
}
