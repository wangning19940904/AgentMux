package provider

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// convert.go implements the Anthropic Messages <-> OpenAI Chat Completions
// translation the local routing proxy needs when a Claude-family client is
// routed to an openai_chat upstream (cc-switch proxy/forwarder parity, P1
// scope: text + tool calls, streaming and non-streaming).

// anthropicToChatRequest converts an Anthropic Messages request body into an
// OpenAI Chat Completions body. overrideModel (provider.Model) replaces the
// client model when set, since upstream model ids rarely match claude-*.
func anthropicToChatRequest(body map[string]any, overrideModel string) (map[string]any, error) {
	out := map[string]any{}
	model := stringValue(body["model"])
	if overrideModel != "" {
		model = overrideModel
	}
	if model == "" {
		return nil, fmt.Errorf("missing model")
	}
	out["model"] = model

	var messages []any
	switch sys := body["system"].(type) {
	case string:
		if sys != "" {
			messages = append(messages, map[string]any{"role": "system", "content": sys})
		}
	case []any:
		text := collectAnthropicText(sys)
		if text != "" {
			messages = append(messages, map[string]any{"role": "system", "content": text})
		}
	}

	msgs, _ := body["messages"].([]any)
	for _, raw := range msgs {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role := stringValue(msg["role"])
		switch content := msg["content"].(type) {
		case string:
			messages = append(messages, map[string]any{"role": role, "content": content})
		case []any:
			messages = append(messages, convertAnthropicBlocks(role, content)...)
		}
	}
	out["messages"] = messages

	if v, ok := body["max_tokens"]; ok {
		out["max_tokens"] = v
	}
	for _, k := range []string{"temperature", "top_p", "stream"} {
		if v, ok := body[k]; ok {
			out[k] = v
		}
	}
	if v, ok := body["stop_sequences"]; ok {
		out["stop"] = v
	}
	if stream, _ := body["stream"].(bool); stream {
		out["stream_options"] = map[string]any{"include_usage": true}
	}
	if tools, ok := body["tools"].([]any); ok && len(tools) > 0 {
		var converted []any
		for _, raw := range tools {
			tool, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			fn := map[string]any{
				"name":        tool["name"],
				"description": tool["description"],
			}
			if schema, ok := tool["input_schema"]; ok {
				fn["parameters"] = schema
			}
			converted = append(converted, map[string]any{"type": "function", "function": fn})
		}
		out["tools"] = converted
	}
	if choice, ok := body["tool_choice"].(map[string]any); ok {
		switch stringValue(choice["type"]) {
		case "auto":
			out["tool_choice"] = "auto"
		case "any":
			out["tool_choice"] = "required"
		case "tool":
			out["tool_choice"] = map[string]any{
				"type":     "function",
				"function": map[string]any{"name": choice["name"]},
			}
		}
	}
	return out, nil
}

// convertAnthropicBlocks flattens one Anthropic message into OpenAI messages.
// tool_result blocks become role:"tool" messages; tool_use blocks become
// assistant tool_calls; text accumulates into the main message content.
func convertAnthropicBlocks(role string, blocks []any) []any {
	var out []any
	var text strings.Builder
	var toolCalls []any
	for _, raw := range blocks {
		block, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch stringValue(block["type"]) {
		case "text":
			text.WriteString(stringValue(block["text"]))
		case "tool_use":
			args, _ := json.Marshal(block["input"])
			toolCalls = append(toolCalls, map[string]any{
				"id":   block["id"],
				"type": "function",
				"function": map[string]any{
					"name":      block["name"],
					"arguments": string(args),
				},
			})
		case "tool_result":
			out = append(out, map[string]any{
				"role":         "tool",
				"tool_call_id": block["tool_use_id"],
				"content":      anthropicToolResultText(block["content"]),
			})
		}
	}
	if text.Len() > 0 || len(toolCalls) > 0 {
		msg := map[string]any{"role": role}
		if text.Len() > 0 {
			msg["content"] = text.String()
		} else {
			msg["content"] = nil
		}
		if len(toolCalls) > 0 {
			msg["tool_calls"] = toolCalls
		}
		// Tool results must precede the next user/assistant turn; text of the
		// same message goes after its tool_result blocks by OpenAI convention.
		out = append(out, msg)
	}
	return out
}

func anthropicToolResultText(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		return collectAnthropicText(v)
	default:
		return ""
	}
}

func collectAnthropicText(blocks []any) string {
	var sb strings.Builder
	for _, raw := range blocks {
		if block, ok := raw.(map[string]any); ok && stringValue(block["type"]) == "text" {
			sb.WriteString(stringValue(block["text"]))
		}
	}
	return sb.String()
}

func chatFinishToStopReason(reason string) string {
	switch reason {
	case "length":
		return "max_tokens"
	case "tool_calls", "function_call":
		return "tool_use"
	default:
		return "end_turn"
	}
}

