package cliagents

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/wangning19940904/AgentMux/core"
)

// cursorStreamEvents maps Cursor's native --output-format stream-json frames.
// Cursor nests assistant content and complete tool payloads, so the generic
// top-level text mapper necessarily drops most of the useful information.
func cursorStreamEvents(line []byte) []*core.Event {
	var frame map[string]any
	if err := json.Unmarshal(line, &frame); err != nil {
		return nil
	}
	typ := firstString(frame, "type")
	subtype := firstString(frame, "subtype")
	switch typ {
	case "assistant":
		if text := cursorMessageText(nestedMap(frame, "message")); text != "" {
			return []*core.Event{{
				Type: core.EventOutput, Text: text, Status: "in_progress",
				Metadata: cursorMetadata("assistant"),
			}}
		}
	case "tool_call":
		if event := cursorToolEvent(frame, subtype); event != nil {
			return []*core.Event{event}
		}
	case "result":
		return cursorResultEvents(frame, subtype)
	case "connection":
		if text := cursorConnectionStatus(frame, subtype); text != "" {
			return []*core.Event{{Type: core.EventThinking, Text: text, Status: subtype, Metadata: cursorMetadata("connection")}}
		}
	case "retry":
		attempt := cursorInt64(frame["attempt"])
		text := "Cursor 正在重试"
		if attempt > 0 {
			text = fmt.Sprintf("Cursor 正在进行第 %d 次重试", attempt)
		}
		return []*core.Event{{Type: core.EventThinking, Text: text, Status: subtype, Metadata: cursorMetadata("retry")}}
	case "system":
		if subtype == "init" {
			model := firstString(frame, "model")
			return []*core.Event{{
				Type: core.EventModelRequest, Status: "in_progress",
				Usage:    &core.TurnUsage{Model: model, RequestedModel: model, ResolvedModel: model},
				Metadata: cursorMetadata("initialized"),
			}}
		}
		if subtype == "task_notification" {
			parts := []string{"Cursor 后台任务"}
			if title := firstString(frame, "title"); title != "" {
				parts = append(parts, title)
			}
			if status := firstString(frame, "status"); status != "" {
				parts = append(parts, status)
			}
			if detail := firstString(frame, "detail"); detail != "" {
				parts = append(parts, detail)
			}
			return []*core.Event{{Type: core.EventThinking, Text: strings.Join(parts, " · "), Status: firstString(frame, "status"), Metadata: cursorMetadata("background_task")}}
		}
	}
	// Cursor's `thinking` frames contain raw model reasoning rather than a
	// deliberately produced user-facing summary. Do not expose them.
	return nil
}

func cursorMessageText(message map[string]any) string {
	if text := firstString(message, "text"); text != "" {
		return text
	}
	if content, ok := message["content"].(string); ok {
		return strings.TrimSpace(content)
	}
	blocks, _ := message["content"].([]any)
	var out strings.Builder
	for _, raw := range blocks {
		block, _ := raw.(map[string]any)
		for _, key := range []string{"text", "content", "value"} {
			if text, ok := block[key].(string); ok && text != "" {
				out.WriteString(text)
				break
			}
		}
	}
	return strings.TrimSpace(out.String())
}

