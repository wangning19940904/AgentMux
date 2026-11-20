package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
)

// The hub's account, for the machines linked to it.
//
// A node signs in with the account its hub is signed in as: the account of
// the primary hub's own machine, or of its standby's. Any other account
// would make it a different person's machine, so a node that wants one
// leaves the hub first (device_leave_hub.go).
//
// A node proves it may use the hub's account with the account's password,
// or by asking a hub to approve: the request waits at the account authority,
// every hub Console signed in as that account shows it, and approving opens
// the node's device session, which the node collects with a secret only it
// holds.
//
//	GET  /api/v1/auth/approvals        — sign-ins waiting for the caller's account
//	POST /api/v1/auth/approvals/decide — approve or deny one

const (
	deviceApprovalTTL = 5 * time.Minute
	// maxDeviceApprovals bounds the requests waiting at once.
	maxDeviceApprovals = 64

	accountCodeNotHubAccount   = "not_hub_account"
	accountCodeApprovalDenied  = "approval_denied"
	accountCodeApprovalExpired = "approval_expired"
)

const (
	deviceApprovalPending  = "pending"
	deviceApprovalApproved = "approved"
	deviceApprovalDenied   = "denied"
)

// hubAccount is an account a hub machine is signed in as.
type hubAccount struct {
	Account *core.AccountIdentity `json:"account"`
	// Machine names the hub machine signed in with it.
	Machine string `json:"machine,omitempty"`
}

