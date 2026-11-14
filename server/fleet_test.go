package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	remotepkg "github.com/wangning19940904/AgentMux/remote"
)

func TestFleetSSHRetryUsesRequestDeadline(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var accepted atomic.Int32
	closed := make(chan struct{}, 4)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			go func() { defer conn.Close(); _, _ = io.Copy(io.Discard, conn); closed <- struct{}{} }()
		}
	}()
	server := newRemoteTestServer(t)
	saved, err := server.remote.Upsert(remotepkg.Host{Name: "deadline", Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, User: "tester", KeyPath: filepath.Join(t.TempDir(), "missing"), HostKeyFingerprint: "test-pin"}, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 900*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, _, err = server.executeRemoteFleetOperation(ctx, saved.ID, fleetOperation{Key: "status", Method: "GET", Path: "/api/v1/status"})
	if err == nil || time.Since(started) > 1500*time.Millisecond {
		t.Fatalf("elapsed=%s error=%v", time.Since(started), err)
	}
	// Without the original request context, http.Transport gives SSH a
	// detached dial context and spends the entire request on the first try.
	if n := accepted.Load(); n < 2 || n > 3 {
		t.Fatalf("SSH connections=%d; deadline did not leave room for bounded retry", n)
	}
	for range accepted.Load() {
		select {
		case <-closed:
		case <-time.After(300 * time.Millisecond):
			t.Fatal("request left a handshake running")
		}
	}
}

func TestFleetQueryIncludesLocalTarget(t *testing.T) {
	server := newRemoteTestServer(t)
	body := bytes.NewBufferString(`{"target_ids":["local"],"requests":[{"key":"status","path":"/api/v1/status"}]}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/remote/fleet/query", body)
	recorder := httptest.NewRecorder()
	server.mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response fleetBatchResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Targets) != 1 || response.Targets[0].Target.ID != fleetLocalTargetID ||
		len(response.Targets[0].Responses) != 1 || !response.Targets[0].Responses[0].OK {
		t.Fatalf("response = %+v", response)
	}
}

func TestFleetQueryRejectsNestedRemotePath(t *testing.T) {
	server := newRemoteTestServer(t)
	body := bytes.NewBufferString(`{"target_ids":["local"],"requests":[{"key":"nested","path":"/api/v1/remote/hosts"}]}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/remote/fleet/query", body)
	recorder := httptest.NewRecorder()
	server.mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestFleetQueryContainsInvalidTargetJSON(t *testing.T) {
	server := newRemoteTestServer(t)
	server.mux.HandleFunc("GET /api/v1/empty", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	body := bytes.NewBufferString(`{"target_ids":["local"],"requests":[{"key":"empty","path":"/api/v1/empty"}]}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/remote/fleet/query", body)
	recorder := httptest.NewRecorder()
	server.mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var response fleetBatchResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	item := response.Targets[0].Responses[0]
	if item.OK || item.Status != http.StatusBadGateway || !strings.Contains(item.Error, "invalid JSON") {
		t.Fatalf("item = %+v", item)
	}
}

func TestFleetSyncAgentIsAddOnlyAndOmitsEnvironment(t *testing.T) {
	source := newRemoteTestServer(t)
	destination := newRemoteTestServer(t)
	workDir := t.TempDir()
	agent := core.AgentInstance{
		ID: "agent-demo", Name: "Demo", RuntimeID: "codex", WorkDir: workDir,
		Env: map[string]string{"API_TOKEN": "secret"}, Enabled: true, Source: "manual",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := source.st.UpsertAgentInstance(context.Background(), &agent); err != nil {
		t.Fatal(err)
	}
	manifest, err := source.exportFleetSyncManifest(context.Background(), false, []string{"agents"})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Agents) != 1 || len(manifest.Agents[0].Env) != 0 || len(manifest.CredentialsOmitted) != 1 {
		t.Fatalf("manifest = %+v", manifest)
	}
	request := fleetSyncInspectRequest{Manifest: manifest}
	preview, err := destination.inspectFleetSync(context.Background(), request, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Resources) != 1 || preview.Resources[0].Action != "add" || !preview.Resources[0].CredentialsMissing {
		t.Fatalf("preview = %+v", preview)
	}
	result, err := destination.inspectFleetSync(context.Background(), request, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Resources[0].Action != "add" {
		t.Fatalf("apply = %+v", result)
	}
	saved, err := destination.st.GetAgentInstance(context.Background(), agent.ID)
	if err != nil || saved == nil || len(saved.Env) != 0 {
		t.Fatalf("saved = %+v, err = %v", saved, err)
	}
	repeated, err := destination.inspectFleetSync(context.Background(), request, false)
	if err != nil {
		t.Fatal(err)
	}
	if repeated.Resources[0].Action != "exists" {
		t.Fatalf("repeat preview = %+v", repeated)
	}
}

// AgentMux no longer manages providers, so a fleet sync never copies them or
// their keys, in either direction.
func TestFleetSyncNeverCopiesProviders(t *testing.T) {
	ctx := context.Background()
	source := newRemoteTestServer(t)
	destination := newRemoteTestServer(t)
	for _, categories := range [][]string{nil, {"providers"}, {"agents"}} {
		manifest, err := source.exportFleetSyncManifest(ctx, true, categories)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(manifest)
		if strings.Contains(string(raw), "alpha") {
			t.Fatalf("categories %v exported a provider: %s", categories, raw)
		}
	}

	// A manifest from an older machine may still carry providers and keys.
	var old fleetSyncManifest
	if err := json.Unmarshal([]byte(`{"version":1,"providers":[{"id":"alpha","name":"Alpha"}],"provider_keys":{"alpha":"alpha-secret"},"routes":[{"tool":"codex","provider_id":"alpha"}]}`), &old); err != nil {
		t.Fatal(err)
	}
	result, err := destination.inspectFleetSync(ctx, fleetSyncInspectRequest{Manifest: old, PreserveActivation: true}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Resources) != 0 {
		t.Fatalf("result = %+v", result.Resources)
	}
}

func TestFleetSyncPeerExportRequiresInternalMarker(t *testing.T) {
	server := newRemoteTestServer(t)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/fleet-sync/export", bytes.NewBufferString(`{"categories":["agents"]}`))
	recorder := httptest.NewRecorder()
	server.mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
}

func TestFleetSyncApplyStreamReturnsStructuredError(t *testing.T) {
	server := newRemoteTestServer(t)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/remote/sync/apply/stream", bytes.NewBufferString(`{"plan_id":"missing"}`))
	recorder := httptest.NewRecorder()
	server.mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "event: progress") || !strings.Contains(recorder.Body.String(), "event: error") {
		t.Fatalf("stream status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}
