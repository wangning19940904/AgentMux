package server

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strings"
	"testing"

	"github.com/wangning19940904/AgentMux/config"
	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
)

// Sign-in asks for the name first. An unknown name registers, waits for an
// administrator, and signs in once approved; a turned-down registration
// frees the name.
func TestIdentifyRegisterAndApprove(t *testing.T) {
	host := newAccountHost(t, true)
	bridge := host.bearer(t, "bridge-secret")
	visitor := host.browser(t)

	identify := func(login string) identifyResponse {
		t.Helper()
		var answer identifyResponse
		visitor.mustOK(http.MethodPost, "/api/v1/auth/identify", map[string]string{"login": login}, &answer)
		return answer
	}
	if answer := identify("alice"); answer.State != store.LoginUnregistered || !answer.SetupRequired || answer.RegistrationOpen {
		t.Fatalf("identify before any account = %+v", answer)
	}
	// Nobody registers before the first administrator exists.
	registration := map[string]string{"login": "alice", "display_name": "Alice", "password": "alice-password-1"}
	if code := visitor.do(http.MethodPost, "/api/v1/auth/register", registration, nil); code != http.StatusConflict {
		t.Fatalf("register before setup = %d", code)
	}
	bridge.mustOK(http.MethodPost, "/api/v1/auth/setup", map[string]string{"login": "root", "password": "root-password-1"}, nil)

	if answer := identify("ROOT"); answer.State != store.LoginRegistered || answer.SetupRequired || answer.Login != "root" {
		t.Fatalf("identify root = %+v", answer)
	}
	if answer := identify("alice"); answer.State != store.LoginUnregistered || !answer.RegistrationOpen {
		t.Fatalf("identify alice = %+v", answer)
	}
	// Registering is a console request, like sign-in.
	plain := host.browser(t)
	plain.plain = true
	if code := plain.do(http.MethodPost, "/api/v1/auth/register", registration, nil); code != http.StatusForbidden {
		t.Fatalf("cross-site register = %d", code)
	}
	visitor.mustOK(http.MethodPost, "/api/v1/auth/register", registration, nil)
	if code := visitor.do(http.MethodPost, "/api/v1/auth/register", registration, nil); code != http.StatusConflict {
		t.Fatalf("second registration = %d", code)
	}
	if answer := identify("alice"); answer.State != store.LoginPending {
		t.Fatalf("identify pending alice = %+v", answer)
	}
	// The right password tells a waiting person why they cannot come in;
	// a wrong one says nothing more than for anyone else.
	if code := visitor.signIn("alice", "alice-password-1"); code != http.StatusForbidden {
		t.Fatalf("pending sign-in = %d", code)
	}
	if code := visitor.signIn("alice", "wrong-password-1"); code != http.StatusUnauthorized {
		t.Fatalf("pending wrong sign-in = %d", code)
	}

	if state := bridge.session(); state.PendingRegistrations != 1 || !state.RegistrationOpen || state.AccountsAuthority != accountsAuthorityLocal {
		t.Fatalf("admin state = %+v", state)
	}
	alice, _ := host.st.GetAccountByLogin(context.Background(), "alice")
	if alice == nil || alice.Status != core.AccountStatusPending || alice.Role != core.AccountRoleMember {
		t.Fatalf("alice = %+v", alice)
	}
	// Members cannot approve.
	if code := visitor.do(http.MethodPost, "/api/v1/accounts", map[string]any{"id": alice.ID, "status": "active"}, nil); code != http.StatusUnauthorized {
		t.Fatalf("approval without credentials = %d", code)
	}
	bridge.mustOK(http.MethodPost, "/api/v1/accounts", map[string]any{"id": alice.ID, "status": "active"}, nil)
	if code := visitor.signIn("alice", "alice-password-1"); code != http.StatusOK {
		t.Fatalf("approved sign-in = %d", code)
	}
	if state := visitor.session(); state.Account == nil || state.Account.Login != "alice" || state.Admin {
		t.Fatalf("alice signed in = %+v", state)
	}
	// An approved account is not a registration any more.
	if code := bridge.do(http.MethodPost, "/api/v1/accounts/reject", map[string]string{"id": alice.ID}, nil); code != http.StatusConflict {
		t.Fatalf("reject active = %d", code)
	}

	// Turning one down frees the name.
	other := host.browser(t)
	other.mustOK(http.MethodPost, "/api/v1/auth/register", map[string]string{"login": "mallory", "password": "mallory-password-1"}, nil)
	mallory, _ := host.st.GetAccountByLogin(context.Background(), "mallory")
	bridge.mustOK(http.MethodPost, "/api/v1/accounts/reject", map[string]string{"id": mallory.ID}, nil)
	if answer := identify("mallory"); answer.State != store.LoginUnregistered {
		t.Fatalf("rejected mallory = %+v", answer)
	}
}

