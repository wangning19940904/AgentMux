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
// Sign-in asks for the login name first (identify), as Persona does: a
// registered name goes on to the password, an unknown one to registration,
// which waits for an administrator's approval. One account works on every hub
// of a group: the primary hub is the authority for accounts and a standby
// sends every change there (account_authority.go).
//
// With no accounts, nothing here changes how AgentMux behaves. The bridge
// token stays the administrator whether or not accounts exist.
//
//	GET  /api/v1/auth/session   — who is calling and whether sign-in is needed (open)
//	POST /api/v1/auth/identify  — is this login name registered? (open, rate-limited)
//	POST /api/v1/auth/register  — ask for an account; an administrator approves it (open, rate-limited)
//	POST /api/v1/auth/sign-in   — password sign-in (open, rate-limited)
//	POST /api/v1/auth/sign-out  — end this browser's session (open)
//	POST /api/v1/auth/setup     — create the first administrator (administrator, once)
//	POST /api/v1/auth/password  — change one's own password (signed-in account)
//	GET  /api/v1/accounts       — list accounts (administrator)
//	POST /api/v1/accounts       — create or update an account; status "active" approves a registration (administrator)
//	POST /api/v1/accounts/password — reset an account's password (administrator)
//	POST /api/v1/accounts/reject   — turn a registration down (administrator)

const (
	accountSessionTTL = 7 * 24 * time.Hour
	authPathPrefix    = "/api/v1/auth/"
)

// authOpenPaths answer without a credential: a browser has to be able to ask
// whether it must sign in, and then sign in or register.
var authOpenPaths = map[string]bool{
	"/api/v1/auth/session":  true,
	"/api/v1/auth/identify": true,
	"/api/v1/auth/register": true,
	"/api/v1/auth/sign-in":  true,
	"/api/v1/auth/sign-out": true,
	// A worker's helper calls carry their Ticket's token instead
	// (superagent_dispatch.go).
	"/api/v1/super-agents/dispatch/start": true,
	"/api/v1/super-agents/dispatch/read":  true,
}

func (s *Server) registerAccountRoutes() {
	s.mux.HandleFunc("GET /api/v1/auth/session", s.handleAuthSession)
	s.mux.HandleFunc("POST /api/v1/auth/identify", s.handleIdentify)
	s.mux.HandleFunc("POST /api/v1/auth/register", s.handleRegister)
	s.mux.HandleFunc("POST /api/v1/auth/sign-in", s.handleSignIn)
	s.mux.HandleFunc("POST /api/v1/auth/sign-out", s.handleSignOut)
	s.mux.HandleFunc("POST /api/v1/auth/setup", s.handleAccountSetup)
	s.mux.HandleFunc("POST /api/v1/auth/password", s.handleOwnPassword)
	s.mux.HandleFunc("GET /api/v1/accounts", s.handleAccountsList)
	s.mux.HandleFunc("POST /api/v1/accounts", s.handleAccountSave)
	s.mux.HandleFunc("POST /api/v1/accounts/password", s.handleAccountPasswordReset)
	s.mux.HandleFunc("POST /api/v1/accounts/reject", s.handleAccountReject)
	s.registerDeviceAccountRoutes()
}

// Where accounts are kept, as /api/v1/auth/session reports it.
const (
	accountsAuthorityLocal   = "local"
	accountsAuthorityPrimary = "primary"
)

