package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
)

// accountHost runs a server behind a real listener so cookies, the Console
// header and the auth middleware all take part.
type accountHost struct {
	srv *Server
	st  *store.Store
	url string
}

func newAccountHost(t *testing.T, bridge bool) accountHost {
	t.Helper()
	srv, st := newTestServer(t)
	srv.cfg.Hub.StateDir = t.TempDir()
	srv.cfg.Bridge.Enabled = bridge
	srv.cfg.Bridge.Token = "bridge-secret"
	listener := httptest.NewServer(srv.withAuth(srv.mux))
	t.Cleanup(listener.Close)
	t.Cleanup(srv.node.stop)
	if srv.linkHub != nil {
		t.Cleanup(srv.linkHub.Close)
	}
	return accountHost{srv: srv, st: st, url: listener.URL}
}

// browser is one Console tab: it keeps cookies and sends the Console header.
type browser struct {
	t      *testing.T
	host   accountHost
	client *http.Client
	// bearer, when set, is sent as the Authorization header instead.
	bearer string
	// plain leaves out the Console header, as a cross-site request would.
	plain bool
}

func (h accountHost) browser(t *testing.T) *browser {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &browser{t: t, host: h, client: &http.Client{Jar: jar}}
}

func (h accountHost) bearer(t *testing.T, token string) *browser {
	b := h.browser(t)
	b.bearer = token
	b.plain = true
	return b
}

func (b *browser) do(method, path string, body any, out any) int {
	b.t.Helper()
	var payload io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			b.t.Fatal(err)
		}
		payload = bytes.NewReader(data)
	}
	request, err := http.NewRequest(method, b.host.url+path, payload)
	if err != nil {
		b.t.Fatal(err)
	}
	if !b.plain {
		request.Header.Set(consoleCSRFHeader, "1")
	}
	if b.bearer != "" {
		request.Header.Set("Authorization", "Bearer "+b.bearer)
	}
	response, err := b.client.Do(request)
	if err != nil {
		b.t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	if out != nil && response.StatusCode < 300 {
		if err := json.Unmarshal(data, out); err != nil {
			b.t.Fatalf("%s %s: decode %q: %v", method, path, data, err)
		}
	}
	return response.StatusCode
}

func (b *browser) session() authSessionResponse {
	b.t.Helper()
	var state authSessionResponse
	if code := b.do(http.MethodGet, "/api/v1/auth/session", nil, &state); code != http.StatusOK {
		b.t.Fatalf("auth session = %d", code)
	}
	return state
}

func (b *browser) signIn(login, password string) int {
	b.t.Helper()
	return b.do(http.MethodPost, "/api/v1/auth/sign-in", map[string]string{"login": login, "password": password}, nil)
}

func (b *browser) mustOK(method, path string, body, out any) {
	b.t.Helper()
	if code := b.do(method, path, body, out); code != http.StatusOK {
		b.t.Fatalf("%s %s = %d", method, path, code)
	}
}

