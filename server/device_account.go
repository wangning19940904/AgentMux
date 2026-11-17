package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// The device account.
//
// Each machine is signed in to one account, kept by that machine's own
// AgentMux in a local file rather than in a browser. The account itself lives
// at the account authority (account_authority.go): the primary hub, which a
// standby follows and a node is linked to, so one account works on every
// machine and what it owns is the same everywhere.
//
// The desktop app shows nothing but sign-in until its machine is signed in,
// and then acts as that account: its requests carry the bridge token and the
// desktop marker, and resolvePrincipal answers with the device account. The
// bridge token on its own (the CLI, integrations) stays the administrator.
//
//	GET  /api/v1/auth/device           — this machine's account and authority
//	POST /api/v1/auth/device/identify  — is this login name registered?
//	POST /api/v1/auth/device/register  — ask for an account (an administrator approves it)
//	POST /api/v1/auth/device/sign-in   — sign this machine in
//	POST /api/v1/auth/device/setup     — create the first administrator and sign in with it
//	POST /api/v1/auth/device/sign-out  — sign this machine out
//
// Only the machine itself may use them: the bridge token, or the local
// Console on a machine without a bridge.

const (
	deviceAccountFile = "device-account.json"
	// deviceAccountRecheck is how long a checked device account is trusted
	// before its session is checked again.
	deviceAccountRecheck = 15 * time.Second
	// deviceAccountOffline is how long the last answer stands while the
	// authority cannot be reached, before it is asked again.
	deviceAccountOffline = time.Minute
	deviceCheckTimeout   = 3 * time.Second
	desktopHeader        = "X-AgentMux-Desktop"
)

// deviceAccountRecord is the file: who this machine is signed in as and the
// session that proves it.
type deviceAccountRecord struct {
	Account    core.Account `json:"account"`
	Token      string       `json:"token"`
	Authority  string       `json:"authority"`
	SignedInAt time.Time    `json:"signed_in_at"`
}

// deviceAccountState caches the checked account between requests.
type deviceAccountState struct {
	mu        sync.Mutex
	checked   bool
	checkedAt time.Time
	trustFor  time.Duration
	account   *core.Account
}

func (s *Server) deviceAccountPath() (string, error) {
	dir, err := s.linkStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, deviceAccountFile), nil
}

func (s *Server) loadDeviceAccount() (*deviceAccountRecord, error) {
	path, err := s.deviceAccountPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var record deviceAccountRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, err
	}
	if record.Token == "" || record.Account.ID == "" {
		return nil, nil
	}
	return &record, nil
}

func (s *Server) saveDeviceAccount(record deviceAccountRecord) error {
	path, err := s.deviceAccountPath()
	if err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	s.forgetDeviceAccountCheck()
	return nil
}

func (s *Server) removeDeviceAccount() error {
	path, err := s.deviceAccountPath()
	if err != nil {
		return err
	}
	err = os.Remove(path)
	s.forgetDeviceAccountCheck()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s *Server) forgetDeviceAccountCheck() {
	s.deviceAccount.mu.Lock()
	s.deviceAccount.checked = false
	s.deviceAccount.account = nil
	s.deviceAccount.mu.Unlock()
}

// currentDeviceAccount is the account this machine is signed in as, or nil.
// Its session is checked at the authority every few seconds, so signing out
// elsewhere, disabling the account or resetting its password signs the
// machine out. While the authority cannot be reached the last answer stands,
// so a laptop away from its hub keeps working.
func (s *Server) currentDeviceAccount(ctx context.Context) *core.Account {
	if s.st == nil {
		return nil
	}
	state := &s.deviceAccount
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.checked && time.Since(state.checkedAt) < state.trustFor {
		return state.account
	}
	remember := func(account *core.Account, trust time.Duration) *core.Account {
		state.checked, state.checkedAt, state.trustFor, state.account = true, time.Now(), trust, account
		return account
	}
	record, err := s.loadDeviceAccount()
	if err != nil && s.log != nil {
		s.log.Warn("unreadable device account; this machine is signed out", "err", err)
	}
	if record == nil {
		return remember(nil, deviceAccountRecheck)
	}
	checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), deviceCheckTimeout)
	defer cancel()
	result, err := s.runDeviceAccountOp(checkCtx, accountOp{Op: accountOpCheckSession, SessionToken: record.Token})
	if err == nil && result.Account != nil {
		return remember(result.Account, deviceAccountRecheck)
	}
	var opErr *accountOpError
	if errors.As(err, &opErr) && opErr.Code == accountCodeSignedOut {
		if s.log != nil {
			s.log.Info("this machine's account session ended; signing the machine out", "login", record.Account.Login)
		}
		if path, pathErr := s.deviceAccountPath(); pathErr == nil {
			_ = os.Remove(path)
		}
		return remember(nil, deviceAccountRecheck)
	}
	// The authority is away: keep the account this machine last had.
	if s.log != nil {
		s.log.Warn("could not check this machine's account; keeping the last one", "login", record.Account.Login, "err", err)
	}
	if state.account != nil && state.account.ID == record.Account.ID {
		return remember(state.account, deviceAccountOffline)
	}
	account := record.Account
	return remember(&account, deviceAccountOffline)
}

