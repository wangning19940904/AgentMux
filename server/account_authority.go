package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
)

// The account authority.
//
// Accounts work as Persona's linked hubs do (github.com/nwverse/persona,
// docs/server/mesh.md): one hub, the primary, is the only authority for
// accounts and their sessions, and every other hub holds a copy. Here the copy
// is the standby's replicated hub data (store/hubsync.go), so one account
// signs in on every hub of the group.
//
//   - A standby never writes accounts itself (the hub tables are fenced
//     there). It sends each account change, sign-in and sign-out up its link
//     to the primary (accountsUplinkPath), which applies it and answers with
//     its change-log position. The standby waits until its copy has that
//     change, so the browser sees its own write at once.
//   - The standby decides who may ask (the request's principal, rate limits)
//     and the primary trusts it, as it trusts the standby with a copy of all
//     hub data anyway.
//   - A hub that starts following hands its own accounts to the primary first
//     (account_handover.go), as a Persona member hands its credentials to the
//     center, so nobody's account disappears when two hubs join.
//
// A machine that is not a standby (a lone hub, a primary, a plain node) is
// its own authority and applies every operation locally.

const (
	accountsUplinkPath = "/uplink/v1/accounts"
	// accountCopyWait bounds how long a standby waits for a change it made
	// on the primary to reach its own copy.
	accountCopyWait   = 10 * time.Second
	accountOpTimeout  = 20 * time.Second
	maxAccountOpBytes = 8 << 20
)

// Account operations.
const (
	accountOpIdentify      = "identify"
	accountOpSetup         = "setup"
	accountOpRegister      = "register"
	accountOpSignIn        = "sign-in"
	accountOpSignOut       = "sign-out"
	accountOpOwnPassword   = "own-password"
	accountOpSave          = "save"
	accountOpResetPassword = "reset-password"
	accountOpReject        = "reject"
	accountOpImport        = "import"
)

// Error codes the Console tells apart.
const (
	accountCodePending       = "pending"
	accountCodeRegistered    = "registered"
	accountCodeSetupRequired = "setup_required"
	accountCodeAccountsExist = "accounts_exist"
	accountCodeWrongPassword = "wrong_credentials"
	accountCodeNoAuthority   = "authority_unreachable"
)

// accountOp is one account operation, as a standby sends it to its primary.
// Who may ask was checked where the request arrived.
type accountOp struct {
	Op              string                  `json:"op"`
	Login           string                  `json:"login,omitempty"`
	DisplayName     *string                 `json:"display_name,omitempty"`
	Role            *string                 `json:"role,omitempty"`
	Status          *string                 `json:"status,omitempty"`
	Password        string                  `json:"password,omitempty"`
	CurrentPassword string                  `json:"current_password,omitempty"`
	AccountID       string                  `json:"account_id,omitempty"`
	SessionToken    string                  `json:"session_token,omitempty"`
	StartSession    bool                    `json:"start_session,omitempty"`
	UserAgent       string                  `json:"user_agent,omitempty"`
	RemoteAddr      string                  `json:"remote_addr,omitempty"`
	Accounts        []store.HandoverAccount `json:"accounts,omitempty"`
}

// accountOpResult is what an operation produced.
type accountOpResult struct {
	Account *core.Account `json:"account,omitempty"`
	// State is the login's state for identify: registered, pending or
	// unregistered.
	State         string `json:"state,omitempty"`
	AccountsExist bool   `json:"accounts_exist,omitempty"`
	// Token is a new session's token, for the hub the browser talks to to
	// set as its cookie.
	Token string `json:"token,omitempty"`
	// Generated is a password the authority made up, shown once.
	Generated string `json:"generated,omitempty"`
	// Existing is true when first-run setup found the person already has an
	// account on the primary and signed them in with it.
	Existing bool                  `json:"existing,omitempty"`
	Handover *store.HandoverResult `json:"handover,omitempty"`
	// Head is the primary's change-log position after the operation.
	Head int64 `json:"head,omitempty"`
}

type accountOpError struct {
	Status  int
	Code    string
	Message string
}

func (e *accountOpError) Error() string { return e.Message }

func accountFailure(status int, code, message string) *accountOpError {
	return &accountOpError{Status: status, Code: code, Message: message}
}

