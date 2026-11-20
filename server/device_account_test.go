package server

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/config"
	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
)

// desktop is the desktop app's native proxy: the bridge token plus the
// desktop marker.
type desktopClient struct {
	*browser
}

func (h accountHost) desktop(t *testing.T) desktopClient {
	b := h.bearer(t, "bridge-secret")
	return desktopClient{b}
}

func (d desktopClient) do(method, path string, body, out any) int {
	d.t.Helper()
	return d.browser.doWith(method, path, body, out, map[string]string{desktopHeader: "1"})
}

func (d desktopClient) session() authSessionResponse {
	d.t.Helper()
	var state authSessionResponse
	if code := d.do(http.MethodGet, "/api/v1/auth/session", nil, &state); code != http.StatusOK {
		d.t.Fatalf("session = %d", code)
	}
	return state
}

// A machine signs in to one account; its desktop app then acts as that
// account, while the bridge token alone stays the administrator.
func TestTheDesktopActsAsItsMachinesAccount(t *testing.T) {
	host := newAccountHost(t, true)
	desktop := host.desktop(t)
	bridge := host.bearer(t, "bridge-secret")

	state := desktop.session()
	if state.Device == nil || state.Device.SignedIn || state.Device.Authority != deviceAuthorityLocal || !state.Device.SetupAllowed {
		t.Fatalf("fresh device = %+v", state.Device)
	}
	if !state.Admin || state.Account != nil {
		t.Fatalf("an unsigned desktop = %+v", state)
	}
	// A browser cannot sign the machine in, and does not see its account.
	visitor := host.browser(t)
	if code := visitor.do(http.MethodPost, "/api/v1/auth/device/setup", map[string]string{"login": "root", "password": "root-password-1"}, nil); code != http.StatusUnauthorized && code != http.StatusForbidden {
		t.Fatalf("browser device setup = %d", code)
	}
	if visitor.session().Device != nil {
		t.Fatal("a browser saw the machine's account")
	}

	if code := desktop.do(http.MethodPost, "/api/v1/auth/device/setup", map[string]string{"login": "root", "display_name": "Root", "password": "root-password-1"}, nil); code != http.StatusOK {
		t.Fatalf("device setup = %d", code)
	}
	state = desktop.session()
	if !state.Device.SignedIn || state.Account == nil || state.Account.Login != "root" || !state.Admin {
		t.Fatalf("signed-in desktop = %+v %+v", state, state.Device)
	}
	if plain := bridge.session(); plain.Account != nil || !plain.Admin {
		t.Fatalf("the bridge token alone = %+v", plain)
	}
	record, _ := host.srv.loadDeviceAccount()
	path, _ := host.srv.deviceAccountPath()
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 || record == nil || record.Account.Login != "root" {
		t.Fatalf("device file = %v %v %+v", info, err, record)
	}
	ctx := context.Background()
	sessionAccount, _ := host.st.AuthenticateAccountSession(ctx, record.Token)
	if sessionAccount == nil {
		t.Fatal("the device session is not at the authority")
	}

	// Signing in as a member makes the desktop a member: one account per
	// machine, and the previous one is signed out.
	if _, err := host.st.CreateAccount(ctx, store.NewAccount{Login: "bob", Password: "bob-password-1"}); err != nil {
		t.Fatal(err)
	}
	if code := desktop.do(http.MethodPost, "/api/v1/auth/device/sign-in", map[string]string{"login": "bob", "password": "wrong-password-1"}, nil); code != http.StatusUnauthorized {
		t.Fatalf("wrong device sign-in = %d", code)
	}
	if code := desktop.do(http.MethodPost, "/api/v1/auth/device/sign-in", map[string]string{"login": "bob", "password": "bob-password-1"}, nil); code != http.StatusOK {
		t.Fatalf("device sign-in = %d", code)
	}
	if old, _ := host.st.AuthenticateAccountSession(ctx, record.Token); old != nil {
		t.Fatal("the machine's previous session stayed open")
	}
	if state = desktop.session(); state.Admin || state.Account == nil || state.Account.Login != "bob" {
		t.Fatalf("member desktop = %+v", state)
	}
	if code := desktop.do(http.MethodGet, "/api/v1/accounts", nil, nil); code != http.StatusForbidden {
		t.Fatalf("member desktop lists accounts = %d", code)
	}

	// Disabling the account signs the machine out at its next check.
	bob, _ := host.st.GetAccountByLogin(ctx, "bob")
	disabled := core.AccountStatusDisabled
	if _, err := host.st.UpdateAccount(ctx, bob.ID, store.AccountUpdate{Status: &disabled}); err != nil {
		t.Fatal(err)
	}
	host.srv.forgetDeviceAccountCheck()
	if state = desktop.session(); state.Device.SignedIn || state.Account != nil {
		t.Fatalf("disabled device account = %+v", state)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the device file stayed: %v", err)
	}

	// Sign out.
	if code := desktop.do(http.MethodPost, "/api/v1/auth/device/sign-in", map[string]string{"login": "root", "password": "root-password-1"}, nil); code != http.StatusOK {
		t.Fatalf("sign back in = %d", code)
	}
	if code := desktop.do(http.MethodPost, "/api/v1/auth/device/sign-out", map[string]any{}, nil); code != http.StatusOK {
		t.Fatalf("device sign-out = %d", code)
	}
	if state = desktop.session(); state.Device.SignedIn || state.Account != nil {
		t.Fatalf("signed-out device = %+v", state)
	}
}