// machineController reports whether a request comes from the machine itself:
// the bridge token, or the local Console on a machine without a bridge.
// Only it may sign the machine in or out.
func (s *Server) machineController(r *http.Request) bool {
	if token, ok := bearerToken(strings.TrimSpace(r.Header.Get("Authorization"))); ok {
		return s.cfg.Bridge.Token != "" && token == s.cfg.Bridge.Token
	}
	return !s.cfg.Bridge.Enabled && !hasExplicitCredential(r) && !isHubClient(r)
}

// desktopRequest reports whether the request comes through this machine's
// desktop app's native proxy, which acts as the machine's account. A request
// another machine forwarded (over the hub link or SSH) never does.
func desktopRequest(r *http.Request) bool {
	return r.Header.Get(desktopHeader) == "1" &&
		r.Header.Get("X-AgentMux-Hub-Link") == "" && r.Header.Get("X-AgentMux-SSH-Host") == ""
}

func (s *Server) registerDeviceAccountRoutes() {
	s.mux.HandleFunc("GET /api/v1/auth/device", s.handleDeviceAccount)
	s.mux.HandleFunc("POST /api/v1/auth/device/identify", s.handleDeviceIdentify)
	s.mux.HandleFunc("POST /api/v1/auth/device/register", s.handleDeviceRegister)
	s.mux.HandleFunc("POST /api/v1/auth/device/sign-in", s.handleDeviceSignIn)
	s.mux.HandleFunc("POST /api/v1/auth/device/setup", s.handleDeviceSetup)
	s.mux.HandleFunc("POST /api/v1/auth/device/sign-out", s.handleDeviceSignOut)
}

// deviceAccountView answers GET /api/v1/auth/device and rides along in
// /api/v1/auth/session for the machine itself.
type deviceAccountView struct {
	SignedIn bool                  `json:"signed_in"`
	Account  *core.AccountIdentity `json:"account,omitempty"`
	// Authority is "local", "primary" (this hub follows a primary) or "hub"
	// (this node signs in with its hub's accounts).
	Authority string `json:"authority"`
	// SetupAllowed is true when the first administrator may be created from
	// this machine: not on a node, whose hub holds the accounts.
	SetupAllowed bool `json:"setup_allowed"`
}

func (s *Server) deviceAccountView(ctx context.Context) deviceAccountView {
	authority, err := s.deviceAuthority(ctx)
	if err != nil {
		authority = deviceAuthorityPrimary
	}
	view := deviceAccountView{Authority: authority, SetupAllowed: authority != deviceAuthorityHub}
	if account := s.currentDeviceAccount(ctx); account != nil {
		view.SignedIn, view.Account = true, account.Identity()
	}
	return view
}

func (s *Server) requireMachineController(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	if s.st == nil {
		serviceUnavailable(w, "accounts")
		return false
	}
	if !s.machineController(r) {
		writeErr(w, http.StatusForbidden, "only this machine's own AgentMux app can sign it in or out")
		return false
	}
	return true
}

func (s *Server) handleDeviceAccount(w http.ResponseWriter, r *http.Request) {
	if !s.requireMachineController(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, s.deviceAccountView(r.Context()))
}

