package provider

import (
	"encoding/json"
	"strings"
)

// xlate_gemini.go converts between the OpenAI Chat IR and Google Gemini's
// generateContent shape. Gemini's function-declaration schema rejects many
// JSON-Schema keywords, so tool parameters are cleaned before being sent
// (cleanGeminiSchema). Gemini functionCall parts often carry no id, so a
// deterministic synthetic id is produced when converting to the IR and
// stripped again when converting back (geminiSynthPrefix).

const geminiSynthPrefix = "gemini_call_"

// chatToGeminiRequest converts the OpenAI Chat IR request into a Gemini
// generateContent request body. The model id is carried in the URL by the
// caller, not in the body.
func chatToGeminiRequest(ir map[string]any) map[string]any {
	out := map[string]any{}

	var systemParts []string
	var contents []any
	// toolNameByID lets us recover the function name for a tool result, since
	// Gemini keys functionResponse by name, not by call id.
	toolNameByID := map[string]string{}

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
			name := toolNameByID[stringValue(msg["tool_call_id"])]
			if name == "" {
				name = "tool"
			}
			contents = append(contents, map[string]any{
				"role": "user",
				"parts": []any{map[string]any{
					"functionResponse": map[string]any{
						"name":     name,
						"response": map[string]any{"content": messageContentText(msg["content"])},
					},
				}},
			})
		case "assistant":
			var parts []any
			if text := messageContentText(msg["content"]); text != "" {
				parts = append(parts, map[string]any{"text": text})
			}
			if calls, ok := msg["tool_calls"].([]any); ok {
				for _, rawCall := range calls {
					call, ok := rawCall.(map[string]any)
					if !ok {
						continue
					}
					fn, _ := call["function"].(map[string]any)
					if fn == nil {
						continue
					}
					name := stringValue(fn["name"])
					toolNameByID[stringValue(call["id"])] = name
					var args any = map[string]any{}
					if raw := stringValue(fn["arguments"]); raw != "" {
						var parsed any
						if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
							args = parsed
						}
					}
					parts = append(parts, map[string]any{
						"functionCall": map[string]any{"name": name, "args": args},
					})
				}
			}
			if len(parts) > 0 {
				contents = append(contents, map[string]any{"role": "model", "parts": parts})
			}
		default: // user
			if text := messageContentText(msg["content"]); text != "" {
				contents = append(contents, map[string]any{
					"role":  "user",
					"parts": []any{map[string]any{"text": text}},
				})
			}
		}
	}
	out["contents"] = contents
	if len(systemParts) > 0 {
		out["systemInstruction"] = map[string]any{
			"parts": []any{map[string]any{"text": strings.Join(systemParts, "\n\n")}},
		}
	}

	genConfig := map[string]any{}
	if v, ok := ir["max_tokens"]; ok && v != nil {
		genConfig["maxOutputTokens"] = v
	}
	if v, ok := ir["temperature"]; ok {
		genConfig["temperature"] = v
	}
	if v, ok := ir["top_p"]; ok {
		genConfig["topP"] = v
	}
	if v, ok := ir["stop"]; ok {
		genConfig["stopSequences"] = v
	}
	if len(genConfig) > 0 {
		out["generationConfig"] = genConfig
	}

	if tools, ok := ir["tools"].([]any); ok && len(tools) > 0 {
		var decls []any
		for _, raw := range tools {
			tool, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			fn, _ := tool["function"].(map[string]any)
			if fn == nil {
				continue
			}
			decl := map[string]any{"name": fn["name"]}
			if desc := fn["description"]; desc != nil {
				decl["description"] = desc
			}
			params := cleanGeminiSchema(fn["parameters"])
			if params == nil {
				params = map[string]any{"type": "OBJECT", "properties": map[string]any{}}
			}
			decl["parameters"] = params
			decls = append(decls, decl)
		}
		if len(decls) > 0 {
			out["tools"] = []any{map[string]any{"functionDeclarations": decls}}
		}
	}
	return out
}