func TestAccountsFromFirstRunToMember(t *testing.T) {
	host := newAccountHost(t, true)
	ctx := context.Background()
	admin := host.browser(t)

	// Before any account exists nothing changes: a browser without a
	// credential is not signed in and is not asked to.
	state := admin.session()
	if state.AccountsEnabled || state.SignInRequired || state.Authenticated || state.SetupAvailable {
		t.Fatalf("fresh state = %+v", state)
	}
	if code := admin.do(http.MethodGet, "/api/v1/agent-instances", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated agent list = %d", code)
	}

	// The first administrator needs administrator credentials.
	firstAdmin := map[string]string{"login": "root", "display_name": "Root", "password": "root-password-1"}
	if code := admin.do(http.MethodPost, "/api/v1/auth/setup", firstAdmin, nil); code != http.StatusUnauthorized {
		t.Fatalf("setup without credentials = %d", code)
	}
	bridge := host.bearer(t, "bridge-secret")
	if state := bridge.session(); !state.SetupAvailable || !state.Admin || state.Account != nil {
		t.Fatalf("bridge state = %+v", state)
	}
	bridge.mustOK(http.MethodPost, "/api/v1/auth/setup", firstAdmin, nil)
	if code := bridge.do(http.MethodPost, "/api/v1/auth/setup", map[string]string{"login": "again", "password": "again-password-1"}, nil); code != http.StatusConflict {
		t.Fatalf("second setup = %d", code)
	}

	// Now the browser must sign in.
	if state := admin.session(); !state.AccountsEnabled || !state.SignInRequired {
		t.Fatalf("state with accounts = %+v", state)
	}
	if code := admin.signIn("root", "wrong-password"); code != http.StatusUnauthorized {
		t.Fatalf("wrong password = %d", code)
	}
	if code := admin.signIn("ROOT", "root-password-1"); code != http.StatusOK {
		t.Fatalf("sign-in = %d", code)
	}
	state = admin.session()
	if state.SignInRequired || !state.Admin || state.Account == nil || state.Account.Login != "root" || state.Account.Role != core.AccountRoleAdmin {
		t.Fatalf("signed-in admin state = %+v", state)
	}
	// The administrator creates a member; without a password one is made.
	var created struct {
		Account  core.Account `json:"account"`
		Password string       `json:"password"`
	}
	admin.mustOK(http.MethodPost, "/api/v1/accounts", map[string]any{"login": "bob", "display_name": "Bob"}, &created)
	if created.Account.Role != core.AccountRoleMember || len(created.Password) < core.MinPasswordLength {
		t.Fatalf("created = %+v", created)
	}
	var accounts []core.Account
	admin.mustOK(http.MethodGet, "/api/v1/accounts", nil, &accounts)
	if len(accounts) != 2 {
		t.Fatalf("accounts = %+v", accounts)
	}

	now := time.Now().UTC()
	for _, agent := range []core.AgentInstance{{ID: "agent-one", Name: "one"}, {ID: "agent-two", Name: "two"}} {
		agent.RuntimeID, agent.Enabled, agent.CreatedAt, agent.UpdatedAt = "codex", true, now, now
		if err := host.st.UpsertAgentInstance(ctx, &agent); err != nil {
			t.Fatal(err)
		}
	}

	member := host.browser(t)
	if code := member.signIn("bob", created.Password); code != http.StatusOK {
		t.Fatalf("member sign-in = %d", code)
	}
	state = member.session()
	if state.Admin || state.Account == nil || state.Account.Role != core.AccountRoleMember {
		t.Fatalf("member state = %+v", state)
	}
	// A member reaches discovery and their own things only.
	var capabilities struct {
		Auth   map[string]any `json:"auth"`
		Agents map[string]any `json:"agents"`
	}
	member.mustOK(http.MethodGet, "/api/v1/capabilities", nil, &capabilities)
	if capabilities.Auth["scope"] != "member" || capabilities.Agents["count"] != float64(0) {
		t.Fatalf("member capabilities = %+v", capabilities)
	}
	member.mustOK(http.MethodGet, "/api/v1/status", nil, nil)
	// Everything else is reserved for administrators.
	for _, path := range []string{"/api/v1/agent-instances", "/api/v1/channels", "/api/v1/triggers", "/api/v1/accounts", "/api/v1/remote/hosts", "/api/v1/link", "/api/v1/usage"} {
		if code := member.do(http.MethodGet, path, nil, nil); code != http.StatusForbidden {
			t.Fatalf("member GET %s = %d", path, code)
		}
	}
	if code := member.do(http.MethodPost, "/api/v1/agent-instances", map[string]any{"name": "new", "runtime_id": "codex"}, nil); code != http.StatusForbidden {
		t.Fatalf("member creating an agent = %d", code)
	}
	if code := member.do(http.MethodPost, "/api/v1/accounts", map[string]any{"login": "eve", "role": "admin"}, nil); code != http.StatusForbidden {
		t.Fatalf("member creating an account = %d", code)
	}
	// The administrator sees everything.
	var all []core.AgentInstance
	admin.mustOK(http.MethodGet, "/api/v1/agent-instances", nil, &all)
	if len(all) != 2 {
		t.Fatalf("admin sees %d agents", len(all))
	}

	// The bridge token stays the administrator with accounts present.
	var bridgeAgents []core.AgentInstance
	bridge.mustOK(http.MethodGet, "/api/v1/agent-instances", nil, &bridgeAgents)
	if len(bridgeAgents) != 2 {
		t.Fatalf("bridge sees %d agents", len(bridgeAgents))
	}

	// Disabling the member ends their session at once.
	admin.mustOK(http.MethodPost, "/api/v1/accounts", map[string]any{"id": created.Account.ID, "status": "disabled"}, nil)
	if code := member.do(http.MethodGet, "/api/v1/agent-instances", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("disabled member request = %d", code)
	}
	if code := member.signIn("bob", created.Password); code != http.StatusUnauthorized {
		t.Fatalf("disabled member sign-in = %d", code)
	}
	admin.mustOK(http.MethodPost, "/api/v1/accounts", map[string]any{"id": created.Account.ID, "status": "active"}, nil)

	// A reset hands out a new password and the old one stops working.
	var reset struct {
		Password string `json:"password"`
	}
	admin.mustOK(http.MethodPost, "/api/v1/accounts/password", map[string]any{"id": created.Account.ID}, &reset)
	if code := member.signIn("bob", created.Password); code != http.StatusUnauthorized {
		t.Fatalf("old password after reset = %d", code)
	}
	if code := member.signIn("bob", reset.Password); code != http.StatusOK {
		t.Fatalf("new password after reset = %d", code)
	}
	// The member changes their own password and stays signed in.
	if code := member.do(http.MethodPost, "/api/v1/auth/password", map[string]string{"current_password": "nope-nope-nope", "new_password": "bob-own-password"}, nil); code != http.StatusForbidden {
		t.Fatalf("own password with a wrong current one = %d", code)
	}
	member.mustOK(http.MethodPost, "/api/v1/auth/password", map[string]string{"current_password": reset.Password, "new_password": "bob-own-password"}, nil)
	if state := member.session(); state.Account == nil {
		t.Fatal("changing one's own password signed the browser out")
	}

	// Signing out ends the session server-side, not only in this browser.
	token := member.cookie()
	if !strings.HasPrefix(token, store.AccountSessionTokenPrefix) {
		t.Fatalf("member cookie = %q", token)
	}
	member.mustOK(http.MethodPost, "/api/v1/auth/sign-out", nil, nil)
	if state := member.session(); !state.SignInRequired || state.Authenticated {
		t.Fatalf("state after sign-out = %+v", state)
	}
	if account, _ := host.st.AuthenticateAccountSession(ctx, token); account != nil {
		t.Fatal("the session survived sign-out")
	}
}

