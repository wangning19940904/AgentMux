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
//   - an administrator: the config.toml bridge token, an administrator
//     account, or the loopback Console of a machine without a bridge token.
//     It sees the whole control plane.
//   - a member: a signed-in account without the administrator role. A member
//     reaches only the routes in memberRoutePolicy, and the handlers behind
//     them narrow every call to what the member owns (their devices and
//     direct messages).
//
// Anything not listed in memberRoutePolicy is closed to members, so a new
// endpoint is administrator-only the moment it is added.

type principalContextKey struct{}

// withPrincipal attaches the authenticated principal to a request context.
func withPrincipal(ctx context.Context, principal *core.Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, principal)
}

// principalFrom returns the principal behind a request. Callers without one
// (the CLI, internal engine calls, tests) are treated as the administrator.
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

// memberRoutePolicy lists the exact endpoints a member account may reach. The
// value is the set of allowed methods; an empty set means every method.
var memberRoutePolicy = map[string][]string{
	// Discovery.
	"/api/v1/status":       {http.MethodGet},
	"/api/v1/capabilities": {http.MethodGet},
	// Their own account.
	"/api/v1/auth/password": {http.MethodPost},
	// The devices they paired. Device handlers narrow every call to them.
	"/api/v1/devices":               {http.MethodGet, http.MethodPost, http.MethodDelete},
	"/api/v1/devices/pairing-codes": {http.MethodPost},
	"/api/v1/devices/reports":       {http.MethodGet},
	// Direct messages: a member's own conversations, and the contacts of
	// what they own. The mailbox narrows every call to that.
	"/api/v1/mailbox":                 {http.MethodGet},
	"/api/v1/mailbox/messages":        {http.MethodGet, http.MethodPost},
	"/api/v1/mailbox/directory":       {http.MethodGet},
	"/api/v1/mailbox/contacts":        {http.MethodGet, http.MethodPost, http.MethodDelete},
	"/api/v1/mailbox/contacts/accept": {http.MethodPost},
}

// memberRouteAllowed reports whether a member may call one route.
func memberRouteAllowed(method, path string) bool {
	methods, ok := memberRoutePolicy[path]
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

// requireAdmin writes 403 and returns false unless the caller administers
// this AgentMux.
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if !requestPrincipal(r).Admin {
		writeErr(w, http.StatusForbidden, "this endpoint requires AgentMux administrator credentials")
		return false
	}
	return true
}

// denyMemberRoute reports the route as unavailable to a member. It uses 403
// rather than 404 so an operator can tell a permission decision from a typo.
func denyMemberRoute(w http.ResponseWriter, r *http.Request, principal *core.Principal) {
	if strings.HasPrefix(r.URL.Path, "/v1/") {
		writeOpenAIError(w, http.StatusForbidden, "this endpoint is reserved for AgentMux administrators",
			"permission_error", nil, "insufficient_scope")
		return
	}
	login := ""
	if principal.Account != nil {
		login = principal.Account.Login
	}
	writeErr(w, http.StatusForbidden,
		"endpoint not available to account \""+login+"\"; it is reserved for AgentMux administrators")
}