// cleanGeminiSchema recursively strips JSON-Schema keywords Gemini rejects and
// upper-cases "type" values. Empty objects get a placeholder property so Vertex
// AI does not reject them.
func cleanGeminiSchema(schema any) any {
	obj, ok := schema.(map[string]any)
	if !ok {
		return schema
	}
	unsupported := map[string]bool{
		"$schema": true, "$id": true, "$ref": true, "additionalProperties": true,
		"const": true, "minLength": true, "maxLength": true, "pattern": true,
		"exclusiveMinimum": true, "exclusiveMaximum": true, "multipleOf": true,
		"minItems": true, "maxItems": true, "uniqueItems": true, "format": true,
		"default": true, "examples": true, "title": true,
	}
	out := map[string]any{}
	for k, v := range obj {
		if unsupported[k] {
			continue
		}
		switch k {
		case "type":
			if s, ok := v.(string); ok {
				out["type"] = strings.ToUpper(s)
			} else {
				out["type"] = v
			}
		case "properties":
			if props, ok := v.(map[string]any); ok {
				cleaned := map[string]any{}
				for name, sub := range props {
					cleaned[name] = cleanGeminiSchema(sub)
				}
				out["properties"] = cleaned
			}
		case "items":
			out["items"] = cleanGeminiSchema(v)
		default:
			out[k] = v
		}
	}
	if strings.EqualFold(stringValue(out["type"]), "object") {
		if _, ok := out["properties"]; !ok {
			out["properties"] = map[string]any{}
		}
	}
	return out
}

// geminiRespToChat converts a non-streaming Gemini generateContent response
// into the OpenAI Chat IR response shape.
func geminiRespToChat(body map[string]any) map[string]any {
	message := map[string]any{"role": "assistant"}
	var textParts []string
	var toolCalls []any
	finish := "stop"
	callIdx := 0

	if candidates, ok := body["candidates"].([]any); ok && len(candidates) > 0 {
		cand, _ := candidates[0].(map[string]any)
		if cand != nil {
			if reason := stringValue(cand["finishReason"]); reason != "" {
				finish = geminiFinishToChat(reason)
			}
			if content, ok := cand["content"].(map[string]any); ok {
				if parts, ok := content["parts"].([]any); ok {
					for _, raw := range parts {
						part, ok := raw.(map[string]any)
						if !ok {
							continue
						}
						if text := stringValue(part["text"]); text != "" {
							textParts = append(textParts, text)
						}
						if fc, ok := part["functionCall"].(map[string]any); ok {
							args, _ := json.Marshal(firstNonNil(fc["args"], map[string]any{}))
							toolCalls = append(toolCalls, map[string]any{
								"id":   geminiSynthID(callIdx),
								"type": "function",
								"function": map[string]any{
									"name":      fc["name"],
									"arguments": string(args),
								},
							})
							callIdx++
						}
					}
				}
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
		finish = "tool_calls"
	}

	usage := map[string]any{"prompt_tokens": 0, "completion_tokens": 0}
	if u, ok := body["usageMetadata"].(map[string]any); ok {
		usage["prompt_tokens"] = firstNonNil(u["promptTokenCount"], 0)
		usage["completion_tokens"] = firstNonNil(u["candidatesTokenCount"], 0)
	}
	return map[string]any{
		"id":      "chatcmpl_proxy",
		"object":  "chat.completion",
		"model":   stringValue(body["modelVersion"]),
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}},
		"usage":   usage,
	}
}