// cookie returns the browser's Console cookie value.
func (b *browser) cookie() string {
	b.t.Helper()
	base, _ := url.Parse(b.host.url)
	for _, cookie := range b.client.Jar.Cookies(base) {
		if cookie.Name == consoleSessionCookie {
			return cookie.Value
		}
	}
	return ""
}

func TestMemberSeesOnlyOwnDevices(t *testing.T) {
	host := newAccountHost(t, true)
	ctx := context.Background()
	bridge := host.bearer(t, "bridge-secret")
	bridge.mustOK(http.MethodPost, "/api/v1/auth/setup", map[string]string{"login": "root", "password": "root-password-1"}, nil)
	admin := host.browser(t)
	if code := admin.signIn("root", "root-password-1"); code != http.StatusOK {
		t.Fatalf("admin sign-in = %d", code)
	}
	for _, login := range []string{"bob", "carol"} {
		admin.mustOK(http.MethodPost, "/api/v1/accounts", map[string]any{"login": login, "password": login + "-password-1"}, nil)
	}
	bob, carol := host.browser(t), host.browser(t)
	if bob.signIn("bob", "bob-password-1") != http.StatusOK || carol.signIn("carol", "carol-password-1") != http.StatusOK {
		t.Fatal("member sign-in failed")
	}

	// Each pairing code makes a device owned by whoever asked for it.
	pair := func(b *browser, name string) string {
		t.Helper()
		var response pairingCodeResponse
		b.mustOK(http.MethodPost, "/api/v1/devices/pairing-codes", map[string]string{"name": name}, &response)
		device, _, err := host.st.RedeemDevicePairingCode(ctx, response.Code, core.DeviceInfo{Hostname: name})
		if err != nil || device == nil {
			t.Fatalf("redeem %s: %+v, %v", name, device, err)
		}
		return device.ID
	}
	bobLaptop := pair(bob, "bob-laptop")
	carolLaptop := pair(carol, "carol-laptop")
	adminBox := pair(admin, "admin-box")
	bridgeBox := pair(bridge, "bridge-box")

	list := func(b *browser) map[string]string {
		t.Helper()
		var response struct {
			Devices []deviceView `json:"devices"`
		}
		b.mustOK(http.MethodGet, "/api/v1/devices", nil, &response)
		out := map[string]string{}
		for _, device := range response.Devices {
			out[device.ID] = device.OwnerAccountName
		}
		return out
	}
	if got := list(bob); len(got) != 1 || got[bobLaptop] != "" {
		t.Fatalf("bob sees %v", got)
	}
	if got := list(carol); len(got) != 1 {
		t.Fatalf("carol sees %v", got)
	} else if _, ok := got[carolLaptop]; !ok {
		t.Fatalf("carol sees %v", got)
	}
	all := list(admin)
	if len(all) != 4 || all[bobLaptop] != "bob" || all[carolLaptop] != "carol" || all[adminBox] != "root" || all[bridgeBox] != "" {
		t.Fatalf("admin sees %v", all)
	}

	// A member cannot touch another member's device, nor read its reports.
	if code := bob.do(http.MethodPost, "/api/v1/devices", map[string]string{"id": carolLaptop, "name": "mine now"}, nil); code != http.StatusNotFound {
		t.Fatalf("bob renaming carol's device = %d", code)
	}
	if code := bob.do(http.MethodDelete, "/api/v1/devices?id="+carolLaptop, nil, nil); code != http.StatusNotFound {
		t.Fatalf("bob removing carol's device = %d", code)
	}
	if code := bob.do(http.MethodGet, "/api/v1/devices/reports?device_id="+carolLaptop, nil, nil); code != http.StatusNotFound {
		t.Fatalf("bob reading carol's reports = %d", code)
	}
	bob.mustOK(http.MethodPost, "/api/v1/devices", map[string]string{"id": bobLaptop, "name": "Bob's laptop"}, nil)
	bob.mustOK(http.MethodGet, "/api/v1/devices/reports?device_id="+bobLaptop, nil, nil)
	// Members do not reach into devices through the hub yet.
	if code := bob.do(http.MethodGet, "/api/v1/remote/proxy/"+bobLaptop+"/status", nil, nil); code != http.StatusForbidden {
		t.Fatalf("bob proxying into his device = %d", code)
	}
	bob.mustOK(http.MethodDelete, "/api/v1/devices?id="+bobLaptop, nil, nil)
	if device, _ := host.st.GetDevice(ctx, bobLaptop); device != nil {
		t.Fatal("bob's device was not removed")
	}

	// An administrator can hand a device to a member, and take it back.
	carolAccount, _ := host.st.GetAccountByLogin(ctx, "carol")
	admin.mustOK(http.MethodPost, "/api/v1/devices/owner", map[string]string{"id": bridgeBox, "account_id": carolAccount.ID}, nil)
	if got := list(carol); len(got) != 2 {
		t.Fatalf("carol after hand-over sees %v", got)
	}
	if code := carol.do(http.MethodPost, "/api/v1/devices/owner", map[string]string{"id": bridgeBox, "account_id": ""}, nil); code != http.StatusForbidden {
		t.Fatalf("member changing a device owner = %d", code)
	}
	if code := admin.do(http.MethodPost, "/api/v1/devices/owner", map[string]string{"id": bridgeBox, "account_id": "acct_missing"}, nil); code != http.StatusNotFound {
		t.Fatalf("hand-over to an unknown account = %d", code)
	}
	admin.mustOK(http.MethodPost, "/api/v1/devices/owner", map[string]string{"id": bridgeBox, "account_id": ""}, nil)
	if got := list(carol); len(got) != 1 {
		t.Fatalf("carol after release sees %v", got)
	}

	// Only the bridge token and accounts authenticate.
	if code := host.bearer(t, "amxt_retired-tenant-token").do(http.MethodGet, "/api/v1/devices", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("an unknown bearer token listing devices = %d", code)
	}
}

