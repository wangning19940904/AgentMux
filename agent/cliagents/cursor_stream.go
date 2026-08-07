package cliagents

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/wangning19940904/AgentMux/agent/cliagent"
	"github.com/wangning19940904/AgentMux/core"
)

const cursorStderrPreviewLimit = 16 * 1024

var (
	cursorAuthURL          = regexp.MustCompile(`https?://[^\s<>"']+`)
	cursorVerificationCode = regexp.MustCompile(
		`(?i)(verification\s+code|device\s+code|验证码|校验码)\s*[:：]?\s*([a-z0-9][a-z0-9_-]{3,})`,
	)
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
			events := []*core.Event{event}
			if event.ToolResultRaw != "" {
				if auth := cursorAuthorizationOutput(event.ToolResultRaw); auth != "" {
					metadata := cursorMetadata("tool_authorization")
					metadata["persistent"] = "true"
					metadata["priority"] = "action_required"
					events = append(events, &core.Event{
						Type: core.EventOutput, Text: auth, Status: "in_progress",
						Metadata: metadata,
					})
				}
			}
			return events
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

func newCursorStderrMapper() cliagent.LineMapper {
	var output string
	return func(line []byte) *core.Event {
		text := strings.TrimSpace(cursorANSISequence.ReplaceAllString(string(line), ""))
		if text == "" {
			return nil
		}
		if output != "" {
			output += "\n"
		}
		output += text
		if len(output) > cursorStderrPreviewLimit {
			output = output[len(output)-cursorStderrPreviewLimit:]
		}
		// Cursor and the commands it launches write package-download progress,
		// warnings, and terminal animations to stderr. Those bytes remain in the
		// bounded process-error tail, but they are not assistant prose and must
		// not replace the live answer in the chat card. The one exception is an
		// explicit login/device-authorization prompt that requires user action.
		auth := cursorAuthorizationOutput(output)
		if auth == "" {
			return nil
		}
		metadata := cursorMetadata("tool_authorization")
		metadata["persistent"] = "true"
		metadata["priority"] = "action_required"
		return &core.Event{Type: core.EventOutput, Text: auth, Status: "in_progress", Metadata: metadata}
	}
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

func cursorAuthorizationOutput(raw string) string {
	if raw == "" {
		return ""
	}
	text := raw
	var decoded any
	if json.Unmarshal([]byte(raw), &decoded) == nil {
		if nested := cursorNestedOutput(decoded); nested != "" {
			text = nested
		}
	}
	text = cursorANSISequence.ReplaceAllString(text, "")
	text = strings.ReplaceAll(text, "\r", "\n")
	url := cursorAuthorizationURL(text)
	if url == "" {
		return ""
	}
	lower := strings.ToLower(text)
	authHint := strings.Contains(lower, "authoriz") || strings.Contains(lower, "login") ||
		strings.Contains(lower, "verification") || strings.Contains(lower, "device code") ||
		strings.Contains(text, "授权") || strings.Contains(text, "验证码") || strings.Contains(text, "扫码")
	if !authHint {
		return ""
	}

	var code string
	if match := cursorVerificationCode.FindStringSubmatch(text); len(match) >= 3 {
		code = match[2]
	}
	var b strings.Builder
	b.WriteString("⚠️ **需要完成授权**\n\n请打开以下链接完成登录：\n")
	b.WriteString(url)
	if code != "" {
		b.WriteString("\n\n**验证码：** `")
		b.WriteString(code)
		b.WriteString("`")
	}
	b.WriteString("\n\n<font color='orange'>⏳ 授权完成后任务会自动继续</font>")
	return b.String()
}

func cursorAuthorizationURL(text string) string {
	matches := cursorAuthURL.FindAllStringIndex(text, -1)
	bestURL := ""
	bestScore := 0
	for _, match := range matches {
		if len(match) != 2 {
			continue
		}
		url := strings.TrimRight(text[match[0]:match[1]], ".,;:!?)]}")
		lowerURL := strings.ToLower(url)
		score := 0
		for _, hint := range []string{"/auth", "login", "oauth", "device", "verify", "signin", "sign-in", "sso"} {
			if strings.Contains(lowerURL, hint) {
				score += 4
			}
		}
		// Some device-login URLs are opaque. In that case, instructions placed
		// immediately around the link still distinguish it from an earlier
		// package source or documentation URL.
		start := match[0] - 160
		if start < 0 {
			start = 0
		}
		end := match[1] + 160
		if end > len(text) {
			end = len(text)
		}
		context := strings.ToLower(text[start:end])
		for _, hint := range []string{"authoriz", "to login", "log in", "verification code", "device code", "授权", "验证码", "扫码"} {
			if strings.Contains(context, hint) {
				score += 2
			}
		}
		if score >= bestScore && score > 0 {
			bestScore = score
			bestURL = url
		}
	}
	return bestURL
}

func cursorNestedOutput(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case []any:
		for _, item := range typed {
			if text := cursorNestedOutput(item); text != "" && cursorAuthURL.MatchString(text) {
				return text
			}
		}
	case map[string]any:
		for _, key := range []string{"interleavedOutput", "stderr", "stdout", "output", "message", "content", "result", "success", "failure", "error"} {
			if text := cursorNestedOutput(typed[key]); text != "" && cursorAuthURL.MatchString(text) {
				return text
			}
		}
	}
	return ""
}

func cursorResultEvents(frame map[string]any, subtype string) []*core.Event {
	duration := cursorInt64(frame["duration_ms"])
	text := firstString(frame, "result", "message")
	if boolValue(frame["is_error"]) || subtype == "error" || subtype == "failed" {
		err := fmt.Errorf("%s", firstNonEmpty(text, codexValue(frame["error"]), "Cursor failed"))
		return []*core.Event{{Type: core.EventError, Err: err, Status: subtype, DurationMs: duration, Metadata: cursorMetadata("failed")}}
	}
	usageMap := nestedMap(frame, "usage")
	usage := &core.TurnUsage{
		RequestID:        firstString(frame, "request_id"),
		InputTokens:      cursorInt64(usageMap["inputTokens"]),
		OutputTokens:     cursorInt64(usageMap["outputTokens"]),
		CacheReadTokens:  cursorInt64(usageMap["cacheReadTokens"]),
		CacheWriteTokens: cursorInt64(usageMap["cacheWriteTokens"]),
		DurationMs:       duration,
	}
	usage.TotalTokens = usage.InputTokens + usage.OutputTokens + usage.CacheReadTokens + usage.CacheWriteTokens
	metadata := cursorMetadata("completed")
	metadata["clear_persistent"] = "true"
	return []*core.Event{{
		Type: core.EventFinal, Text: text, Final: true, Status: firstNonEmpty(subtype, "success"),
		DurationMs: duration, Usage: usage, Metadata: metadata,
	}}
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
