package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func TestOpenAIResponsesAcceptsImagesAndDirectFiles(t *testing.T) {
	server, _ := newTestServer(t)
	invoker := &invocationTestService{result: core.InvocationResult{Answer: "seen"}}
	server.SetInvoker(invoker)
	png := []byte("fake-png")
	document := []byte("document text")
	recorder := doJSON(t, server, http.MethodPost, "/v1/responses", map[string]any{
		"model": "agent-1",
		"input": []any{map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "input_text", "text": "inspect both"},
				map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)},
				map[string]any{"type": "input_file", "filename": "notes.txt", "file_data": base64.StdEncoding.EncodeToString(document)},
			},
		}},
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if invoker.request.Input != "inspect both" || len(invoker.request.Attachments) != 2 {
		t.Fatalf("request = %+v", invoker.request)
	}
	if got := invoker.request.Attachments[0]; got.Kind != "image" || got.MIMEType != "image/png" || !bytes.Equal(got.Data, png) {
		t.Fatalf("image = %+v", got)
	}
	if got := invoker.request.Attachments[1]; got.Kind != "file" || got.Name != "notes.txt" || !bytes.Equal(got.Data, document) {
		t.Fatalf("file = %+v", got)
	}
}

func TestOpenAIFilesUploadAndUseByFileID(t *testing.T) {
	server, _ := newTestServer(t)
	invoker := &invocationTestService{result: core.InvocationResult{Answer: "read"}}
	server.SetInvoker(invoker)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("purpose", "user_data"); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("file", "report.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("quarterly report")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/files", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	recorder := httptest.NewRecorder()
	server.mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("upload status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var file openAIFileObject
	if err := json.Unmarshal(recorder.Body.Bytes(), &file); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(file.ID, "file-") || file.Filename != "report.txt" || file.Status != "processed" {
		t.Fatalf("file = %+v", file)
	}
	response := doJSON(t, server, http.MethodPost, "/v1/responses", map[string]any{
		"model": "agent-1",
		"input": []any{map[string]any{
			"role": "user", "content": []any{map[string]any{"type": "input_file", "file_id": file.ID}},
		}},
	})
	if response.Code != http.StatusOK {
		t.Fatalf("response status = %d, body = %s", response.Code, response.Body.String())
	}
	if len(invoker.request.Attachments) != 1 || string(invoker.request.Attachments[0].Data) != "quarterly report" || invoker.request.Attachments[0].Name != "report.txt" {
		t.Fatalf("resolved attachment = %+v", invoker.request.Attachments)
	}
	content := httptest.NewRecorder()
	server.mux.ServeHTTP(content, httptest.NewRequest(http.MethodGet, "/v1/files/"+file.ID+"/content", nil))
	if content.Code != http.StatusOK || content.Body.String() != "quarterly report" {
		t.Fatalf("content status=%d body=%q", content.Code, content.Body.String())
	}
}

func TestOpenAIResponsesValidatesStructuredOutput(t *testing.T) {
	server, _ := newTestServer(t)
	invoker := &invocationTestService{result: core.InvocationResult{Answer: "```json\n{\"name\":\"Ada\",\"age\":37}\n```"}}
	server.SetInvoker(invoker)
	request := map[string]any{
		"model": "agent-1", "input": "extract person",
		"text": map[string]any{"format": map[string]any{
			"type": "json_schema", "name": "person", "strict": true,
			"schema": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{"name": map[string]any{"type": "string"}, "age": map[string]any{"type": "integer"}},
				"required":   []string{"name", "age"},
			},
		}},
	}
	recorder := doJSON(t, server, http.MethodPost, "/v1/responses", request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response openAIResponseObject
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if got := response.Output[0].Content[0].Text; got != `{"age":37,"name":"Ada"}` && got != `{"name":"Ada","age":37}` {
		t.Fatalf("structured output = %q", got)
	}
	if invoker.request.OutputSchema["type"] != "object" || !strings.Contains(invoker.request.Input, "JSON Schema") {
		t.Fatalf("invocation = %+v", invoker.request)
	}

	invoker.result.Answer = `{"name":"Ada","extra":true}`
	invalid := doJSON(t, server, http.MethodPost, "/v1/responses", request)
	if invalid.Code != http.StatusBadGateway || !strings.Contains(invalid.Body.String(), "output_validation_failed") {
		t.Fatalf("invalid status = %d, body = %s", invalid.Code, invalid.Body.String())
	}
}

func TestOpenAIResponsesReturnsFunctionCalls(t *testing.T) {
	server, _ := newTestServer(t)
	answer := `{"agentmux_function_calls":[{"name":"get_weather","arguments":{"city":"Paris"}}]}`
	result := core.InvocationResult{Answer: answer}
	invoker := &invocationTestService{
		result: result,
		streamEvents: []core.InvocationStreamEvent{
			{Type: "started"}, {Type: "final", Text: answer}, {Type: "completed", Result: &result},
		},
	}
	server.SetInvoker(invoker)
	request := map[string]any{
		"model": "agent-1", "input": "weather", "tool_choice": "required",
		"tools": []any{map[string]any{
			"type": "function", "name": "get_weather", "description": "Get weather",
			"strict": true,
			"parameters": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{"city": map[string]any{"type": "string"}}, "required": []string{"city"},
			},
		}},
	}
	recorder := doJSON(t, server, http.MethodPost, "/v1/responses", request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response openAIResponseObject
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Output) != 1 || response.Output[0].Type != "function_call" || response.Output[0].Name != "get_weather" || response.Output[0].Arguments != `{"city":"Paris"}` || response.Output[0].CallID == "" {
		t.Fatalf("function output = %+v", response.Output)
	}
	if !strings.Contains(invoker.request.Input, "caller provides these functions") {
		t.Fatalf("tool prompt = %q", invoker.request.Input)
	}

	request["stream"] = true
	stream := doJSON(t, server, http.MethodPost, "/v1/responses", request)
	if stream.Code != http.StatusOK {
		t.Fatalf("stream status = %d, body = %s", stream.Code, stream.Body.String())
	}
	for _, want := range []string{
		"event: response.function_call_arguments.delta", "event: response.function_call_arguments.done", `"type":"function_call"`, "event: response.completed",
	} {
		if !strings.Contains(stream.Body.String(), want) {
			t.Fatalf("stream missing %q:\n%s", want, stream.Body.String())
		}
	}
	if strings.Contains(stream.Body.String(), "response.output_text.delta") {
		t.Fatalf("function call stream leaked sentinel as text:\n%s", stream.Body.String())
	}
}

func TestOpenAIResponsesAcceptsHostedTools(t *testing.T) {
	server, _ := newTestServer(t)
	invoker := &invocationTestService{result: core.InvocationResult{Answer: "researched"}}
	server.SetInvoker(invoker)
	recorder := doJSON(t, server, http.MethodPost, "/v1/responses", map[string]any{
		"model": "agent-1", "input": "latest news", "tools": []any{map[string]any{"type": "web_search"}},
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response openAIResponseObject
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Tools) != 1 || !strings.Contains(invoker.request.Input, "web_search") {
		t.Fatalf("response tools=%+v prompt=%q", response.Tools, invoker.request.Input)
	}
}

type openAIBackgroundTestInvoker struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	result  core.InvocationResult
}

func (i *openAIBackgroundTestInvoker) Invoke(ctx context.Context, _ core.InvocationRequest) (core.InvocationResult, error) {
	i.once.Do(func() { close(i.started) })
	select {
	case <-i.release:
		return i.result, nil
	case <-ctx.Done():
		return core.InvocationResult{}, ctx.Err()
	}
}

func TestOpenAIBackgroundResponseLifecycle(t *testing.T) {
	server, _ := newTestServer(t)
	invoker := &openAIBackgroundTestInvoker{
		started: make(chan struct{}), release: make(chan struct{}), result: core.InvocationResult{Answer: "finished"},
	}
	server.SetInvoker(invoker)
	created := doJSON(t, server, http.MethodPost, "/v1/responses", map[string]any{
		"model": "agent-1", "input": "long task", "background": true,
	})
	if created.Code != http.StatusOK {
		t.Fatalf("create status = %d, body = %s", created.Code, created.Body.String())
	}
	var initial openAIResponseObject
	if err := json.Unmarshal(created.Body.Bytes(), &initial); err != nil {
		t.Fatal(err)
	}
	if initial.Status != "queued" || !initial.Background {
		t.Fatalf("initial = %+v", initial)
	}
	select {
	case <-invoker.started:
	case <-time.After(2 * time.Second):
		t.Fatal("background invocation did not start")
	}
	close(invoker.release)
	completed := waitForOpenAIResponseStatus(t, server, initial.ID, "completed")
	if len(completed.Output) != 1 || completed.Output[0].Content[0].Text != "finished" {
		t.Fatalf("completed = %+v", completed)
	}
	inputItems := httptest.NewRecorder()
	server.mux.ServeHTTP(inputItems, httptest.NewRequest(http.MethodGet, "/v1/responses/"+initial.ID+"/input_items", nil))
	if inputItems.Code != http.StatusOK || !strings.Contains(inputItems.Body.String(), "long task") {
		t.Fatalf("input items status=%d body=%s", inputItems.Code, inputItems.Body.String())
	}
	deleted := httptest.NewRecorder()
	server.mux.ServeHTTP(deleted, httptest.NewRequest(http.MethodDelete, "/v1/responses/"+initial.ID, nil))
	if deleted.Code != http.StatusOK || !strings.Contains(deleted.Body.String(), `"deleted":true`) {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
}

func TestOpenAIBackgroundResponseCanBeCancelled(t *testing.T) {
	server, _ := newTestServer(t)
	invoker := &openAIBackgroundTestInvoker{started: make(chan struct{}), release: make(chan struct{})}
	server.SetInvoker(invoker)
	created := doJSON(t, server, http.MethodPost, "/v1/responses", map[string]any{
		"model": "agent-1", "input": "long task", "background": true,
	})
	var initial openAIResponseObject
	if err := json.Unmarshal(created.Body.Bytes(), &initial); err != nil {
		t.Fatal(err)
	}
	select {
	case <-invoker.started:
	case <-time.After(2 * time.Second):
		t.Fatal("background invocation did not start")
	}
	cancelled := doJSON(t, server, http.MethodPost, "/v1/responses/"+initial.ID+"/cancel", map[string]any{})
	if cancelled.Code != http.StatusOK {
		t.Fatalf("cancel status = %d, body = %s", cancelled.Code, cancelled.Body.String())
	}
	var response openAIResponseObject
	if err := json.Unmarshal(cancelled.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Status != "cancelled" {
		t.Fatalf("cancelled response = %+v", response)
	}
	again := doJSON(t, server, http.MethodPost, "/v1/responses/"+initial.ID+"/cancel", map[string]any{})
	if again.Code != http.StatusOK {
		t.Fatalf("second cancel status = %d, body = %s", again.Code, again.Body.String())
	}
}

func TestOpenAIBackgroundStreamCanBeResumedBySequence(t *testing.T) {
	server, _ := newTestServer(t)
	result := core.InvocationResult{Answer: "hello"}
	server.SetInvoker(&invocationTestService{
		result: result,
		streamEvents: []core.InvocationStreamEvent{
			{Type: "started"}, {Type: "output", Text: "hel"}, {Type: "final", Text: "hello"},
			{Type: "completed", Result: &result},
		},
	})
	stream := doJSON(t, server, http.MethodPost, "/v1/responses", map[string]any{
		"model": "agent-1", "input": "hello", "background": true, "stream": true, "store": false,
	})
	if stream.Code != http.StatusOK {
		t.Fatalf("stream status = %d, body = %s", stream.Code, stream.Body.String())
	}
	responseID := responseIDFromSSE(t, stream.Body.String())
	if !strings.Contains(stream.Body.String(), `"sequence_number":0`) || !strings.Contains(stream.Body.String(), "event: response.completed") {
		t.Fatalf("background stream = %s", stream.Body.String())
	}
	resumed := httptest.NewRecorder()
	resumeRequest := httptest.NewRequest(http.MethodGet, "/v1/responses/"+responseID+"?stream=true&starting_after=3", nil)
	server.mux.ServeHTTP(resumed, resumeRequest)
	if resumed.Code != http.StatusOK || !strings.Contains(resumed.Body.String(), "event: response.completed") {
		t.Fatalf("resume status=%d body=%s", resumed.Code, resumed.Body.String())
	}
	for _, excluded := range []string{`"sequence_number":0`, `"sequence_number":1`, `"sequence_number":2`, `"sequence_number":3`} {
		if strings.Contains(resumed.Body.String(), excluded) {
			t.Fatalf("resumed stream contains old cursor %s:\n%s", excluded, resumed.Body.String())
		}
	}
}

func responseIDFromSSE(t *testing.T, body string) string {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event struct {
			Type     string `json:"type"`
			Response struct {
				ID string `json:"id"`
			} `json:"response"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event) == nil && event.Response.ID != "" {
			return event.Response.ID
		}
	}
	t.Fatalf("stream does not contain a response id:\n%s", body)
	return ""
}

func waitForOpenAIResponseStatus(t *testing.T, server *Server, id, status string) openAIResponseObject {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		recorder := httptest.NewRecorder()
		server.mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/responses/"+id, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("get status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		var response openAIResponseObject
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Status == status {
			return response
		}
		if time.Now().After(deadline) {
			t.Fatalf("response status = %q, want %q", response.Status, status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