// A node keeps no copy of the hub's accounts: it signs itself in with the
// hub's, and the hub decides whether the session still holds.
func TestANodeSignsInWithItsHubsAccount(t *testing.T) {
	const token = "node-bridge-token"
	hub := newLiveServer(t, nil)
	node := newLiveServer(t, func(cfg *config.Config) { cfg.Bridge.Enabled, cfg.Bridge.Token = true, token })
	node.nodeOfHubForTest = true
	joinNode(t, hub, node, token)
	ctx := context.Background()
	root, err := hub.st.CreateFirstAdmin(ctx, store.NewAccount{Login: "root", Password: "root-password-1"})
	if err != nil {
		t.Fatal(err)
	}
	desktop := desktopClient{&browser{t: t, host: accountHost{srv: node.Server, st: node.st, url: node.url}, client: http.DefaultClient, bearer: token, plain: true}}

	var identify identifyResponse
	if code := desktop.do(http.MethodPost, "/api/v1/auth/device/identify", map[string]string{"login": "root"}, &identify); code != http.StatusOK || identify.State != store.LoginRegistered {
		t.Fatalf("identify through the hub = %d %+v", code, identify)
	}
	// Only a hub creates the first administrator.
	if state := desktop.session(); state.Device.Authority != deviceAuthorityHub || state.Device.SetupAllowed {
		t.Fatalf("node device = %+v", state.Device)
	}
	if code := desktop.do(http.MethodPost, "/api/v1/auth/device/setup", map[string]string{"login": "other", "password": "other-password-1"}, nil); code != http.StatusForbidden {
		t.Fatalf("setup from a node = %d", code)
	}
	// While the hub's machine is signed in to nothing, a node has no
	// account to sign in with.
	if state := desktop.session(); len(state.Device.HubAccounts) != 0 {
		t.Fatalf("hub accounts before the hub signed in = %+v", state.Device)
	}
	if code := desktop.do(http.MethodPost, "/api/v1/auth/device/sign-in", map[string]string{"login": "root", "password": "root-password-1"}, nil); code != http.StatusForbidden {
		t.Fatalf("node sign-in before the hub signed in = %d", code)
	}
	signInHubMachine(t, hub, root)
	node.forgetHubAccounts()
	if state := desktop.session(); len(state.Device.HubAccounts) != 1 || state.Device.HubAccounts[0].Account.Login != "root" {
		t.Fatalf("hub accounts = %+v", state.Device.HubAccounts)
	}
	// Another account at the hub is not the hub's: it would need leaving.
	if _, err := hub.st.CreateAccount(ctx, store.NewAccount{Login: "bob", Password: "bob-password-1"}); err != nil {
		t.Fatal(err)
	}
	if code := desktop.do(http.MethodPost, "/api/v1/auth/device/sign-in", map[string]string{"login": "bob", "password": "bob-password-1"}, nil); code != http.StatusForbidden {
		t.Fatalf("node sign-in as another account = %d", code)
	}
	if code := desktop.do(http.MethodPost, "/api/v1/auth/device/sign-in", map[string]string{"login": "root", "password": "root-password-1"}, nil); code != http.StatusOK {
		t.Fatalf("node sign-in = %d", code)
	}
	if local, _ := node.st.GetAccountByLogin(ctx, "root"); local != nil {
		t.Fatal("the node made a local copy of the account")
	}
	state := desktop.session()
	if state.Account == nil || state.Account.ID != root.ID || !state.Admin {
		t.Fatalf("node desktop = %+v", state)
	}

	// The hub ends the session (a password reset): the node signs out.
	if err := hub.st.SetAccountPassword(ctx, root.ID, "new-root-password-1", ""); err != nil {
		t.Fatal(err)
	}
	node.forgetDeviceAccountCheck()
	if state = desktop.session(); state.Device.SignedIn || state.Account != nil {
		t.Fatalf("node after the hub ended its session = %+v", state)
	}
}

