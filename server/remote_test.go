package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wangning19940904/AgentMux/config"
	"github.com/wangning19940904/AgentMux/store"
)

func newRemoteTestServer(t *testing.T) *Server {
	t.Helper()
	cfg := config.Default()
	cfg.Remote.HostsFile = filepath.Join(t.TempDir(), "remote-hosts.json")
	st, err := store.Open(filepath.Join(t.TempDir(), "remote-test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), st, nil, nil)
}

func TestRemoteHostCRUDRedactsSecrets(t *testing.T) {
	server := newRemoteTestServer(t)
	body := []byte(`{
		"name":"build-box",
		"host":"10.0.0.5",
		"port":22,
		"user":"dev",
		"remote_addr":"127.0.0.1:8765",
		"api_token":"top-secret"
	}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/remote/hosts", bytes.NewReader(body))
	recorder := httptest.NewRecorder()
	server.mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("upsert status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var saved map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	id, _ := saved["id"].(string)
	if id == "" || saved["api_token_set"] != true {
		t.Fatalf("saved host = %+v", saved)
	}
	if strings.Contains(recorder.Body.String(), "top-secret") {
		t.Fatalf("upsert leaked API token: %s", recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/remote/hosts", nil)
	recorder = httptest.NewRecorder()
	server.mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), "top-secret") {
		t.Fatalf("list status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodDelete, "/api/v1/remote/hosts?id="+id, nil)
	recorder = httptest.NewRecorder()
	server.mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestRemoteProxyRejectsNestedProxying(t *testing.T) {
	server := newRemoteTestServer(t)
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/remote/proxy/anything/remote/hosts",
		nil,
	)
	recorder := httptest.NewRecorder()
	server.mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestRemoteDiscoveredHostsReadsUserSSHConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configBody := "Host build-box\n  HostName 10.0.0.8\n  User deploy\n"
	if err := os.WriteFile(filepath.Join(sshDir, "config"), []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}

	server := newRemoteTestServer(t)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/remote/discovered-hosts", nil)
	recorder := httptest.NewRecorder()
	server.mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var hosts []map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &hosts); err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 || hosts[0]["name"] != "build-box" ||
		hosts[0]["host"] != "10.0.0.8" || hosts[0]["user"] != "deploy" {
		t.Fatalf("hosts = %+v", hosts)
	}
}
