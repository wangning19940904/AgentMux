package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wangning19940904/AgentMux/config"
)

func TestStatusUsesBuildVersion(t *testing.T) {
	srv := &Server{cfg: config.Default(), version: "0.1.1-pg"}
	response := httptest.NewRecorder()
	srv.handleStatus(response, httptest.NewRequest("GET", "/api/v1/status", nil))
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["version"] != "0.1.1-pg" {
		t.Fatalf("status version = %#v", body["version"])
	}
}

func TestManagementAPIUsesBridgeBearerAuth(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.cfg.Bridge.Enabled = true
	srv.cfg.Bridge.Token = "bridge-secret"
	handler := srv.withAuth(srv.mux)

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/v1/status", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("missing token code = %d", unauthorized.Code)
	}

	authorized := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	request.Header.Set("Authorization", "Bearer bridge-secret")
	handler.ServeHTTP(authorized, request)
	if authorized.Code != http.StatusOK {
		t.Fatalf("valid token code = %d body = %s", authorized.Code, authorized.Body.String())
	}
}