// The machine remembers who signed it in, after signing out too, so the
// sign-in screen can offer those accounts again. It keeps no password or
// session for them, and only the machine itself may see or change the list.
func TestTheMachineRemembersItsAccounts(t *testing.T) {
	host := newAccountHost(t, true)
	desktop := host.desktop(t)
	ctx := context.Background()

	if saved := desktop.session().Device.SavedAccounts; saved == nil || len(saved) != 0 {
		t.Fatalf("fresh saved accounts = %#v", saved)
	}
	if code := desktop.do(http.MethodPost, "/api/v1/auth/device/setup", map[string]string{"login": "root", "display_name": "Root", "password": "root-password-1"}, nil); code != http.StatusOK {
		t.Fatalf("device setup = %d", code)
	}
	if _, err := host.st.CreateAccount(ctx, store.NewAccount{Login: "bob", Password: "bob-password-1"}); err != nil {
		t.Fatal(err)
	}
	if code := desktop.do(http.MethodPost, "/api/v1/auth/device/sign-in", map[string]string{"login": "bob", "password": "bob-password-1"}, nil); code != http.StatusOK {
		t.Fatalf("device sign-in = %d", code)
	}
	if code := desktop.do(http.MethodPost, "/api/v1/auth/device/sign-out", map[string]any{}, nil); code != http.StatusOK {
		t.Fatalf("device sign-out = %d", code)
	}

	state := desktop.session()
	saved := state.Device.SavedAccounts
	if state.Device.SignedIn || len(saved) != 2 || saved[0].Login != "bob" || saved[1].Login != "root" || saved[1].DisplayName != "Root" {
		t.Fatalf("saved after sign-out = %+v", saved)
	}
	path, _ := host.srv.deviceSavedAccountsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("saved accounts file mode = %v", info.Mode().Perm())
	}
	if strings.Contains(string(data), "password") || strings.Contains(string(data), "token") {
		t.Fatalf("saved accounts keep a secret: %s", data)
	}

	visitor := host.browser(t)
	if code := visitor.do(http.MethodPost, "/api/v1/auth/device/forget", map[string]string{"login": "bob"}, nil); code != http.StatusUnauthorized && code != http.StatusForbidden {
		t.Fatalf("browser forget = %d", code)
	}
	if code := desktop.do(http.MethodPost, "/api/v1/auth/device/forget", map[string]string{"login": "Bob"}, nil); code != http.StatusOK {
		t.Fatalf("forget = %d", code)
	}
	if saved = desktop.session().Device.SavedAccounts; len(saved) != 1 || saved[0].Login != "root" {
		t.Fatalf("saved after forget = %+v", saved)
	}
}

