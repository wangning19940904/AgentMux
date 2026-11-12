package server

import (
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
)

// Accounts and sign-in.
//
// People sign in to the Console with a login name and password and get the
// same HttpOnly Console cookie a one-time entry link gives, carrying a
// database-backed session instead of an in-memory one. The account behind a
// session is read on every request, so disabling an account or changing its
// role takes effect at once.
//
// With no accounts, nothing here changes how AgentMux behaves. The bridge
// token stays the administrator and tenant tokens keep their scope whether or
// not accounts exist.
//
//	GET  /api/v1/auth/session   — who is calling and whether sign-in is needed (open)
//	POST /api/v1/auth/sign-in   — password sign-in (open, rate-limited)
//	POST /api/v1/auth/sign-out  — end this browser's session (open)
//	POST /api/v1/auth/setup     — create the first administrator (administrator, once)
//	POST /api/v1/auth/password  — change one's own password (signed-in account)
//	GET  /api/v1/accounts       — list accounts (administrator)
//	POST /api/v1/accounts       — create or update an account (administrator)
//	POST /api/v1/accounts/password — reset an account's password (administrator)

const (
	accountSessionTTL = 7 * 24 * time.Hour
	authPathPrefix    = "/api/v1/auth/"
)

// authOpenPaths answer without a credential: a browser has to be able to ask
// whether it must sign in, and then sign in.
var authOpenPaths = map[string]bool{
	"/api/v1/auth/session":  true,
	"/api/v1/auth/sign-in":  true,
	"/api/v1/auth/sign-out": true,
}

func (s *Server) registerAccountRoutes() {
	s.mux.HandleFunc("GET /api/v1/auth/session", s.handleAuthSession)
	s.mux.HandleFunc("POST /api/v1/auth/sign-in", s.handleSignIn)
	s.mux.HandleFunc("POST /api/v1/auth/sign-out", s.handleSignOut)
	s.mux.HandleFunc("POST /api/v1/auth/setup", s.handleAccountSetup)
	s.mux.HandleFunc("POST /api/v1/auth/password", s.handleOwnPassword)
	s.mux.HandleFunc("GET /api/v1/accounts", s.handleAccountsList)
	s.mux.HandleFunc("POST /api/v1/accounts", s.handleAccountSave)
	s.mux.HandleFunc("POST /api/v1/accounts/password", s.handleAccountPasswordReset)
}

type authSessionResponse struct {
	// AccountsEnabled is true once any account exists.
	AccountsEnabled bool `json:"accounts_enabled"`
	// SetupAvailable is true when there are no accounts and the caller is an
	// administrator who may create the first one.
	SetupAvailable bool `json:"setup_available"`
	// SignInRequired is true when accounts exist and the caller has no
	// credential of its own: no account session, bridge token, tenant token or
	// entry-link session.
	SignInRequired bool `json:"sign_in_required"`
	Authenticated  bool `json:"authenticated"`
	Admin          bool `json:"admin"`
	// Account is the signed-in person, if there is one.
	Account  *core.AccountIdentity `json:"account,omitempty"`
	TenantID string                `json:"tenant_id,omitempty"`
	Tenant   string                `json:"tenant,omitempty"`
	// SignInMethods lists the ways a person can sign in here.
	SignInMethods []string `json:"sign_in_methods"`
}

// callerPrincipal resolves the request's own credential the way withAuth
// does, for the open auth endpoints withAuth does not resolve. anonymous is
// true for the loopback administrator a bridge-less machine grants without
// any credential.
func (s *Server) callerPrincipal(r *http.Request) (principal *core.Principal, anonymous bool) {
	if principal := s.resolvePrincipal(r); principal != nil {
		return principal, false
	}
	if !hasExplicitCredential(r) && !s.cfg.Bridge.Enabled {
		return core.AdminPrincipal(), true
	}
	return nil, false
}

