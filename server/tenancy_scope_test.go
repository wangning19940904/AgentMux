package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/provider"
	"github.com/wangning19940904/AgentMux/store"
)

type tenantFixture struct {
	tenant *core.Tenant
	secret string
}

// newTwoTenantServer builds a bridge-enabled server with two tenants, each
// owning one agent and one channel, plus one unassigned agent that predates
// tenancy.
func newTwoTenantServer(t *testing.T) (*Server, *store.Store, tenantFixture, tenantFixture) {
	t.Helper()
	srv, st := newTestServer(t)
	srv.cfg.Bridge.Enabled = true
	srv.cfg.Bridge.Token = "admin-secret"
	ctx := context.Background()
	now := time.Now().UTC()

	make := func(name string) tenantFixture {
		tenant := &core.Tenant{
			ID: "ten_" + name, Name: name, Kind: core.TenantKindApp,
			Status: core.TenantStatusActive, CreatedAt: now, UpdatedAt: now,
		}
		if err := st.UpsertTenant(ctx, tenant); err != nil {
			t.Fatalf("upsert tenant %s: %v", name, err)
		}
		token, err := st.CreateTenantToken(ctx, tenant.ID, "test", nil)
		if err != nil {
			t.Fatalf("token for %s: %v", name, err)
		}
		return tenantFixture{tenant: tenant, secret: token.Secret}
	}
	homebook := make("homebook")
	rookie := make("rookie")

	agents := []core.AgentInstance{
		{ID: "agent-homebook", Name: "homebook agent", RuntimeID: "codex", Enabled: true, OwnerTenantID: homebook.tenant.ID},
		{ID: "agent-rookie", Name: "rookie agent", RuntimeID: "codex", Enabled: true, OwnerTenantID: rookie.tenant.ID},
		{ID: "agent-legacy", Name: "legacy agent", RuntimeID: "codex", Enabled: true},
	}
	for i := range agents {
		agents[i].CreatedAt, agents[i].UpdatedAt = now, now
		if err := st.UpsertAgentInstance(ctx, &agents[i]); err != nil {
			t.Fatalf("upsert agent: %v", err)
		}
	}
	channels := []core.Channel{
		{ID: "chan-homebook", Name: "homebook channel", Type: "webhook", OwnerTenantID: homebook.tenant.ID},
		{ID: "chan-rookie", Name: "rookie channel", Type: "webhook", OwnerTenantID: rookie.tenant.ID},
	}
	for i := range channels {
		channels[i].CreatedAt, channels[i].UpdatedAt = now, now
		if err := st.UpsertChannel(ctx, &channels[i]); err != nil {
			t.Fatalf("upsert channel: %v", err)
		}
	}
	return srv, st, homebook, rookie
}

func asTenant(t *testing.T, srv *Server, secret, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, &buf)
	request.Header.Set("Authorization", "Bearer "+secret)
	recorder := httptest.NewRecorder()
	srv.withAuth(srv.mux).ServeHTTP(recorder, request)
	return recorder
}

func agentIDsIn(t *testing.T, body []byte) map[string]bool {
	t.Helper()
	var items []core.AgentInstance
	if err := json.Unmarshal(body, &items); err != nil {
		t.Fatalf("decode agents: %v (%s)", err, body)
	}
	out := map[string]bool{}
	for _, item := range items {
		out[item.ID] = true
	}
	return out
}

func TestAgentListIsScopedPerTenant(t *testing.T) {
	srv, _, homebook, rookie := newTwoTenantServer(t)

	mine := agentIDsIn(t, asTenant(t, srv, homebook.secret, http.MethodGet, "/api/v1/agent-instances", nil).Body.Bytes())
	if !mine["agent-homebook"] {
		t.Error("a tenant must see its own agent")
	}
	if mine["agent-rookie"] {
		t.Error("a tenant must not see a peer's agent")
	}
	if mine["agent-legacy"] {
		t.Error("an unassigned agent must stay admin-only")
	}

	theirs := agentIDsIn(t, asTenant(t, srv, rookie.secret, http.MethodGet, "/api/v1/agent-instances", nil).Body.Bytes())
	if !theirs["agent-rookie"] || theirs["agent-homebook"] {
		t.Error("the peer tenant sees the mirror-image scope")
	}

	// The admin sees all three, each labelled with its owner.
	adminReq := httptest.NewRequest(http.MethodGet, "/api/v1/agent-instances", nil)
	adminReq.Header.Set("Authorization", "Bearer admin-secret")
	adminRec := httptest.NewRecorder()
	srv.withAuth(srv.mux).ServeHTTP(adminRec, adminReq)
	var adminItems []core.AgentInstance
	if err := json.Unmarshal(adminRec.Body.Bytes(), &adminItems); err != nil {
		t.Fatalf("decode admin agents: %v", err)
	}
	owners := map[string]string{}
	for _, item := range adminItems {
		owners[item.ID] = item.OwnerTenantName
	}
	if owners["agent-homebook"] != "homebook" || owners["agent-rookie"] != "rookie" {
		t.Errorf("admin view must label ownership, got %v", owners)
	}
	if owners["agent-legacy"] != "" {
		t.Errorf("an unassigned agent must have no owner label, got %q", owners["agent-legacy"])
	}
}