// First-run setup on a hub whose authority already has accounts signs the
// person in with theirs instead of failing.
func TestSetupSignsInAnExistingAccount(t *testing.T) {
	host := newAccountHost(t, false)
	local := host.browser(t)
	local.mustOK(http.MethodPost, "/api/v1/auth/setup", map[string]string{"login": "root", "password": "root-password-1"}, nil)

	again := host.browser(t)
	var answer struct {
		Existing bool         `json:"existing"`
		Account  core.Account `json:"account"`
	}
	again.mustOK(http.MethodPost, "/api/v1/auth/setup", map[string]string{"login": "root", "password": "root-password-1"}, &answer)
	if !answer.Existing || answer.Account.Login != "root" {
		t.Fatalf("setup with an existing account = %+v", answer)
	}
	if state := again.session(); state.Account == nil || state.Account.Login != "root" {
		t.Fatalf("not signed in after setup found the account: %+v", state)
	}
	if code := again.do(http.MethodPost, "/api/v1/auth/setup", map[string]string{"login": "root", "password": "wrong-password-1"}, nil); code != http.StatusConflict {
		t.Fatalf("setup with a wrong password = %d", code)
	}
}

// A standby keeps no accounts of its own: setup, registration, sign-in and
// approvals made through it happen on the primary, and only the standby may
// ask the primary to.
func TestAStandbyChangesAccountsOnThePrimary(t *testing.T) {
	const token = "node-bridge-token"
	withToken := func(cfg *config.Config) { cfg.Bridge.Enabled, cfg.Bridge.Token = true, token }
	hub := newLiveServer(t, nil)
	standby := newLiveServer(t, withToken)
	standby.followsForTest = true
	node := newLiveServer(t, withToken)
	standbyID := joinNode(t, hub, standby, token)
	joinNode(t, hub, node, token)
	hub.standbyForTest = standbyID
	ctx := context.Background()

	browserOf := func(server liveServer) *browser {
		jar, _ := cookiejar.New(nil)
		return &browser{t: t, host: accountHost{srv: server.Server, st: server.st, url: server.url}, client: &http.Client{Jar: jar}}
	}
	admin := browserOf(standby)
	admin.bearer = token
	var setup struct {
		Account core.Account `json:"account"`
	}
	admin.mustOK(http.MethodPost, "/api/v1/auth/setup", map[string]string{"login": "root", "password": "root-password-1"}, &setup)
	if onHub, _ := hub.st.GetAccountByLogin(ctx, "root"); onHub == nil || onHub.ID != setup.Account.ID || !onHub.IsAdmin() {
		t.Fatalf("the first administrator is not on the primary: %+v", onHub)
	}
	if onStandby, _ := standby.st.GetAccountByLogin(ctx, "root"); onStandby != nil {
		t.Fatal("the standby wrote the account itself")
	}
	if state := admin.session(); state.AccountsAuthority != accountsAuthorityPrimary {
		t.Fatalf("standby state = %+v", state)
	}

	// Registration through the standby lands on the primary.
	visitor := browserOf(standby)
	visitor.mustOK(http.MethodPost, "/api/v1/auth/register", map[string]string{"login": "alice", "password": "alice-password-1"}, nil)
	alice, _ := hub.st.GetAccountByLogin(ctx, "alice")
	if alice == nil || alice.Status != core.AccountStatusPending {
		t.Fatalf("alice on the primary = %+v", alice)
	}
	if code := visitor.signIn("alice", "alice-password-1"); code != http.StatusForbidden {
		t.Fatalf("pending sign-in through the standby = %d", code)
	}
	admin.mustOK(http.MethodPost, "/api/v1/accounts", map[string]any{"id": alice.ID, "status": "active"}, nil)
	// The primary checks the password and opens the session; the standby
	// hands the browser its cookie.
	if code := visitor.signIn("alice", "alice-password-1"); code != http.StatusOK {
		t.Fatalf("sign-in through the standby = %d", code)
	}
	if code := visitor.signIn("alice", "wrong-password-1"); code != http.StatusUnauthorized {
		t.Fatalf("wrong password through the standby = %d", code)
	}
	if alice, _ = hub.st.GetAccount(ctx, alice.ID); alice.LastSignInAt == nil {
		t.Fatal("the session was not opened on the primary")
	}

	// A plain node may not change the primary's accounts.
	var refused map[string]string
	client := node.node.uplink()
	if client == nil {
		t.Fatal("the node has no uplink")
	}
	request, _ := http.NewRequest(http.MethodPost, "http://hub"+accountsUplinkPath,
		strings.NewReader(`{"op":"save","account_id":"`+alice.ID+`","role":"admin"}`))
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("node account operation = %d %v", response.StatusCode, refused)
	}
}