func TestSavedAccountsKeepTheNewestFirst(t *testing.T) {
	var saved []savedDeviceAccount
	for i := 0; i < maxDeviceSavedAccounts+2; i++ {
		saved = mergeSavedAccount(saved, savedDeviceAccount{Login: fmt.Sprintf("user%d", i)})
	}
	saved = mergeSavedAccount(saved, savedDeviceAccount{Login: "user5", DisplayName: "Five"})
	if len(saved) != maxDeviceSavedAccounts || saved[0].Login != "user5" || saved[0].DisplayName != "Five" || saved[1].Login != "user9" {
		t.Fatalf("saved = %+v", saved)
	}
}

// An account signed in on this machine's own browser shows up as saved too,
// while sessions from elsewhere and phone apps do not. Removing it from the
// list sticks until it signs this machine in.
func TestSavedAccountsIncludeSessionsFromThisMachine(t *testing.T) {
	host := newAccountHost(t, true)
	desktop := host.desktop(t)
	ctx := context.Background()
	if code := desktop.do(http.MethodPost, "/api/v1/auth/device/setup", map[string]string{"login": "root", "password": "root-password-1"}, nil); code != http.StatusOK {
		t.Fatalf("device setup = %d", code)
	}
	if code := desktop.do(http.MethodPost, "/api/v1/auth/device/sign-out", map[string]any{}, nil); code != http.StatusOK {
		t.Fatalf("device sign-out = %d", code)
	}
	newAccount := func(login, name string) *core.Account {
		account, err := host.st.CreateAccount(ctx, store.NewAccount{Login: login, DisplayName: name, Password: login + "-password-1"})
		if err != nil {
			t.Fatal(err)
		}
		return account
	}
	admin := newAccount("admin", "Admin")
	remote := newAccount("remote", "")
	phone := newAccount("phone", "")
	for _, session := range []struct {
		account     *core.Account
		agent, addr string
	}{
		{admin, "Mozilla/5.0 (Macintosh)", "127.0.0.1"},
		{remote, "Mozilla/5.0 (Windows)", "10.1.2.3"},
		{phone, "okhttp/4.12.0", "127.0.0.1"},
	} {
		if _, _, err := host.st.CreateAccountSession(ctx, session.account.ID, core.SignInMethodPassword, session.agent, session.addr, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	logins := func() []string {
		var out []string
		for _, entry := range desktop.session().Device.SavedAccounts {
			out = append(out, entry.Login)
		}
		return out
	}
	if got := logins(); len(got) != 2 || got[0] != "admin" || got[1] != "root" {
		t.Fatalf("saved = %v", got)
	}
	if code := desktop.do(http.MethodPost, "/api/v1/auth/device/forget", map[string]string{"login": "admin"}, nil); code != http.StatusOK {
		t.Fatalf("forget = %d", code)
	}
	if got := logins(); len(got) != 1 || got[0] != "root" {
		t.Fatalf("saved after forget = %v", got)
	}
	if code := desktop.do(http.MethodPost, "/api/v1/auth/device/sign-in", map[string]string{"login": "admin", "password": "admin-password-1"}, nil); code != http.StatusOK {
		t.Fatalf("admin device sign-in = %d", code)
	}
	if got := logins(); len(got) != 2 || got[0] != "admin" {
		t.Fatalf("saved after signing in again = %v", got)
	}
}

func TestHiddenAccountsDoNotCrowdOutSavedOnes(t *testing.T) {
	var saved []savedDeviceAccount
	for i := 0; i < maxDeviceSavedAccounts; i++ {
		saved = mergeSavedAccount(saved, savedDeviceAccount{Login: fmt.Sprintf("user%d", i)})
	}
	for i := 0; i < 5; i++ {
		saved = mergeSavedAccount(saved, savedDeviceAccount{Login: fmt.Sprintf("gone%d", i), Hidden: true})
	}
	visible := 0
	for _, entry := range saved {
		if !entry.Hidden {
			visible++
		}
	}
	if visible != maxDeviceSavedAccounts || len(saved) != maxDeviceSavedAccounts+5 {
		t.Fatalf("saved = %+v", saved)
	}
}

// signInHubMachine signs the hub's own machine in as account, as its
// desktop app would.
func signInHubMachine(t *testing.T, hub liveServer, account *core.Account) {
	t.Helper()
	token, _, err := hub.st.CreateAccountSession(context.Background(), account.ID, core.SignInMethodDevice, deviceUserAgent(), "127.0.0.1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := hub.saveDeviceAccount(deviceAccountRecord{Account: *account, Token: token, Authority: deviceAuthorityLocal, SignedInAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

func newLinkedNode(t *testing.T) (liveServer, liveServer, desktopClient, *core.Account) {
	t.Helper()
	const token = "node-bridge-token"
	hub := newLiveServer(t, nil)
	node := newLiveServer(t, func(cfg *config.Config) { cfg.Bridge.Enabled, cfg.Bridge.Token = true, token })
	node.nodeOfHubForTest = true
	joinNode(t, hub, node, token)
	root, err := hub.st.CreateFirstAdmin(context.Background(), store.NewAccount{Login: "root", DisplayName: "Root", Password: "root-password-1"})
	if err != nil {
		t.Fatal(err)
	}
	signInHubMachine(t, hub, root)
	desktop := desktopClient{&browser{t: t, host: accountHost{srv: node.Server, st: node.st, url: node.url}, client: http.DefaultClient, bearer: token, plain: true}}
	return hub, node, desktop, root
}

// A node signs in with the hub's account once a hub approves it, without
// its password; turned down, it stays signed out.
func TestAHubApprovesANodesSignIn(t *testing.T) {
	hub, node, desktop, root := newLinkedNode(t)
	ctx := context.Background()

	ask := func() string {
		var asked struct {
			Approval deviceApprovalView `json:"approval"`
		}
		if code := desktop.do(http.MethodPost, "/api/v1/auth/device/approval", map[string]string{"login": "root"}, &asked); code != http.StatusOK || asked.Approval.ID == "" {
			t.Fatalf("ask for approval = %d %+v", code, asked)
		}
		return asked.Approval.ID
	}
	status := func(id string) int {
		return desktop.do(http.MethodGet, "/api/v1/auth/device/approval?id="+id, nil, nil)
	}

	id := ask()
	waiting, _ := hub.runAccountOp(ctx, accountOp{Op: accountOpListApprovals, AccountID: root.ID})
	if len(waiting.Approvals) != 1 || waiting.Approvals[0].Login != "root" || waiting.Approvals[0].Machine == "" {
		t.Fatalf("waiting at the hub = %+v", waiting.Approvals)
	}
	// Someone else's account cannot decide it.
	if _, err := hub.runAccountOp(ctx, accountOp{Op: accountOpDecideApproval, AccountID: "acct_other", ApprovalID: id, Approve: true}); err == nil {
		t.Fatal("another account approved the sign-in")
	}
	if code := status(id); code != http.StatusOK || desktop.session().Device.SignedIn {
		t.Fatalf("pending status = %d", code)
	}
	if _, err := hub.runAccountOp(ctx, accountOp{Op: accountOpDecideApproval, AccountID: root.ID, ApprovalID: id, Approve: false}); err != nil {
		t.Fatal(err)
	}
	if code := status(id); code != http.StatusForbidden {
		t.Fatalf("denied status = %d", code)
	}

	id = ask()
	if _, err := hub.runAccountOp(ctx, accountOp{Op: accountOpDecideApproval, AccountID: root.ID, ApprovalID: id, Approve: true}); err != nil {
		t.Fatal(err)
	}
	if code := status(id); code != http.StatusOK {
		t.Fatalf("approved status = %d", code)
	}
	if state := desktop.session(); !state.Device.SignedIn || state.Account == nil || state.Account.ID != root.ID {
		t.Fatalf("node after approval = %+v", state.Device)
	}
	// The secret was used up: the same request cannot sign in again.
	if code := status(id); code != http.StatusGone {
		t.Fatalf("collecting twice = %d", code)
	}
	if local, _ := node.st.GetAccountByLogin(ctx, "root"); local != nil {
		t.Fatal("the node made a local copy of the account")
	}
}

// A node that wants another account leaves the hub, and cannot take a login
// name the hub group already uses.
func TestANodeLeavesTheHubForAnAccountOfItsOwn(t *testing.T) {
	_, node, desktop, _ := newLinkedNode(t)
	ctx := context.Background()
	if code := desktop.do(http.MethodPost, "/api/v1/auth/device/leave-hub", map[string]string{"login": "root", "password": "other-password-1"}, nil); code != http.StatusConflict {
		t.Fatalf("leave with a taken name = %d", code)
	}
	if node.node.uplink() == nil {
		t.Fatal("a refused leave left the hub")
	}
	if code := desktop.do(http.MethodPost, "/api/v1/auth/device/leave-hub", map[string]string{"login": "solo", "display_name": "Solo", "password": "solo-password-1"}, nil); code != http.StatusOK {
		t.Fatalf("leave = %d", code)
	}
	node.nodeOfHubForTest = false
	if node.node.uplink() != nil {
		t.Fatal("the node is still linked")
	}
	solo, _ := node.st.GetAccountByLogin(ctx, "solo")
	if solo == nil || solo.Role != core.AccountRoleAdmin {
		t.Fatalf("local account = %+v", solo)
	}
	if state := desktop.session(); !state.Device.SignedIn || state.Account == nil || state.Account.Login != "solo" || state.Device.Authority != deviceAuthorityLocal {
		t.Fatalf("node after leaving = %+v %+v", state, state.Device)
	}
}

// The standby hub's machine counts as the hub's too: its device session at
// the primary names its host.
func TestTheStandbysAccountIsTheHubsAccount(t *testing.T) {
	const token = "node-bridge-token"
	hub := newLiveServer(t, nil)
	standby := newLiveServer(t, func(cfg *config.Config) { cfg.Bridge.Enabled, cfg.Bridge.Token = true, token })
	joinNode(t, hub, standby, token)
	ctx := context.Background()
	devices, err := hub.st.ListDevices(ctx)
	if err != nil || len(devices) != 1 || devices[0].Hostname == "" {
		t.Fatalf("devices = %+v %v", devices, err)
	}
	hub.standbyForTest = devices[0].ID
	ning, err := hub.st.CreateFirstAdmin(ctx, store.NewAccount{Login: "ning", Password: "ning-password-1"})
	if err != nil {
		t.Fatal(err)
	}
	if accounts, _ := hub.hubMachineAccounts(ctx); len(accounts) != 0 {
		t.Fatalf("hub accounts before anyone signed in = %+v", accounts)
	}
	if _, _, err := hub.st.CreateAccountSession(ctx, ning.ID, core.SignInMethodDevice, deviceAgentFor(devices[0].Hostname), "127.0.0.1", time.Hour); err != nil {
		t.Fatal(err)
	}
	accounts, err := hub.hubMachineAccounts(ctx)
	if err != nil || len(accounts) != 1 || accounts[0].Account.Login != "ning" || accounts[0].Machine != devices[0].Name {
		t.Fatalf("hub accounts = %+v %v", accounts, err)
	}
}