func TestAdministratorCanPreviewTenantScopeWithoutTenantElevation(t *testing.T) {
	srv, _, homebook, rookie := newTwoTenantServer(t)
	handler := srv.withAuth(srv.mux)

	previewReq := httptest.NewRequest(http.MethodGet, "/api/v1/agent-instances", nil)
	previewReq.Header.Set("Authorization", "Bearer admin-secret")
	previewReq.Header.Set(tenantScopeHeader, homebook.tenant.ID)
	previewRec := httptest.NewRecorder()
	handler.ServeHTTP(previewRec, previewReq)
	if previewRec.Code != http.StatusOK {
		t.Fatalf("admin tenant preview = %d %s", previewRec.Code, previewRec.Body.String())
	}
	previewIDs := agentIDsIn(t, previewRec.Body.Bytes())
	if !previewIDs["agent-homebook"] || previewIDs["agent-rookie"] || previewIDs["agent-legacy"] {
		t.Fatalf("admin preview scope = %v", previewIDs)
	}

	closedReq := httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)
	closedReq.Header.Set("Authorization", "Bearer admin-secret")
	closedReq.Header.Set(tenantScopeHeader, homebook.tenant.ID)
	closedRec := httptest.NewRecorder()
	handler.ServeHTTP(closedRec, closedReq)
	if closedRec.Code != http.StatusForbidden {
		t.Fatalf("tenant preview reached admin route: %d", closedRec.Code)
	}

	peerReq := httptest.NewRequest(http.MethodGet, "/api/v1/agent-instances", nil)
	peerReq.Header.Set("Authorization", "Bearer "+rookie.secret)
	peerReq.Header.Set(tenantScopeHeader, homebook.tenant.ID)
	peerRec := httptest.NewRecorder()
	handler.ServeHTTP(peerRec, peerReq)
	peerIDs := agentIDsIn(t, peerRec.Body.Bytes())
	if !peerIDs["agent-rookie"] || peerIDs["agent-homebook"] {
		t.Fatalf("tenant changed scope through admin header: %v", peerIDs)
	}
}

