package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	usagepkg "github.com/wangning19940904/AgentMux/usage"
)

type fakeUsageSourceManager struct{ actions []string }

func (f *fakeUsageSourceManager) UsageSources(context.Context) []usagepkg.CursorUsageSourceStatus {
	return []usagepkg.CursorUsageSourceStatus{{Source: "cursor", Connected: true, Scope: "agent", BackfillDays: 90}}
}

func (f *fakeUsageSourceManager) UsageSourceAction(_ context.Context, source, action string) (usagepkg.CursorUsageActionResult, error) {
	f.actions = append(f.actions, source+":"+action)
	return usagepkg.CursorUsageActionResult{OK: true, Action: action, Message: "ok"}, nil
}

func TestUsageSourceManagementRoutes(t *testing.T) {
	srv, _ := newTestServer(t)
	manager := &fakeUsageSourceManager{}
	srv.SetUsageSourceManager(manager)

	list := httptest.NewRecorder()
	srv.mux.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/v1/usage/sources", nil))
	if list.Code != http.StatusOK {
		t.Fatalf("list code=%d body=%s", list.Code, list.Body.String())
	}
	var sources []usagepkg.CursorUsageSourceStatus
	if json.Unmarshal(list.Body.Bytes(), &sources) != nil || len(sources) != 1 || !sources[0].Connected {
		t.Fatalf("sources=%+v", sources)
	}

	action := httptest.NewRecorder()
	srv.mux.ServeHTTP(action, httptest.NewRequest(http.MethodPost, "/api/v1/usage/sources/cursor/sync", nil))
	if action.Code != http.StatusOK || len(manager.actions) != 1 || manager.actions[0] != "cursor:sync" {
		t.Fatalf("action code=%d actions=%v body=%s", action.Code, manager.actions, action.Body.String())
	}
}
