package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// Console session embedding lets a host application (holding a bridge or
// tenant token) hand its users an authenticated browser session for the embedded
// Console without ever exposing the bearer token to the browser:
//
//  1. Host backend: POST /api/v1/console/sessions (Bearer) -> enter_url
//  2. Browser: GET /console/enter?nonce=... consumes the single-use nonce and
//     receives an HttpOnly session cookie, then is redirected to the Console.
//  3. The cookie is accepted by withAuth as an equivalent credential for
//     /api/* and /v1/* on same-origin requests.
const (
	consoleSessionCookie   = "agentmux_console"
	consoleNonceTTL        = 60 * time.Second
	consoleSessionTTL      = 8 * time.Hour
	consoleCSRFHeader      = "X-AgentMux-Console"
	consoleEnterPath       = "/console/enter"
	consoleSessionEndpoint = "/api/v1/console/sessions"
)

// consoleGrant is a nonce or session together with the principal that minted
// it. Binding the principal is what makes an embedded Console inherit its host
// application's scope: a session minted with a tenant token can only ever see
// that tenant's resources, no matter what the browser asks for.
type consoleGrant struct {
	expiry    time.Time
	principal *core.Principal
}

type consoleSessionManager struct {
	mu       sync.Mutex
	nonces   map[string]consoleGrant
	sessions map[string]consoleGrant
	now      func() time.Time
}

func newConsoleSessionManager() *consoleSessionManager {
	return &consoleSessionManager{
		nonces:   map[string]consoleGrant{},
		sessions: map[string]consoleGrant{},
		now:      time.Now,
	}
}

func (m *consoleSessionManager) issueNonce(principal *core.Principal) (string, time.Time) {
	nonce := randomToken()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prune()
	expiry := m.now().Add(consoleNonceTTL)
	m.nonces[nonce] = consoleGrant{expiry: expiry, principal: principal}
	return nonce, expiry
}

// consumeNonce exchanges a valid nonce for a session token. The nonce is
// removed regardless of validity so it can never be replayed.
func (m *consoleSessionManager) consumeNonce(nonce string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prune()
	grant, ok := m.nonces[nonce]
	delete(m.nonces, nonce)
	if !ok || m.now().After(grant.expiry) {
		return "", false
	}
	session := randomToken()
	m.sessions[session] = consoleGrant{
		expiry:    m.now().Add(consoleSessionTTL),
		principal: grant.principal,
	}
	return session, true
}

// sessionPrincipal returns the principal bound to a session cookie, or nil
// when the session is unknown or expired.
func (m *consoleSessionManager) sessionPrincipal(token string) *core.Principal {
	if token == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prune()
	for stored, grant := range m.sessions {
		if subtle.ConstantTimeCompare([]byte(stored), []byte(token)) == 1 {
			if !m.now().Before(grant.expiry) {
				return nil
			}
			if grant.principal == nil {
				return core.AdminPrincipal()
			}
			return grant.principal
		}
	}
	return nil
}

func (m *consoleSessionManager) prune() {
	now := m.now()
	for nonce, grant := range m.nonces {
		if now.After(grant.expiry) {
			delete(m.nonces, nonce)
		}
	}
	for session, grant := range m.sessions {
		if now.After(grant.expiry) {
			delete(m.sessions, session)
		}
	}
}

func randomToken() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		panic("console session: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(buf)
}

// handleConsoleSessionCreate mints a one-time entry URL. It deliberately
// requires a Bearer token (not a console cookie) so a browser session can
// never mint or extend further sessions. The resulting session inherits the
// caller's scope: an admin token yields the full Console, a tenant token
// yields a Console confined to that tenant.
func (s *Server) handleConsoleSessionCreate(w http.ResponseWriter, r *http.Request) {
	token, ok := bearerToken(strings.TrimSpace(r.Header.Get("Authorization")))
	if !ok {
		writeErr(w, http.StatusUnauthorized, "console sessions require a bearer token")
		return
	}
	principal := core.AdminPrincipal()
	if s.cfg.Bridge.Token == "" || token != s.cfg.Bridge.Token {
		if s.st == nil {
			writeErr(w, http.StatusUnauthorized, "console sessions require the bridge or a tenant bearer token")
			return
		}
		tenant, err := s.st.AuthenticateTenantToken(r.Context(), token)
		if err != nil || tenant == nil {
			writeErr(w, http.StatusUnauthorized, "console sessions require the bridge or a tenant bearer token")
			return
		}
		principal = &core.Principal{TenantID: tenant.ID, TenantName: tenant.Name}
	}
	landing, ok := normalizeConsoleLanding(r.URL.Query().Get("landing"))
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid console landing page")
		return
	}
	nonce, expiry := s.consoleSessions.issueNonce(principal)
	enterURL := requestBaseURL(r) + consoleEnterPath + "?nonce=" + url.QueryEscape(nonce)
	if landing != "" {
		enterURL += "&landing=" + url.QueryEscape(landing)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enter_url":           enterURL,
		"expires_at":          expiry.UTC().Format(time.RFC3339),
		"session_ttl_seconds": int(consoleSessionTTL / time.Second),
	})
}

// handleConsoleEnter exchanges a single-use nonce for the HttpOnly session
// cookie and lands the browser on the Console.
func (s *Server) handleConsoleEnter(w http.ResponseWriter, r *http.Request) {
	landing, validLanding := normalizeConsoleLanding(r.URL.Query().Get("landing"))
	if !validLanding {
		http.Error(w, "invalid console landing page", http.StatusBadRequest)
		return
	}
	session, ok := s.consoleSessions.consumeNonce(strings.TrimSpace(r.URL.Query().Get("nonce")))
	if !ok {
		http.Error(w, "invalid or expired console entry link", http.StatusUnauthorized)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     consoleSessionCookie,
		Value:    session,
		Path:     "/",
		MaxAge:   int(consoleSessionTTL / time.Second),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   requestIsTLS(r),
	})
	target := "/"
	if landing != "" {
		target = "/#" + landing
	}
	http.Redirect(w, r, target, http.StatusFound)
}

// normalizeConsoleLanding accepts only an internal hash-route segment. The
// caller can choose a Console panel, but can never turn the one-time entry URL
// into an open redirect.
func normalizeConsoleLanding(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", true
	}
	if len(value) > 48 {
		return "", false
	}
	for i, char := range value {
		if (char >= 'a' && char <= 'z') || (i > 0 && char >= '0' && char <= '9') || (i > 0 && char == '-') {
			continue
		}
		return "", false
	}
	return value, true
}

// consoleSessionPrincipal returns the principal behind a valid console session
// cookie on a same-origin request, or nil. Cross-site requests are rejected:
// SameSite=Lax already withholds the cookie from cross-site subresources, and
// the Sec-Fetch-Site / custom-header requirement blocks the remaining
// top-level navigation and legacy-browser cases.
func (s *Server) consoleSessionPrincipal(r *http.Request) *core.Principal {
	cookie, err := r.Cookie(consoleSessionCookie)
	if err != nil || cookie == nil {
		return nil
	}
	principal := s.consoleSessions.sessionPrincipal(cookie.Value)
	if principal == nil {
		return nil
	}
	if r.Header.Get(consoleCSRFHeader) == "" && r.Header.Get("Sec-Fetch-Site") != "same-origin" {
		return nil
	}
	return principal
}

func requestBaseURL(r *http.Request) string {
	scheme := "http"
	if requestIsTLS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func requestIsTLS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
