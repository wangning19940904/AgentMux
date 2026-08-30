package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// Tenancy management API.
//
// Everything under /api/v1/tenancy/ is admin-only except two endpoints:
//
//	POST /api/v1/tenancy/register — open self-service registration. It creates
//	    an empty tenant namespace and its first token. Resource access is still
//	    assigned separately by the administrator.
//	GET  /api/v1/tenancy/self     — lets a registered caller read back its own
//	    identity, which is how the Console learns whether it is scoped.

func (s *Server) registerTenancyRoutes() {
	s.mux.HandleFunc("GET /api/v1/tenancy/self", s.handleTenancySelf)
	s.mux.HandleFunc("POST /api/v1/tenancy/register", s.handleTenancyRegister)

	s.mux.HandleFunc("GET /api/v1/tenancy/tenants", s.handleTenantsList)
	s.mux.HandleFunc("POST /api/v1/tenancy/tenants", s.handleTenantUpsert)
	s.mux.HandleFunc("DELETE /api/v1/tenancy/tenants", s.handleTenantDelete)

	s.mux.HandleFunc("GET /api/v1/tenancy/tokens", s.handleTenantTokensList)
	s.mux.HandleFunc("POST /api/v1/tenancy/tokens", s.handleTenantTokenCreate)
	s.mux.HandleFunc("DELETE /api/v1/tenancy/tokens", s.handleTenantTokenRevoke)

	s.mux.HandleFunc("GET /api/v1/tenancy/grants", s.handleGrantsList)
	s.mux.HandleFunc("POST /api/v1/tenancy/grants", s.handleGrantUpsert)
	s.mux.HandleFunc("DELETE /api/v1/tenancy/grants", s.handleGrantDelete)

	s.mux.HandleFunc("POST /api/v1/tenancy/ownership", s.handleOwnershipAssign)
}

// requireAdmin rejects tenant principals. The route policy already blocks them
// from admin endpoints; this is the defence in depth that keeps a handler safe
// even if it is ever added to the policy by mistake.
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if requestPrincipal(r).IsTenant() {
		writeErr(w, http.StatusForbidden, "this endpoint requires AgentMux administrator credentials")
		return false
	}
	return true
}

func (s *Server) handleTenancySelf(w http.ResponseWriter, r *http.Request) {
	principal := requestPrincipal(r)
	response := map[string]any{
		"admin":     principal.Admin,
		"tenant_id": principal.TenantID,
		"tenant":    principal.TenantName,
	}
	if principal.IsTenant() {
		tenant, err := s.st.GetTenant(r.Context(), principal.TenantID)
		if err == nil && tenant != nil {
			response["kind"] = tenant.Kind
			response["status"] = tenant.Status
		}
	}
	writeJSON(w, http.StatusOK, response)
}

// handleTenancyRegister creates a tenant and its first token in one call. It
// is open without an existing credential because a fresh tenant starts with
// nothing beyond its own empty namespace and public resources. Duplicate names
// are rejected rather than rotated, so an existing tenant cannot be hijacked.
func (s *Server) handleTenancyRegister(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeJSON[struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
	}](w, r)
	if !ok {
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeErr(w, http.StatusBadRequest, "a tenant name is required")
		return
	}
	existing, err := s.st.GetTenantByName(r.Context(), name)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if existing != nil {
		writeErr(w, http.StatusConflict, "a tenant named \""+name+"\" already exists; ask the administrator to issue a token for it")
		return
	}
	kind := strings.TrimSpace(body.Kind)
	if kind == "" {
		kind = core.TenantKindApp
	}
	now := time.Now().UTC()
	tenant := &core.Tenant{
		ID:        "ten_" + randomToken()[:16],
		Name:      name,
		Kind:      kind,
		Status:    core.TenantStatusActive,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.st.UpsertTenant(r.Context(), tenant); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	token, err := s.st.CreateTenantToken(r.Context(), tenant.ID, "registered", nil)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.log != nil {
		s.log.Info("tenant self-registered", "tenant", tenant.Name, "id", tenant.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tenant": tenant,
		"token":  token.Secret,
		"prefix": token.Prefix,
	})
}

