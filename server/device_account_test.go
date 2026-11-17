package server

import (
	"context"
	"net/http"
	"os"
	"testing"

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