func (s *Server) handleAuthSession(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	response := authSessionResponse{SignInMethods: []string{core.SignInMethodPassword}}
	if s.st != nil {
		count, err := s.st.CountAccounts(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		response.AccountsEnabled = count > 0
	}
	principal, anonymous := s.callerPrincipal(r)
	if principal != nil {
		response.Authenticated = true
		response.Admin = principal.Admin
		response.Account = principal.Account
		response.TenantID, response.Tenant = principal.TenantID, principal.TenantName
	}
	response.SetupAvailable = s.st != nil && !response.AccountsEnabled && principal != nil && principal.Admin
	response.SignInRequired = response.AccountsEnabled && (principal == nil || anonymous)
	writeJSON(w, http.StatusOK, response)
}

// consoleOriginRequest reports whether a request comes from the Console's
// own pages. Sign-in, sign-out and first-run setup set or clear the session
// cookie, so they get the cross-site protection cookie use already has.
func consoleOriginRequest(r *http.Request) bool {
	return r.Header.Get(consoleCSRFHeader) != "" || r.Header.Get("Sec-Fetch-Site") == "same-origin"
}

func clientAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) handleSignIn(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !consoleOriginRequest(r) {
		writeErr(w, http.StatusForbidden, "sign in from the AgentMux Console")
		return
	}
	if s.st == nil {
		serviceUnavailable(w, "accounts")
		return
	}
	var req struct {
		Login    string `json:"login"`
		Password string `json:"password"`
	}
	if !decodeJSONInto(w, r, &req) {
		return
	}
	login, addr := core.NormalizeLogin(req.Login), clientAddr(r)
	if wait := s.signIns.retryAfter(login, addr); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeErr(w, http.StatusTooManyRequests, "too many failed sign-ins; try again later")
		return
	}
	var account *core.Account
	if login != "" && len(req.Password) <= core.MaxPasswordLength {
		var err error
		account, err = s.st.VerifyAccountPassword(r.Context(), login, req.Password)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if account == nil {
		s.signIns.failure(login, addr)
		if s.log != nil {
			s.log.Warn("sign-in failed", "login", login, "addr", addr)
		}
		writeErr(w, http.StatusUnauthorized, "wrong login name or password")
		return
	}
	s.signIns.success(login, addr)
	if !s.startAccountSession(w, r, account, core.SignInMethodPassword) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": account.Identity()})
}

// startAccountSession signs a browser in. Any session the browser already
// held ends first, so a cookie planted before sign-in is never promoted.
func (s *Server) startAccountSession(w http.ResponseWriter, r *http.Request, account *core.Account, method string) bool {
	s.endBrowserSession(r)
	token, _, err := s.st.CreateAccountSession(r.Context(), account.ID, method, r.UserAgent(), clientAddr(r), accountSessionTTL)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return false
	}
	http.SetCookie(w, &http.Cookie{
		Name:     consoleSessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(accountSessionTTL / time.Second),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   requestIsTLS(r),
	})
	if s.log != nil {
		s.log.Info("account signed in", "login", account.Login, "method", method, "addr", clientAddr(r))
	}
	return true
}

// endBrowserSession ends whichever session the request's cookie names.
func (s *Server) endBrowserSession(r *http.Request) {
	cookie, err := r.Cookie(consoleSessionCookie)
	if err != nil || cookie.Value == "" {
		return
	}
	if strings.HasPrefix(cookie.Value, store.AccountSessionTokenPrefix) {
		if s.st != nil {
			_ = s.st.DeleteAccountSession(r.Context(), cookie.Value)
		}
		return
	}
	s.consoleSessions.revoke(cookie.Value)
}

func (s *Server) handleSignOut(w http.ResponseWriter, r *http.Request) {
	if !consoleOriginRequest(r) {
		writeErr(w, http.StatusForbidden, "sign out from the AgentMux Console")
		return
	}
	s.endBrowserSession(r)
	http.SetCookie(w, &http.Cookie{
		Name: consoleSessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: requestIsTLS(r),
	})
	writeOK(w)
}

type accountRequest struct {
	ID          string  `json:"id"`
	Login       string  `json:"login"`
	DisplayName *string `json:"display_name"`
	Role        *string `json:"role"`
	Status      *string `json:"status"`
	Password    string  `json:"password"`
}

