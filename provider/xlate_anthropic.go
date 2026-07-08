package provider

import (
	"encoding/json"
	"strings"
)

// xlate_anthropic.go holds the Anthropic<->IR converters not already in
// convert.go. convert.go owns the forward path used when a Claude client hits
// an OpenAI upstream (anthropicToChatRequest / chatToAnthropicResponse); this
// file adds the reverse path used when a non-Anthropic client is routed to an
// Anthropic upstream (IR -> Anthropic request, Anthropic response -> IR).

// chatToAnthropicRequest converts the OpenAI Chat IR request into an Anthropic
// Messages request body. System messages are lifted into the top-level
// "system" field, tool_calls become tool_use blocks and role:"tool" messages
// become tool_result blocks inside a user turn.
func chatToAnthropicRequest(ir map[string]any) map[string]any {
	out := map[string]any{}
	if model := stringValue(ir["model"]); model != "" {
		out["model"] = model
	}

	var systemParts []string
	var messages []any
	// pendingToolResults accumulates consecutive tool outputs so they land in a
	// single user turn, as Anthropic expects.
	var pendingToolResults []any
	flushToolResults := func() {
		if len(pendingToolResults) > 0 {
			messages = append(messages, map[string]any{"role": "user", "content": pendingToolResults})
			pendingToolResults = nil
		}
	}

	msgs, _ := ir["messages"].([]any)
	for _, raw := range msgs {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role := stringValue(msg["role"])
		switch role {
		case "system":
			if text := messageContentText(msg["content"]); text != "" {
				systemParts = append(systemParts, text)
			}
		case "tool":
			pendingToolResults = append(pendingToolResults, map[string]any{
				"type":        "tool_result",
				"tool_use_id": msg["tool_call_id"],
				"content":     messageContentText(msg["content"]),
			})
		case "assistant":
			flushToolResults()
			var blocks []any
			if text := messageContentText(msg["content"]); text != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": text})
			}
			if calls, ok := msg["tool_calls"].([]any); ok {
				for _, rawCall := range calls {
					call, ok := rawCall.(map[string]any)
					if !ok {
						continue
					}
					fn, _ := call["function"].(map[string]any)
					name := ""
					var input any = map[string]any{}
					if fn != nil {
						name = stringValue(fn["name"])
						if args := stringValue(fn["arguments"]); args != "" {
							var parsed any
							if err := json.Unmarshal([]byte(args), &parsed); err == nil {
								input = parsed
							}
						}
					}
					blocks = append(blocks, map[string]any{
						"type": "tool_use", "id": call["id"], "name": name, "input": input,
					})
				}
			}
			if len(blocks) > 0 {
				messages = append(messages, map[string]any{"role": "assistant", "content": blocks})
			}
		default: // user (and any unknown role treated as user)
			flushToolResults()
			if text := messageContentText(msg["content"]); text != "" {
				messages = append(messages, map[string]any{"role": "user", "content": text})
			}
		}
	}
	flushToolResults()

	out["messages"] = messages
	if len(systemParts) > 0 {
		out["system"] = strings.Join(systemParts, "\n\n")
	}

	// Anthropic requires max_tokens; default when the client omitted it.
	if v, ok := ir["max_tokens"]; ok && v != nil {
		out["max_tokens"] = v
	} else {
		out["max_tokens"] = 4096
	}
	for _, k := range []string{"temperature", "top_p", "stream"} {
		if v, ok := ir[k]; ok {
			out[k] = v
		}
	}
	if v, ok := ir["stop"]; ok {
		out["stop_sequences"] = v
	}

	if tools, ok := ir["tools"].([]any); ok && len(tools) > 0 {
		var converted []any
		for _, raw := range tools {
			tool, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			fn, _ := tool["function"].(map[string]any)
			if fn == nil {
				continue
			}
			entry := map[string]any{"name": fn["name"]}
			if desc := fn["description"]; desc != nil {
				entry["description"] = desc
			}
			if params, ok := fn["parameters"]; ok {
				entry["input_schema"] = params
			} else {
				entry["input_schema"] = map[string]any{"type": "object"}
			}
			converted = append(converted, entry)
		}
		out["tools"] = converted
	}
	out["tool_choice"] = openAIToolChoiceToAnthropic(ir["tool_choice"])
	if out["tool_choice"] == nil {
		delete(out, "tool_choice")
	}
	return out
}

func openAIToolChoiceToAnthropic(choice any) any {
	switch v := choice.(type) {
	case string:
		switch v {
		case "auto":
			return map[string]any{"type": "auto"}
		case "required", "any":
			return map[string]any{"type": "any"}
		case "none":
			return nil
		}
	case map[string]any:
		if fn, ok := v["function"].(map[string]any); ok {
			return map[string]any{"type": "tool", "name": fn["name"]}
		}
	}
	return nil
}

// anthropicRespToChat converts a non-streaming Anthropic Messages response into
// the OpenAI Chat IR response shape.
func anthropicRespToChat(body map[string]any) map[string]any {
	message := map[string]any{"role": "assistant"}
	var textParts []string
	var toolCalls []any
	if blocks, ok := body["content"].([]any); ok {
		for _, raw := range blocks {
			block, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			switch stringValue(block["type"]) {
			case "text":
				textParts = append(textParts, stringValue(block["text"]))
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
			}
		}
	}
	if len(textParts) > 0 {
		message["content"] = strings.Join(textParts, "")
	} else {
		message["content"] = nil
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
	}
	finish := stopReasonToChatFinish(stringValue(body["stop_reason"]))

	usage := map[string]any{"prompt_tokens": 0, "completion_tokens": 0}
	if u, ok := body["usage"].(map[string]any); ok {
		usage["prompt_tokens"] = firstNonNil(u["input_tokens"], 0)
		usage["completion_tokens"] = firstNonNil(u["output_tokens"], 0)
	}
	id := stringValue(body["id"])
	if id == "" {
		id = "chatcmpl_proxy"
	}
	return map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"model":   stringValue(body["model"]),
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}},
		"usage":   usage,
	}
}

// stopReasonToChatFinish inverts chatFinishToStopReason for the reverse path.
func stopReasonToChatFinish(reason string) string {
	switch reason {
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "end_turn", "stop_sequence":
		return "stop"
	default:
		return "stop"
	}
}

// messageContentText flattens an OpenAI message "content" value (string or an
// array of content parts) into plain text.
func messageContentText(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var sb strings.Builder
		for _, raw := range v {
			part, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			switch stringValue(part["type"]) {
			case "text", "input_text", "output_text", "":
				sb.WriteString(stringValue(part["text"]))
			}
		}
		return sb.String()
	default:
		return ""
	}
}

func firstNonNil(values ...any) any {
	for _, v := range values {
		if v != nil {
			return v
		}
	}
	return nil
}