func (s *Server) handleDeviceIdentify(w http.ResponseWriter, r *http.Request) {
	if !s.requireMachineController(w, r) {
		return
	}
	var req struct {
		Login string `json:"login"`
	}
	if !decodeJSONInto(w, r, &req) {
		return
	}
	login := core.NormalizeLogin(req.Login)
	if err := core.ValidateLogin(login); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := s.runDeviceAccountOp(r.Context(), accountOp{Op: accountOpIdentify, Login: login})
	if err != nil {
		writeAccountOpError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, identifyResponse{
		Login: login, State: result.State, SetupRequired: !result.AccountsExist, RegistrationOpen: result.AccountsExist,
	})
}

func (s *Server) handleDeviceRegister(w http.ResponseWriter, r *http.Request) {
	if !s.requireMachineController(w, r) {
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
	result, err := s.runDeviceAccountOp(r.Context(), accountOp{
		Op: accountOpRegister, Login: login, DisplayName: req.DisplayName, Password: req.Password,
	})
	if err != nil {
		writeAccountOpError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": result.Account.Identity(), "status": core.AccountStatusPending})
}

func (s *Server) handleDeviceSignIn(w http.ResponseWriter, r *http.Request) {
	if !s.requireMachineController(w, r) {
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
	if s.limitedAuthRequest(w, login, addr) {
		return
	}
	result, err := s.runDeviceAccountOp(r.Context(), accountOp{
		Op: accountOpSignIn, Login: login, Password: req.Password, UserAgent: deviceUserAgent(), RemoteAddr: addr,
	})
	if err != nil {
		var opErr *accountOpError
		if errors.As(err, &opErr) && opErr.Code == accountCodeWrongPassword {
			s.signIns.failure(login, addr)
		}
		writeAccountOpError(w, err)
		return
	}
	s.signIns.success(login, addr)
	s.finishDeviceSignIn(w, r, result)
}

func (s *Server) handleDeviceSetup(w http.ResponseWriter, r *http.Request) {
	if !s.requireMachineController(w, r) {
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
	result, err := s.runDeviceAccountOp(r.Context(), accountOp{
		Op: accountOpSetup, Login: login, DisplayName: req.DisplayName, Password: req.Password,
		StartSession: true, UserAgent: deviceUserAgent(), RemoteAddr: addr,
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
	s.finishDeviceSignIn(w, r, result)
}

// finishDeviceSignIn keeps the session the authority opened as this
// machine's account. One account per machine: the one it had before is
// signed out.
func (s *Server) finishDeviceSignIn(w http.ResponseWriter, r *http.Request, result accountOpResult) {
	if result.Account == nil || result.Token == "" {
		writeErr(w, http.StatusBadGateway, "the account authority did not open a session")
		return
	}
	authority, _ := s.deviceAuthority(r.Context())
	previous, _ := s.loadDeviceAccount()
	if err := s.saveDeviceAccount(deviceAccountRecord{
		Account: *result.Account, Token: result.Token, Authority: authority, SignedInAt: time.Now().UTC(),
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "keep this machine's account: "+err.Error())
		return
	}
	if previous != nil && previous.Token != result.Token {
		_, _ = s.runDeviceAccountOp(r.Context(), accountOp{Op: accountOpSignOut, SessionToken: previous.Token})
	}
	if s.log != nil {
		s.log.Info("this machine signed in", "login", result.Account.Login, "authority", authority)
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": result.Account.Identity(), "existing": result.Existing})
}

func (s *Server) handleDeviceSignOut(w http.ResponseWriter, r *http.Request) {
	if !s.requireMachineController(w, r) {
		return
	}
	record, err := s.loadDeviceAccount()
	if err != nil && s.log != nil {
		s.log.Warn("unreadable device account", "err", err)
	}
	if record != nil {
		if _, err := s.runDeviceAccountOp(r.Context(), accountOp{Op: accountOpSignOut, SessionToken: record.Token}); err != nil && s.log != nil {
			// The machine signs out either way; the session ends when it
			// expires or the account's password changes.
			s.log.Warn("could not end this machine's session at the account authority", "err", err)
		}
	}
	if err := s.removeDeviceAccount(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.log != nil && record != nil {
		s.log.Info("this machine signed out", "login", record.Account.Login)
	}
	writeOK(w)
}

func deviceUserAgent() string {
	host, _ := os.Hostname()
	return "AgentMux device " + strings.TrimSuffix(host, ".local")
}