// accountStoreError gives a store error its HTTP status.
func accountStoreError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrAccountNotFound):
		return accountFailure(http.StatusNotFound, "", err.Error())
	case errors.Is(err, store.ErrLoginTaken):
		return accountFailure(http.StatusConflict, accountCodeRegistered, err.Error())
	case errors.Is(err, store.ErrAccountsExist):
		return accountFailure(http.StatusConflict, accountCodeAccountsExist, err.Error())
	case errors.Is(err, store.ErrLastAdmin), errors.Is(err, store.ErrAccountNotPending):
		return accountFailure(http.StatusConflict, "", err.Error())
	}
	var opErr *accountOpError
	if errors.As(err, &opErr) {
		return err
	}
	if strings.Contains(err.Error(), "SQLSTATE 25006") || strings.Contains(err.Error(), "hub data can only be changed on the primary hub") {
		return accountFailure(http.StatusServiceUnavailable, accountCodeNoAuthority,
			"this hub cannot change accounts right now: it is waiting for the relay to confirm which hub is primary; try again in a few seconds")
	}
	if strings.HasPrefix(err.Error(), "a login name") || strings.HasPrefix(err.Error(), "a password") {
		return accountFailure(http.StatusBadRequest, "", err.Error())
	}
	return accountFailure(http.StatusInternalServerError, "", err.Error())
}

