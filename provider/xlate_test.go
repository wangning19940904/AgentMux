package provider

import (
	"net/http"
	"strings"
	"testing"
)

// roundTrip decodes a client body to the IR then encodes it for an upstream,
// exercising a full request-translation chain.
func encodeChain(t *testing.T, clientProto, upstream string, body map[string]any) map[string]any {
	t.Helper()
	ir, err := decodeToIR(clientProto, body)
	if err != nil {
		t.Fatalf("decodeToIR(%s): %v", clientProto, err)
	}
	out, err := encodeFromIR(upstream, ir)
	if err != nil {
		t.Fatalf("encodeFromIR(%s): %v", upstream, err)
	}
	return out
}

func TestAnthropicToGeminiRequest(t *testing.T) {
	body := map[string]any{
		"model":  "claude-sonnet-4-8",
		"system": "be terse",
		"messages": []any{
			map[string]any{"role": "user", "content": "hi"},
		},
		"tools": []any{map[string]any{
			"name": "read_file", "description": "read",
			"input_schema": map[string]any{
				"type":       "object",
				"properties": map[string]any{"path": map[string]any{"type": "string", "minLength": 1}},
			},
		}},
	}
	out := encodeChain(t, protoAnthropic, protoGemini, body)

	sys, ok := out["systemInstruction"].(map[string]any)
	if !ok {
		t.Fatalf("missing systemInstruction: %#v", out)
	}
	if txt := geminiPartsText(sys["parts"]); txt != "be terse" {
		t.Fatalf("system text = %q", txt)
	}
	tools, ok := out["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatalf("missing tools: %#v", out["tools"])
	}
	decls := tools[0].(map[string]any)["functionDeclarations"].([]any)
	params := decls[0].(map[string]any)["parameters"].(map[string]any)
	// Type upper-cased and minLength stripped by cleanGeminiSchema.
	if params["type"] != "OBJECT" {
		t.Fatalf("schema type = %v", params["type"])
	}
	props := params["properties"].(map[string]any)
	path := props["path"].(map[string]any)
	if path["type"] != "STRING" {
		t.Fatalf("path type = %v", path["type"])
	}
	if _, bad := path["minLength"]; bad {
		t.Fatal("minLength should be stripped for Gemini")
	}
}

func TestAnthropicToResponsesRequest(t *testing.T) {
	body := map[string]any{
		"model":  "claude-sonnet-4-8",
		"system": "sys",
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "id": "t1", "name": "ls", "input": map[string]any{"dir": "."}},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": "file.txt"},
			}},
		},
	}
	out := encodeChain(t, protoAnthropic, protoResponses, body)
	if out["instructions"] != "sys" {
		t.Fatalf("instructions = %v", out["instructions"])
	}
	input := out["input"].([]any)
	var sawCall, sawOutput bool
	for _, raw := range input {
		item := raw.(map[string]any)
		switch item["type"] {
		case "function_call":
			sawCall = true
			if item["call_id"] != "t1" || item["name"] != "ls" {
				t.Fatalf("function_call = %#v", item)
			}
		case "function_call_output":
			sawOutput = true
			if item["call_id"] != "t1" || item["output"] != "file.txt" {
				t.Fatalf("function_call_output = %#v", item)
			}
		}
	}
	if !sawCall || !sawOutput {
		t.Fatalf("missing call/output items: %#v", input)
	}
}

