package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/wangning19940904/AgentMux/core"
)

// Principal resolution and route policy.
//
// Two kinds of caller reach the bridge API:
//
//   - the admin, holding the config.toml bridge token (or a Console session
//     minted by it). It sees the whole control plane.
//   - a tenant, holding a token issued from the tenants table. It is confined
//     to the endpoints AgentMux publishes to third parties and, within those,
//     to the resources it owns or was granted.
//
// The tenant route policy below is the enforcement of the stability tiers
// already documented in contract/CONTRACT.md: stable and beta endpoints are
// the third-party surface, everything else is "Console 专用管理面" that
// third parties must not depend on. Anything not listed is denied, so a new
// internal endpoint is closed to tenants the moment it is added.

type principalContextKey struct{}

// withPrincipal attaches the authenticated principal to a request context.
func withPrincipal(ctx context.Context, principal *core.Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, principal)
}

// principalFrom returns the principal behind a request. Callers that predate
// tenancy (the CLI, internal engine calls, tests) carry no principal; they are
// treated as the admin so existing behaviour is unchanged.
func principalFrom(ctx context.Context) *core.Principal {
	if principal, ok := ctx.Value(principalContextKey{}).(*core.Principal); ok && principal != nil {
		return principal
	}
	return core.AdminPrincipal()
}

// requestPrincipal is the request-scoped shorthand used by handlers.
func requestPrincipal(r *http.Request) *core.Principal {
	return principalFrom(r.Context())
}

// tenantRoutePolicy lists the exact endpoints a tenant token may reach. The
// value is the set of allowed methods; an empty set means every method.
var tenantRoutePolicy = map[string][]string{
	// Discovery and handshake.
	"/api/v1/status":       {http.MethodGet},
	"/api/v1/capabilities": {http.MethodGet},
	"/api/v1/platforms":    {http.MethodGet},

	// Agent instances (stable).
	"/api/v1/agents":                     {http.MethodGet},
	"/api/v1/agent-instances":            {http.MethodGet, http.MethodPost, http.MethodDelete},
	"/api/v1/agent-instances/initialize": {http.MethodPost},

	// Channels (stable).
	"/api/v1/channels":          {http.MethodGet, http.MethodPost, http.MethodDelete},
	"/api/v1/channels/validate": {http.MethodPost},
	"/api/v1/channels/restart":  {http.MethodPost},

	// Channel onboarding. These flows only exchange the current user's Feishu
	// device/web session for credentials and then save through tenant-scoped
	// channel CRUD. They do not grant access to another tenant's resources.
	"/api/v1/setup/feishu/begin":                {http.MethodPost},
	"/api/v1/setup/feishu/poll":                 {http.MethodPost},
	"/api/v1/setup/feishu/automation/begin":     {http.MethodPost},
	"/api/v1/setup/feishu/automation/poll":      {http.MethodPost},
	"/api/v1/setup/feishu/automation/configure": {http.MethodPost},

	// Triggers and orchestrations (beta).
	"/api/v1/triggers":              {http.MethodGet, http.MethodPost, http.MethodDelete},
	"/api/v1/triggers/run":          {http.MethodPost},
	"/api/v1/orchestrations":        {http.MethodGet, http.MethodPost},
	"/api/v1/orchestrations/cancel": {http.MethodPost},

	// Execution and reporting (stable/beta).
	"/api/v1/invocations":        {http.MethodPost},
	"/api/v1/invocations/stream": {http.MethodPost},
	"/api/v1/send":               {http.MethodPost},
	"/api/v1/usage":              {http.MethodGet},

	// Console embedding: a tenant mints a session scoped to itself.
	"/api/v1/console/sessions": {http.MethodPost},

	// Tenant self-service: read back its own identity, and register a new
	// tenant without an admin approval step (names are unique, so this can
	// only mint fresh identities, never take over an existing one).
	"/api/v1/tenancy/self": {http.MethodGet},

	// Read-only catalogues needed by the tenant Console. Provider results and
	// active routes are narrowed to providers explicitly granted by the admin;
	// the other catalogues describe host capabilities rather than tenant data.
	"/api/v1/providers":        {http.MethodGet},
	"/api/v1/providers/active": {http.MethodGet},
	"/api/v1/tools":            {http.MethodGet},
	"/api/v1/frameworks":       {http.MethodGet},
}

// tenantRouteAllowed reports whether a tenant principal may call one route.
func tenantRouteAllowed(method, path string) bool {
	// The OpenAI-compatible surface is published to third parties as a whole.
	if strings.HasPrefix(path, "/v1/") {
		return true
	}
	methods, ok := tenantRoutePolicy[path]
	if !ok {
		return false
	}
	if len(methods) == 0 {
		return true
	}
	for _, allowed := range methods {
		if allowed == method {
			return true
		}
	}
	return false
}

// resolvePrincipal maps the credentials on a request to a principal. It
// returns nil when the request carries no valid credential.
func (s *Server) resolvePrincipal(r *http.Request) *core.Principal {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if token, ok := bearerToken(header); ok {
		if s.cfg.Bridge.Token != "" && token == s.cfg.Bridge.Token {
			return core.AdminPrincipal()
		}
		if s.st != nil {
			tenant, err := s.st.AuthenticateTenantToken(r.Context(), token)
			if err != nil {
				if s.log != nil {
					s.log.Warn("tenant token lookup failed", "err", err)
				}
				return nil
			}
			if tenant != nil {
				return &core.Principal{TenantID: tenant.ID, TenantName: tenant.Name}
			}
		}
		return nil
	}
	return s.consoleSessionPrincipal(r)
}

func bearerToken(header string) (string, bool) {
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	return strings.TrimSpace(header[len(prefix):]), true
}

// denyTenantRoute reports the route as unavailable to the caller. It uses 403
// rather than 404 so an operator can tell a scoping decision from a typo.
func denyTenantRoute(w http.ResponseWriter, r *http.Request, principal *core.Principal) {
	if strings.HasPrefix(r.URL.Path, "/v1/") {
		writeOpenAIError(w, http.StatusForbidden, "this endpoint is not available to application credentials",
			"permission_error", nil, "insufficient_scope")
		return
	}
	writeErr(w, http.StatusForbidden,
		"endpoint not available to tenant \""+principal.TenantName+"\"; it is reserved for the AgentMux administrator")
}
