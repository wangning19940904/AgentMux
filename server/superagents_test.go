package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/remote"
	"github.com/wangning19940904/AgentMux/superagent"
)

func TestSuperAgentRoutesRequireRuntime(t *testing.T) {
	srv := &Server{mux: http.NewServeMux()}
	srv.registerSuperAgentRoutes()
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/super-agents"},
		{http.MethodPost, "/api/v1/super-agents"},
		{http.MethodDelete, "/api/v1/super-agents?id=x"},
		{http.MethodGet, "/api/v1/super-agents/activity?id=x"},
		{http.MethodPost, "/api/v1/super-agents/setup"},
		{http.MethodPost, "/api/v1/super-agents/trigger"},
		{http.MethodPost, "/api/v1/super-agents/tickets/stop"},
		{http.MethodPost, "/api/v1/super-agents/tickets/retry"},
		{http.MethodPost, "/hook/super-agent/github/src"},
	} {
		recorder := httptest.NewRecorder()
		srv.mux.ServeHTTP(recorder, httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}")))
		if recorder.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s: %d", tc.method, tc.path, recorder.Code)
		}
	}
}

func TestSuperAgentRoutesAreNotOpenToTenants(t *testing.T) {
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/super-agents"},
		{http.MethodPost, "/api/v1/super-agents"},
		{http.MethodPost, "/api/v1/super-agents/trigger"},
	} {
		if tenantRouteAllowed(route.method, route.path) {
			t.Errorf("%s %s must stay Console-only: Super Agents hold credentials and control every worker", route.method, route.path)
		}
	}
}

func TestSuperAgentGitHubHookStatusCodes(t *testing.T) {
	srv := &Server{mux: http.NewServeMux(), superAgents: superagent.New(superagent.Options{Store: emptySuperAgentStore{}})}
	srv.registerSuperAgentRoutes()
	recorder := httptest.NewRecorder()
	body := `{"zen":"keep it simple"}`
	mac := hmac.New(sha256.New, []byte("secret"))
	mac.Write([]byte(body))
	request := httptest.NewRequest(http.MethodPost, "/hook/super-agent/github/unknown", strings.NewReader(body))
	request.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	srv.mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown source: %d", recorder.Code)
	}
}

type emptySuperAgentStore struct{ superagent.Store }

// streamingInvokerStub is the remote daemon's Invoker. The rest of this test is
// the real SSE handler, so a change to that contract breaks the remote fleet here.
type streamingInvokerStub struct {
	core.Invoker
	run func(context.Context, core.InvocationRequest, core.InvocationEventSink) (core.InvocationResult, error)
}

func (s streamingInvokerStub) InvokeStream(ctx context.Context, req core.InvocationRequest, sink core.InvocationEventSink) (core.InvocationResult, error) {
	return s.run(ctx, req, sink)
}

type dialerToServer struct {
	address string
}

func (d dialerToServer) Get(string) (remote.Host, bool) {
	return remote.Host{ID: "h", Name: "devbox", RemoteAddr: "127.0.0.1:8765"}, true
}

func (d dialerToServer) DialContext(ctx context.Context, _, network string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, d.address)
}

func TestRemoteFleetAgainstRealInvocationStreamHandler(t *testing.T) {
	cases := []struct {
		name    string
		run     func(context.Context, core.InvocationRequest, core.InvocationEventSink) (core.InvocationResult, error)
		answer  string
		failure string
		retry   bool
	}{
		{name: "completes", answer: "from remote", run: func(_ context.Context, req core.InvocationRequest, sink core.InvocationEventSink) (core.InvocationResult, error) {
			_ = sink(core.InvocationStreamEvent{Type: "started"})
			result := core.InvocationResult{Answer: "from remote", ConversationID: req.ConversationID}
			_ = sink(core.InvocationStreamEvent{Type: "completed", Final: true, Result: &result})
			return result, nil
		}},
		{name: "turn fails after starting", failure: "tests failed", run: func(_ context.Context, _ core.InvocationRequest, sink core.InvocationEventSink) (core.InvocationResult, error) {
			_ = sink(core.InvocationStreamEvent{Type: "started"})
			return core.InvocationResult{}, errors.New("tests failed")
		}},
		{name: "busy before starting is retried", failure: "active invocation", retry: true, run: func(context.Context, core.InvocationRequest, core.InvocationEventSink) (core.InvocationResult, error) {
			return core.InvocationResult{}, fmt.Errorf("%w: conv", core.ErrInvocationBusy)
		}},
		{name: "missing agent is final", failure: "not found", run: func(context.Context, core.InvocationRequest, core.InvocationEventSink) (core.InvocationResult, error) {
			return core.InvocationResult{}, fmt.Errorf("%w: agent", core.ErrInvocationNotFound)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			remoteDaemon := &Server{mux: http.NewServeMux(), invoker: streamingInvokerStub{run: tc.run}}
			remoteDaemon.mux.HandleFunc("POST /api/v1/invocations/stream", remoteDaemon.handleInvocationStream)
			server := httptest.NewServer(remoteDaemon.mux)
			defer server.Close()
			fleet := &superagent.Fleet{Hosts: dialerToServer{address: server.Listener.Addr().String()}}
			answer, err := fleet.Run(context.Background(), core.SuperAgentWorker{HostID: "h", AgentID: "a"}, "conv", "input")
			if tc.failure == "" {
				if err != nil || answer != tc.answer {
					t.Fatalf("answer=%q err=%v", answer, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.failure) || superagent.IsRetryable(err) != tc.retry {
				t.Fatalf("err=%v retryable=%v", err, superagent.IsRetryable(err))
			}
		})
	}
}
