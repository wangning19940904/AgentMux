package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
)

// newTenantServer returns a bridge-enabled server plus one active tenant and a
// usable token secret for it.
func newTenantServer(t *testing.T) (*Server, *store.Store, *core.Tenant, string) {
	t.Helper()
	srv, st := newTestServer(t)
	srv.cfg.Bridge.Enabled = true
	srv.cfg.Bridge.Token = "admin-secret"

	now := time.Now().UTC()
	tenant := &core.Tenant{
		ID:        "ten_homebook",
		Name:      "homebook",
		Kind:      core.TenantKindWeb,
		Status:    core.TenantStatusActive,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := st.UpsertTenant(context.Background(), tenant); err != nil {
		t.Fatalf("upsert tenant: %v", err)
	}
	token, err := st.CreateTenantToken(context.Background(), tenant.ID, "test", nil)
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	return srv, st, tenant, token.Secret
}

func TestTenantRoutePolicyAndMiddleware(t *testing.T) {
	tests := []struct {
		method  string
		path    string
		allowed bool
	}{
		{http.MethodGet, "/api/v1/status", true},
		{http.MethodGet, "/api/v1/capabilities", true},
		{http.MethodGet, "/api/v1/agent-instances", true},
		{http.MethodGet, "/api/v1/channels", true},
		{http.MethodGet, "/api/v1/triggers", true},
		{http.MethodGet, "/api/v1/usage", true},
		{http.MethodPost, "/api/v1/invocations", true},
		{http.MethodPost, "/v1/responses", true},
		{http.MethodPost, "/api/v1/setup/feishu/begin", true},
		{http.MethodPost, "/api/v1/setup/feishu/poll", true},
		{http.MethodPost, "/api/v1/setup/feishu/automation/begin", true},
		{http.MethodPost, "/api/v1/setup/feishu/automation/poll", true},
		{http.MethodPost, "/api/v1/setup/feishu/automation/configure", true},
		{http.MethodDelete, "/api/v1/usage", false},
		{http.MethodGet, "/api/v1/setup/feishu/begin", false},
		{http.MethodGet, "/api/v1/remote/hosts", false},
		{http.MethodGet, "/api/v1/sessions", false},
		{http.MethodGet, "/api/v1/observability/traces", false},
		{http.MethodPost, "/api/v1/providers", false},
		{http.MethodGet, "/api/v1/system/directories", false},
		{http.MethodGet, "/api/v1/meetings", false},
		{http.MethodGet, "/api/v1/tenancy/tenants", false},
		{http.MethodDelete, "/api/v1/providers", false},
		{http.MethodGet, "/api/v1/unknown-future-endpoint", false},
	}
	for _, test := range tests {
		if got := tenantRouteAllowed(test.method, test.path); got != test.allowed {
			t.Errorf("tenantRouteAllowed(%s, %s) = %t, want %t", test.method, test.path, got, test.allowed)
		}
	}

	srv, _ := newTestServer(t)
	nonce, _ := srv.consoleSessions.issueNonce(&core.Principal{TenantID: "ten_homebook", TenantName: "homebook"})
	sessionToken, ok := srv.consoleSessions.consumeNonce(nonce)
	if !ok {
		t.Fatal("could not create tenant console fixture")
	}
	handler := srv.withAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	request := func(path string, tenant bool) int {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if tenant {
			req.AddCookie(&http.Cookie{Name: consoleSessionCookie, Value: sessionToken})
			req.Header.Set(consoleCSRFHeader, "1")
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		return recorder.Code
	}
	if code := request("/api/v1/status", true); code != http.StatusNoContent {
		t.Fatalf("allowed tenant route = %d", code)
	}
	if code := request("/api/v1/sessions", true); code != http.StatusForbidden {
		t.Fatalf("denied tenant route = %d", code)
	}
	if code := request("/api/v1/sessions", false); code != http.StatusNoContent {
		t.Fatalf("bridge-disabled admin route = %d", code)
	}
}

func TestRevokedTenantTokenStopsAuthenticating(t *testing.T) {
	srv, st, tenant, secret := newTenantServer(t)
	handler := srv.withAuth(srv.mux)

	tokens, err := st.ListTenantTokens(context.Background(), tenant.ID)
	if err != nil || len(tokens) != 1 {
		t.Fatalf("list tokens: %v (n=%d)", err, len(tokens))
	}
	if err := st.RevokeTenantToken(context.Background(), tokens[0].ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	request.Header.Set("Authorization", "Bearer "+secret)
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token code = %d", recorder.Code)
	}
}

func TestConsoleSessionInheritsTenantScope(t *testing.T) {
	srv, _, tenant, secret := newTenantServer(t)
	// Self-registered applications can embed their tenant Console even when
	// the instance keeps legacy bridge authentication disabled.
	srv.cfg.Bridge.Enabled = false
	handler := srv.withAuth(srv.mux)

	// Explicit bearer credentials remain tenant-scoped even though anonymous
	// requests retain the bridge-disabled legacy administrator behaviour.
	bearerSelf := httptest.NewRecorder()
	bearerSelfReq := httptest.NewRequest(http.MethodGet, "/api/v1/tenancy/self", nil)
	bearerSelfReq.Header.Set("Authorization", "Bearer "+secret)
	handler.ServeHTTP(bearerSelf, bearerSelfReq)
	if bearerSelf.Code != http.StatusOK || !contains(bearerSelf.Body.String(), tenant.Name) {
		t.Fatalf("bridge-disabled tenant bearer identity = %d %s", bearerSelf.Code, bearerSelf.Body.String())
	}

	// A tenant token mints a Console session bound to itself.
	mint := httptest.NewRequest(http.MethodPost, consoleSessionEndpoint, nil)
	mint.Header.Set("Authorization", "Bearer "+secret)
	minted := httptest.NewRecorder()
	handler.ServeHTTP(minted, mint)
	if minted.Code != http.StatusOK {
		t.Fatalf("tenant mint code = %d body = %s", minted.Code, minted.Body.String())
	}

	nonce := extractNonce(t, minted.Body.String())
	entered := httptest.NewRecorder()
	handler.ServeHTTP(entered, httptest.NewRequest(http.MethodGet, consoleEnterPath+"?nonce="+nonce, nil))
	if entered.Code != http.StatusFound {
		t.Fatalf("enter code = %d", entered.Code)
	}
	var sessionCookie *http.Cookie
	for _, cookie := range entered.Result().Cookies() {
		if cookie.Name == consoleSessionCookie {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil {
		t.Fatal("expected a console session cookie")
	}

	// The cookie carries the tenant scope, not admin rights: a published
	// route works, an internal one does not.
	open := httptest.NewRecorder()
	openReq := httptest.NewRequest(http.MethodGet, "/api/v1/agent-instances", nil)
	openReq.AddCookie(sessionCookie)
	openReq.Header.Set(consoleCSRFHeader, "1")
	handler.ServeHTTP(open, openReq)
	if open.Code != http.StatusOK {
		t.Fatalf("tenant console session must reach agent-instances, got %d", open.Code)
	}

	closed := httptest.NewRecorder()
	closedReq := httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)
	closedReq.AddCookie(sessionCookie)
	closedReq.Header.Set(consoleCSRFHeader, "1")
	handler.ServeHTTP(closed, closedReq)
	if closed.Code != http.StatusForbidden {
		t.Fatalf("tenant console session must not reach internal routes, got %d", closed.Code)
	}

	// The session reports the tenant identity back to the Console.
	self := httptest.NewRecorder()
	selfReq := httptest.NewRequest(http.MethodGet, "/api/v1/tenancy/self", nil)
	selfReq.AddCookie(sessionCookie)
	selfReq.Header.Set(consoleCSRFHeader, "1")
	handler.ServeHTTP(self, selfReq)
	if self.Code != http.StatusOK {
		t.Fatalf("self code = %d body = %s", self.Code, self.Body.String())
	}
	if !contains(self.Body.String(), tenant.Name) {
		t.Fatalf("self response must name the tenant, got %s", self.Body.String())
	}
}

func TestBridgeDisabledRejectsInvalidExplicitCredential(t *testing.T) {
	srv, _ := newTestServer(t)
	handler := srv.withAuth(srv.mux)

	request := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	request.Header.Set("Authorization", "Bearer invalid")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("invalid explicit credential code = %d body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestTenancyRegisterCreatesTenantWithoutApproval(t *testing.T) {
	srv, st, _, _ := newTenantServer(t)
	handler := srv.withAuth(srv.mux)

	// A new application has no credential yet. Registration creates an empty
	// tenant and its first token without requiring the administrator token.
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/tenancy/register",
		strings.NewReader(`{"name":"rookie-trade","kind":"web"}`))
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("register code = %d body = %s", recorder.Code, recorder.Body.String())
	}
	var created struct {
		Tenant core.Tenant `json:"tenant"`
		Token  string      `json:"token"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.Tenant.Name != "rookie-trade" || created.Token == "" {
		t.Fatalf("unexpected registration result: %+v", created)
	}

	// The issued token authenticates as the new tenant.
	authed, err := st.AuthenticateTenantToken(context.Background(), created.Token)
	if err != nil || authed == nil || authed.ID != created.Tenant.ID {
		t.Fatalf("issued token must authenticate the new tenant: %v %+v", err, authed)
	}

	// A duplicate name is a conflict, not a token rotation.
	duplicate := httptest.NewRecorder()
	again := httptest.NewRequest(http.MethodPost, "/api/v1/tenancy/register",
		strings.NewReader(`{"name":"rookie-trade"}`))
	handler.ServeHTTP(duplicate, again)
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate register code = %d body = %s", duplicate.Code, duplicate.Body.String())
	}

	// A missing name is rejected outright.
	blank := httptest.NewRecorder()
	empty := httptest.NewRequest(http.MethodPost, "/api/v1/tenancy/register", strings.NewReader(`{}`))
	handler.ServeHTTP(blank, empty)
	if blank.Code != http.StatusBadRequest {
		t.Fatalf("blank register code = %d", blank.Code)
	}
}

func extractNonce(t *testing.T, body string) string {
	t.Helper()
	const marker = "?nonce="
	index := indexOf(body, marker)
	if index < 0 {
		t.Fatalf("no nonce in %s", body)
	}
	rest := body[index+len(marker):]
	for i := 0; i < len(rest); i++ {
		if rest[i] == '"' {
			return rest[:i]
		}
	}
	t.Fatalf("unterminated nonce in %s", body)
	return ""
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

func contains(haystack, needle string) bool { return indexOf(haystack, needle) >= 0 }