// deviceApprovalView is one request, as a hub Console shows it.
type deviceApprovalView struct {
	ID        string    `json:"id"`
	Login     string    `json:"login"`
	Machine   string    `json:"machine"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

type deviceApproval struct {
	deviceApprovalView
	accountID  string
	secretHash string
	userAgent  string
	remoteAddr string
}

// deviceApprovalBook holds the requests at the account authority. They live
// in memory: a request outlives neither its five minutes nor the hub.
type deviceApprovalBook struct {
	mu    sync.Mutex
	items map[string]*deviceApproval
}

func (b *deviceApprovalBook) prune(now time.Time) {
	for id, item := range b.items {
		if now.After(item.ExpiresAt) {
			delete(b.items, id)
		}
	}
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// hubMachineAccounts lists the accounts this hub group's hub machines are
// signed in as: this authority's own machine first, then its standby's,
// whose device session names its host.
func (s *Server) hubMachineAccounts(ctx context.Context) ([]hubAccount, error) {
	var out []hubAccount
	seen := map[string]bool{}
	add := func(account *core.Account, machine string) {
		if account == nil || seen[account.ID] || !account.Active() {
			return
		}
		seen[account.ID] = true
		out = append(out, hubAccount{Account: account.Identity(), Machine: machine})
	}
	add(s.currentDeviceAccount(ctx), machineHostname())
	if s.st == nil {
		return out, nil
	}
	devices, err := s.st.ListDevices(ctx)
	if err != nil {
		return out, err
	}
	standbys := map[string]string{}
	for _, device := range devices {
		if device.Hostname != "" && s.isStandbyDevice(ctx, device.ID) {
			standbys[deviceAgentFor(device.Hostname)] = device.Name
		}
	}
	if len(standbys) == 0 {
		return out, nil
	}
	sessions, err := s.st.ListAccountSessions(ctx, "", "")
	if err != nil {
		return out, err
	}
	sort.SliceStable(sessions, func(i, j int) bool { return sessions[i].CreatedAt.After(sessions[j].CreatedAt) })
	for _, session := range sessions {
		machine, ok := standbys[session.UserAgent]
		if !ok || session.Method != core.SignInMethodDevice {
			continue
		}
		account, err := s.st.GetAccount(ctx, session.AccountID)
		if err == nil {
			add(account, machine)
		}
	}
	return out, nil
}

func machineHostname() string {
	return strings.TrimPrefix(deviceUserAgent(), deviceAgentPrefix)
}

// isHubAccount reports whether login is an account a hub machine is signed
// in as.
func isHubAccount(accounts []hubAccount, login string) bool {
	for _, entry := range accounts {
		if entry.Account != nil && entry.Account.Login == login {
			return true
		}
	}
	return false
}

// requireHubAccount refuses a node's sign-in with any other account.
func (s *Server) requireHubAccount(ctx context.Context, login string) error {
	accounts, err := s.hubMachineAccounts(ctx)
	if err != nil {
		return err
	}
	if !isHubAccount(accounts, login) {
		return accountFailure(http.StatusForbidden, accountCodeNotHubAccount,
			"a machine linked to the hub signs in with the account the hub is signed in as; to use another account, leave the hub")
	}
	return nil
}

// requestDeviceApproval opens a request for op.Login, from op.Machine.
func (s *Server) requestDeviceApproval(ctx context.Context, op accountOp) (accountOpResult, error) {
	login := core.NormalizeLogin(op.Login)
	if err := s.requireHubAccount(ctx, login); err != nil {
		return accountOpResult{}, err
	}
	account, err := s.st.GetAccountByLogin(ctx, login)
	if err != nil || account == nil || !account.Active() {
		return accountOpResult{}, accountFailure(http.StatusForbidden, accountCodeNotHubAccount, "that account cannot sign in")
	}
	id, err := randomHex(12)
	if err != nil {
		return accountOpResult{}, err
	}
	secret, err := randomHex(24)
	if err != nil {
		return accountOpResult{}, err
	}
	now := time.Now().UTC()
	machine := strings.TrimSpace(op.Machine)
	if machine == "" {
		machine = strings.TrimPrefix(op.UserAgent, deviceAgentPrefix)
	}
	book := &s.approvals
	book.mu.Lock()
	defer book.mu.Unlock()
	book.prune(now)
	if len(book.items) >= maxDeviceApprovals {
		return accountOpResult{}, accountFailure(http.StatusTooManyRequests, "", "too many sign-in requests are waiting; try again in a few minutes")
	}
	if book.items == nil {
		book.items = map[string]*deviceApproval{}
	}
	book.items[id] = &deviceApproval{
		deviceApprovalView: deviceApprovalView{
			ID: id, Login: account.Login, Machine: machine, State: deviceApprovalPending,
			CreatedAt: now, ExpiresAt: now.Add(deviceApprovalTTL),
		},
		accountID: account.ID, secretHash: store.HashSecret(secret),
		userAgent: op.UserAgent, remoteAddr: op.RemoteAddr,
	}
	if s.log != nil {
		s.log.Info("a machine asks to sign in", "login", account.Login, "machine", machine)
	}
	return accountOpResult{Approval: &deviceApprovalView{ID: id, Login: account.Login, Machine: machine, State: deviceApprovalPending,
		CreatedAt: now, ExpiresAt: now.Add(deviceApprovalTTL)}, ApprovalSecret: secret}, nil
}

// deviceApprovalStatus answers the requesting machine. Once approved it
// opens the machine's session and forgets the request.
func (s *Server) deviceApprovalStatus(ctx context.Context, op accountOp) (accountOpResult, error) {
	book := &s.approvals
	book.mu.Lock()
	book.prune(time.Now())
	item := book.items[op.ApprovalID]
	if item == nil || subtle.ConstantTimeCompare([]byte(item.secretHash), []byte(store.HashSecret(op.ApprovalSecret))) != 1 {
		book.mu.Unlock()
		return accountOpResult{}, accountFailure(http.StatusGone, accountCodeApprovalExpired, "this sign-in request expired; ask again")
	}
	view := item.deviceApprovalView
	if item.State != deviceApprovalPending {
		delete(book.items, item.ID)
	}
	book.mu.Unlock()
	switch view.State {
	case deviceApprovalDenied:
		return accountOpResult{}, accountFailure(http.StatusForbidden, accountCodeApprovalDenied, "the hub turned this sign-in down")
	case deviceApprovalPending:
		return accountOpResult{Approval: &view}, nil
	}
	account, err := s.st.GetAccount(ctx, item.accountID)
	if err != nil || account == nil || !account.Active() {
		return accountOpResult{}, accountFailure(http.StatusForbidden, accountCodeNotHubAccount, "that account cannot sign in")
	}
	token, err := s.newAccountSession(ctx, account, accountOp{Device: true, UserAgent: item.userAgent, RemoteAddr: item.remoteAddr})
	if err != nil {
		return accountOpResult{}, err
	}
	return accountOpResult{Approval: &view, Account: account, Token: token}, nil
}

func (s *Server) listDeviceApprovals(op accountOp) accountOpResult {
	book := &s.approvals
	book.mu.Lock()
	defer book.mu.Unlock()
	book.prune(time.Now())
	approvals := []deviceApprovalView{}
	for _, item := range book.items {
		if item.accountID == op.AccountID && item.State == deviceApprovalPending {
			approvals = append(approvals, item.deviceApprovalView)
		}
	}
	sort.Slice(approvals, func(i, j int) bool { return approvals[i].CreatedAt.Before(approvals[j].CreatedAt) })
	return accountOpResult{Approvals: approvals}
}

func (s *Server) decideDeviceApproval(op accountOp) (accountOpResult, error) {
	book := &s.approvals
	book.mu.Lock()
	defer book.mu.Unlock()
	book.prune(time.Now())
	item := book.items[op.ApprovalID]
	if item == nil || item.accountID != op.AccountID {
		return accountOpResult{}, accountFailure(http.StatusNotFound, accountCodeApprovalExpired, "this sign-in request is no longer waiting")
	}
	if item.State == deviceApprovalPending {
		item.State = deviceApprovalDenied
		if op.Approve {
			item.State = deviceApprovalApproved
		}
		if s.log != nil {
			s.log.Info("a machine's sign-in was decided", "login", item.Login, "machine", item.Machine, "state", item.State)
		}
	}
	view := item.deviceApprovalView
	return accountOpResult{Approval: &view}, nil
}

// The hub Console's side.

func (s *Server) registerHubApprovalRoutes() {
	s.mux.HandleFunc("GET /api/v1/auth/approvals", s.handleApprovalsList)
	s.mux.HandleFunc("POST /api/v1/auth/approvals/decide", s.handleApprovalDecide)
}

// approvalAccount is the account whose requests the caller sees: a signed-in
// account only.
func approvalAccount(w http.ResponseWriter, r *http.Request) (*core.AccountIdentity, bool) {
	w.Header().Set("Cache-Control", "no-store")
	principal := requestPrincipal(r)
	if principal == nil || principal.Account == nil {
		writeJSON(w, http.StatusOK, map[string]any{"approvals": []deviceApprovalView{}})
		return nil, false
	}
	return principal.Account, true
}

func (s *Server) handleApprovalsList(w http.ResponseWriter, r *http.Request) {
	account, ok := approvalAccount(w, r)
	if !ok {
		return
	}
	if s.st == nil {
		writeJSON(w, http.StatusOK, map[string]any{"approvals": []deviceApprovalView{}})
		return
	}
	result, err := s.runAccountOp(r.Context(), accountOp{Op: accountOpListApprovals, AccountID: account.ID})
	if err != nil {
		writeAccountOpError(w, err)
		return
	}
	approvals := result.Approvals
	if approvals == nil {
		approvals = []deviceApprovalView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"approvals": approvals})
}

func (s *Server) handleApprovalDecide(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	principal := requestPrincipal(r)
	if principal == nil || principal.Account == nil {
		writeErr(w, http.StatusForbidden, "sign in with the account the machine asks for to approve it")
		return
	}
	var req struct {
		ID      string `json:"id"`
		Approve bool   `json:"approve"`
	}
	if !decodeJSONInto(w, r, &req) {
		return
	}
	result, err := s.runAccountOp(r.Context(), accountOp{
		Op: accountOpDecideApproval, AccountID: principal.Account.ID, ApprovalID: strings.TrimSpace(req.ID), Approve: req.Approve,
	})
	if err != nil {
		writeAccountOpError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"approval": result.Approval})
}