func TestBridgelessMachineKeepsLoopbackAdministrator(t *testing.T) {
	host := newAccountHost(t, false)
	anonymous := host.browser(t)

	// No accounts: the local Console is the administrator, as before.
	state := anonymous.session()
	if state.AccountsEnabled || state.SignInRequired || !state.Admin || !state.SetupAvailable {
		t.Fatalf("bridgeless fresh state = %+v", state)
	}

	// The local Console creates the first administrator and is signed in.
	console := host.browser(t)
	console.mustOK(http.MethodPost, "/api/v1/auth/setup", map[string]string{"login": "owner", "password": "owner-password-1"}, nil)
	if state := console.session(); state.Account == nil || state.Account.Login != "owner" || state.SignInRequired {
		t.Fatalf("after setup = %+v", state)
	}

	// The CLI and other credential-less local callers keep working; the
	// Console is asked to sign in.
	cli := host.browser(t)
	cli.plain = true
	if code := cli.do(http.MethodGet, "/api/v1/agent-instances", nil, nil); code != http.StatusOK {
		t.Fatalf("credential-less local call = %d", code)
	}
	if state := host.browser(t).session(); !state.SignInRequired {
		t.Fatalf("a fresh browser is not asked to sign in: %+v", state)
	}
	// An ended account session is refused rather than falling back to the
	// loopback administrator.
	if err := host.st.DeleteAccountSession(context.Background(), console.cookie()); err != nil {
		t.Fatal(err)
	}
	if code := console.do(http.MethodGet, "/api/v1/agent-instances", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("ended session = %d", code)
	}
	if state := console.session(); !state.SignInRequired || state.Authenticated {
		t.Fatalf("ended session state = %+v", state)
	}
}

