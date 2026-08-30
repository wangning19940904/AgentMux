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
	remotepkg "github.com/wangning19940904/AgentMux/remote"
	"github.com/wangning19940904/AgentMux/store"
)

func newRemoteTestServer(t *testing.T) *Server {
	t.Helper()
	cfg := config.Default()
	cfg.Remote.HostsFile = filepath.Join(t.TempDir(), "remote-hosts.json")
	st, err := store.OpenLegacySQLite(filepath.Join(t.TempDir(), "remote-test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return New(Dependencies{Config: cfg, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Store: st})
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
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("list Cache-Control = %q, want no-store", got)
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

func TestRemoteDirectoryEndpointsValidateHost(t *testing.T) {
	server := newRemoteTestServer(t)
	for _, test := range []struct {
		method string
		path   string
		body   io.Reader
		status int
	}{
		{method: http.MethodGet, path: "/api/v1/remote/directories", status: http.StatusBadRequest},
		{method: http.MethodGet, path: "/api/v1/remote/directories?id=missing", status: http.StatusNotFound},
		{method: http.MethodPost, path: "/api/v1/remote/directories", body: strings.NewReader(`{"path":"/tmp/demo"}`), status: http.StatusBadRequest},
	} {
		request := httptest.NewRequest(test.method, test.path, test.body)
		recorder := httptest.NewRecorder()
		server.mux.ServeHTTP(recorder, request)
		if recorder.Code != test.status {
			t.Fatalf("%s %s: status = %d, want %d; body = %s", test.method, test.path, recorder.Code, test.status, recorder.Body.String())
		}
	}
}

func TestRemoteUpdateEndpointValidatesHost(t *testing.T) {
	server := newRemoteTestServer(t)
	for _, test := range []struct {
		path   string
		status int
	}{
		{path: "/api/v1/remote/hosts/status", status: http.StatusBadRequest},
		{path: "/api/v1/remote/hosts/status?id=missing", status: http.StatusNotFound},
		{path: "/api/v1/remote/hosts/update", status: http.StatusBadRequest},
		{path: "/api/v1/remote/hosts/update?id=missing", status: http.StatusNotFound},
	} {
		request := httptest.NewRequest(http.MethodPost, test.path, nil)
		recorder := httptest.NewRecorder()
		server.mux.ServeHTTP(recorder, request)
		if recorder.Code != test.status {
			t.Fatalf("POST %s: status = %d, want %d; body = %s", test.path, recorder.Code, test.status, recorder.Body.String())
		}
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
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
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

func TestRemoteHostsSyncSSHConfigRefreshesConfiguredAliases(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configBody := "Host aliyun-ecs-bj\n  HostName 101.200.234.220\n  User root\n  IdentityFile ~/.ssh/ecs\n"
	if err := os.WriteFile(filepath.Join(sshDir, "config"), []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}

	server := newRemoteTestServer(t)
	saved, err := server.remote.Upsert(remotepkg.Host{
		Name: "aliyun-ecs", Host: "101.200.234.220", Port: 22, User: "root",
		KeyPath: filepath.Join(sshDir, "ecs"), RemoteAddr: "127.0.0.1:8765",
		APIToken: "secret", HostKeyFingerprint: "SHA256:trusted",
	}, false)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/remote/hosts/sync-ssh-config", nil)
	recorder := httptest.NewRecorder()
	server.mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	var result remotepkg.SSHConfigSyncResult
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Updated != 1 || result.Unmatched != 0 || len(result.Hosts) != 1 {
		t.Fatalf("sync result = %+v", result)
	}
	host, ok := server.remote.Get(saved.ID)
	if !ok || host.Name != "aliyun-ecs-bj" || host.SSHAlias != "aliyun-ecs-bj" ||
		host.APIToken != "secret" || host.HostKeyFingerprint != "SHA256:trusted" {
		t.Fatalf("updated host = %+v, ok = %v", host, ok)
	}
}