// A hub that starts following hands its own accounts to the primary first;
// the primary keeps those it lacks and never lets a handover take a login
// that is someone else's there.
func TestAFollowingHubHandsItsAccountsOver(t *testing.T) {
	const token = "node-bridge-token"
	withToken := func(cfg *config.Config) { cfg.Bridge.Enabled, cfg.Bridge.Token = true, token }
	hub := newLiveServer(t, nil)
	standby := newLiveServer(t, withToken)
	standby.followsForTest = true
	standbyID := joinNode(t, hub, standby, token)
	hub.standbyForTest = standbyID
	ctx := context.Background()

	if _, err := hub.st.CreateFirstAdmin(ctx, store.NewAccount{Login: "root", Password: "root-password-1"}); err != nil {
		t.Fatal(err)
	}
	// The standby had accounts of its own before it followed.
	mine, err := standby.st.CreateFirstAdmin(ctx, store.NewAccount{Login: "ning", Password: "ning-password-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := standby.st.CreateAccount(ctx, store.NewAccount{Login: "root", Password: "other-password-1"}); err != nil {
		t.Fatal(err)
	}

	if err := standby.handOverAccounts(ctx); err != nil {
		t.Fatal(err)
	}
	ning, _ := hub.st.GetAccountByLogin(ctx, "ning")
	if ning == nil || ning.ID != mine.ID || !ning.IsAdmin() {
		t.Fatalf("ning on the primary = %+v", ning)
	}
	if signedIn, _ := hub.st.VerifyAccountPassword(ctx, "ning", "ning-password-1"); signedIn == nil {
		t.Fatal("the handed-over password does not sign in on the primary")
	}
	if root, _ := hub.st.VerifyAccountPassword(ctx, "root", "other-password-1"); root != nil {
		t.Fatal("a handover took over the primary's root")
	}
	path, _ := standby.accountHandoverPath()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the handover file stayed after delivery: %v", err)
	}

	// While the primary cannot be reached the accounts wait in the file
	// and go once it can.
	standby.node.stop()
	extra, err := standby.st.CreateAccount(ctx, store.NewAccount{Login: "late", Password: "late-password-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := standby.handOverAccounts(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("no handover file while the primary is away: %v", err)
	}
	// It became the primary itself before it could hand them over: it
	// keeps them.
	standby.followsForTest = false
	if !standby.deliverAccountHandover(ctx) {
		t.Fatal("the waiting handover was not delivered")
	}
	if kept, _ := standby.st.GetAccount(ctx, extra.ID); kept == nil {
		t.Fatal("late account lost")
	}
}
