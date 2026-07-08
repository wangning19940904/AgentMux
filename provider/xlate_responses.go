package provider

import (
	"encoding/json"
	"strings"
)

// xlate_responses.go converts between the OpenAI Chat IR and the OpenAI
// Responses API (the newer flat input/output structure Codex uses). In the
// Responses shape tool calls and tool outputs are top-level input items rather
// than being nested inside message content.

// chatToResponsesRequest converts the OpenAI Chat IR request into a Responses
// API request body.
func chatToResponsesRequest(ir map[string]any) map[string]any {
	out := map[string]any{}
	if model := stringValue(ir["model"]); model != "" {
		out["model"] = model
	}

	var instructions []string
	var input []any

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
				instructions = append(instructions, text)
			}
		case "tool":
			input = append(input, map[string]any{
				"type":    "function_call_output",
				"call_id": msg["tool_call_id"],
				"output":  messageContentText(msg["content"]),
			})
		case "assistant":
			if text := messageContentText(msg["content"]); text != "" {
				input = append(input, map[string]any{
					"type": "message", "role": "assistant",
					"content": []any{map[string]any{"type": "output_text", "text": text}},
				})
			}
			if calls, ok := msg["tool_calls"].([]any); ok {
				for _, rawCall := range calls {
					call, ok := rawCall.(map[string]any)
					if !ok {
						continue
					}
					fn, _ := call["function"].(map[string]any)
					item := map[string]any{
						"type":    "function_call",
						"call_id": call["id"],
					}
					if fn != nil {
						item["name"] = fn["name"]
						item["arguments"] = fn["arguments"]
					}
					input = append(input, item)
				}
			}
		default: // user
			if text := messageContentText(msg["content"]); text != "" {
				input = append(input, map[string]any{
					"type": "message", "role": "user",
					"content": []any{map[string]any{"type": "input_text", "text": text}},
				})
			}
		}
	}
	out["input"] = input
	if len(instructions) > 0 {
		out["instructions"] = strings.Join(instructions, "\n\n")
	}
	if v, ok := ir["max_tokens"]; ok && v != nil {
		out["max_output_tokens"] = v
	}
	if v, ok := ir["temperature"]; ok {
		out["temperature"] = v
	}
	if v, ok := ir["top_p"]; ok {
		out["top_p"] = v
	}
	if v, ok := ir["stream"]; ok {
		out["stream"] = v
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
			entry := map[string]any{
				"type":        "function",
				"name":        fn["name"],
				"description": fn["description"],
			}
			if params, ok := fn["parameters"]; ok {
				entry["parameters"] = params
			}
			converted = append(converted, entry)
		}
		out["tools"] = converted
	}
	if choice, ok := ir["tool_choice"]; ok {
		out["tool_choice"] = choice
	}
	return out
}

// responsesToChatRequest converts a Responses API client request into the
// OpenAI Chat IR (used when a Responses client is routed to another upstream).
func responsesToChatRequest(body map[string]any) (map[string]any, error) {
	out := map[string]any{}
	if model := stringValue(body["model"]); model != "" {
		out["model"] = model
	}
	var messages []any
	if instr := stringValue(body["instructions"]); instr != "" {
		messages = append(messages, map[string]any{"role": "system", "content": instr})
	}

	// input may be a bare string or an array of typed items.
	switch input := body["input"].(type) {
	case string:
		messages = append(messages, map[string]any{"role": "user", "content": input})
	case []any:
		for _, raw := range input {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			switch stringValue(item["type"]) {
			case "message", "":
				role := stringValue(item["role"])
				if role == "" {
					role = "user"
				}
				messages = append(messages, map[string]any{
					"role": role, "content": responsesContentText(item["content"]),
				})
			case "function_call":
				messages = append(messages, map[string]any{
					"role":    "assistant",
					"content": nil,
					"tool_calls": []any{map[string]any{
						"id":       item["call_id"],
						"type":     "function",
						"function": map[string]any{"name": item["name"], "arguments": item["arguments"]},
					}},
				})
			case "function_call_output":
				messages = append(messages, map[string]any{
					"role":         "tool",
					"tool_call_id": item["call_id"],
					"content":      responsesContentText(item["output"]),
				})
			}
		}
	}
	out["messages"] = messages

	if v, ok := body["max_output_tokens"]; ok {
		out["max_tokens"] = v
	}
	for _, k := range []string{"temperature", "top_p", "stream"} {
		if v, ok := body[k]; ok {
			out[k] = v
		}
	}
	if tools, ok := body["tools"].([]any); ok {
		var converted []any
		for _, raw := range tools {
			tool, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if stringValue(tool["type"]) != "function" {
				continue
			}
			fn := map[string]any{"name": tool["name"], "description": tool["description"]}
			if params, ok := tool["parameters"]; ok {
				fn["parameters"] = params
			}
			converted = append(converted, map[string]any{"type": "function", "function": fn})
		}
		if len(converted) > 0 {
			out["tools"] = converted
		}
	}
	if choice, ok := body["tool_choice"]; ok {
		out["tool_choice"] = choice
	}
	return out, nil
}

