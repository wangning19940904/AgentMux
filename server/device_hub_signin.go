package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	linkpkg "github.com/wangning19940904/AgentMux/link"
)

// A node's own sign-in (hub_account_approval.go explains the rules).
//
//	POST /api/v1/auth/device/approval     — ask a hub to approve signing in with its account
//	GET  /api/v1/auth/device/approval?id= — whether it did; signs the machine in once it has
//	POST /api/v1/auth/device/leave-hub    — leave the hub and sign in with a new local account

// hubAccountsRecheck is how long a node trusts the hub's accounts it last
// asked about.
const hubAccountsRecheck = 10 * time.Second

type hubAccountsCache struct {
	mu       sync.Mutex
	at       time.Time
	accounts []hubAccount
	err      error
}

// nodeHubAccounts asks the hub which accounts its machines are signed in as.
func (s *Server) nodeHubAccounts(ctx context.Context) ([]hubAccount, error) {
	cache := &s.hubAccounts
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if !cache.at.IsZero() && time.Since(cache.at) < hubAccountsRecheck {
		return cache.accounts, cache.err
	}
	askCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), deviceCheckTimeout)
	defer cancel()
	result, err := s.runDeviceAccountOp(askCtx, accountOp{Op: accountOpHubAccounts})
	cache.at, cache.accounts, cache.err = time.Now(), result.HubAccounts, err
	if cache.accounts == nil {
		cache.accounts = []hubAccount{}
	}
	return cache.accounts, err
}

func (s *Server) forgetHubAccounts() {
	s.hubAccounts.mu.Lock()
	s.hubAccounts.at = time.Time{}
	s.hubAccounts.mu.Unlock()
}

// nodeOfHub reports whether this machine signs in with its hub's accounts.
func (s *Server) nodeOfHub(ctx context.Context) bool {
	authority, err := s.deviceAuthority(ctx)
	return err == nil && authority == deviceAuthorityHub
}

func (s *Server) handleDeviceApprovalRequest(w http.ResponseWriter, r *http.Request) {
	if !s.requireMachineController(w, r) {
		return
	}
	if !s.nodeOfHub(r.Context()) {
		writeErr(w, http.StatusConflict, "this machine is not linked to a hub")
		return
	}
	var req struct {
		Login string `json:"login"`
	}
	if !decodeJSONInto(w, r, &req) {
		return
	}
	result, err := s.runDeviceAccountOp(r.Context(), accountOp{
		Op: accountOpRequestApproval, Login: core.NormalizeLogin(req.Login), UserAgent: deviceUserAgent(), RemoteAddr: clientAddr(r),
	})
	if err != nil {
		writeAccountOpError(w, err)
		return
	}
	if result.Approval == nil || result.ApprovalSecret == "" {
		writeErr(w, http.StatusBadGateway, "the hub did not open a sign-in request")
		return
	}
	s.approvalSecrets.Store(result.Approval.ID, result.ApprovalSecret)
	writeJSON(w, http.StatusOK, map[string]any{"approval": result.Approval})
}

func (s *Server) handleDeviceApprovalStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requireMachineController(w, r) {
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	secret, ok := s.approvalSecrets.Load(id)
	if !ok {
		writeAccountOpError(w, accountFailure(http.StatusGone, accountCodeApprovalExpired, "this sign-in request expired; ask again"))
		return
	}
	result, err := s.runDeviceAccountOp(r.Context(), accountOp{Op: accountOpApprovalStatus, ApprovalID: id, ApprovalSecret: secret.(string)})
	if err != nil {
		var opErr *accountOpError
		if errors.As(err, &opErr) && (opErr.Code == accountCodeApprovalDenied || opErr.Code == accountCodeApprovalExpired) {
			s.approvalSecrets.Delete(id)
		}
		writeAccountOpError(w, err)
		return
	}
	if result.Token == "" {
		writeJSON(w, http.StatusOK, map[string]any{"approval": result.Approval})
		return
	}
	s.approvalSecrets.Delete(id)
	s.finishDeviceSignIn(w, r, result)
}

// handleDeviceLeaveHub signs this machine in with an account of its own: it
// checks the login name is not taken at the hub, leaves the hub, and creates
// the account as this machine's first administrator.
func (s *Server) handleDeviceLeaveHub(w http.ResponseWriter, r *http.Request) {
	if !s.requireMachineController(w, r) {
		return
	}
	ctx := r.Context()
	if !s.nodeOfHub(ctx) {
		writeErr(w, http.StatusConflict, "this machine is not linked to a hub")
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
	// Login names stay unique: one the hub group uses cannot become this
	// machine's own.
	identified, err := s.runDeviceAccountOp(ctx, accountOp{Op: accountOpIdentify, Login: login})
	if err != nil {
		writeAccountOpError(w, err)
		return
	}
	if identified.State != "unregistered" {
		writeAccountOpError(w, accountFailure(http.StatusConflict, accountCodeRegistered,
			"the hub already has an account named "+login+"; choose another login name"))
		return
	}
	if count, err := s.st.CountAccounts(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	} else if count > 0 {
		writeAccountOpError(w, accountFailure(http.StatusConflict, accountCodeAccountsExist,
			"this machine already has accounts of its own; leave the hub from the Devices page and sign in with one"))
		return
	}
	if err := s.leaveHub(); err != nil {
		writeErr(w, http.StatusInternalServerError, "leave the hub: "+err.Error())
		return
	}
	s.forgetHubAccounts()
	if s.log != nil {
		s.log.Info("this machine left its hub to use an account of its own", "login", login)
	}
	if previous, _ := s.loadDeviceAccount(); previous != nil {
		_ = s.removeDeviceAccount()
	}
	result, err := s.runAccountOp(ctx, accountOp{
		Op: accountOpSetup, Login: login, DisplayName: req.DisplayName, Password: req.Password,
		StartSession: true, Device: true, UserAgent: deviceUserAgent(), RemoteAddr: clientAddr(r),
	})
	if err != nil {
		writeAccountOpError(w, err)
		return
	}
	s.finishDeviceSignIn(w, r, result)
}

// leaveHub ends this machine's link to its hub, whichever way it was
// linked. The hub keeps listing the machine until someone removes it there.
func (s *Server) leaveHub() error {
	if s.node == nil {
		return nil
	}
	path, err := s.node.statePath()
	if err != nil {
		return err
	}
	s.node.stop()
	if err := linkpkg.RemoveState(path); err != nil {
		return err
	}
	s.node.mu.Lock()
	passive := s.node.passive
	s.node.mu.Unlock()
	if passive != nil && passive.Status().Paired {
		return passive.Forget()
	}
	return nil
}
