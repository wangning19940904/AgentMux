package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
)

func TestOpenAIResponsesCreateReturnsSDKShape(t *testing.T) {
	server, _ := newTestServer(t)
	invoker := &invocationTestService{result: core.InvocationResult{
		ID: "inv_1", AgentID: "agent-1", ConversationID: "ignored", Answer: "hello",
		Usage: &core.TurnUsage{InputTokens: 4, OutputTokens: 3, CacheReadTokens: 1, ReasoningTokens: 2},
	}}
	server.SetInvoker(invoker)
	recorder := doJSON(t, server, http.MethodPost, "/v1/responses", map[string]any{
		"model": "agent-1", "input": "say hello",
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response openAIResponseObject
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(response.ID, "resp_") || response.Object != "response" || response.Status != "completed" {
		t.Fatalf("response identity/status = %+v", response)
	}
	if response.Model != "agent-1" || len(response.Output) != 1 || len(response.Output[0].Content) != 1 || response.Output[0].Content[0].Text != "hello" {
		t.Fatalf("response output = %+v", response)
	}
	usage, ok := response.Usage.(map[string]any)
	if !ok || usage["input_tokens"] != float64(4) || usage["output_tokens"] != float64(3) || usage["total_tokens"] != float64(7) {
		t.Fatalf("usage = %#v", response.Usage)
	}
	if invoker.request.AgentID != "agent-1" || invoker.request.Input != "say hello" || !strings.HasPrefix(invoker.request.ConversationID, "oai_") {
		t.Fatalf("invocation request = %+v", invoker.request)
	}
}

func TestOpenAIResponsesAcceptsMessageInputAndExplicitAgent(t *testing.T) {
	server, _ := newTestServer(t)
	invoker := &invocationTestService{result: core.InvocationResult{Answer: "ok"}}
	server.SetInvoker(invoker)
	body := []byte(`{
		"model":"agentmux",
		"instructions":"Be concise",
		"input":[
			{"role":"developer","content":"Use the repository context"},
			{"role":"user","content":[{"type":"input_text","text":"Run tests"}]}
		]
	}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(openAIAgentHeader, "demo")
	recorder := httptest.NewRecorder()
	server.mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if invoker.request.AgentID != "demo" {
		t.Fatalf("target = %+v", invoker.request)
	}
	for _, want := range []string{"Instructions:\nBe concise", "DEVELOPER:\nUse the repository context", "USER:\nRun tests"} {
		if !strings.Contains(invoker.request.Input, want) {
			t.Fatalf("prompt missing %q: %q", want, invoker.request.Input)
		}
	}
}

func TestOpenAIResponsesStreamUsesTypedDeltaEvents(t *testing.T) {
	server, _ := newTestServer(t)
	result := core.InvocationResult{
		ID: "inv_1", ConversationID: "conv_1", Answer: "hello",
		Usage: &core.TurnUsage{InputTokens: 2, OutputTokens: 1},
	}
	server.SetInvoker(&invocationTestService{
		result: result,
		streamEvents: []core.InvocationStreamEvent{
			{Type: "started"},
			{Type: "output", Text: "hel"},
			{Type: "final", Text: "hello", Final: true},
			{Type: "completed", Final: true, Result: &result},
		},
	})
	recorder := doJSON(t, server, http.MethodPost, "/v1/responses", map[string]any{
		"model": "agent-1", "input": "say hello", "stream": true,
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "text/event-stream; charset=utf-8" {
		t.Fatalf("content type = %q", contentType)
	}
	body := recorder.Body.String()
	events := []string{
		"response.created", "response.in_progress", "response.output_item.added", "response.content_part.added",
		"response.output_text.delta", "response.output_text.done", "response.content_part.done",
		"response.output_item.done", "response.completed",
	}
	last := -1
	for _, event := range events {
		index := strings.Index(body[last+1:], "event: "+event+"\n")
		if index < 0 {
			t.Fatalf("stream missing ordered event %q:\n%s", event, body)
		}
		last += index + 1
	}
	for _, want := range []string{`"delta":"hel"`, `"delta":"lo"`, `"type":"response.completed"`, `"total_tokens":3`} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "[DONE]") {
		t.Fatalf("Responses stream must not contain Chat Completions [DONE]:\n%s", body)
	}
}

func TestOpenAIResponsesPreviousResponseContinuesConversation(t *testing.T) {
	server, _ := newTestServer(t)
	invoker := &invocationTestService{result: core.InvocationResult{Answer: "one"}}
	server.SetInvoker(invoker)
	first := doJSON(t, server, http.MethodPost, "/v1/responses", map[string]any{
		"model": "agent-1", "input": "first",
	})
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d, body = %s", first.Code, first.Body.String())
	}
	var firstResponse openAIResponseObject
	if err := json.Unmarshal(first.Body.Bytes(), &firstResponse); err != nil {
		t.Fatal(err)
	}
	firstConversation := invoker.request.ConversationID
	invoker.result.Answer = "two"
	second := doJSON(t, server, http.MethodPost, "/v1/responses", map[string]any{
		"model": "agent-1", "input": "second", "previous_response_id": firstResponse.ID,
	})
	if second.Code != http.StatusOK {
		t.Fatalf("second status = %d, body = %s", second.Code, second.Body.String())
	}
	var secondResponse openAIResponseObject
	if err := json.Unmarshal(second.Body.Bytes(), &secondResponse); err != nil {
		t.Fatal(err)
	}
	if invoker.request.ConversationID != firstConversation {
		t.Fatalf("conversation changed from %q to %q", firstConversation, invoker.request.ConversationID)
	}
	if secondResponse.PreviousResponseID != firstResponse.ID || secondResponse.ID == firstResponse.ID {
		t.Fatalf("response chain = first %q, second %+v", firstResponse.ID, secondResponse)
	}
}

type openAIMissingInvoker struct {
	requests []core.InvocationRequest
}

func (i *openAIMissingInvoker) Invoke(_ context.Context, req core.InvocationRequest) (core.InvocationResult, error) {
	i.requests = append(i.requests, req)
	return core.InvocationResult{}, core.ErrInvocationNotFound
}

func TestOpenAIResponsesModelDoesNotFallBackToProject(t *testing.T) {
	server, _ := newTestServer(t)
	invoker := &openAIMissingInvoker{}
	server.SetInvoker(invoker)
	recorder := doJSON(t, server, http.MethodPost, "/v1/responses", map[string]any{
		"model": "demo", "input": "hello",
	})
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if len(invoker.requests) != 1 || invoker.requests[0].AgentID != "demo" {
		t.Fatalf("requests = %+v", invoker.requests)
	}
}

func TestOpenAIResponsesReturnsOpenAIErrors(t *testing.T) {
	server, _ := newTestServer(t)
	server.SetInvoker(&invocationTestService{err: core.ErrInvocationNotFound})
	for _, test := range []struct {
		name string
		body map[string]any
		want int
		code string
	}{
		{"invalid previous response", map[string]any{"model": "agent-1", "input": "hello", "previous_response_id": "resp_external"}, http.StatusBadRequest, "invalid_request"},
		{"missing target", map[string]any{"model": "missing", "input": "hello"}, http.StatusNotFound, "model_not_found"},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := doJSON(t, server, http.MethodPost, "/v1/responses", test.body)
			if recorder.Code != test.want {
				t.Fatalf("status = %d, want %d, body = %s", recorder.Code, test.want, recorder.Body.String())
			}
			var response openAIErrorResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Error.Message == "" || response.Error.Type == "" || response.Error.Code != test.code {
				t.Fatalf("error = %+v", response.Error)
			}
		})
	}
}

func TestOpenAIResponsesUsesBridgeBearerAuth(t *testing.T) {
	server, _ := newTestServer(t)
	server.cfg.Bridge.Enabled = true
	server.cfg.Bridge.Token = "bridge-secret"
	server.SetInvoker(&invocationTestService{result: core.InvocationResult{Answer: "ok"}})
	body := []byte(`{"model":"agent-1","input":"hello"}`)

	unauthorized := httptest.NewRecorder()
	server.withAuth(server.mux).ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body)))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer bridge-secret")
	authorized := httptest.NewRecorder()
	server.withAuth(server.mux).ServeHTTP(authorized, request)
	if authorized.Code != http.StatusOK {
		t.Fatalf("authorized status = %d, body = %s", authorized.Code, authorized.Body.String())
	}
}

func TestOpenAIResponseStreamPreflightFailureStaysJSON(t *testing.T) {
	server, _ := newTestServer(t)
	server.SetInvoker(&invocationTestService{err: errors.New("boom")})
	recorder := doJSON(t, server, http.MethodPost, "/v1/responses", map[string]any{
		"model": "agent-1", "input": "hello", "stream": true,
	})
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("content type = %q", contentType)
	}
}
