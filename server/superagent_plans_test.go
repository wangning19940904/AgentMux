package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/superagent"
)

func TestSuperAgentPlanRoutes(t *testing.T) {
	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/super-agents/plans?agent_id=a", ""},
		{http.MethodPost, "/api/v1/super-agents/plans/approve", `{"agent_id":"a","plan_id":"pln_x"}`},
		{http.MethodPost, "/api/v1/super-agents/plans/decline", `{"agent_id":"a","plan_id":"pln_x"}`},
		{http.MethodGet, "/api/v1/super-agents/trust?agent_id=a", ""},
		{http.MethodDelete, "/api/v1/super-agents/trust?agent_id=a&requester=alice", ""},
	}
	bare := &Server{mux: http.NewServeMux()}
	bare.registerSuperAgentRoutes()
	for _, route := range routes {
		recorder := httptest.NewRecorder()
		bare.mux.ServeHTTP(recorder, httptest.NewRequest(route.method, route.path, strings.NewReader(route.body)))
		if recorder.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s without a runtime = %d", route.method, route.path, recorder.Code)
		}
		if memberRouteAllowed(route.method, strings.Split(route.path, "?")[0]) {
			t.Errorf("%s %s must stay Console-only", route.method, route.path)
		}
	}

	srv, st := newTestServer(t)
	records := &agentRecords{Store: st, agents: map[string]core.SuperAgent{}}
	srv.SetSuperAgents(superagent.New(superagent.Options{Store: records, StateDir: t.TempDir()}), nil)
	recorder := httptest.NewRecorder()
	srv.mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/super-agents/plans?agent_id=a", nil))
	var listed struct {
		Plans []json.RawMessage `json:"plans"`
		Trust []json.RawMessage `json:"trust"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &listed); err != nil || recorder.Code != http.StatusOK || listed.Plans == nil || listed.Trust == nil {
		t.Fatalf("status %d body %s", recorder.Code, recorder.Body.String())
	}
	for _, route := range routes[1:3] {
		recorder := httptest.NewRecorder()
		srv.mux.ServeHTTP(recorder, httptest.NewRequest(route.method, route.path, strings.NewReader(route.body)))
		if recorder.Code != http.StatusNotFound {
			t.Errorf("%s for an unknown agent = %d %s", route.path, recorder.Code, recorder.Body.String())
		}
	}
	recorder = httptest.NewRecorder()
	srv.mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, routes[4].path, nil))
	if recorder.Code != http.StatusNotFound {
		t.Errorf("revoking nothing = %d", recorder.Code)
	}
}
