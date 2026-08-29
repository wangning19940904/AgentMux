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

func TestTenantTokenIsConfinedToPublishedRoutes(t *testing.T) {
	srv, _, _, secret := newTenantServer(t)
	handler := srv.withAuth(srv.mux)

	allowed := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/status"},
		{http.MethodGet, "/api/v1/capabilities"},
		{http.MethodGet, "/api/v1/agent-instances"},
		{http.MethodGet, "/api/v1/channels"},
		{http.MethodGet, "/api/v1/triggers"},
		{http.MethodGet, "/api/v1/usage"},
	}
	for _, route := range allowed {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(route.method, route.path, nil)
		request.Header.Set("Authorization", "Bearer "+secret)
		handler.ServeHTTP(recorder, request)
		if recorder.Code == http.StatusForbidden || recorder.Code == http.StatusUnauthorized {
			t.Errorf("%s %s must be available to a tenant, got %d", route.method, route.path, recorder.Code)
		}
	}

	// The Console-only management surface is closed to tenants. These are the
	// "internal" tier in contract/CONTRACT.md.
	denied := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/v1/remote/hosts"},
		{http.MethodGet, "/api/v1/sessions"},
		{http.MethodGet, "/api/v1/observability/traces"},
		{http.MethodPost, "/api/v1/providers"},
		{http.MethodGet, "/api/v1/system/directories"},
		{http.MethodGet, "/api/v1/meetings"},
		{http.MethodGet, "/api/v1/tenancy/tenants"},
		{http.MethodDelete, "/api/v1/providers"},
	}
	for _, route := range denied {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(route.method, route.path, nil)
		request.Header.Set("Authorization", "Bearer "+secret)
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusForbidden {
			t.Errorf("%s %s must be forbidden for a tenant, got %d", route.method, route.path, recorder.Code)
		}
	}

	// The same routes stay open to the admin token.
	for _, route := range denied {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(route.method, route.path, nil)
		request.Header.Set("Authorization", "Bearer admin-secret")
		handler.ServeHTTP(recorder, request)
		if recorder.Code == http.StatusForbidden {
			t.Errorf("%s %s must remain available to the admin", route.method, route.path)
		}
	}
}

func TestTenantRoutePolicyDeniesUnlistedMethods(t *testing.T) {
	if tenantRouteAllowed(http.MethodDelete, "/api/v1/usage") {
		t.Error("DELETE on a GET-only route must be denied")
	}
	if !tenantRouteAllowed(http.MethodPost, "/api/v1/invocations") {
		t.Error("POST /api/v1/invocations must be allowed")
	}
	for _, path := range []string{
		"/api/v1/setup/feishu/begin",
		"/api/v1/setup/feishu/poll",
		"/api/v1/setup/feishu/automation/begin",
		"/api/v1/setup/feishu/automation/poll",
		"/api/v1/setup/feishu/automation/configure",
	} {
		if !tenantRouteAllowed(http.MethodPost, path) {
			t.Errorf("POST %s must be available to a tenant configuring its channel", path)
		}
	}
	if tenantRouteAllowed(http.MethodGet, "/api/v1/setup/feishu/begin") {
		t.Error("GET on the Feishu setup route must remain denied")
	}
	if tenantRouteAllowed(http.MethodGet, "/api/v1/unknown-future-endpoint") {
		t.Error("an unlisted route must default to denied")
	}
	if !tenantRouteAllowed(http.MethodPost, "/v1/responses") {
		t.Error("the OpenAI-compatible surface must be allowed")
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
	handler := srv.withAuth(srv.mux)

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
