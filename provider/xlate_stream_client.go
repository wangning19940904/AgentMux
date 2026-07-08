package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// xlate_stream_client.go emits Gemini and Responses SSE to clients from a
// normalized OpenAI chat SSE stream. (Anthropic emission lives in convert.go's
// chatStreamToAnthropicSSE; OpenAI passthrough uses copySSE.)

// toolCallAccum accumulates streamed tool-call fragments by index so complete
// calls can be emitted when the stream ends (Gemini and Responses need the
// full arguments string, not per-token deltas).
type toolCallAccum struct {
	id   string
	name string
	args strings.Builder
}

// collectChatStream drains a normalized OpenAI chat SSE stream, invoking
// onText for each text delta and returning the finish reason, usage and the
// ordered tool calls.
func collectChatStream(chatSSE io.Reader, onText func(string) error) (finish string, inputTokens, outputTokens int, tools []*toolCallAccum, err error) {
	finish = "stop"
	byIndex := map[int]*toolCallAccum{}
	var order []int
	scanErr := scanSSE(chatSSE, func(ev sseEvent) error {
		if ev.data == "" || ev.data == "[DONE]" {
			return nil
		}
		var chunk map[string]any
		if e := json.Unmarshal([]byte(ev.data), &chunk); e != nil {
			return nil
		}
		if u, ok := chunk["usage"].(map[string]any); ok {
			inputTokens = intValue(u["prompt_tokens"])
			outputTokens = intValue(u["completion_tokens"])
		}
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			return nil
		}
		choice, _ := choices[0].(map[string]any)
		if choice == nil {
			return nil
		}
		if reason := stringValue(choice["finish_reason"]); reason != "" {
			finish = reason
		}
		delta, _ := choice["delta"].(map[string]any)
		if delta == nil {
			return nil
		}
		if text := stringValue(delta["content"]); text != "" && onText != nil {
			if e := onText(text); e != nil {
				return e
			}
		}
		if calls, ok := delta["tool_calls"].([]any); ok {
			for _, raw := range calls {
				call, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				idx := intValue(call["index"])
				acc := byIndex[idx]
				if acc == nil {
					acc = &toolCallAccum{}
					byIndex[idx] = acc
					order = append(order, idx)
				}
				if id := stringValue(call["id"]); id != "" {
					acc.id = id
				}
				if fn, ok := call["function"].(map[string]any); ok {
					if name := stringValue(fn["name"]); name != "" {
						acc.name = name
					}
					acc.args.WriteString(stringValue(fn["arguments"]))
				}
			}
		}
		return nil
	})
	if scanErr != nil {
		return finish, inputTokens, outputTokens, nil, scanErr
	}
	for _, idx := range order {
		tools = append(tools, byIndex[idx])
	}
	if len(tools) > 0 && finish == "stop" {
		finish = "tool_calls"
	}
	return finish, inputTokens, outputTokens, tools, nil
}

// chatStreamToGeminiSSE emits a Gemini streamGenerateContent SSE stream. Gemini
// clients tolerate a small number of chunks; text is streamed as it arrives and
// tool calls plus the final usage/finish are emitted at the end.
func chatStreamToGeminiSSE(chatSSE io.Reader, w http.ResponseWriter, requestModel string) error {
	flusher, _ := w.(http.Flusher)
	emit := func(payload map[string]any) error {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}
	onText := func(text string) error {
		return emit(map[string]any{
			"candidates": []any{map[string]any{
				"content": map[string]any{"role": "model", "parts": []any{map[string]any{"text": text}}},
				"index":   0,
			}},
		})
	}
	finish, inputTokens, outputTokens, tools, err := collectChatStream(chatSSE, onText)
	if err != nil {
		return err
	}
	var parts []any
	for _, tc := range tools {
		var args any = map[string]any{}
		if tc.args.Len() > 0 {
			var parsed any
			if e := json.Unmarshal([]byte(tc.args.String()), &parsed); e == nil {
				args = parsed
			}
		}
		parts = append(parts, map[string]any{"functionCall": map[string]any{"name": tc.name, "args": args}})
	}
	final := map[string]any{
		"candidates": []any{map[string]any{
			"content":      map[string]any{"role": "model", "parts": parts},
			"finishReason": chatFinishToGemini(finish),
			"index":        0,
		}},
		"modelVersion": requestModel,
		"usageMetadata": map[string]any{
			"promptTokenCount":     inputTokens,
			"candidatesTokenCount": outputTokens,
		},
	}
	return emit(final)
}

// chatStreamToResponsesSSE emits a minimal Responses API SSE stream: streamed
// text deltas followed by a terminal response.completed event carrying the
// full output (text + tool calls) and usage.
func chatStreamToResponsesSSE(chatSSE io.Reader, w http.ResponseWriter, requestModel string) error {
	flusher, _ := w.(http.Flusher)
	emit := func(event string, payload map[string]any) error {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}
	var full strings.Builder
	onText := func(text string) error {
		full.WriteString(text)
		return emit("response.output_text.delta", map[string]any{
			"type": "response.output_text.delta", "delta": text,
		})
	}
	finish, inputTokens, outputTokens, tools, err := collectChatStream(chatSSE, onText)
	if err != nil {
		return err
	}
	var output []any
	if full.Len() > 0 {
		output = append(output, map[string]any{
			"type": "message", "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": full.String()}},
		})
	}
	for _, tc := range tools {
		output = append(output, map[string]any{
			"type": "function_call", "call_id": tc.id,
			"name": tc.name, "arguments": tc.args.String(),
		})
	}
	_ = finish
	return emit("response.completed", map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id": "resp_proxy", "object": "response", "status": "completed",
			"model":  requestModel,
			"output": output,
			"usage":  map[string]any{"input_tokens": inputTokens, "output_tokens": outputTokens},
		},
	})
}