// chatToAnthropicResponse converts a non-streaming Chat Completions response
// into an Anthropic Messages response.
func chatToAnthropicResponse(body map[string]any, requestModel string) map[string]any {
	content := []any{}
	stopReason := "end_turn"
	if choices, ok := body["choices"].([]any); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]any); ok {
			stopReason = chatFinishToStopReason(stringValue(choice["finish_reason"]))
			if message, ok := choice["message"].(map[string]any); ok {
				if text := stringValue(message["content"]); text != "" {
					content = append(content, map[string]any{"type": "text", "text": text})
				}
				if calls, ok := message["tool_calls"].([]any); ok {
					for _, raw := range calls {
						call, ok := raw.(map[string]any)
						if !ok {
							continue
						}
						fn, _ := call["function"].(map[string]any)
						var input any = map[string]any{}
						if fn != nil {
							args := stringValue(fn["arguments"])
							if args != "" {
								var parsed any
								if err := json.Unmarshal([]byte(args), &parsed); err == nil {
									input = parsed
								}
							}
						}
						name := ""
						if fn != nil {
							name = stringValue(fn["name"])
						}
						content = append(content, map[string]any{
							"type": "tool_use", "id": call["id"], "name": name, "input": input,
						})
					}
				}
			}
		}
	}
	usage := map[string]any{"input_tokens": 0, "output_tokens": 0}
	if u, ok := body["usage"].(map[string]any); ok {
		usage["input_tokens"] = u["prompt_tokens"]
		usage["output_tokens"] = u["completion_tokens"]
	}
	id := stringValue(body["id"])
	if id == "" {
		id = "msg_proxy"
	}
	model := stringValue(body["model"])
	if model == "" {
		model = requestModel
	}
	return map[string]any{
		"id": id, "type": "message", "role": "assistant", "model": model,
		"content": content, "stop_reason": stopReason, "stop_sequence": nil,
		"usage": usage,
	}
}

// sseWriter emits Anthropic-style SSE events.
type sseWriter struct {
	w       io.Writer
	flusher http.Flusher
}

func (s *sseWriter) event(name string, payload map[string]any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, data); err != nil {
		return err
	}
	if s.flusher != nil {
		s.flusher.Flush()
	}
	return nil
}

// chatStreamToAnthropicSSE reads an OpenAI Chat Completions SSE stream and
// rewrites it as an Anthropic Messages SSE stream (cc-switch's
// create_anthropic_sse_stream equivalent).
func chatStreamToAnthropicSSE(upstream io.Reader, w http.ResponseWriter, requestModel string) error {
	flusher, _ := w.(http.Flusher)
	out := &sseWriter{w: w, flusher: flusher}

	started := false
	blockOpen := false
	blockIndex := -1
	blockIsTool := false
	currentToolIdx := -1
	stopReason := "end_turn"
	var outputTokens, inputTokens any = 0, 0
	model := requestModel

	startMessage := func() error {
		if started {
			return nil
		}
		started = true
		return out.event("message_start", map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id": "msg_proxy", "type": "message", "role": "assistant",
				"model": model, "content": []any{}, "stop_reason": nil,
				"stop_sequence": nil,
				"usage":         map[string]any{"input_tokens": 0, "output_tokens": 0},
			},
		})
	}
	closeBlock := func() error {
		if !blockOpen {
			return nil
		}
		blockOpen = false
		return out.event("content_block_stop", map[string]any{
			"type": "content_block_stop", "index": blockIndex,
		})
	}
	openTextBlock := func() error {
		if blockOpen && !blockIsTool {
			return nil
		}
		if err := closeBlock(); err != nil {
			return err
		}
		blockIndex++
		blockOpen, blockIsTool = true, false
		return out.event("content_block_start", map[string]any{
			"type": "content_block_start", "index": blockIndex,
			"content_block": map[string]any{"type": "text", "text": ""},
		})
	}

	scanner := bufio.NewScanner(upstream)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if m := stringValue(chunk["model"]); m != "" {
			model = m
		}
		if u, ok := chunk["usage"].(map[string]any); ok {
			if v, ok := u["prompt_tokens"]; ok {
				inputTokens = v
			}
			if v, ok := u["completion_tokens"]; ok {
				outputTokens = v
			}
		}
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		choice, _ := choices[0].(map[string]any)
		if choice == nil {
			continue
		}
		if err := startMessage(); err != nil {
			return err
		}
		if delta, ok := choice["delta"].(map[string]any); ok {
			if text := stringValue(delta["content"]); text != "" {
				if err := openTextBlock(); err != nil {
					return err
				}
				if err := out.event("content_block_delta", map[string]any{
					"type": "content_block_delta", "index": blockIndex,
					"delta": map[string]any{"type": "text_delta", "text": text},
				}); err != nil {
					return err
				}
			}
			if calls, ok := delta["tool_calls"].([]any); ok {
				for _, raw := range calls {
					call, ok := raw.(map[string]any)
					if !ok {
						continue
					}
					idx := 0
					if v, ok := call["index"].(float64); ok {
						idx = int(v)
					}
					fn, _ := call["function"].(map[string]any)
					if idx != currentToolIdx || !blockOpen || !blockIsTool {
						if err := closeBlock(); err != nil {
							return err
						}
						currentToolIdx = idx
						blockIndex++
						blockOpen, blockIsTool = true, true
						id := stringValue(call["id"])
						if id == "" {
							id = fmt.Sprintf("toolu_proxy_%d", idx)
						}
						name := ""
						if fn != nil {
							name = stringValue(fn["name"])
						}
						if err := out.event("content_block_start", map[string]any{
							"type": "content_block_start", "index": blockIndex,
							"content_block": map[string]any{
								"type": "tool_use", "id": id, "name": name, "input": map[string]any{},
							},
						}); err != nil {
							return err
						}
					}
					if fn != nil {
						if args := stringValue(fn["arguments"]); args != "" {
							if err := out.event("content_block_delta", map[string]any{
								"type": "content_block_delta", "index": blockIndex,
								"delta": map[string]any{"type": "input_json_delta", "partial_json": args},
							}); err != nil {
								return err
							}
						}
					}
				}
			}
		}
		if reason := stringValue(choice["finish_reason"]); reason != "" {
			stopReason = chatFinishToStopReason(reason)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if !started {
		if err := startMessage(); err != nil {
			return err
		}
	}
	if err := closeBlock(); err != nil {
		return err
	}
	if err := out.event("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil},
		"usage": map[string]any{"input_tokens": inputTokens, "output_tokens": outputTokens},
	}); err != nil {
		return err
	}
	return out.event("message_stop", map[string]any{"type": "message_stop"})
}