type authSessionResponse struct {
	// AccountsEnabled is true once any account exists.
	AccountsEnabled bool `json:"accounts_enabled"`
	// SetupAvailable is true when there are no accounts and the caller is an
	// administrator who may create the first one.
	SetupAvailable bool `json:"setup_available"`
	// SignInRequired is true when accounts exist and the caller has no
	// credential of its own: no account session, bridge token or entry-link
	// session.
	SignInRequired bool `json:"sign_in_required"`
	Authenticated  bool `json:"authenticated"`
	Admin          bool `json:"admin"`
	// Account is the signed-in person, if there is one.
	Account *core.AccountIdentity `json:"account,omitempty"`
	// SignInMethods lists the ways a person can sign in here.
	SignInMethods []string `json:"sign_in_methods"`
	// RegistrationOpen is true when a person without an account can ask
	// for one; an administrator approves it.
	RegistrationOpen bool `json:"registration_open"`
	// AccountsAuthority is "primary" when this hub follows a primary hub
	// that keeps the accounts, "local" otherwise.
	AccountsAuthority string `json:"accounts_authority"`
	// PendingRegistrations counts registrations waiting for approval, for
	// administrators.
	PendingRegistrations int `json:"pending_registrations,omitempty"`
	// Device is this machine's own sign-in, for the machine itself (its
	// desktop app): it shows sign-in until the machine is signed in.
	Device *deviceAccountView `json:"device,omitempty"`
}

// callerPrincipal resolves the request's own credential the way withAuth
// does, for the open auth endpoints withAuth does not resolve. anonymous is
// true for the loopback administrator a bridge-less machine grants without
// any credential.
func (s *Server) callerPrincipal(r *http.Request) (principal *core.Principal, anonymous bool) {
	if principal := s.resolvePrincipal(r); principal != nil {
		return principal, false
	}
	if !hasExplicitCredential(r) && !s.cfg.Bridge.Enabled && !isHubClient(r) {
		return core.AdminPrincipal(), true
	}
	return nil, false
}

