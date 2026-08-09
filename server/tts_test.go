package server

import (
	"encoding/json"
	"net/http"
	"testing"

	ttspkg "github.com/wangning19940904/AgentMux/tts"
)

func TestTTSModelCatalogAndDeleteRoutes(t *testing.T) {
	server, _ := newTestServer(t)
	server.ttsModels = ttspkg.NewManager(t.TempDir(), nil)
	recorder := doJSON(t, server, http.MethodGet, "/api/v1/tts/models", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("catalog: code = %d body = %s", recorder.Code, recorder.Body.String())
	}
	var catalog ttspkg.CatalogStatus
	if err := json.Unmarshal(recorder.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Models) != 3 || catalog.Models[0].ID != ttspkg.DefaultLocalModel {
		t.Fatalf("catalog = %+v", catalog)
	}
	recorder = doJSON(t, server, http.MethodDelete, "/api/v1/tts/models?id=piper-zh-huayan", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("delete: code = %d body = %s", recorder.Code, recorder.Body.String())
	}
	recorder = doJSON(t, server, http.MethodDelete, "/api/v1/tts/models?id=unknown", nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unknown delete: code = %d body = %s", recorder.Code, recorder.Body.String())
	}
}