func TestGeminiResponseToAnthropic(t *testing.T) {
	geminiResp := map[string]any{
		"candidates": []any{map[string]any{
			"content": map[string]any{"role": "model", "parts": []any{
				map[string]any{"text": "hello"},
				map[string]any{"functionCall": map[string]any{"name": "ls", "args": map[string]any{"dir": "."}}},
			}},
			"finishReason": "STOP",
		}},
		"usageMetadata": map[string]any{"promptTokenCount": 7, "candidatesTokenCount": 3},
	}
	ir, err := upstreamRespToIR(protoGemini, geminiResp)
	if err != nil {
		t.Fatal(err)
	}
	anthropic, err := irRespToClient(protoAnthropic, ir, "claude-sonnet-4-8")
	if err != nil {
		t.Fatal(err)
	}
	if anthropic["stop_reason"] != "tool_use" {
		t.Fatalf("stop_reason = %v", anthropic["stop_reason"])
	}
	content := anthropic["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("content = %#v", content)
	}
	tool := content[1].(map[string]any)
	if tool["type"] != "tool_use" || tool["name"] != "ls" {
		t.Fatalf("tool_use = %#v", tool)
	}
	usage := anthropic["usage"].(map[string]any)
	if intValue(usage["input_tokens"]) != 7 || intValue(usage["output_tokens"]) != 3 {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestResponsesResponseToAnthropic(t *testing.T) {
	resp := map[string]any{
		"model": "gpt-5",
		"output": []any{
			map[string]any{"type": "message", "content": []any{
				map[string]any{"type": "output_text", "text": "done"},
			}},
			map[string]any{"type": "function_call", "call_id": "c1", "name": "grep", "arguments": `{"q":"x"}`},
		},
		"usage": map[string]any{"input_tokens": 5, "output_tokens": 9},
	}
	ir, err := upstreamRespToIR(protoResponses, resp)
	if err != nil {
		t.Fatal(err)
	}
	anthropic, err := irRespToClient(protoAnthropic, ir, "claude-sonnet-4-8")
	if err != nil {
		t.Fatal(err)
	}
	if anthropic["stop_reason"] != "tool_use" {
		t.Fatalf("stop_reason = %v", anthropic["stop_reason"])
	}
	content := anthropic["content"].([]any)
	if len(content) != 2 || content[0].(map[string]any)["text"] != "done" {
		t.Fatalf("content = %#v", content)
	}
}

func TestGeminiClientRequestToChat(t *testing.T) {
	body := map[string]any{
		"contents": []any{
			map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hey"}}},
		},
		"systemInstruction": map[string]any{"parts": []any{map[string]any{"text": "sys"}}},
		"generationConfig":  map[string]any{"maxOutputTokens": 128, "temperature": 0.5},
	}
	ir, err := decodeToIR(protoGemini, body)
	if err != nil {
		t.Fatal(err)
	}
	messages := ir["messages"].([]any)
	if messages[0].(map[string]any)["role"] != "system" {
		t.Fatalf("first message = %#v", messages[0])
	}
	if ir["max_tokens"] != 128 {
		t.Fatalf("max_tokens = %v", ir["max_tokens"])
	}
}

func TestChatStreamToGeminiSSE(t *testing.T) {
	chatSSE := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"He"}}]}`,
		`data: {"choices":[{"delta":{"content":"llo"}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3}}`,
		"data: [DONE]",
		"",
	}, "\n\n")
	rec := &recordingResponse{header: http.Header{}}
	if err := chatStreamToGeminiSSE(strings.NewReader(chatSSE), rec, "gemini-pro"); err != nil {
		t.Fatal(err)
	}
	out := rec.body.String()
	if !strings.Contains(out, `"text":"He"`) || !strings.Contains(out, `"finishReason":"STOP"`) {
		t.Fatalf("gemini stream missing content:\n%s", out)
	}
	if !strings.Contains(out, `"candidatesTokenCount":3`) {
		t.Fatalf("gemini stream missing usage:\n%s", out)
	}
}

func TestAnthropicStreamNormalizedToChat(t *testing.T) {
	anthropicSSE := strings.Join([]string{
		`event: message_start` + "\n" + `data: {"type":"message_start","message":{"usage":{"input_tokens":4}}}`,
		`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
		"",
	}, "\n\n")
	var sb strings.Builder
	if err := anthropicSSEToChatSSE(strings.NewReader(anthropicSSE), &sb); err != nil {
		t.Fatal(err)
	}
	out := sb.String()
	if !strings.Contains(out, `"content":"hi"`) {
		t.Fatalf("chat stream missing text:\n%s", out)
	}
	if !strings.Contains(out, `"finish_reason":"stop"`) {
		t.Fatalf("chat stream missing finish:\n%s", out)
	}
	// Confirm the final chunk carries usage.
	if !strings.Contains(out, `"completion_tokens":2`) {
		t.Fatalf("chat stream missing usage:\n%s", out)
	}
}

func TestToolChoiceRoundTrip(t *testing.T) {
	// Anthropic "any" -> OpenAI "required" -> back to Anthropic "any".
	ir, err := anthropicToChatRequest(map[string]any{
		"model":       "m",
		"messages":    []any{},
		"tool_choice": map[string]any{"type": "any"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if ir["tool_choice"] != "required" {
		t.Fatalf("tool_choice = %v", ir["tool_choice"])
	}
	back := chatToAnthropicRequest(ir)
	tc, _ := back["tool_choice"].(map[string]any)
	if tc == nil || tc["type"] != "any" {
		t.Fatalf("round-trip tool_choice = %#v", back["tool_choice"])
	}
}

func TestUnsupportedProtocolErrors(t *testing.T) {
	if _, err := decodeToIR("bogus", map[string]any{}); err == nil {
		t.Fatal("expected error for unknown client protocol")
	}
	if _, err := encodeFromIR("bogus", map[string]any{}); err == nil {
		t.Fatal("expected error for unknown upstream protocol")
	}
}

// recordingResponse is a minimal http.ResponseWriter capturing the body.
type recordingResponse struct {
	header http.Header
	body   strings.Builder
	status int
}

func (r *recordingResponse) Header() http.Header         { return r.header }
func (r *recordingResponse) Write(p []byte) (int, error) { return r.body.Write(p) }
func (r *recordingResponse) WriteHeader(status int)      { r.status = status }
