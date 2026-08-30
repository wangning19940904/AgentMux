package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/wangning19940904/AgentMux/config"
	"github.com/wangning19940904/AgentMux/core"
	observationpkg "github.com/wangning19940904/AgentMux/observability"
	"github.com/wangning19940904/AgentMux/store"
)

func newObservationHTTPTestServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	st, err := store.OpenLegacySQLite(filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	recorder, err := store.NewObservationRecorder(st, store.ObservationRecorderOptions{CaptureContent: false})
	if err != nil {
		t.Fatal(err)
	}
	bus := core.NewObservationBus()
	ingest := observationpkg.NewIngestService(nil, bus, t.TempDir(), "ingest-token")
	srv := New(Dependencies{Config: cfg, Store: st})
	srv.SetObservability(cfg.Observability, recorder, observationpkg.NewInsightEngine(st), nil, ingest)
	httpServer := httptest.NewServer(srv.withAuth(srv.mux))
	t.Cleanup(func() {
		httpServer.Close()
		_ = st.Close()
	})
	return httpServer, st
}

func TestObservabilityConsoleUsesOneTimeNonceAndHttpOnlySession(t *testing.T) {
	httpServer, _ := newObservationHTTPTestServer(t)
	if response, err := http.Get(httpServer.URL + "/api/v1/observability/traces"); err != nil {
		t.Fatal(err)
	} else {
		_ = response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("unauthenticated status = %d", response.StatusCode)
		}
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	response, err := client.Get(httpServer.URL + "/api/v1/observability/session/nonce")
	if err != nil {
		t.Fatal(err)
	}
	var nonce struct {
		Nonce string `json:"nonce"`
	}
	if err := json.NewDecoder(response.Body).Decode(&nonce); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	requestBody, _ := json.Marshal(map[string]string{"nonce": nonce.Nonce})
	response, err = client.Post(httpServer.URL+"/api/v1/observability/session", "application/json", bytes.NewReader(requestBody))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("session status = %d", response.StatusCode)
	}
	cookies := response.Cookies()
	_ = response.Body.Close()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("session cookies = %+v", cookies)
	}
	response, err = client.Get(httpServer.URL + "/api/v1/observability/traces")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("authenticated traces status = %d", response.StatusCode)
	}
	if response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("sensitive trace response is cacheable: %q", response.Header.Get("Cache-Control"))
	}
	// Nonces are one-time credentials.
	response, err = client.Post(httpServer.URL+"/api/v1/observability/session", "application/json", bytes.NewReader(requestBody))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("reused nonce status = %d", response.StatusCode)
	}
}

func TestObservabilityAllowsMemoryOnlyBearerOnlyForWailsOrigin(t *testing.T) {
	httpServer, _ := newObservationHTTPTestServer(t)
	client := &http.Client{}
	nonceRequest, _ := http.NewRequest(http.MethodGet, httpServer.URL+"/api/v1/observability/session/nonce", nil)
	nonceRequest.Header.Set("Origin", "wails://wails.localhost")
	response, err := client.Do(nonceRequest)
	if err != nil {
		t.Fatal(err)
	}
	var nonce struct {
		Nonce string `json:"nonce"`
	}
	_ = json.NewDecoder(response.Body).Decode(&nonce)
	_ = response.Body.Close()
	body, _ := json.Marshal(map[string]string{"nonce": nonce.Nonce})
	sessionRequest, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/api/v1/observability/session", bytes.NewReader(body))
	sessionRequest.Header.Set("Content-Type", "application/json")
	sessionRequest.Header.Set("Origin", "wails://wails.localhost")
	sessionRequest.Header.Set("X-AgentMux-Desktop", "1")
	response, err = client.Do(sessionRequest)
	if err != nil {
		t.Fatal(err)
	}
	var session struct {
		Token string `json:"session_token"`
	}
	_ = json.NewDecoder(response.Body).Decode(&session)
	_ = response.Body.Close()
	if session.Token == "" || response.Header.Get("Access-Control-Allow-Origin") != "wails://wails.localhost" {
		t.Fatalf("desktop session token/header missing: token=%t header=%q", session.Token != "", response.Header.Get("Access-Control-Allow-Origin"))
	}
	tracesRequest, _ := http.NewRequest(http.MethodGet, httpServer.URL+"/api/v1/observability/traces", nil)
	tracesRequest.Header.Set("Authorization", "Bearer "+session.Token)
	response, err = client.Do(tracesRequest)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("desktop bearer status = %d", response.StatusCode)
	}
}