func (s *Server) handleAuthSession(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	response := authSessionResponse{SignInMethods: []string{core.SignInMethodPassword}, AccountsAuthority: accountsAuthorityLocal}
	var accounts []core.Account
	if s.st != nil {
		var err error
		accounts, err = s.st.ListAccounts(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		response.AccountsEnabled = len(accounts) > 0
		if follows, _ := s.followsPrimaryHub(r.Context()); follows {
			response.AccountsAuthority = accountsAuthorityPrimary
		}
	}
	principal, anonymous := s.callerPrincipal(r)
	if principal != nil {
		response.Authenticated = true
		response.Admin = principal.Admin
		response.Account = principal.Account
	}
	response.SetupAvailable = s.st != nil && !response.AccountsEnabled && principal != nil && principal.Admin
	response.SignInRequired = response.AccountsEnabled && (principal == nil || anonymous)
	response.RegistrationOpen = response.AccountsEnabled
	if s.st != nil && s.machineController(r) {
		view := s.deviceAccountView(r.Context())
		response.Device = &view
	}
	if response.Admin {
		for _, account := range accounts {
			if account.Status == core.AccountStatusPending {
				response.PendingRegistrations++
			}
		}
	}
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

// limitedAuthRequest applies the sign-in limiter to an open auth request and
// answers 429 when it must wait.
func (s *Server) limitedAuthRequest(w http.ResponseWriter, login, addr string) bool {
	if wait := s.signIns.retryAfter(login, addr); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeErr(w, http.StatusTooManyRequests, "too many attempts; try again later")
		return true
	}
	return false
}

type identifyResponse struct {
	Login string `json:"login"`
	// State is "registered", "pending" (registered, waiting for approval)
	// or "unregistered".
	State string `json:"state"`
	// SetupRequired is true when there are no accounts at all: the first
	// administrator has to be created where AgentMux runs.
	SetupRequired    bool `json:"setup_required"`
	RegistrationOpen bool `json:"registration_open"`
}

// handleIdentify answers the first step of sign-in: whether the name the
// person typed is registered. Looking up unknown names counts against the
// caller's address like a failed sign-in, which bounds guessing who has an
// account.
func (s *Server) handleIdentify(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !consoleOriginRequest(r) {
		writeErr(w, http.StatusForbidden, "sign in from the AgentMux Console")
		return
	}
	var req struct {
		Login string `json:"login"`
	}
	if !decodeJSONInto(w, r, &req) {
		return
	}
	login, addr := core.NormalizeLogin(req.Login), clientAddr(r)
	if err := core.ValidateLogin(login); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.limitedAuthRequest(w, login, addr) {
		return
	}
	result, err := s.runAccountOp(r.Context(), accountOp{Op: accountOpIdentify, Login: login})
	if err != nil {
		writeAccountOpError(w, err)
		return
	}
	if result.State == store.LoginUnregistered {
		s.signIns.failure("identify:"+login, addr)
	}
	writeJSON(w, http.StatusOK, identifyResponse{
		Login: login, State: result.State, SetupRequired: !result.AccountsExist, RegistrationOpen: result.AccountsExist,
	})
}

// handleRegister asks for an account. It is created waiting for approval,
// on the primary hub when this hub follows one, and cannot sign in until an
// administrator approves it.
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !consoleOriginRequest(r) {
		writeErr(w, http.StatusForbidden, "register from the AgentMux Console")
		return
	}
	var req accountRequest
	if !decodeJSONInto(w, r, &req) {
		return
	}
	login, addr := core.NormalizeLogin(req.Login), clientAddr(r)
	if s.limitedAuthRequest(w, login, addr) {
		return
	}
	if err := validateNewAccount(login, req.Password); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Every registration counts, so one address cannot flood the
	// administrators' approval list.
	s.signIns.failure("register:"+login, addr)
	result, err := s.runAccountOp(r.Context(), accountOp{
		Op: accountOpRegister, Login: login, DisplayName: req.DisplayName, Password: req.Password,
	})
	if err != nil {
		writeAccountOpError(w, err)
		return
	}
	if s.log != nil {
		s.log.Info("account registration waiting for approval", "login", login, "addr", addr)
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": result.Account.Identity(), "status": core.AccountStatusPending})
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
	if login == "" || len(req.Password) > core.MaxPasswordLength {
		s.signIns.failure(login, addr)
		writeAccountOpError(w, accountFailure(http.StatusUnauthorized, accountCodeWrongPassword, "wrong login name or password"))
		return
	}
	result, err := s.runAccountOp(r.Context(), accountOp{
		Op: accountOpSignIn, Login: login, Password: req.Password, UserAgent: r.UserAgent(), RemoteAddr: addr,
	})
	if err != nil {
		var opErr *accountOpError
		if errors.As(err, &opErr) && opErr.Code == accountCodeWrongPassword {
			s.signIns.failure(login, addr)
			if s.log != nil {
				s.log.Warn("sign-in failed", "login", login, "addr", addr)
			}
		}
		writeAccountOpError(w, err)
		return
	}
	s.signIns.success(login, addr)
	// The session the browser held before ends, so a cookie planted before
	// sign-in is never promoted.
	s.endBrowserSession(r)
	s.setAccountCookie(w, r, result.Account, result.Token)
	writeJSON(w, http.StatusOK, map[string]any{"account": result.Account.Identity()})
}

// setAccountCookie hands the browser a session the authority created.
func (s *Server) setAccountCookie(w http.ResponseWriter, r *http.Request, account *core.Account, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     consoleSessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(accountSessionTTL / time.Second),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   requestIsTLS(r),
	})
	if s.log != nil && account != nil {
		s.log.Info("account signed in", "login", account.Login, "addr", clientAddr(r))
	}
}