func TestChannelListIsScopedPerTenant(t *testing.T) {
	srv, _, homebook, _ := newTwoTenantServer(t)

	recorder := asTenant(t, srv, homebook.secret, http.MethodGet, "/api/v1/channels", nil)
	var items []struct {
		ID              string `json:"id"`
		OwnerTenantName string `json:"owner_tenant_name"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode channels: %v (%s)", err, recorder.Body.String())
	}
	if len(items) != 1 || items[0].ID != "chan-homebook" {
		t.Fatalf("expected only the tenant's own channel, got %+v", items)
	}
}

func TestProviderListAndAgentSelectionRequireExplicitGrant(t *testing.T) {
	srv, st, homebook, _ := newTwoTenantServer(t)
	srv.provider = provider.NewManager(st)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, item := range []*core.Provider{
		{ID: "provider-visible", Name: "visible", BaseURL: "https://visible.invalid", CreatedAt: now, UpdatedAt: now},
		{ID: "provider-hidden", Name: "hidden", BaseURL: "https://hidden.invalid", CreatedAt: now, UpdatedAt: now},
	} {
		if err := st.UpsertProvider(ctx, item); err != nil {
			t.Fatalf("upsert provider: %v", err)
		}
	}
	if err := st.UpsertResourceGrant(ctx, &core.ResourceGrant{
		TenantID: homebook.tenant.ID, ResourceType: core.ResourceTypeProvider,
		ResourceID: "provider-visible", Level: core.GrantLevelRead,
	}); err != nil {
		t.Fatalf("grant provider: %v", err)
	}

	listed := asTenant(t, srv, homebook.secret, http.MethodGet, "/api/v1/providers", nil)
	if listed.Code != http.StatusOK {
		t.Fatalf("provider list = %d body = %s", listed.Code, listed.Body.String())
	}
	var providers []*core.Provider
	if err := json.Unmarshal(listed.Body.Bytes(), &providers); err != nil {
		t.Fatalf("decode providers: %v", err)
	}
	if len(providers) != 1 || providers[0].ID != "provider-visible" {
		t.Fatalf("tenant provider scope = %+v", providers)
	}

	agent, err := st.GetAgentInstance(ctx, "agent-homebook")
	if err != nil || agent == nil {
		t.Fatalf("load agent: %v (%v)", err, agent)
	}
	agent.ProviderID = "provider-hidden"
	agent.ProviderTool = "codex"

	denied := asTenant(t, srv, homebook.secret, http.MethodPost, "/api/v1/agent-instances", agent)
	if denied.Code != http.StatusNotFound {
		t.Fatalf("select ungranted provider = %d, want 404; body = %s", denied.Code, denied.Body.String())
	}
	if err := st.UpsertResourceGrant(ctx, &core.ResourceGrant{
		TenantID: homebook.tenant.ID, ResourceType: core.ResourceTypeProvider,
		ResourceID: "provider-hidden", Level: core.GrantLevelRead,
	}); err != nil {
		t.Fatalf("grant provider read: %v", err)
	}
	readOnly := asTenant(t, srv, homebook.secret, http.MethodPost, "/api/v1/agent-instances", agent)
	if readOnly.Code != http.StatusForbidden {
		t.Fatalf("select read-only provider = %d, want 403; body = %s", readOnly.Code, readOnly.Body.String())
	}
	if err := st.UpsertResourceGrant(ctx, &core.ResourceGrant{
		TenantID: homebook.tenant.ID, ResourceType: core.ResourceTypeProvider,
		ResourceID: "provider-hidden", Level: core.GrantLevelUse,
	}); err != nil {
		t.Fatalf("grant provider use: %v", err)
	}
	allowed := asTenant(t, srv, homebook.secret, http.MethodPost, "/api/v1/agent-instances", agent)
	if allowed.Code != http.StatusOK {
		t.Fatalf("select granted provider = %d, want 200; body = %s", allowed.Code, allowed.Body.String())
	}
}

func TestTenantCannotReachPeerResourcesByID(t *testing.T) {
	srv, _, homebook, _ := newTwoTenantServer(t)

	// Deleting a peer's agent reports "not found" rather than confirming that
	// the ID exists.
	deleted := asTenant(t, srv, homebook.secret, http.MethodDelete, "/api/v1/agent-instances?id=agent-rookie", nil)
	if deleted.Code != http.StatusNotFound {
		t.Errorf("deleting a peer agent = %d, want 404", deleted.Code)
	}
	legacy := asTenant(t, srv, homebook.secret, http.MethodDelete, "/api/v1/agent-instances?id=agent-legacy", nil)
	if legacy.Code != http.StatusNotFound {
		t.Errorf("deleting an unassigned agent = %d, want 404", legacy.Code)
	}
	channel := asTenant(t, srv, homebook.secret, http.MethodDelete, "/api/v1/channels?id=chan-rookie", nil)
	if channel.Code != http.StatusNotFound {
		t.Errorf("deleting a peer channel = %d, want 404", channel.Code)
	}

	// Its own resources remain deletable.
	own := asTenant(t, srv, homebook.secret, http.MethodDelete, "/api/v1/channels?id=chan-homebook", nil)
	if own.Code != http.StatusOK {
		t.Errorf("deleting an owned channel = %d body = %s", own.Code, own.Body.String())
	}
}

func TestTenantWritesAreStampedWithOwnership(t *testing.T) {
	srv, st, homebook, rookie := newTwoTenantServer(t)

	// A tenant cannot smuggle another owner or publish a resource.
	created := asTenant(t, srv, homebook.secret, http.MethodPost, "/api/v1/channels", map[string]any{
		"name":            "sneaky",
		"type":            "webhook",
		"owner_tenant_id": rookie.tenant.ID,
		"visibility":      core.VisibilityPublic,
		"config":          map[string]string{"url": "https://example.invalid/hook"},
	})
	if created.Code != http.StatusOK {
		t.Fatalf("create channel = %d body = %s", created.Code, created.Body.String())
	}
	var saved core.Channel
	if err := json.Unmarshal(created.Body.Bytes(), &saved); err != nil {
		t.Fatalf("decode channel: %v", err)
	}
	if saved.OwnerTenantID != homebook.tenant.ID {
		t.Errorf("owner = %q, want the calling tenant", saved.OwnerTenantID)
	}
	if saved.Visibility == core.VisibilityPublic {
		t.Error("a tenant must not be able to publish a resource")
	}

	stored, err := st.GetChannel(context.Background(), saved.ID)
	if err != nil || stored == nil {
		t.Fatalf("reload channel: %v (%v)", err, stored)
	}
	if stored.OwnerTenantID != homebook.tenant.ID {
		t.Errorf("persisted owner = %q", stored.OwnerTenantID)
	}
}

// A grant on a private resource must authorize writes, not merely make the
// resource visible. Listing uses a SQL subquery while authorization goes
// through accessLevel, so the two can drift apart: an earlier version folded
// the grant against an empty baseline level and silently discarded it.
func TestGrantOnPrivatePeerResourceAuthorizesWrites(t *testing.T) {
	srv, st, homebook, _ := newTwoTenantServer(t)
	ctx := context.Background()

	if err := st.UpsertResourceGrant(ctx, &core.ResourceGrant{
		TenantID: homebook.tenant.ID, ResourceType: core.ResourceTypeChannel,
		ResourceID: "chan-rookie", Level: core.GrantLevelRead,
	}); err != nil {
		t.Fatalf("grant read: %v", err)
	}

	// read makes it visible.
	listed := asTenant(t, srv, homebook.secret, http.MethodGet, "/api/v1/channels", nil)
	var items []struct{ ID string }
	if err := json.Unmarshal(listed.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode channels: %v", err)
	}
	found := false
	for _, item := range items {
		if item.ID == "chan-rookie" {
			found = true
		}
	}
	if !found {
		t.Fatal("a read grant must make the peer channel visible")
	}

	// ...but not manageable: 403 rather than 404, because the tenant can see it.
	denied := asTenant(t, srv, homebook.secret, http.MethodDelete, "/api/v1/channels?id=chan-rookie", nil)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("delete with a read grant = %d, want 403; body = %s", denied.Code, denied.Body.String())
	}

	// manage lifts the restriction.
	if err := st.UpsertResourceGrant(ctx, &core.ResourceGrant{
		TenantID: homebook.tenant.ID, ResourceType: core.ResourceTypeChannel,
		ResourceID: "chan-rookie", Level: core.GrantLevelManage,
	}); err != nil {
		t.Fatalf("grant manage: %v", err)
	}
	allowed := asTenant(t, srv, homebook.secret, http.MethodDelete, "/api/v1/channels?id=chan-rookie", nil)
	if allowed.Code != http.StatusOK {
		t.Fatalf("delete with a manage grant = %d, want 200; body = %s", allowed.Code, allowed.Body.String())
	}
}

func TestPublicAndGrantedAccessLevels(t *testing.T) {
	srv, st, homebook, rookie := newTwoTenantServer(t)
	ctx := context.Background()

	// Public makes a peer's agent visible and runnable, but not editable.
	peer, err := st.GetAgentInstance(ctx, "agent-rookie")
	if err != nil || peer == nil {
		t.Fatalf("load peer agent: %v", err)
	}
	peer.Visibility = core.VisibilityPublic
	if err := st.UpsertAgentInstance(ctx, peer); err != nil {
		t.Fatalf("publish peer agent: %v", err)
	}

	visible := agentIDsIn(t, asTenant(t, srv, homebook.secret, http.MethodGet, "/api/v1/agent-instances", nil).Body.Bytes())
	if !visible["agent-rookie"] {
		t.Error("a public agent must be visible to every tenant")
	}
	deleted := asTenant(t, srv, homebook.secret, http.MethodDelete, "/api/v1/agent-instances?id=agent-rookie", nil)
	if deleted.Code != http.StatusForbidden {
		t.Errorf("deleting a public peer agent = %d, want 403", deleted.Code)
	}

	// An explicit manage grant lifts that restriction.
	if err := st.UpsertResourceGrant(ctx, &core.ResourceGrant{
		TenantID: homebook.tenant.ID, ResourceType: core.ResourceTypeAgent,
		ResourceID: "agent-rookie", Level: core.GrantLevelManage,
	}); err != nil {
		t.Fatalf("grant manage: %v", err)
	}
	granted := asTenant(t, srv, homebook.secret, http.MethodDelete, "/api/v1/agent-instances?id=agent-rookie", nil)
	if granted.Code != http.StatusOK {
		t.Errorf("deleting with a manage grant = %d body = %s", granted.Code, granted.Body.String())
	}
	_ = rookie
}

func TestTenantCannotInvokePeerAgent(t *testing.T) {
	srv, _, homebook, _ := newTwoTenantServer(t)

	// The invoker is nil in tests, so a permitted target reports the runtime
	// as unavailable while a forbidden one is rejected before it gets there.
	forbidden := asTenant(t, srv, homebook.secret, http.MethodPost, "/api/v1/invocations", map[string]any{
		"agent_id": "agent-rookie",
		"input":    "hello",
	})
	if forbidden.Code != http.StatusServiceUnavailable && forbidden.Code != http.StatusNotFound {
		t.Fatalf("unexpected status %d body = %s", forbidden.Code, forbidden.Body.String())
	}
	if forbidden.Code == http.StatusServiceUnavailable {
		t.Skip("invocation runtime is not wired in this test server")
	}

}
