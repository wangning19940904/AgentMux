package server

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
)

// Saved accounts.
//
// Every account that signs this machine in is remembered in a local file next
// to the device account, so the sign-in screen can offer it again after the
// machine signs out. Only who it was is kept: the login name, display name
// and when it last signed in, never a password or a session. Picking one
// still asks for its password.
//
// The screen also offers accounts with an active session from this machine:
// the machine's own sign-in (its user agent names this host) and browsers on
// it (a loopback address, not a phone app). The loopback test is approximate:
// sessions are replicated between hubs, so a browser on another hub's own
// machine looks local too.
//
//	POST /api/v1/auth/device/forget — remove one login from this machine's list

const (
	deviceSavedAccountsFile = "device-saved-accounts.json"
	maxDeviceSavedAccounts  = 8
	// maxDeviceHiddenAccounts bounds the logins removed from the list, kept so
	// an account with an active session here does not come straight back.
	maxDeviceHiddenAccounts = 32
)

// savedDeviceAccount is one remembered account, as the sign-in screen shows it.
type savedDeviceAccount struct {
	Login        string    `json:"login"`
	DisplayName  string    `json:"display_name,omitempty"`
	LastSignedIn time.Time `json:"last_signed_in_at"`
	// Hidden: removed from the list. Signing in with it again shows it again.
	Hidden bool `json:"hidden,omitempty"`
}

func (s *Server) deviceSavedAccountsPath() (string, error) {
	dir, err := s.linkStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, deviceSavedAccountsFile), nil
}

func (s *Server) loadDeviceSavedAccounts() ([]savedDeviceAccount, error) {
	path, err := s.deviceSavedAccountsPath()
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
	var saved []savedDeviceAccount
	if err := json.Unmarshal(data, &saved); err != nil {
		return nil, err
	}
	return saved, nil
}

func (s *Server) writeDeviceSavedAccounts(saved []savedDeviceAccount) error {
	path, err := s.deviceSavedAccountsPath()
	if err != nil {
		return err
	}
	data, err := json.Marshal(saved)
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

// mergeSavedAccount puts account first in saved, replacing an older entry
// for the same login, and keeps the list short.
func mergeSavedAccount(saved []savedDeviceAccount, account savedDeviceAccount) []savedDeviceAccount {
	next := []savedDeviceAccount{account}
	visible, hidden := 0, 0
	if !account.Hidden {
		visible++
	}
	for _, entry := range saved {
		if entry.Login == account.Login {
			continue
		}
		if entry.Hidden {
			if hidden < maxDeviceHiddenAccounts {
				hidden++
				next = append(next, entry)
			}
		} else if visible < maxDeviceSavedAccounts {
			visible++
			next = append(next, entry)
		}
	}
	return next
}

// rememberDeviceAccount records an account that just signed this machine in.
func (s *Server) rememberDeviceAccount(account core.Account, at time.Time) {
	s.deviceSaved.Lock()
	defer s.deviceSaved.Unlock()
	saved, err := s.loadDeviceSavedAccounts()
	if err != nil && s.log != nil {
		s.log.Warn("unreadable saved accounts; starting the list again", "err", err)
	}
	saved = mergeSavedAccount(saved, savedDeviceAccount{
		Login: account.Login, DisplayName: account.DisplayName, LastSignedIn: at,
	})
	if err := s.writeDeviceSavedAccounts(saved); err != nil && s.log != nil {
		s.log.Warn("could not remember this machine's account", "login", account.Login, "err", err)
	}
}

// deviceSavedAccounts lists the accounts the sign-in screen offers, most
// recent first: the remembered ones, the one this machine is signed in to
// (a machine signed in before the list existed), and those with an active
// session from this machine, less the ones removed from the list.
func (s *Server) deviceSavedAccounts(ctx context.Context) []savedDeviceAccount {
	here := s.accountsSignedInHere(ctx)
	s.deviceSaved.Lock()
	defer s.deviceSaved.Unlock()
	stored, err := s.loadDeviceSavedAccounts()
	if err != nil && s.log != nil {
		s.log.Warn("unreadable saved accounts", "err", err)
	}
	listed := map[string]bool{}
	saved := []savedDeviceAccount{}
	for _, entry := range stored {
		listed[entry.Login] = true
		if !entry.Hidden {
			saved = append(saved, entry)
		}
	}
	add := func(entry savedDeviceAccount) {
		if !listed[entry.Login] {
			listed[entry.Login] = true
			saved = append(saved, entry)
		}
	}
	if record, _ := s.loadDeviceAccount(); record != nil {
		add(savedDeviceAccount{Login: record.Account.Login, DisplayName: record.Account.DisplayName, LastSignedIn: record.SignedInAt})
	}
	for _, entry := range here {
		add(entry)
	}
	sort.SliceStable(saved, func(i, j int) bool { return saved[i].LastSignedIn.After(saved[j].LastSignedIn) })
	if len(saved) > maxDeviceSavedAccounts {
		saved = saved[:maxDeviceSavedAccounts]
	}
	return saved
}

// accountsSignedInHere are the active accounts with an unexpired session
// from this machine, in this hub's own copy of the sessions.
func (s *Server) accountsSignedInHere(ctx context.Context) []savedDeviceAccount {
	if s.st == nil {
		return nil
	}
	sessions, err := s.st.ListAccountSessions(ctx, "", "")
	if err != nil {
		if s.log != nil {
			s.log.Warn("could not list sessions for saved accounts", "err", err)
		}
		return nil
	}
	machine := deviceUserAgent()
	latest := map[string]time.Time{}
	for _, session := range sessions {
		if !sessionFromThisMachine(session, machine) {
			continue
		}
		at := session.CreatedAt
		if session.LastSeenAt != nil && session.LastSeenAt.After(at) {
			at = *session.LastSeenAt
		}
		if previous, ok := latest[session.AccountID]; !ok || at.After(previous) {
			latest[session.AccountID] = at
		}
	}
	if len(latest) == 0 {
		return nil
	}
	accounts, err := s.st.ListAccounts(ctx)
	if err != nil {
		return nil
	}
	var here []savedDeviceAccount
	for _, account := range accounts {
		if at, ok := latest[account.ID]; ok && account.Status == core.AccountStatusActive {
			here = append(here, savedDeviceAccount{Login: account.Login, DisplayName: account.DisplayName, LastSignedIn: at})
		}
	}
	return here
}

// sessionFromThisMachine: a machine sign-in names its host in its user
// agent; a browser session counts when it came over loopback and is not a
// phone app's.
func sessionFromThisMachine(session store.AccountSessionInfo, machineAgent string) bool {
	if session.Method == core.SignInMethodDevice {
		return session.UserAgent == machineAgent
	}
	if _, phone := parsePhoneAgent(session.UserAgent); phone {
		return false
	}
	ip := net.ParseIP(session.RemoteAddr)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) handleDeviceForget(w http.ResponseWriter, r *http.Request) {
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
	s.deviceSaved.Lock()
	defer s.deviceSaved.Unlock()
	saved, err := s.loadDeviceSavedAccounts()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Kept as hidden, so an account with a session here stays off the list.
	entry := savedDeviceAccount{Login: login, Hidden: true}
	for _, existing := range saved {
		if existing.Login == login {
			entry.DisplayName, entry.LastSignedIn = existing.DisplayName, existing.LastSignedIn
		}
	}
	if err := s.writeDeviceSavedAccounts(mergeSavedAccount(saved, entry)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w)
}