// responsesRespToChat converts a non-streaming Responses API response into the
// OpenAI Chat IR response.
func responsesRespToChat(body map[string]any) map[string]any {
	message := map[string]any{"role": "assistant"}
	var textParts []string
	var toolCalls []any
	finish := "stop"

	if output, ok := body["output"].([]any); ok {
		for _, raw := range output {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			switch stringValue(item["type"]) {
			case "message":
				textParts = append(textParts, responsesContentText(item["content"]))
			case "function_call":
				toolCalls = append(toolCalls, map[string]any{
					"id":       item["call_id"],
					"type":     "function",
					"function": map[string]any{"name": item["name"], "arguments": item["arguments"]},
				})
			}
		}
	}
	// Some gateways return a convenience output_text field.
	if len(textParts) == 0 {
		if text := stringValue(body["output_text"]); text != "" {
			textParts = append(textParts, text)
		}
	}
	if len(textParts) > 0 {
		message["content"] = strings.Join(textParts, "")
	} else {
		message["content"] = nil
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
		finish = "tool_calls"
	}

	usage := map[string]any{"prompt_tokens": 0, "completion_tokens": 0}
	if u, ok := body["usage"].(map[string]any); ok {
		usage["prompt_tokens"] = firstNonNil(u["input_tokens"], 0)
		usage["completion_tokens"] = firstNonNil(u["output_tokens"], 0)
	}
	return map[string]any{
		"id":      firstNonNilString(stringValue(body["id"]), "chatcmpl_proxy"),
		"object":  "chat.completion",
		"model":   stringValue(body["model"]),
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}},
		"usage":   usage,
	}
}

// chatToResponsesResponse converts the OpenAI Chat IR response into a Responses
// API response (used when a Responses client is served by another upstream).
func chatToResponsesResponse(ir map[string]any, requestModel string) map[string]any {
	var output []any
	if choices, ok := ir["choices"].([]any); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]any); ok {
			if message, ok := choice["message"].(map[string]any); ok {
				if text := stringValue(message["content"]); text != "" {
					output = append(output, map[string]any{
						"type": "message", "role": "assistant", "status": "completed",
						"content": []any{map[string]any{"type": "output_text", "text": text}},
					})
				}
				if calls, ok := message["tool_calls"].([]any); ok {
					for _, raw := range calls {
						call, ok := raw.(map[string]any)
						if !ok {
							continue
						}
						fn, _ := call["function"].(map[string]any)
						item := map[string]any{"type": "function_call", "call_id": call["id"]}
						if fn != nil {
							item["name"] = fn["name"]
							item["arguments"] = fn["arguments"]
						}
						output = append(output, item)
					}
				}
			}
		}
	}
	model := stringValue(ir["model"])
	if model == "" {
		model = requestModel
	}
	usage := map[string]any{"input_tokens": 0, "output_tokens": 0}
	if u, ok := ir["usage"].(map[string]any); ok {
		usage["input_tokens"] = firstNonNil(u["prompt_tokens"], 0)
		usage["output_tokens"] = firstNonNil(u["completion_tokens"], 0)
	}
	return map[string]any{
		"id":     firstNonNilString(stringValue(ir["id"]), "resp_proxy"),
		"object": "response",
		"status": "completed",
		"model":  model,
		"output": output,
		"usage":  usage,
	}
}

func responsesContentText(content any) string {
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
			case "input_text", "output_text", "text", "":
				sb.WriteString(stringValue(part["text"]))
			}
		}
		return sb.String()
	default:
		if v == nil {
			return ""
		}
		b, _ := json.Marshal(v)
		return string(b)
	}
}

func firstNonNilString(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