func cursorToolEvent(frame map[string]any, subtype string) *core.Event {
	toolCall := nestedMap(frame, "tool_call")
	caseName, payload := cursorToolCase(toolCall)
	if caseName == "" {
		return nil
	}
	callID := firstString(frame, "call_id")
	if callID == "" {
		callID = firstString(toolCall, "toolCallId")
	}
	metadata := cursorMetadata(subtype)
	metadata["tool_case"] = caseName
	if subtype == "started" {
		name, input, raw := cursorToolDescriptor(caseName, payload)
		return &core.Event{
			Type: core.EventToolUse, ToolCallID: callID, ToolName: name,
			ToolInput: truncateCodex(input, 600), ToolInputRaw: raw,
			Status: "in_progress", Metadata: metadata,
		}
	}

	result := payload["result"]
	raw := codexValue(result)
	summary, failed := cursorToolResultSummary(result)
	status := "completed"
	var eventErr error
	if failed {
		status = "failed"
		eventErr = fmt.Errorf("%s", firstNonEmpty(summary, "Cursor tool failed"))
	}
	startedAt := cursorInt64(toolCall["startedAtMs"])
	completedAt := cursorInt64(toolCall["completedAtMs"])
	duration := int64(0)
	if completedAt >= startedAt && startedAt > 0 {
		duration = completedAt - startedAt
	}
	return &core.Event{
		Type: core.EventToolUse, ToolCallID: callID,
		ToolResult: truncateCodex(summary, 800), ToolResultRaw: raw,
		Status: status, DurationMs: duration, Err: eventErr, Metadata: metadata,
	}
}

func cursorToolCase(toolCall map[string]any) (string, map[string]any) {
	keys := make([]string, 0, len(toolCall))
	for key := range toolCall {
		if strings.HasSuffix(strings.ToLower(key), "toolcall") && key != "toolCallId" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		if payload, ok := toolCall[key].(map[string]any); ok {
			return key, payload
		}
	}
	return "", nil
}

func cursorToolDescriptor(caseName string, payload map[string]any) (name, input, raw string) {
	args := nestedMap(payload, "args")
	if len(args) == 0 {
		args = payload
	}
	raw = codexValue(args)
	name = map[string]string{
		"shellToolCall":      "执行命令",
		"readToolCall":       "读取文件",
		"writeToolCall":      "写入文件",
		"editToolCall":       "修改文件",
		"deleteToolCall":     "删除文件",
		"listDirToolCall":    "列出目录",
		"grepToolCall":       "搜索内容",
		"fileSearchToolCall": "搜索文件",
		"webSearchToolCall":  "网页搜索",
		"webFetchToolCall":   "读取网页",
		"mcpToolCall":        "MCP 工具",
	}[caseName]
	if name == "" {
		name = humanizeCursorToolCase(caseName)
	}
	if caseName == "mcpToolCall" {
		server := firstString(args, "server", "serverName")
		tool := firstString(args, "tool", "toolName", "name")
		if qualified := strings.Trim(strings.Join([]string{server, tool}, ":"), ":"); qualified != "" {
			name = qualified
		}
	}
	input = firstString(args, "command", "path", "filePath", "query", "pattern", "url", "prompt", "name")
	if input == "" {
		input = firstString(payload, "description")
	}
	if input == "" {
		input = raw
	}
	return name, input, raw
}

func cursorToolResultSummary(result any) (string, bool) {
	resultMap, _ := result.(map[string]any)
	if len(resultMap) == 0 {
		return codexValue(result), false
	}
	failed := false
	var body any = resultMap
	for _, key := range []string{"success", "failure", "error", "timeout", "rejected", "permissionDenied", "spawnError"} {
		if value, ok := resultMap[key]; ok {
			body = value
			failed = key != "success"
			break
		}
	}
	bodyMap, _ := body.(map[string]any)
	if exitCode, ok := cursorOptionalInt64(bodyMap["exitCode"]); ok && exitCode != 0 {
		failed = true
	}
	output := cursorToolOutput(bodyMap)
	parts := make([]string, 0, 2)
	if exitCode, ok := cursorOptionalInt64(bodyMap["exitCode"]); ok {
		parts = append(parts, fmt.Sprintf("exit %d", exitCode))
	}
	if output != "" {
		parts = append(parts, output)
	}
	if len(parts) == 0 {
		parts = append(parts, codexValue(body))
	}
	return strings.Join(parts, " · "), failed
}

