package provider

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// xlate_stream.go implements the streaming half of the translation hub. Every
// upstream SSE stream is first normalized into an OpenAI Chat Completions SSE
// byte stream, then re-emitted in the client's protocol. This lets the four
// client emitters share one normalized input (ccr-x's approach: fan-in to
// OpenAI chunks, fan-out to the target format).

// streamUpstreamToClient converts an upstream SSE stream (upstreamProto) into
// the client's SSE format (clientProto) and writes it to w. Same-protocol
// streams are copied verbatim.
func streamUpstreamToClient(upstreamProto, clientProto string, upstream io.Reader, w http.ResponseWriter, requestModel string) error {
	if upstreamProto == clientProto {
		return copySSE(upstream, w)
	}
	chatSSE := normalizeToChatSSE(upstreamProto, upstream)
	switch clientProto {
	case protoAnthropic:
		return chatStreamToAnthropicSSE(chatSSE, w, requestModel)
	case protoOpenAIChat:
		return copySSE(chatSSE, w)
	case protoGemini:
		return chatStreamToGeminiSSE(chatSSE, w, requestModel)
	case protoResponses:
		return chatStreamToResponsesSSE(chatSSE, w, requestModel)
	default:
		return fmt.Errorf("unsupported client protocol %q for streaming", clientProto)
	}
}

// normalizeToChatSSE returns an io.Reader emitting OpenAI Chat Completions SSE,
// converting from upstreamProto on the fly. openai_chat is returned as-is.
func normalizeToChatSSE(upstreamProto string, upstream io.Reader) io.Reader {
	if upstreamProto == protoOpenAIChat {
		return upstream
	}
	pr, pw := io.Pipe()
	go func() {
		var err error
		switch upstreamProto {
		case protoAnthropic:
			err = anthropicSSEToChatSSE(upstream, pw)
		case protoResponses:
			err = responsesSSEToChatSSE(upstream, pw)
		case protoGemini:
			err = geminiSSEToChatSSE(upstream, pw)
		default:
			err = fmt.Errorf("unsupported upstream protocol %q for streaming", upstreamProto)
		}
		_ = pw.CloseWithError(err)
	}()
	return pr
}

// sseEvent is one parsed SSE frame (event name + data payload).
type sseEvent struct {
	name string
	data string
}

// scanSSE invokes fn for each SSE frame in r. Frames are delimited by blank
// lines; multiple data: lines are concatenated.
func scanSSE(r io.Reader, fn func(sseEvent) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	var ev sseEvent
	var data strings.Builder
	flush := func() error {
		if data.Len() == 0 && ev.name == "" {
			return nil
		}
		ev.data = data.String()
		err := fn(ev)
		ev = sseEvent{}
		data.Reset()
		return err
	}
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimRight(line, "\r")
		if trimmed == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		switch {
		case strings.HasPrefix(trimmed, "event:"):
			ev.name = strings.TrimSpace(strings.TrimPrefix(trimmed, "event:"))
		case strings.HasPrefix(trimmed, "data:"):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(trimmed, "data:")))
		}
	}
	if err := flush(); err != nil {
		return err
	}
	return scanner.Err()
}

// writeChatChunk serializes an OpenAI chat.completion.chunk and writes it as an
// SSE data frame.
func writeChatChunk(w io.Writer, chunk map[string]any) error {
	chunk["object"] = "chat.completion.chunk"
	data, err := json.Marshal(chunk)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", data)
	return err
}

func chatDeltaChunk(delta map[string]any) map[string]any {
	return map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta}}}
}

