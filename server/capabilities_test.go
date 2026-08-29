package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wangning19940904/AgentMux/contract"
)

func TestCapabilitiesHandshake(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.SetVersion("v9.9.9-test")

	recorder := httptest.NewRecorder()
	srv.mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/capabilities", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		OK              bool                       `json:"ok"`
		Product         string                     `json:"product"`
		Version         string                     `json:"version"`
		ContractVersion string                     `json:"contract_version"`
		Features        []string                   `json:"features"`
		Modules         map[string]map[string]bool `json:"modules"`
		Agents          map[string]any             `json:"agents"`
		Channels        map[string]any             `json:"channels"`
		Auth            map[string]any             `json:"auth"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.OK || body.Product != "agentmux" {
		t.Fatalf("unexpected identity: %+v", body)
	}
	if body.Version != "v9.9.9-test" {
		t.Fatalf("version = %q", body.Version)
	}
	if body.ContractVersion != contract.Version {
		t.Fatalf("contract_version = %q, want %q", body.ContractVersion, contract.Version)
	}
	features := map[string]bool{}
	for _, feature := range body.Features {
		features[feature] = true
	}
	// send and triggers are always available with a store; invocations depend
	// on an attached invoker which newTestServer does not wire.
	if !features["send"] || !features["triggers"] {
		t.Fatalf("features = %v", body.Features)
	}
	if features["invocations"] {
		t.Fatalf("invocations advertised without an invoker: %v", body.Features)
	}
	if _, ok := body.Modules["router"]; !ok {
		t.Fatalf("modules = %v", body.Modules)
	}
	if enabled, _ := body.Auth["bridge_enabled"].(bool); enabled {
		t.Fatalf("bridge should default to disabled: %v", body.Auth)
	}
	// An unauthenticated local probe is the admin scope, and tenancy is
	// advertised whenever a store is attached.
	if body.Auth["scope"] != "admin" {
		t.Fatalf("auth scope = %v", body.Auth["scope"])
	}
	if !features["tenancy"] || !features["console.session"] {
		t.Fatalf("tenancy should be advertised with a store: %v", body.Features)
	}
	if _, ok := body.Agents["count"]; !ok {
		t.Fatalf("agents summary = %v", body.Agents)
	}
	if _, ok := body.Channels["count"]; !ok {
		t.Fatalf("channels summary = %v", body.Channels)
	}
}

func TestStatusReportsContractVersion(t *testing.T) {
	srv, _ := newTestServer(t)
	recorder := httptest.NewRecorder()
	srv.handleStatus(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/status", nil))
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["contract_version"] != contract.Version {
		t.Fatalf("contract_version = %#v", body["contract_version"])
	}
}