func TestSignInIsRateLimitedAndSameOrigin(t *testing.T) {
	host := newAccountHost(t, true)
	host.bearer(t, "bridge-secret").mustOK(http.MethodPost, "/api/v1/auth/setup", map[string]string{"login": "root", "password": "root-password-1"}, nil)

	crossSite := host.browser(t)
	crossSite.plain = true
	if code := crossSite.signIn("root", "root-password-1"); code != http.StatusForbidden {
		t.Fatalf("sign-in without the Console header = %d", code)
	}

	attacker := host.browser(t)
	for attempt := 0; attempt < 5; attempt++ {
		if code := attacker.signIn("root", "guess-"+strings.Repeat("x", attempt+8)); code != http.StatusUnauthorized {
			t.Fatalf("guess %d = %d", attempt, code)
		}
	}
	if code := attacker.signIn("root", "root-password-1"); code != http.StatusTooManyRequests {
		t.Fatalf("right password after lockout = %d", code)
	}

	// The account cookie is useless without the Console header.
	owner := host.browser(t)
	host.srv.signIns = newSignInLimiter()
	if code := owner.signIn("root", "root-password-1"); code != http.StatusOK {
		t.Fatalf("sign-in = %d", code)
	}
	owner.plain = true
	if code := owner.do(http.MethodGet, "/api/v1/agent-instances", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("cookie without the Console header = %d", code)
	}
}

func TestSignInLimiterWindows(t *testing.T) {
	limiter := newSignInLimiter()
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	limiter.now = func() time.Time { return now }

	for i := 0; i < 4; i++ {
		limiter.failure("bob", "10.0.0.1")
	}
	if limiter.retryAfter("bob", "10.0.0.1") != 0 {
		t.Fatal("locked before the fifth failure")
	}
	limiter.failure("bob", "10.0.0.1")
	if wait := limiter.retryAfter("bob", "10.0.0.1"); wait != signInLockout {
		t.Fatalf("wait = %v", wait)
	}
	// Another address may still try the same login, and the same address
	// another login.
	if limiter.retryAfter("bob", "10.0.0.2") != 0 || limiter.retryAfter("carol", "10.0.0.1") != 0 {
		t.Fatal("one pair locked out others")
	}
	now = now.Add(signInLockout)
	if limiter.retryAfter("bob", "10.0.0.1") != 0 {
		t.Fatal("still locked after the lockout")
	}

	// Spraying many logins from one address locks the address.
	for i := 0; i < 20; i++ {
		limiter.failure("user"+string(rune('a'+i)), "10.0.0.9")
	}
	if limiter.retryAfter("someone-new", "10.0.0.9") == 0 {
		t.Fatal("a spraying address is not locked")
	}
	// A success clears only its own pair.
	limiter.failure("dave", "10.0.0.3")
	limiter.success("dave", "10.0.0.3")
	if _, ok := limiter.entries["pair:dave|10.0.0.3"]; ok {
		t.Fatal("success left the pair counter")
	}
}