func writeAccountOpError(w http.ResponseWriter, err error) {
	var opErr *accountOpError
	if !errors.As(accountStoreError(err), &opErr) {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	body := map[string]string{"error": opErr.Message}
	if opErr.Code != "" {
		body["code"] = opErr.Code
	}
	writeJSON(w, opErr.Status, body)
}

// followsPrimaryHub reports whether account changes must go to the primary.
// A fenced former primary has no authority and is not following yet.
func (s *Server) followsPrimaryHub(ctx context.Context) (bool, error) {
	if s.followsForTest {
		return true, nil
	}
	if s.st == nil || !s.st.IsPostgres() {
		return false, nil
	}
	state, err := s.st.HubReplication(ctx)
	if err != nil {
		return false, nil
	}
	switch state.Role {
	case store.HubRoleStandby:
		return true, nil
	case store.HubRoleFenced:
		return false, accountFailure(http.StatusServiceUnavailable, accountCodeNoAuthority,
			"this hub lost its lease and is waiting for the relay; try again in a few seconds")
	}
	return false, nil
}

// runAccountOp applies an operation here, or on the primary when this hub
// follows one.
func (s *Server) runAccountOp(ctx context.Context, op accountOp) (accountOpResult, error) {
	if s.st == nil {
		return accountOpResult{}, accountFailure(http.StatusServiceUnavailable, "", "accounts are not available: no database")
	}
	follows, err := s.followsPrimaryHub(ctx)
	if err != nil {
		return accountOpResult{}, err
	}
	if !follows {
		return s.applyAccountOp(ctx, op)
	}
	if op.Op == accountOpIdentify {
		// Answered from the copy: it is current, and asking stays possible
		// while the primary is briefly away.
		if result, err := s.applyAccountOp(ctx, op); err == nil {
			return result, nil
		}
	}
	return s.forwardAccountOp(ctx, op)
}

// forwardAccountOp sends an operation to the primary and waits until this
// hub's copy has what it changed.
func (s *Server) forwardAccountOp(ctx context.Context, op accountOp) (accountOpResult, error) {
	var client *http.Client
	if s.node != nil {
		client = s.node.uplink()
	}
	if client == nil {
		return accountOpResult{}, accountFailure(http.StatusServiceUnavailable, accountCodeNoAuthority,
			"accounts are kept by the primary hub, and this hub has no link to it right now")
	}
	body, err := json.Marshal(op)
	if err != nil {
		return accountOpResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, accountOpTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://hub"+accountsUplinkPath, bytes.NewReader(body))
	if err != nil {
		return accountOpResult{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return accountOpResult{}, accountFailure(http.StatusServiceUnavailable, accountCodeNoAuthority,
			"the primary hub keeps accounts and could not be reached: "+err.Error())
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return accountOpResult{}, err
	}
	if response.StatusCode != http.StatusOK {
		var failure struct {
			Error string `json:"error"`
			Code  string `json:"code"`
		}
		_ = json.Unmarshal(data, &failure)
		message := strings.TrimSpace(failure.Error)
		if message == "" {
			message = strings.TrimSpace(string(data))
		}
		if response.StatusCode == http.StatusNotFound && failure.Code == "" && !strings.Contains(message, "not found") {
			// An older primary has no account authority to forward to.
			return accountOpResult{}, accountFailure(http.StatusServiceUnavailable, accountCodeNoAuthority,
				"the primary hub runs an AgentMux without shared accounts; update it to change accounts from this hub")
		}
		return accountOpResult{}, accountFailure(response.StatusCode, failure.Code, "primary hub: "+message)
	}
	var result accountOpResult
	if err := json.Unmarshal(data, &result); err != nil {
		return accountOpResult{}, fmt.Errorf("the primary hub sent an unreadable answer: %w", err)
	}
	s.waitForHubCopy(ctx, result.Head)
	return result, nil
}

// waitForHubCopy waits until this standby applied the primary's changes up
// to head, so what a person just changed shows here.
func (s *Server) waitForHubCopy(ctx context.Context, head int64) {
	if head <= 0 || s.st == nil || !s.st.IsPostgres() {
		return
	}
	deadline := time.Now().Add(accountCopyWait)
	for {
		state, err := s.st.HubReplication(ctx)
		if err == nil && (state.Role != store.HubRoleStandby || state.AppliedSeq >= head) {
			return
		}
		if !time.Now().Before(deadline) {
			if s.log != nil {
				s.log.Warn("an account change on the primary hub has not reached this standby yet", "head", head)
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// applyAccountOp performs an operation on this machine's own accounts.
func (s *Server) applyAccountOp(ctx context.Context, op accountOp) (accountOpResult, error) {
	st := s.st
	login := core.NormalizeLogin(op.Login)
	switch op.Op {
	case accountOpIdentify:
		count, err := st.CountAccounts(ctx)
		if err != nil {
			return accountOpResult{}, err
		}
		state, err := st.AccountLoginState(ctx, login)
		if err != nil {
			return accountOpResult{}, err
		}
		return accountOpResult{State: state, AccountsExist: count > 0}, nil

	case accountOpSetup:
		if err := validateNewAccount(login, op.Password); err != nil {
			return accountOpResult{}, accountFailure(http.StatusBadRequest, "", err.Error())
		}
		account, err := st.CreateFirstAdmin(ctx, store.NewAccount{
			Login: login, DisplayName: derefString(op.DisplayName), Password: op.Password,
		})
		existing := false
		if errors.Is(err, store.ErrAccountsExist) {
			// Accounts exist on the authority already, perhaps made on the
			// other hub. A person who already has one is signed in with it
			// instead of being told no.
			found, checkErr := st.CheckAccountPassword(ctx, login, op.Password)
			if checkErr != nil {
				return accountOpResult{}, checkErr
			}
			switch {
			case found != nil && found.Active():
				account, err, existing = found, nil, true
			case found != nil && found.Status == core.AccountStatusPending:
				return accountOpResult{}, accountFailure(http.StatusForbidden, accountCodePending,
					"your registration is waiting for an administrator's approval")
			default:
				return accountOpResult{}, accountFailure(http.StatusConflict, accountCodeAccountsExist,
					"accounts already exist on this hub group: sign in with yours, or register and wait for an administrator")
			}
		}
		if err != nil {
			return accountOpResult{}, err
		}
		result := accountOpResult{Account: account, Existing: existing}
		if op.StartSession {
			token, err := s.newAccountSession(ctx, account, op)
			if err != nil {
				return accountOpResult{}, err
			}
			result.Token = token
		}
		return result, nil

	case accountOpRegister:
		if err := validateNewAccount(login, op.Password); err != nil {
			return accountOpResult{}, accountFailure(http.StatusBadRequest, "", err.Error())
		}
		count, err := st.CountAccounts(ctx)
		if err != nil {
			return accountOpResult{}, err
		}
		if count == 0 {
			return accountOpResult{}, accountFailure(http.StatusConflict, accountCodeSetupRequired,
				"this AgentMux has no administrator yet; whoever runs it creates the first account")
		}
		account, err := st.CreateAccount(ctx, store.NewAccount{
			Login: login, DisplayName: derefString(op.DisplayName), Password: op.Password, Pending: true,
		})
		if err != nil {
			return accountOpResult{}, err
		}
		return accountOpResult{Account: account}, nil

	case accountOpSignIn:
		account, err := st.CheckAccountPassword(ctx, login, op.Password)
		if err != nil {
			return accountOpResult{}, err
		}
		switch {
		case account == nil || account.Status == core.AccountStatusDisabled:
			return accountOpResult{}, accountFailure(http.StatusUnauthorized, accountCodeWrongPassword, "wrong login name or password")
		case account.Status == core.AccountStatusPending:
			return accountOpResult{}, accountFailure(http.StatusForbidden, accountCodePending,
				"your registration is waiting for an administrator's approval")
		}
		token, err := s.newAccountSession(ctx, account, op)
		if err != nil {
			return accountOpResult{}, err
		}
		return accountOpResult{Account: account, Token: token}, nil

	case accountOpSignOut:
		if op.SessionToken != "" {
			if err := st.DeleteAccountSession(ctx, op.SessionToken); err != nil {
				return accountOpResult{}, err
			}
		}
		return accountOpResult{}, nil

	case accountOpOwnPassword:
		if err := core.ValidatePassword(op.Password); err != nil {
			return accountOpResult{}, accountFailure(http.StatusBadRequest, "", err.Error())
		}
		account, err := st.VerifyAccountPassword(ctx, login, op.CurrentPassword)
		if err != nil {
			return accountOpResult{}, err
		}
		if account == nil || account.ID != op.AccountID {
			return accountOpResult{}, accountFailure(http.StatusForbidden, accountCodeWrongPassword, "the current password is wrong")
		}
		if err := st.SetAccountPassword(ctx, account.ID, op.Password, op.SessionToken); err != nil {
			return accountOpResult{}, err
		}
		return accountOpResult{Account: account}, nil

	case accountOpSave:
		if id := strings.TrimSpace(op.AccountID); id != "" {
			account, err := st.UpdateAccount(ctx, id, store.AccountUpdate{
				DisplayName: op.DisplayName, Role: op.Role, Status: op.Status,
			})
			if err != nil {
				return accountOpResult{}, err
			}
			return accountOpResult{Account: account}, nil
		}
		count, err := st.CountAccounts(ctx)
		if err != nil {
			return accountOpResult{}, err
		}
		if count == 0 {
			return accountOpResult{}, accountFailure(http.StatusConflict, accountCodeSetupRequired, "create the first administrator first")
		}
		generated, password := "", op.Password
		if password == "" {
			generated = store.GeneratePassword()
			password = generated
		}
		if err := validateNewAccount(login, password); err != nil {
			return accountOpResult{}, accountFailure(http.StatusBadRequest, "", err.Error())
		}
		account, err := st.CreateAccount(ctx, store.NewAccount{
			Login: login, DisplayName: derefString(op.DisplayName), Role: derefString(op.Role), Password: password,
		})
		if err != nil {
			return accountOpResult{}, err
		}
		return accountOpResult{Account: account, Generated: generated}, nil

	case accountOpResetPassword:
		generated, password := "", op.Password
		if password == "" {
			generated = store.GeneratePassword()
			password = generated
		}
		if err := core.ValidatePassword(password); err != nil {
			return accountOpResult{}, accountFailure(http.StatusBadRequest, "", err.Error())
		}
		if err := st.SetAccountPassword(ctx, strings.TrimSpace(op.AccountID), password, ""); err != nil {
			return accountOpResult{}, err
		}
		return accountOpResult{Generated: generated}, nil

	case accountOpReject:
		if err := st.RejectPendingAccount(ctx, strings.TrimSpace(op.AccountID)); err != nil {
			return accountOpResult{}, err
		}
		return accountOpResult{}, nil

	case accountOpImport:
		handover, err := st.ImportHandoverAccounts(ctx, op.Accounts)
		if err != nil {
			return accountOpResult{}, err
		}
		return accountOpResult{Handover: &handover}, nil
	}
	return accountOpResult{}, accountFailure(http.StatusBadRequest, "", "unknown account operation "+op.Op)
}

func (s *Server) newAccountSession(ctx context.Context, account *core.Account, op accountOp) (string, error) {
	token, _, err := s.st.CreateAccountSession(ctx, account.ID, core.SignInMethodPassword, op.UserAgent, op.RemoteAddr, accountSessionTTL)
	return token, err
}

// handleUplinkAccounts is the primary's side: its standby's account
// operations, applied here as the one authority.
func (s *Server) handleUplinkAccounts(w http.ResponseWriter, r *http.Request) {
	source, ok := s.relaySource(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if !s.isStandbyDevice(ctx, source.ID) {
		writeErr(w, http.StatusForbidden, "only this hub's standby may change accounts through it")
		return
	}
	if follows, err := s.followsPrimaryHub(ctx); err != nil || follows {
		writeAccountOpError(w, accountFailure(http.StatusServiceUnavailable, accountCodeNoAuthority,
			"this hub is not the primary hub now"))
		return
	}
	var op accountOp
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAccountOpBytes)).Decode(&op); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid account operation")
		return
	}
	result, err := s.applyAccountOp(ctx, op)
	if err != nil {
		writeAccountOpError(w, err)
		return
	}
	if s.st.IsPostgres() {
		if state, err := s.st.HubReplication(ctx); err == nil {
			result.Head = state.HeadSeq
		}
	}
	if s.log != nil && op.Op != accountOpIdentify {
		s.log.Info("account change from the standby hub", "op", op.Op, "login", core.NormalizeLogin(op.Login), "device", source.ID)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, result)
}

// Handover of a following hub's own accounts.

const accountHandoverFile = "account-handover.json"

var accountHandoverRetry = 30 * time.Second

func (s *Server) accountHandoverPath() (string, error) {
	dir, err := s.linkStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, accountHandoverFile), nil
}

// handOverAccounts runs before a following hub replaces its hub data with
// the primary's. It keeps its accounts in a file first, so they survive the
// replacement whatever happens next, then tries to hand them over at once.
func (s *Server) handOverAccounts(ctx context.Context) error {
	if s.st == nil {
		return nil
	}
	accounts, err := s.st.ExportAccountsForHandover(ctx)
	if err != nil {
		return fmt.Errorf("read accounts to hand over: %w", err)
	}
	if len(accounts) == 0 {
		return nil
	}
	if err := s.saveAccountHandover(accounts); err != nil {
		return fmt.Errorf("keep accounts to hand over: %w", err)
	}
	s.deliverAccountHandover(ctx)
	return nil
}

// saveAccountHandover adds accounts to the waiting handover file.
func (s *Server) saveAccountHandover(accounts []store.HandoverAccount) error {
	path, err := s.accountHandoverPath()
	if err != nil {
		return err
	}
	waiting, _ := loadAccountHandover(path)
	seen := map[string]bool{}
	for _, account := range waiting {
		seen[account.ID] = true
	}
	for _, account := range accounts {
		if !seen[account.ID] {
			waiting = append(waiting, account)
			seen[account.ID] = true
		}
	}
	data, err := json.Marshal(waiting)
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
	return os.Rename(tmp, path)
}

func loadAccountHandover(path string) ([]store.HandoverAccount, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var accounts []store.HandoverAccount
	if err := json.Unmarshal(data, &accounts); err != nil {
		return nil, err
	}
	return accounts, nil
}

// deliverAccountHandover hands the waiting accounts to the authority: the
// primary while this hub follows one, otherwise this hub itself (it became
// the primary before it could hand them over). It reports whether nothing
// waits any more.
func (s *Server) deliverAccountHandover(ctx context.Context) bool {
	path, err := s.accountHandoverPath()
	if err != nil {
		return true
	}
	accounts, err := loadAccountHandover(path)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	if err != nil {
		if s.log != nil {
			s.log.Warn("unreadable account handover file; leaving it in place", "path", path, "err", err)
		}
		return false
	}
	result, err := s.runAccountOp(ctx, accountOp{Op: accountOpImport, Accounts: accounts})
	if err != nil {
		if s.log != nil {
			s.log.Warn("could not hand this hub's accounts to the primary yet; will retry", "accounts", len(accounts), "err", err)
		}
		return false
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false
	}
	if s.log != nil && result.Handover != nil {
		s.log.Info("handed this hub's accounts over", "added", strings.Join(result.Handover.Added, ","),
			"already_known", result.Handover.Known, "login_conflicts", strings.Join(result.Handover.Conflicts, ","))
	}
	return true
}

// runAccountHandover retries a handover left waiting until it goes through.
func (s *Server) runAccountHandover(ctx context.Context) {
	for {
		s.deliverAccountHandover(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(accountHandoverRetry):
		}
	}
}
