package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/framework"
)

type endpointSmokeAgent struct{ cfg map[string]any }

func (a *endpointSmokeAgent) Name() string { return "endpoint-smoke" }
func (a *endpointSmokeAgent) StartSession(context.Context, string) (core.AgentSession, error) {
	return endpointSmokeSession{}, nil
}
func (a *endpointSmokeAgent) ListSessions(context.Context) ([]string, error) { return nil, nil }
func (a *endpointSmokeAgent) Stop(context.Context) error                     { return nil }

type endpointSmokeSession struct{}

func (endpointSmokeSession) ID() string { return "s" }
func (endpointSmokeSession) Send(context.Context, string) (<-chan *core.Event, error) {
	out := make(chan *core.Event, 1)
	out <- &core.Event{Type: core.EventFinal, Text: "ok"}
	close(out)
	return out, nil
}
func (endpointSmokeSession) RespondPermission(context.Context, bool) error { return nil }
func (endpointSmokeSession) Close(context.Context) error                   { return nil }

var (
	endpointSmokeOnce sync.Once
	endpointSmokeMu   sync.Mutex
	endpointSmokeLast map[string]any
)

func registerEndpointSmokeAgent() {
	endpointSmokeOnce.Do(func() {
		core.RegisterAgent("endpoint-smoke", func(cfg map[string]any) (core.Agent, error) {
			endpointSmokeMu.Lock()
			endpointSmokeLast = cfg
			endpointSmokeMu.Unlock()
			return &endpointSmokeAgent{cfg: cfg}, nil
		})
	})
}

func TestFrameworkTestEndpoint(t *testing.T) {
	registerEndpointSmokeAgent()
	s, st := newTestServer(t)

	if rec := doJSON(t, s, http.MethodPost, "/api/v1/frameworks/test", frameworkTestRequest{}); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing kind: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, s, http.MethodPost, "/api/v1/frameworks/test", frameworkTestRequest{Kind: "no-such-framework"}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown kind: code=%d body=%s", rec.Code, rec.Body.String())
	}

	rec := doJSON(t, s, http.MethodPost, "/api/v1/frameworks/test", frameworkTestRequest{Kind: "endpoint-smoke", Model: "group/fast"})
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var result framework.SmokeResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.Kind != "endpoint-smoke" || result.Model != "group/fast" {
		t.Fatalf("result = %+v", result)
	}

	// An Agent's runtime, environment and default model are used unless the
	// request overrides them.
	agent := &core.AgentInstance{
		ID: "agent-smoke", Name: "smoke", RuntimeID: "endpoint-smoke", WorkDir: t.TempDir(),
		DefaultModel: "deepseek/deepseek-v4-pro", DefaultReasoningEffort: "high",
		Env: map[string]string{"ANTHROPIC_BASE_URL": "http://127.0.0.1:3425"},
	}
	if err := st.UpsertAgentInstance(context.Background(), agent); err != nil {
		t.Fatal(err)
	}
	rec = doJSON(t, s, http.MethodPost, "/api/v1/frameworks/test", frameworkTestRequest{AgentID: "agent-smoke"})
	if rec.Code != http.StatusOK {
		t.Fatalf("agent test: code=%d body=%s", rec.Code, rec.Body.String())
	}
	endpointSmokeMu.Lock()
	cfg := endpointSmokeLast
	endpointSmokeMu.Unlock()
	env, _ := cfg["env"].(map[string]string)
	if cfg["model"] != "deepseek/deepseek-v4-pro" || cfg["reasoning_effort"] != "high" || env["ANTHROPIC_BASE_URL"] == "" {
		t.Fatalf("agent cfg = %+v", cfg)
	}
	if rec := doJSON(t, s, http.MethodPost, "/api/v1/frameworks/test", frameworkTestRequest{AgentID: "agent-smoke", Kind: "codex"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("mismatched kind: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, s, http.MethodPost, "/api/v1/frameworks/test", frameworkTestRequest{AgentID: "missing"}); rec.Code != http.StatusNotFound {
		t.Fatalf("missing agent: code=%d body=%s", rec.Code, rec.Body.String())
	}
}