// handleAccountSetup creates the first administrator. Only an administrator
// may: the bridge token, or on a machine without a bridge, the local Console.
// A browser that does it is signed in as that administrator.
func (s *Server) handleAccountSetup(w http.ResponseWriter, r *http.Request) {
	if s.st == nil {
		serviceUnavailable(w, "accounts")
		return
	}
	principal := requestPrincipal(r)
	if !principal.Admin {
		writeErr(w, http.StatusForbidden, "only the AgentMux administrator can create the first account")
		return
	}
	var req accountRequest
	if !decodeJSONInto(w, r, &req) {
		return
	}
	login := core.NormalizeLogin(req.Login)
	if err := validateNewAccount(login, req.Password); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	account, err := s.st.CreateFirstAdmin(r.Context(), store.NewAccount{
		Login: login, DisplayName: derefString(req.DisplayName), Password: req.Password,
	})
	if err != nil {
		writeAccountError(w, err)
		return
	}
	if s.log != nil {
		s.log.Info("first administrator account created", "login", account.Login)
	}
	if consoleOriginRequest(r) && strings.TrimSpace(r.Header.Get("Authorization")) == "" {
		if !s.startAccountSession(w, r, account, core.SignInMethodPassword) {
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": account})
}

func (s *Server) handleOwnPassword(w http.ResponseWriter, r *http.Request) {
	principal := requestPrincipal(r)
	if principal.Account == nil || s.st == nil {
		writeErr(w, http.StatusForbidden, "sign in to change your password")
		return
	}
	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if !decodeJSONInto(w, r, &req) {
		return
	}
	login, addr := principal.Account.Login, clientAddr(r)
	if wait := s.signIns.retryAfter(login, addr); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeErr(w, http.StatusTooManyRequests, "too many failed attempts; try again later")
		return
	}
	if err := core.ValidatePassword(req.NewPassword); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	account, err := s.st.VerifyAccountPassword(r.Context(), login, req.CurrentPassword)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if account == nil || account.ID != principal.Account.ID {
		s.signIns.failure(login, addr)
		writeErr(w, http.StatusForbidden, "the current password is wrong")
		return
	}
	keep := ""
	if cookie, err := r.Cookie(consoleSessionCookie); err == nil {
		keep = cookie.Value
	}
	if err := s.st.SetAccountPassword(r.Context(), account.ID, req.NewPassword, keep); err != nil {
		writeAccountError(w, err)
		return
	}
	writeOK(w)
}

func (s *Server) handleAccountsList(w http.ResponseWriter, r *http.Request) {
	if !s.requireAccountAdmin(w, r) {
		return
	}
	accounts, err := s.st.ListAccounts(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if accounts == nil {
		accounts = []core.Account{}
	}
	writeJSON(w, http.StatusOK, accounts)
}

// handleAccountSave creates an account when no id is given and otherwise
// changes its display name, role or status. A new account without a
// password gets a generated one, returned once.
func (s *Server) handleAccountSave(w http.ResponseWriter, r *http.Request) {
	if !s.requireAccountAdmin(w, r) {
		return
	}
	var req accountRequest
	if !decodeJSONInto(w, r, &req) {
		return
	}
	if id := strings.TrimSpace(req.ID); id != "" {
		account, err := s.st.UpdateAccount(r.Context(), id, store.AccountUpdate{
			DisplayName: req.DisplayName, Role: req.Role, Status: req.Status,
		})
		if err != nil {
			writeAccountError(w, err)
			return
		}
		if s.log != nil {
			s.log.Info("account updated", "login", account.Login, "role", account.Role, "status", account.Status)
		}
		writeJSON(w, http.StatusOK, map[string]any{"account": account})
		return
	}
	count, err := s.st.CountAccounts(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if count == 0 {
		writeErr(w, http.StatusConflict, "create the first administrator first")
		return
	}
	login := core.NormalizeLogin(req.Login)
	generated := ""
	if req.Password == "" {
		generated = generateAccountPassword()
		req.Password = generated
	}
	if err := validateNewAccount(login, req.Password); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	account, err := s.st.CreateAccount(r.Context(), store.NewAccount{
		Login: login, DisplayName: derefString(req.DisplayName), Role: derefString(req.Role), Password: req.Password,
	})
	if err != nil {
		writeAccountError(w, err)
		return
	}
	if s.log != nil {
		s.log.Info("account created", "login", account.Login, "role", account.Role)
	}
	response := map[string]any{"account": account}
	if generated != "" {
		response["password"] = generated
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, response)
}

// handleAccountPasswordReset sets a new password and ends every session of
// the account. Without a password in the request one is generated and
// returned once.
func (s *Server) handleAccountPasswordReset(w http.ResponseWriter, r *http.Request) {
	if !s.requireAccountAdmin(w, r) {
		return
	}
	var req struct {
		ID       string `json:"id"`
		Password string `json:"password"`
	}
	if !decodeJSONInto(w, r, &req) {
		return
	}
	generated := ""
	if req.Password == "" {
		generated = generateAccountPassword()
		req.Password = generated
	}
	if err := core.ValidatePassword(req.Password); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.st.SetAccountPassword(r.Context(), strings.TrimSpace(req.ID), req.Password, ""); err != nil {
		writeAccountError(w, err)
		return
	}
	response := map[string]any{"ok": true}
	if generated != "" {
		response["password"] = generated
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, response)
}

// requireAccountAdmin admits administrators only, and needs the store.
func (s *Server) requireAccountAdmin(w http.ResponseWriter, r *http.Request) bool {
	if !s.requireAdmin(w, r) {
		return false
	}
	if s.st == nil {
		serviceUnavailable(w, "accounts")
		return false
	}
	return true
}

func validateNewAccount(login, password string) error {
	if err := core.ValidateLogin(login); err != nil {
		return err
	}
	return core.ValidatePassword(password)
}

func writeAccountError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrAccountNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, store.ErrLoginTaken), errors.Is(err, store.ErrAccountsExist), errors.Is(err, store.ErrLastAdmin):
		writeErr(w, http.StatusConflict, err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

// generateAccountPassword returns a 20-character password for an
// administrator to hand over; the person should change it after signing in.
func generateAccountPassword() string {
	token := randomToken()
	return token[:5] + "-" + token[5:10] + "-" + token[10:15] + "-" + token[15:20]
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
