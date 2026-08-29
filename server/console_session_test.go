package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestConsoleSessionFlow(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.cfg.Bridge.Enabled = true
	srv.cfg.Bridge.Token = "bridge-secret"
	handler := srv.withAuth(srv.mux)

	// 1. Minting requires the real bridge bearer token.
	denied := httptest.NewRecorder()
	handler.ServeHTTP(denied, httptest.NewRequest(http.MethodPost, consoleSessionEndpoint, nil))
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated mint code = %d", denied.Code)
	}

	mint := httptest.NewRequest(http.MethodPost, consoleSessionEndpoint+"?landing=tenants", nil)
	mint.Header.Set("Authorization", "Bearer bridge-secret")
	mint.Host = "127.0.0.1:8766"
	minted := httptest.NewRecorder()
	handler.ServeHTTP(minted, mint)
	if minted.Code != http.StatusOK {
		t.Fatalf("mint code = %d body = %s", minted.Code, minted.Body.String())
	}
	var session struct {
		EnterURL          string `json:"enter_url"`
		ExpiresAt         string `json:"expires_at"`
		SessionTTLSeconds int    `json:"session_ttl_seconds"`
	}
	if err := json.Unmarshal(minted.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(session.EnterURL, "http://127.0.0.1:8766"+consoleEnterPath+"?nonce=") {
		t.Fatalf("enter_url = %q", session.EnterURL)
	}
	if !strings.Contains(session.EnterURL, "&landing=tenants") {
		t.Fatalf("tenant landing missing from enter_url = %q", session.EnterURL)
	}
	if session.SessionTTLSeconds <= 0 || session.ExpiresAt == "" {
		t.Fatalf("session metadata = %+v", session)
	}

	// 2. The nonce is exchanged for an HttpOnly cookie exactly once.
	entered := httptest.NewRecorder()
	enterURL, err := url.Parse(session.EnterURL)
	if err != nil {
		t.Fatal(err)
	}
	handler.ServeHTTP(entered, httptest.NewRequest(http.MethodGet, enterURL.RequestURI(), nil))
	if entered.Code != http.StatusFound {
		t.Fatalf("enter code = %d body = %s", entered.Code, entered.Body.String())
	}
	if location := entered.Header().Get("Location"); location != "/#tenants" {
		t.Fatalf("enter redirect = %q", location)
	}
	cookies := entered.Result().Cookies()
	var sessionCookie *http.Cookie
	for _, cookie := range cookies {
		if cookie.Name == consoleSessionCookie {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil || !sessionCookie.HttpOnly || sessionCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie = %+v", sessionCookie)
	}

	replayed := httptest.NewRecorder()
	handler.ServeHTTP(replayed, httptest.NewRequest(http.MethodGet, enterURL.RequestURI(), nil))
	if replayed.Code != http.StatusUnauthorized {
		t.Fatalf("nonce replay code = %d", replayed.Code)
	}

	// 3. The cookie authorizes /api/* only on same-origin requests.
	authorized := httptest.NewRecorder()
	apiReq := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	apiReq.AddCookie(sessionCookie)
	apiReq.Header.Set(consoleCSRFHeader, "1")
	handler.ServeHTTP(authorized, apiReq)
	if authorized.Code != http.StatusOK {
		t.Fatalf("cookie-authorized code = %d body = %s", authorized.Code, authorized.Body.String())
	}

	fetchSame := httptest.NewRecorder()
	fetchReq := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	fetchReq.AddCookie(sessionCookie)
	fetchReq.Header.Set("Sec-Fetch-Site", "same-origin")
	handler.ServeHTTP(fetchSame, fetchReq)
	if fetchSame.Code != http.StatusOK {
		t.Fatalf("same-origin fetch code = %d", fetchSame.Code)
	}

	crossSite := httptest.NewRecorder()
	crossReq := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	crossReq.AddCookie(sessionCookie)
	crossReq.Header.Set("Sec-Fetch-Site", "cross-site")
	handler.ServeHTTP(crossSite, crossReq)
	if crossSite.Code != http.StatusUnauthorized {
		t.Fatalf("cross-site code = %d", crossSite.Code)
	}

	// 4. A console cookie must not mint further sessions.
	cookieMint := httptest.NewRecorder()
	cookieMintReq := httptest.NewRequest(http.MethodPost, consoleSessionEndpoint, nil)
	cookieMintReq.AddCookie(sessionCookie)
	cookieMintReq.Header.Set(consoleCSRFHeader, "1")
	handler.ServeHTTP(cookieMint, cookieMintReq)
	if cookieMint.Code != http.StatusUnauthorized {
		t.Fatalf("cookie mint code = %d", cookieMint.Code)
	}
}

func TestConsoleSessionRejectsUnsafeLanding(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.cfg.Bridge.Enabled = true
	srv.cfg.Bridge.Token = "bridge-secret"
	handler := srv.withAuth(srv.mux)

	request := httptest.NewRequest(
		http.MethodPost,
		consoleSessionEndpoint+"?landing=https%3A%2F%2Fevil.example",
		nil,
	)
	request.Header.Set("Authorization", "Bearer bridge-secret")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unsafe landing code = %d body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestConsoleSessionRequiresBearerToken(t *testing.T) {
	srv, _ := newTestServer(t)
	recorder := httptest.NewRecorder()
	srv.mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, consoleSessionEndpoint, nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated mint code = %d", recorder.Code)
	}
	enter := httptest.NewRecorder()
	srv.mux.ServeHTTP(enter, httptest.NewRequest(http.MethodGet, consoleEnterPath+"?nonce=x", nil))
	if enter.Code != http.StatusUnauthorized {
		t.Fatalf("invalid nonce enter code = %d", enter.Code)
	}
}
