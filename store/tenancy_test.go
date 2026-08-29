package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func newTenancyStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "tenancy.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestTenantCRUDAndTokenAuthentication(t *testing.T) {
	ctx := context.Background()
	st := newTenancyStore(t)

	now := time.Now().UTC()
	tenant := &core.Tenant{
		ID:        "ten_homebook",
		Name:      "homebook",
		Kind:      core.TenantKindWeb,
		Status:    core.TenantStatusActive,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := st.UpsertTenant(ctx, tenant); err != nil {
		t.Fatalf("upsert tenant: %v", err)
	}

	loaded, err := st.GetTenantByName(ctx, "homebook")
	if err != nil || loaded == nil {
		t.Fatalf("get tenant by name: %v (loaded=%v)", err, loaded)
	}
	if loaded.Kind != core.TenantKindWeb || !loaded.Active() {
		t.Fatalf("unexpected tenant round trip: %+v", loaded)
	}

	token, err := st.CreateTenantToken(ctx, tenant.ID, "primary", nil)
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	if token.Secret == "" {
		t.Fatal("expected the plaintext secret on the create response")
	}
	if token.Prefix == token.Secret {
		t.Fatal("prefix must be a truncation, not the whole secret")
	}

	authenticated, err := st.AuthenticateTenantToken(ctx, token.Secret)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if authenticated == nil || authenticated.ID != tenant.ID {
		t.Fatalf("expected the owning tenant, got %v", authenticated)
	}

	// The plaintext must not be recoverable from a listing.
	tokens, err := st.ListTenantTokens(ctx, tenant.ID)
	if err != nil || len(tokens) != 1 {
		t.Fatalf("list tokens: %v (n=%d)", err, len(tokens))
	}
	if tokens[0].Secret != "" {
		t.Fatal("listed tokens must never carry the plaintext secret")
	}

	if err := st.RevokeTenantToken(ctx, token.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	revoked, err := st.AuthenticateTenantToken(ctx, token.Secret)
	if err != nil {
		t.Fatalf("authenticate after revoke: %v", err)
	}
	if revoked != nil {
		t.Fatal("a revoked token must stop authenticating")
	}
}

func TestTenantTokenRejectsExpiredAndDisabled(t *testing.T) {
	ctx := context.Background()
	st := newTenancyStore(t)

	now := time.Now().UTC()
	tenant := &core.Tenant{ID: "ten_a", Name: "a", Status: core.TenantStatusActive, CreatedAt: now, UpdatedAt: now}
	if err := st.UpsertTenant(ctx, tenant); err != nil {
		t.Fatalf("upsert tenant: %v", err)
	}

	past := now.Add(-time.Hour)
	expired, err := st.CreateTenantToken(ctx, tenant.ID, "expired", &past)
	if err != nil {
		t.Fatalf("create expired token: %v", err)
	}
	got, err := st.AuthenticateTenantToken(ctx, expired.Secret)
	if err != nil {
		t.Fatalf("authenticate expired: %v", err)
	}
	if got != nil {
		t.Fatal("an expired token must not authenticate")
	}

	live, err := st.CreateTenantToken(ctx, tenant.ID, "live", nil)
	if err != nil {
		t.Fatalf("create live token: %v", err)
	}
	tenant.Status = core.TenantStatusDisabled
	if err := st.UpsertTenant(ctx, tenant); err != nil {
		t.Fatalf("disable tenant: %v", err)
	}
	got, err = st.AuthenticateTenantToken(ctx, live.Secret)
	if err != nil {
		t.Fatalf("authenticate disabled: %v", err)
	}
	if got != nil {
		t.Fatal("a disabled tenant's token must not authenticate")
	}
}

func TestTenantVisibilityCoversOwnPublicAndGranted(t *testing.T) {
	ctx := context.Background()
	st := newTenancyStore(t)

	now := time.Now().UTC()
	for _, name := range []string{"homebook", "rookie"} {
		if err := st.UpsertTenant(ctx, &core.Tenant{
			ID: "ten_" + name, Name: name, Status: core.TenantStatusActive,
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("upsert tenant %s: %v", name, err)
		}
	}

	agents := []core.AgentInstance{
		{ID: "own", Name: "own", RuntimeID: "codex", OwnerTenantID: "ten_homebook"},
		{ID: "peer", Name: "peer", RuntimeID: "codex", OwnerTenantID: "ten_rookie"},
		{ID: "shared", Name: "shared", RuntimeID: "codex", Visibility: core.VisibilityPublic},
		{ID: "granted", Name: "granted", RuntimeID: "codex", OwnerTenantID: "ten_rookie"},
		{ID: "orphan", Name: "orphan", RuntimeID: "codex"},
	}
	for i := range agents {
		agents[i].CreatedAt, agents[i].UpdatedAt = now, now
		if err := st.UpsertAgentInstance(ctx, &agents[i]); err != nil {
			t.Fatalf("upsert agent %s: %v", agents[i].ID, err)
		}
	}
	if err := st.UpsertResourceGrant(ctx, &core.ResourceGrant{
		TenantID: "ten_homebook", ResourceType: core.ResourceTypeAgent,
		ResourceID: "granted", Level: core.GrantLevelUse,
	}); err != nil {
		t.Fatalf("grant: %v", err)
	}

	visible, err := st.ListAgentInstancesForTenant(ctx, "ten_homebook")
	if err != nil {
		t.Fatalf("scoped list: %v", err)
	}
	seen := map[string]bool{}
	for _, agent := range visible {
		seen[agent.ID] = true
	}
	for _, expected := range []string{"own", "shared", "granted"} {
		if !seen[expected] {
			t.Errorf("expected %q to be visible to homebook", expected)
		}
	}
	for _, hidden := range []string{"peer", "orphan"} {
		if seen[hidden] {
			t.Errorf("%q must not be visible to homebook", hidden)
		}
	}

	// The admin view is unfiltered.
	all, err := st.ListAgentInstances(ctx)
	if err != nil {
		t.Fatalf("admin list: %v", err)
	}
	if len(all) != len(agents) {
		t.Fatalf("admin must see every agent, got %d of %d", len(all), len(agents))
	}

	level, err := st.GrantLevelFor(ctx, "ten_homebook", core.ResourceTypeAgent, "granted")
	if err != nil {
		t.Fatalf("grant level: %v", err)
	}
	if !core.GrantSatisfies(level, core.GrantLevelUse) {
		t.Fatalf("expected use to be satisfied by %q", level)
	}
	if core.GrantSatisfies(level, core.GrantLevelManage) {
		t.Fatal("a use grant must not satisfy manage")
	}

	if err := st.DeleteResourceGrant(ctx, "ten_homebook", core.ResourceTypeAgent, "granted"); err != nil {
		t.Fatalf("revoke grant: %v", err)
	}
	visible, err = st.ListAgentInstancesForTenant(ctx, "ten_homebook")
	if err != nil {
		t.Fatalf("scoped list after revoke: %v", err)
	}
	for _, agent := range visible {
		if agent.ID == "granted" {
			t.Fatal("a revoked grant must remove visibility")
		}
	}
}

func TestDeleteTenantOrphansResourcesInsteadOfDeletingThem(t *testing.T) {
	ctx := context.Background()
	st := newTenancyStore(t)

	now := time.Now().UTC()
	if err := st.UpsertTenant(ctx, &core.Tenant{
		ID: "ten_gone", Name: "gone", Status: core.TenantStatusActive, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("upsert tenant: %v", err)
	}
	agent := &core.AgentInstance{ID: "a1", Name: "a1", RuntimeID: "codex", OwnerTenantID: "ten_gone", CreatedAt: now, UpdatedAt: now}
	if err := st.UpsertAgentInstance(ctx, agent); err != nil {
		t.Fatalf("upsert agent: %v", err)
	}
	token, err := st.CreateTenantToken(ctx, "ten_gone", "t", nil)
	if err != nil {
		t.Fatalf("create token: %v", err)
	}

	if err := st.DeleteTenant(ctx, "ten_gone"); err != nil {
		t.Fatalf("delete tenant: %v", err)
	}

	survivor, err := st.GetAgentInstance(ctx, "a1")
	if err != nil || survivor == nil {
		t.Fatalf("the agent must survive its tenant: %v (%v)", err, survivor)
	}
	if survivor.OwnerTenantID != "" {
		t.Fatalf("expected unassigned ownership, got %q", survivor.OwnerTenantID)
	}
	authenticated, err := st.AuthenticateTenantToken(ctx, token.Secret)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if authenticated != nil {
		t.Fatal("a deleted tenant's token must stop authenticating")
	}
}