// geminiToChatRequest converts a Gemini generateContent client request into the
// OpenAI Chat IR (used when a Gemini CLI client is routed to a non-Gemini
// upstream).
func geminiToChatRequest(body map[string]any) (map[string]any, error) {
	out := map[string]any{}
	var messages []any

	if sys, ok := body["systemInstruction"].(map[string]any); ok {
		if text := geminiPartsText(sys["parts"]); text != "" {
			messages = append(messages, map[string]any{"role": "system", "content": text})
		}
	}

	contents, _ := body["contents"].([]any)
	for _, raw := range contents {
		content, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role := stringValue(content["role"])
		parts, _ := content["parts"].([]any)
		var text strings.Builder
		var toolCalls []any
		var toolResults []map[string]any
		callIdx := 0
		for _, rawPart := range parts {
			part, ok := rawPart.(map[string]any)
			if !ok {
				continue
			}
			if t := stringValue(part["text"]); t != "" {
				text.WriteString(t)
			}
			if fc, ok := part["functionCall"].(map[string]any); ok {
				args, _ := json.Marshal(firstNonNil(fc["args"], map[string]any{}))
				toolCalls = append(toolCalls, map[string]any{
					"id":       geminiSynthID(callIdx),
					"type":     "function",
					"function": map[string]any{"name": fc["name"], "arguments": string(args)},
				})
				callIdx++
			}
			if fr, ok := part["functionResponse"].(map[string]any); ok {
				resp, _ := json.Marshal(fr["response"])
				toolResults = append(toolResults, map[string]any{
					"role":         "tool",
					"tool_call_id": geminiSynthID(len(toolResults)),
					"content":      string(resp),
				})
			}
		}
		switch role {
		case "model":
			msg := map[string]any{"role": "assistant"}
			if text.Len() > 0 {
				msg["content"] = text.String()
			} else {
				msg["content"] = nil
			}
			if len(toolCalls) > 0 {
				msg["tool_calls"] = toolCalls
			}
			messages = append(messages, msg)
		default:
			for _, tr := range toolResults {
				messages = append(messages, tr)
			}
			if text.Len() > 0 {
				messages = append(messages, map[string]any{"role": "user", "content": text.String()})
			}
		}
	}
	out["messages"] = messages

	if cfg, ok := body["generationConfig"].(map[string]any); ok {
		if v, ok := cfg["maxOutputTokens"]; ok {
			out["max_tokens"] = v
		}
		if v, ok := cfg["temperature"]; ok {
			out["temperature"] = v
		}
		if v, ok := cfg["topP"]; ok {
			out["top_p"] = v
		}
		if v, ok := cfg["stopSequences"]; ok {
			out["stop"] = v
		}
	}

	if tools, ok := body["tools"].([]any); ok {
		var converted []any
		for _, raw := range tools {
			tool, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			decls, _ := tool["functionDeclarations"].([]any)
			for _, rawDecl := range decls {
				decl, ok := rawDecl.(map[string]any)
				if !ok {
					continue
				}
				fn := map[string]any{"name": decl["name"], "description": decl["description"]}
				if params, ok := decl["parameters"]; ok {
					fn["parameters"] = params
				}
				converted = append(converted, map[string]any{"type": "function", "function": fn})
			}
		}
		if len(converted) > 0 {
			out["tools"] = converted
		}
	}
	return out, nil
}

// chatToGeminiResponse converts the OpenAI Chat IR response into a Gemini
// generateContent response (used when a Gemini client is served by a
// non-Gemini upstream).
func chatToGeminiResponse(ir map[string]any, requestModel string) map[string]any {
	var parts []any
	finish := "STOP"
	if choices, ok := ir["choices"].([]any); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]any); ok {
			finish = chatFinishToGemini(stringValue(choice["finish_reason"]))
			if message, ok := choice["message"].(map[string]any); ok {
				if text := stringValue(message["content"]); text != "" {
					parts = append(parts, map[string]any{"text": text})
				}
				if calls, ok := message["tool_calls"].([]any); ok {
					for _, raw := range calls {
						call, ok := raw.(map[string]any)
						if !ok {
							continue
						}
						fn, _ := call["function"].(map[string]any)
						var args any = map[string]any{}
						name := ""
						if fn != nil {
							name = stringValue(fn["name"])
							if raw := stringValue(fn["arguments"]); raw != "" {
								var parsed any
								if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
									args = parsed
								}
							}
						}
						parts = append(parts, map[string]any{
							"functionCall": map[string]any{"name": name, "args": args},
						})
					}
				}
			}
		}
	}
	model := requestModel
	if m := stringValue(ir["model"]); m != "" {
		model = m
	}
	usage := map[string]any{"promptTokenCount": 0, "candidatesTokenCount": 0}
	if u, ok := ir["usage"].(map[string]any); ok {
		usage["promptTokenCount"] = firstNonNil(u["prompt_tokens"], 0)
		usage["candidatesTokenCount"] = firstNonNil(u["completion_tokens"], 0)
	}
	return map[string]any{
		"candidates": []any{map[string]any{
			"content":      map[string]any{"role": "model", "parts": parts},
			"finishReason": finish,
			"index":        0,
		}},
		"modelVersion":  model,
		"usageMetadata": usage,
	}
}

func geminiPartsText(parts any) string {
	list, ok := parts.([]any)
	if !ok {
		return ""
	}
	var sb strings.Builder
	for _, raw := range list {
		if part, ok := raw.(map[string]any); ok {
			sb.WriteString(stringValue(part["text"]))
		}
	}
	return sb.String()
}

func geminiSynthID(idx int) string {
	return geminiSynthPrefix + itoa(idx)
}

func geminiFinishToChat(reason string) string {
	switch strings.ToUpper(reason) {
	case "MAX_TOKENS":
		return "length"
	case "STOP":
		return "stop"
	default:
		return "stop"
	}
}

func chatFinishToGemini(reason string) string {
	switch reason {
	case "length":
		return "MAX_TOKENS"
	case "tool_calls", "function_call":
		return "STOP"
	default:
		return "STOP"
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