// endBrowserSession ends whichever session the request's cookie names. An
// account session ends at the authority, so it ends on every hub.
func (s *Server) endBrowserSession(r *http.Request) {
	cookie, err := r.Cookie(consoleSessionCookie)
	if err != nil || cookie.Value == "" {
		return
	}
	if strings.HasPrefix(cookie.Value, store.AccountSessionTokenPrefix) {
		if s.st != nil {
			if _, err := s.runAccountOp(r.Context(), accountOp{Op: accountOpSignOut, SessionToken: cookie.Value}); err != nil && s.log != nil {
				s.log.Warn("could not end an account session", "err", err)
			}
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
// A browser that does it is signed in as that administrator. When accounts
// already exist at the authority (made on another hub of the group) and the
// login and password match one, that account is signed in instead.
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
	login, addr := core.NormalizeLogin(req.Login), clientAddr(r)
	if err := validateNewAccount(login, req.Password); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.limitedAuthRequest(w, login, addr) {
		return
	}
	browser := consoleOriginRequest(r) && strings.TrimSpace(r.Header.Get("Authorization")) == ""
	result, err := s.runAccountOp(r.Context(), accountOp{
		Op: accountOpSetup, Login: login, DisplayName: req.DisplayName, Password: req.Password,
		StartSession: browser, UserAgent: r.UserAgent(), RemoteAddr: addr,
	})
	if err != nil {
		var opErr *accountOpError
		if errors.As(err, &opErr) && opErr.Code == accountCodeAccountsExist {
			s.signIns.failure(login, addr)
		}
		writeAccountOpError(w, err)
		return
	}
	if s.log != nil && !result.Existing {
		s.log.Info("first administrator account created", "login", result.Account.Login)
	}
	if result.Token != "" {
		s.endBrowserSession(r)
		s.setAccountCookie(w, r, result.Account, result.Token)
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": result.Account, "existing": result.Existing})
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
	keep := ""
	if cookie, err := r.Cookie(consoleSessionCookie); err == nil {
		keep = cookie.Value
	}
	op := accountOp{
		Op: accountOpOwnPassword, Login: login, AccountID: principal.Account.ID,
		CurrentPassword: req.CurrentPassword, Password: req.NewPassword,
	}
	run := s.runAccountOp
	if desktopRequest(r) && s.machineController(r) {
		// The desktop changes the password of the account its machine is
		// signed in to, at that account's authority, and the machine stays
		// signed in.
		if record, _ := s.loadDeviceAccount(); record != nil && record.Account.ID == principal.Account.ID {
			keep, run = record.Token, s.runDeviceAccountOp
		}
	}
	op.SessionToken = keep
	_, err := run(r.Context(), op)
	if err != nil {
		var opErr *accountOpError
		if errors.As(err, &opErr) && opErr.Code == accountCodeWrongPassword {
			s.signIns.failure(login, addr)
		}
		writeAccountOpError(w, err)
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
// changes its display name, role or status; setting a pending registration
// active approves it. A new account without a password gets a generated
// one, returned once.
func (s *Server) handleAccountSave(w http.ResponseWriter, r *http.Request) {
	if !s.requireAccountAdmin(w, r) {
		return
	}
	var req accountRequest
	if !decodeJSONInto(w, r, &req) {
		return
	}
	result, err := s.runAccountOp(r.Context(), accountOp{
		Op: accountOpSave, AccountID: strings.TrimSpace(req.ID), Login: req.Login,
		DisplayName: req.DisplayName, Role: req.Role, Status: req.Status, Password: req.Password,
	})
	if err != nil {
		writeAccountOpError(w, err)
		return
	}
	if s.log != nil {
		s.log.Info("account saved", "login", result.Account.Login, "role", result.Account.Role, "status", result.Account.Status)
	}
	response := map[string]any{"account": result.Account}
	if result.Generated != "" {
		response["password"] = result.Generated
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
	result, err := s.runAccountOp(r.Context(), accountOp{Op: accountOpResetPassword, AccountID: req.ID, Password: req.Password})
	if err != nil {
		writeAccountOpError(w, err)
		return
	}
	response := map[string]any{"ok": true}
	if result.Generated != "" {
		response["password"] = result.Generated
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, response)
}

// handleAccountReject turns a registration down: the pending account is
// deleted, so its login name is free again.
func (s *Server) handleAccountReject(w http.ResponseWriter, r *http.Request) {
	if !s.requireAccountAdmin(w, r) {
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if !decodeJSONInto(w, r, &req) {
		return
	}
	if _, err := s.runAccountOp(r.Context(), accountOp{Op: accountOpReject, AccountID: req.ID}); err != nil {
		writeAccountOpError(w, err)
		return
	}
	writeOK(w)
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

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
