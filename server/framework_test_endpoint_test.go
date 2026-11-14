package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
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
	// With a kind, the request's settings are tested as given (an empty model
	// is the framework default) in the Agent's environment.
	rec = doJSON(t, s, http.MethodPost, "/api/v1/frameworks/test", frameworkTestRequest{AgentID: "agent-smoke", Kind: "endpoint-smoke"})
	if rec.Code != http.StatusOK {
		t.Fatalf("explicit test: code=%d body=%s", rec.Code, rec.Body.String())
	}
	endpointSmokeMu.Lock()
	cfg = endpointSmokeLast
	endpointSmokeMu.Unlock()
	if _, hasModel := cfg["model"]; hasModel || cfg["env"] == nil {
		t.Fatalf("explicit cfg = %+v", cfg)
	}
	if rec := doJSON(t, s, http.MethodPost, "/api/v1/frameworks/test", frameworkTestRequest{AgentID: "missing"}); rec.Code != http.StatusNotFound {
		t.Fatalf("missing agent: code=%d body=%s", rec.Code, rec.Body.String())
	}

	// The latest result per framework and model is kept for display.
	doJSON(t, s, http.MethodPost, "/api/v1/frameworks/test", frameworkTestRequest{Kind: "endpoint-smoke", Model: "group/fast"})
	rec = doJSON(t, s, http.MethodGet, "/api/v1/frameworks/health?kind=endpoint-smoke", nil)
	var health struct {
		Results []framework.SmokeResult `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	// Newest first, one per model: group/fast (retested), default, deepseek.
	if len(health.Results) != 3 || health.Results[0].Model != "group/fast" || health.Results[1].Model != "" || health.Results[2].Model != "deepseek/deepseek-v4-pro" {
		t.Fatalf("health = %+v", health.Results)
	}
	rec = doJSON(t, s, http.MethodGet, "/api/v1/frameworks/health?kind=other", nil)
	if !strings.Contains(rec.Body.String(), `"results":[]`) {
		t.Fatalf("filtered health = %s", rec.Body.String())
	}
}