func (s *Server) handleTenantsList(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	tenants, err := s.st.ListTenants(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	counts, err := s.st.CountOwnedResourcesByTenant(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	type tenantListItem struct {
		core.Tenant
		ResourceCount int `json:"resource_count"`
	}
	items := make([]tenantListItem, 0, len(tenants))
	for _, tenant := range tenants {
		items = append(items, tenantListItem{Tenant: tenant, ResourceCount: counts[tenant.ID]})
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleTenantUpsert(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	tenant, ok := decodeJSON[core.Tenant](w, r)
	if !ok {
		return
	}
	tenant.Name = strings.TrimSpace(tenant.Name)
	if tenant.Name == "" {
		writeErr(w, http.StatusBadRequest, "a tenant name is required")
		return
	}
	now := time.Now().UTC()
	if strings.TrimSpace(tenant.ID) == "" {
		tenant.ID = "ten_" + randomToken()[:16]
		tenant.CreatedAt = now
	} else {
		existing, err := s.st.GetTenant(r.Context(), tenant.ID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if existing == nil {
			tenant.CreatedAt = now
		} else {
			tenant.CreatedAt = existing.CreatedAt
		}
	}
	if tenant.Kind == "" {
		tenant.Kind = core.TenantKindApp
	}
	if tenant.Status != core.TenantStatusDisabled {
		tenant.Status = core.TenantStatusActive
	}
	tenant.UpdatedAt = now
	if err := s.st.UpsertTenant(r.Context(), &tenant); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tenant)
}

func (s *Server) handleTenantDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	if err := s.st.DeleteTenant(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleTenantTokensList(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	tenantID := strings.TrimSpace(r.URL.Query().Get("tenant_id"))
	if tenantID == "" {
		writeErr(w, http.StatusBadRequest, "tenant_id is required")
		return
	}
	tokens, err := s.st.ListTenantTokens(r.Context(), tenantID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if tokens == nil {
		tokens = []core.TenantToken{}
	}
	writeJSON(w, http.StatusOK, tokens)
}

// handleTenantTokenCreate returns the only copy of the plaintext secret.
func (s *Server) handleTenantTokenCreate(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	body, ok := decodeJSON[struct {
		TenantID   string `json:"tenant_id"`
		Name       string `json:"name"`
		ExpiresInH int    `json:"expires_in_hours"`
	}](w, r)
	if !ok {
		return
	}
	if strings.TrimSpace(body.TenantID) == "" {
		writeErr(w, http.StatusBadRequest, "tenant_id is required")
		return
	}
	tenant, err := s.st.GetTenant(r.Context(), body.TenantID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if tenant == nil {
		writeErr(w, http.StatusNotFound, "unknown tenant")
		return
	}
	var expiresAt *time.Time
	if body.ExpiresInH > 0 {
		deadline := time.Now().UTC().Add(time.Duration(body.ExpiresInH) * time.Hour)
		expiresAt = &deadline
	}
	token, err := s.st.CreateTenantToken(r.Context(), body.TenantID, body.Name, expiresAt)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, token)
}

func (s *Server) handleTenantTokenRevoke(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	if err := s.st.RevokeTenantToken(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleGrantsList(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	grants, err := s.st.ListResourceGrants(r.Context(), strings.TrimSpace(r.URL.Query().Get("tenant_id")))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if grants == nil {
		grants = []core.ResourceGrant{}
	}
	writeJSON(w, http.StatusOK, grants)
}

func (s *Server) handleGrantUpsert(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	grant, ok := decodeJSON[core.ResourceGrant](w, r)
	if !ok {
		return
	}
	if strings.TrimSpace(grant.TenantID) == "" || strings.TrimSpace(grant.ResourceID) == "" {
		writeErr(w, http.StatusBadRequest, "tenant_id and resource_id are required")
		return
	}
	if !isGrantableResourceType(grant.ResourceType) {
		writeErr(w, http.StatusBadRequest, "resource_type must be one of agent, channel, trigger, provider")
		return
	}
	if err := s.st.UpsertResourceGrant(r.Context(), &grant); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, grant)
}

func (s *Server) handleGrantDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	query := r.URL.Query()
	tenantID := strings.TrimSpace(query.Get("tenant_id"))
	resourceType := strings.TrimSpace(query.Get("resource_type"))
	resourceID := strings.TrimSpace(query.Get("resource_id"))
	if tenantID == "" || resourceType == "" || resourceID == "" {
		writeErr(w, http.StatusBadRequest, "tenant_id, resource_type and resource_id are required")
		return
	}
	if err := s.st.DeleteResourceGrant(r.Context(), tenantID, resourceType, resourceID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleOwnershipAssign adopts a resource into a tenant, or with an empty
// tenant_id returns it to the unassigned pool. This is how resources created
// before tenancy existed are handed to the application that should own them.
func (s *Server) handleOwnershipAssign(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	body, ok := decodeJSON[struct {
		ResourceType string `json:"resource_type"`
		ResourceID   string `json:"resource_id"`
		TenantID     string `json:"tenant_id"`
	}](w, r)
	if !ok {
		return
	}
	resourceID := strings.TrimSpace(body.ResourceID)
	if resourceID == "" {
		writeErr(w, http.StatusBadRequest, "resource_id is required")
		return
	}
	tenantID := strings.TrimSpace(body.TenantID)
	if tenantID != "" {
		tenant, err := s.st.GetTenant(r.Context(), tenantID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if tenant == nil {
			writeErr(w, http.StatusNotFound, "unknown tenant")
			return
		}
	}

	var err error
	switch strings.TrimSpace(body.ResourceType) {
	case core.ResourceTypeAgent:
		err = s.st.SetAgentInstanceOwner(r.Context(), resourceID, tenantID)
	case core.ResourceTypeChannel:
		err = s.st.SetChannelOwner(r.Context(), resourceID, tenantID)
	case core.ResourceTypeTrigger:
		err = s.st.SetTriggerOwner(r.Context(), resourceID, tenantID)
	default:
		writeErr(w, http.StatusBadRequest, "resource_type must be one of agent, channel, trigger")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func isGrantableResourceType(value string) bool {
	switch value {
	case core.ResourceTypeAgent, core.ResourceTypeChannel,
		core.ResourceTypeTrigger, core.ResourceTypeProvider:
		return true
	}
	return false
}
