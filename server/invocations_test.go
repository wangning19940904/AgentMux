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

type invocationTestService struct {
	request      core.InvocationRequest
	result       core.InvocationResult
	streamEvents []core.InvocationStreamEvent
	err          error
}

func (s *invocationTestService) Invoke(_ context.Context, req core.InvocationRequest) (core.InvocationResult, error) {
	s.request = req
	return s.result, s.err
}

func (s *invocationTestService) InvokeStream(_ context.Context, req core.InvocationRequest, sink core.InvocationEventSink) (core.InvocationResult, error) {
	s.request = req
	if s.err != nil {
		return s.result, s.err
	}
	for _, event := range s.streamEvents {
		if err := sink(event); err != nil {
			return s.result, err
		}
	}
	return s.result, nil
}

func TestHandleInvocationReturnsAgentResult(t *testing.T) {
	server, _ := newTestServer(t)
	invoker := &invocationTestService{result: core.InvocationResult{
		ID: "inv_1", AgentID: "agent-1", ConversationID: "conv_1", SessionID: "session-1", Answer: "done",
	}}
	server.SetInvoker(invoker)
	recorder := doJSON(t, server, http.MethodPost, "/api/v1/invocations", map[string]any{
		"agent_id": "agent-1", "conversation_id": "conv_1", "input": "do the work",
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var result core.InvocationResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if invoker.request.AgentID != "agent-1" || invoker.request.Input != "do the work" || result.Answer != "done" {
		t.Fatalf("request=%+v result=%+v", invoker.request, result)
	}
}

func TestHandleInvocationMapsDomainErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want int
	}{
		{"invalid", core.ErrInvalidInvocation, http.StatusBadRequest},
		{"missing", core.ErrInvocationNotFound, http.StatusNotFound},
		{"disabled", core.ErrInvocationDisabled, http.StatusConflict},
		{"busy", core.ErrInvocationBusy, http.StatusConflict},
		{"runtime", core.ErrInvocationRuntime, http.StatusServiceUnavailable},
		{"internal", errors.New("boom"), http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, _ := newTestServer(t)
			server.SetInvoker(&invocationTestService{err: test.err})
			recorder := doJSON(t, server, http.MethodPost, "/api/v1/invocations", map[string]string{
				"project": "demo", "input": "hello",
			})
			if recorder.Code != test.want {
				t.Fatalf("status = %d, want %d, body = %s", recorder.Code, test.want, recorder.Body.String())
			}
		})
	}
}

func TestInvocationEndpointUsesBridgeBearerAuth(t *testing.T) {
	server, _ := newTestServer(t)
	server.cfg.Bridge.Enabled = true
	server.cfg.Bridge.Token = "bridge-secret"
	server.SetInvoker(&invocationTestService{result: core.InvocationResult{
		ID: "inv_1", ConversationID: "conv_1", Answer: "ok",
	}})
	body := []byte(`{"project":"demo","input":"hello"}`)

	unauthorized := httptest.NewRecorder()
	server.withAuth(server.mux).ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/api/v1/invocations", bytes.NewReader(body)))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/invocations", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer bridge-secret")
	authorized := httptest.NewRecorder()
	server.withAuth(server.mux).ServeHTTP(authorized, request)
	if authorized.Code != http.StatusOK {
		t.Fatalf("authorized status = %d, body = %s", authorized.Code, authorized.Body.String())
	}
}

func TestHandleInvocationLimitsRequestBody(t *testing.T) {
	server, _ := newTestServer(t)
	server.SetInvoker(&invocationTestService{})
	body := append([]byte(`{"project":"demo","input":"`), bytes.Repeat([]byte("x"), maxInvocationRequestBytes)...)
	body = append(body, []byte(`"}`)...)
	recorder := httptest.NewRecorder()
	server.mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/invocations", bytes.NewReader(body)))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body = %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
}

func TestHandleInvocationStreamReturnsSSE(t *testing.T) {
	server, _ := newTestServer(t)
	result := core.InvocationResult{ID: "inv_1", ConversationID: "conv_1", SessionID: "session-1", Answer: "hello"}
	server.SetInvoker(&invocationTestService{
		result: result,
		streamEvents: []core.InvocationStreamEvent{
			{Type: "started", InvocationID: "inv_1", ConversationID: "conv_1"},
			{Type: "output", InvocationID: "inv_1", ConversationID: "conv_1", Text: "hel"},
			{Type: "final", InvocationID: "inv_1", ConversationID: "conv_1", Text: "hello", Final: true},
			{Type: "completed", InvocationID: "inv_1", ConversationID: "conv_1", Final: true, Result: &result},
		},
	})
	recorder := doJSON(t, server, http.MethodPost, "/api/v1/invocations/stream", map[string]string{
		"agent_id": "agent-1", "input": "say hello",
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "text/event-stream; charset=utf-8" {
		t.Fatalf("content type = %q", contentType)
	}
	body := recorder.Body.String()
	for _, want := range []string{"event: started", "event: output", `"text":"hel"`, "event: final", "event: completed"} {
		if !strings.Contains(body, want) {
			t.Fatalf("SSE body missing %q:\n%s", want, body)
		}
	}
}

func TestHandleInvocationStreamKeepsPreflightErrorAsJSON(t *testing.T) {
	server, _ := newTestServer(t)
	server.SetInvoker(&invocationTestService{err: core.ErrInvocationNotFound})
	recorder := doJSON(t, server, http.MethodPost, "/api/v1/invocations/stream", map[string]string{
		"agent_id": "missing", "input": "hello",
	})
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("content type = %q", contentType)
	}
}