func cursorToolOutput(value map[string]any) string {
	if len(value) == 0 {
		return ""
	}
	if interleaved := firstString(value, "interleavedOutput"); interleaved != "" {
		return interleaved
	}
	stdout := firstString(value, "stdout", "output", "content", "message", "result", "error")
	stderr := firstString(value, "stderr")
	if stdout != "" && stderr != "" && !strings.Contains(stdout, stderr) {
		return stdout + "\n" + stderr
	}
	return firstNonEmpty(stdout, stderr)
}

func cursorResultEvents(frame map[string]any, subtype string) []*core.Event {
	duration := cursorInt64(frame["duration_ms"])
	text := firstString(frame, "result", "message")
	if boolValue(frame["is_error"]) || subtype == "error" || subtype == "failed" {
		err := fmt.Errorf("%s", firstNonEmpty(text, codexValue(frame["error"]), "Cursor failed"))
		return []*core.Event{{Type: core.EventError, Err: err, Status: subtype, DurationMs: duration, Metadata: cursorMetadata("failed")}}
	}
	usageMap := nestedMap(frame, "usage")
	if len(usageMap) == 0 {
		usageMap = nestedMap(frame, "tokenUsage")
	}
	if len(usageMap) == 0 {
		usageMap = nestedMap(frame, "token_usage")
	}
	usage := &core.TurnUsage{
		RequestID:        firstString(frame, "request_id", "requestId"),
		InputTokens:      cursorFirstInt64(usageMap, "inputTokens", "input_tokens"),
		OutputTokens:     cursorFirstInt64(usageMap, "outputTokens", "output_tokens"),
		CacheReadTokens:  cursorFirstInt64(usageMap, "cacheReadTokens", "cache_read_tokens", "cachedInputTokens", "cached_input_tokens"),
		CacheWriteTokens: cursorFirstInt64(usageMap, "cacheWriteTokens", "cache_write_tokens", "cacheCreationInputTokens", "cache_creation_input_tokens"),
		DurationMs:       duration,
	}
	usage.TotalTokens = usage.InputTokens + usage.OutputTokens + usage.CacheReadTokens + usage.CacheWriteTokens
	return []*core.Event{{
		Type: core.EventFinal, Text: text, Final: true, Status: firstNonEmpty(subtype, "success"),
		DurationMs: duration, Usage: usage, Metadata: cursorMetadata("completed"),
	}}
}

func cursorFirstInt64(values map[string]any, keys ...string) int64 {
	for _, key := range keys {
		if value := cursorInt64(values[key]); value != 0 {
			return value
		}
	}
	return 0
}

func cursorConnectionStatus(frame map[string]any, subtype string) string {
	switch subtype {
	case "reconnecting":
		attempt := cursorInt64(frame["attempt"])
		if attempt > 0 {
			return fmt.Sprintf("Cursor 连接中断，正在进行第 %d 次重连", attempt)
		}
		return "Cursor 连接中断，正在重连"
	case "reconnected":
		return "Cursor 已重新连接"
	case "disconnected":
		return "Cursor 连接已断开"
	}
	return ""
}

func cursorMetadata(lifecycle string) map[string]string {
	return map[string]string{
		"runtime": "cursor", "transport": "stream-json", "coverage": "structured", "lifecycle": lifecycle,
	}
}

func cursorInt64(value any) int64 {
	if number := numberValue(value); number != 0 {
		return int64(number)
	}
	if text, ok := value.(string); ok {
		number, _ := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
		return number
	}
	return 0
}

func cursorOptionalInt64(value any) (int64, bool) {
	if value == nil {
		return 0, false
	}
	if text, ok := value.(string); ok {
		number, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
		return number, err == nil
	}
	return int64(numberValue(value)), true
}

func humanizeCursorToolCase(value string) string {
	value = strings.TrimSuffix(value, "ToolCall")
	var out []rune
	for i, r := range value {
		if i > 0 && unicode.IsUpper(r) {
			out = append(out, ' ')
		}
		out = append(out, unicode.ToLower(r))
	}
	if len(out) == 0 {
		return "Cursor 工具"
	}
	return string(out)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