// copySSE relays an SSE stream to the client, flushing as it goes.
func copySSE(r io.Reader, w http.ResponseWriter) error {
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return werr
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// ---- upstream SSE -> OpenAI chat SSE normalizers ----

// anthropicSSEToChatSSE converts an Anthropic Messages SSE stream into OpenAI
// chat SSE. Tool_use blocks map onto indexed tool_calls.
func anthropicSSEToChatSSE(upstream io.Reader, w io.Writer) error {
	toolIndex := -1
	blockIsTool := false
	inputTokens, outputTokens := 0, 0
	finish := "stop"

	err := scanSSE(upstream, func(ev sseEvent) error {
		if ev.data == "" {
			return nil
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(ev.data), &payload); err != nil {
			return nil
		}
		switch stringValue(payload["type"]) {
		case "content_block_start":
			block, _ := payload["content_block"].(map[string]any)
			if block != nil && stringValue(block["type"]) == "tool_use" {
				toolIndex++
				blockIsTool = true
				return writeChatChunk(w, chatDeltaChunk(map[string]any{
					"tool_calls": []any{map[string]any{
						"index": toolIndex,
						"id":    block["id"],
						"type":  "function",
						"function": map[string]any{
							"name":      block["name"],
							"arguments": "",
						},
					}},
				}))
			}
			blockIsTool = false
		case "content_block_delta":
			delta, _ := payload["delta"].(map[string]any)
			if delta == nil {
				return nil
			}
			switch stringValue(delta["type"]) {
			case "text_delta":
				return writeChatChunk(w, chatDeltaChunk(map[string]any{"content": stringValue(delta["text"])}))
			case "input_json_delta":
				if blockIsTool {
					return writeChatChunk(w, chatDeltaChunk(map[string]any{
						"tool_calls": []any{map[string]any{
							"index":    toolIndex,
							"function": map[string]any{"arguments": stringValue(delta["partial_json"])},
						}},
					}))
				}
			}
		case "message_delta":
			if delta, ok := payload["delta"].(map[string]any); ok {
				if reason := stringValue(delta["stop_reason"]); reason != "" {
					finish = stopReasonToChatFinish(reason)
				}
			}
			if usage, ok := payload["usage"].(map[string]any); ok {
				outputTokens = intValue(usage["output_tokens"])
			}
		case "message_start":
			if msg, ok := payload["message"].(map[string]any); ok {
				if usage, ok := msg["usage"].(map[string]any); ok {
					inputTokens = intValue(usage["input_tokens"])
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return writeChatFinalChunk(w, finish, inputTokens, outputTokens)
}

// responsesSSEToChatSSE converts a Responses API SSE stream into OpenAI chat
// SSE.
func responsesSSEToChatSSE(upstream io.Reader, w io.Writer) error {
	toolIndex := -1
	inputTokens, outputTokens := 0, 0
	finish := "stop"
	err := scanSSE(upstream, func(ev sseEvent) error {
		if ev.data == "" || ev.data == "[DONE]" {
			return nil
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(ev.data), &payload); err != nil {
			return nil
		}
		typ := stringValue(payload["type"])
		if typ == "" {
			typ = ev.name
		}
		switch typ {
		case "response.output_text.delta":
			return writeChatChunk(w, chatDeltaChunk(map[string]any{"content": stringValue(payload["delta"])}))
		case "response.output_item.added":
			item, _ := payload["item"].(map[string]any)
			if item != nil && stringValue(item["type"]) == "function_call" {
				toolIndex++
				finish = "tool_calls"
				return writeChatChunk(w, chatDeltaChunk(map[string]any{
					"tool_calls": []any{map[string]any{
						"index":    toolIndex,
						"id":       item["call_id"],
						"type":     "function",
						"function": map[string]any{"name": item["name"], "arguments": ""},
					}},
				}))
			}
		case "response.function_call_arguments.delta":
			if toolIndex < 0 {
				toolIndex = 0
			}
			return writeChatChunk(w, chatDeltaChunk(map[string]any{
				"tool_calls": []any{map[string]any{
					"index":    toolIndex,
					"function": map[string]any{"arguments": stringValue(payload["delta"])},
				}},
			}))
		case "response.completed":
			if resp, ok := payload["response"].(map[string]any); ok {
				if usage, ok := resp["usage"].(map[string]any); ok {
					inputTokens = intValue(usage["input_tokens"])
					outputTokens = intValue(usage["output_tokens"])
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return writeChatFinalChunk(w, finish, inputTokens, outputTokens)
}

// geminiSSEToChatSSE converts a Gemini streamGenerateContent SSE stream into
// OpenAI chat SSE.
func geminiSSEToChatSSE(upstream io.Reader, w io.Writer) error {
	toolIndex := -1
	inputTokens, outputTokens := 0, 0
	finish := "stop"
	err := scanSSE(upstream, func(ev sseEvent) error {
		if ev.data == "" {
			return nil
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(ev.data), &payload); err != nil {
			return nil
		}
		if u, ok := payload["usageMetadata"].(map[string]any); ok {
			inputTokens = intValue(u["promptTokenCount"])
			outputTokens = intValue(u["candidatesTokenCount"])
		}
		candidates, _ := payload["candidates"].([]any)
		if len(candidates) == 0 {
			return nil
		}
		cand, _ := candidates[0].(map[string]any)
		if cand == nil {
			return nil
		}
		if reason := stringValue(cand["finishReason"]); reason != "" {
			finish = geminiFinishToChat(reason)
		}
		content, _ := cand["content"].(map[string]any)
		if content == nil {
			return nil
		}
		parts, _ := content["parts"].([]any)
		for _, raw := range parts {
			part, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if text := stringValue(part["text"]); text != "" {
				if err := writeChatChunk(w, chatDeltaChunk(map[string]any{"content": text})); err != nil {
					return err
				}
			}
			if fc, ok := part["functionCall"].(map[string]any); ok {
				toolIndex++
				finish = "tool_calls"
				args, _ := json.Marshal(firstNonNil(fc["args"], map[string]any{}))
				if err := writeChatChunk(w, chatDeltaChunk(map[string]any{
					"tool_calls": []any{map[string]any{
						"index":    toolIndex,
						"id":       geminiSynthID(toolIndex),
						"type":     "function",
						"function": map[string]any{"name": fc["name"], "arguments": string(args)},
					}},
				})); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return writeChatFinalChunk(w, finish, inputTokens, outputTokens)
}

func writeChatFinalChunk(w io.Writer, finish string, inputTokens, outputTokens int) error {
	if err := writeChatChunk(w, map[string]any{
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}},
		"usage":   map[string]any{"prompt_tokens": inputTokens, "completion_tokens": outputTokens},
	}); err != nil {
		return err
	}
	_, err := io.WriteString(w, "data: [DONE]\n\n")
	return err
}

func intValue(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	default:
		return 0
	}
}
