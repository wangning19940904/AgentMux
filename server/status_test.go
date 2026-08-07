package server

import (
	"encoding/json"
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
