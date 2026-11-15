package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/wangning19940904/AgentMux/config"
	linkpkg "github.com/wangning19940904/AgentMux/link"
)

// hubClientHost starts the hub listener of a machine whose bridge is off, the
// case where the Console port grants loopback callers the administrator.
func hubClientHost(t *testing.T, clientAPI bool) (local, hub accountHost) {
	t.Helper()
	local = newAccountHost(t, false)
	local.srv.cfg.Hub.ClientAPI = clientAPI
	listener := httptest.NewServer(local.srv.hubListenerHandler())
	t.Cleanup(listener.Close)
	hub = local
	hub.url = listener.URL
	return local, hub
}

func TestHubListenerServesTheAPIOnlyWhenAsked(t *testing.T) {
	_, hub := hubClientHost(t, false)
	if code := hub.bearer(t, "bridge-secret").do(http.MethodGet, "/api/v1/status", nil, nil); code != http.StatusNotFound {
		t.Fatalf("API on the hub listener without client_api = %d", code)
	}
	// Devices still pair there.
	if code := hub.browser(t).do(http.MethodPost, linkpkg.PairPath, map[string]any{"code": "WRONG234"}, nil); code != http.StatusUnauthorized {
		t.Fatalf("pairing with a wrong code = %d", code)
	}
}

// A relay's tunnel delivers every remote caller from 127.0.0.1. On the hub
// listener that must never count as this machine.
func TestHubClientsNeedACredentialAndAreNeverLoopback(t *testing.T) {
	local, hub := hubClientHost(t, true)

	// The Console port of a bridge-less machine still trusts its own user.
	if code := local.browser(t).do(http.MethodGet, "/api/v1/agent-instances", nil, nil); code != http.StatusOK {
		t.Fatalf("loopback Console = %d", code)
	}
	phone := hub.browser(t)
	if code := phone.do(http.MethodGet, "/api/v1/agent-instances", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("hub client without a credential = %d", code)
	}
	if state := phone.session(); state.Authenticated || state.Admin {
		t.Fatalf("hub client session without a credential = %+v", state)
	}
	if code := phone.do(http.MethodPost, "/api/v1/auth/setup", map[string]string{"login": "x", "password": "x-password-1"}, nil); code != http.StatusUnauthorized {
		t.Fatalf("first-run setup from a hub client = %d", code)
	}
	if code := hub.bearer(t, "wrong").do(http.MethodGet, "/api/v1/status", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("wrong bearer = %d", code)
	}
	if code := hub.bearer(t, "bridge-secret").do(http.MethodGet, "/api/v1/agent-instances", nil, nil); code != http.StatusOK {
		t.Fatalf("bridge token on the hub listener = %d", code)
	}
	if code := hub.bearer(t, "bridge-secret").do(http.MethodGet, "/api/v1/observability/sessions", nil, nil); code != http.StatusNotFound {
		t.Fatalf("observability on the hub listener = %d", code)
	}

	// What a handler sees of a hub client.
	var seen *http.Request
	probe := local.srv.hubClientAPI(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = r }))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	request.RemoteAddr = "127.0.0.1:50123"
	probe.ServeHTTP(httptest.NewRecorder(), request)
	if seen == nil || isLoopbackRequest(seen) || !isHubClient(seen) || clientAddr(seen) != hubClientRemoteHost {
		t.Fatalf("hub client request: loopback=%v hub client=%v addr=%q", isLoopbackRequest(seen), isHubClient(seen), seen.RemoteAddr)
	}
}

// A phone signs in with an account, as the Console does, and keeps the
// session cookie.
func TestPhoneSignsInThroughTheHubListener(t *testing.T) {
	local, hub := hubClientHost(t, true)
	setup := local.bearer(t, "bridge-secret")
	setup.mustOK(http.MethodPost, "/api/v1/auth/setup", map[string]string{"login": "root", "password": "root-password-1"}, nil)
	setup.mustOK(http.MethodPost, "/api/v1/accounts", map[string]any{"login": "bob", "password": "bob-password-1"}, nil)

	admin := hub.browser(t)
	if code := admin.signIn("root", "wrong-password"); code != http.StatusUnauthorized {
		t.Fatalf("wrong password = %d", code)
	}
	if code := admin.signIn("root", "root-password-1"); code != http.StatusOK {
		t.Fatalf("admin sign-in = %d", code)
	}
	if state := admin.session(); !state.Admin || state.Account == nil || state.Account.Login != "root" {
		t.Fatalf("admin phone session = %+v", state)
	}
	admin.mustOK(http.MethodGet, "/api/v1/agent-instances", nil, nil)

	member := hub.browser(t)
	if code := member.signIn("bob", "bob-password-1"); code != http.StatusOK {
		t.Fatalf("member sign-in = %d", code)
	}
	member.mustOK(http.MethodGet, "/api/v1/mailbox", nil, nil)
	if code := member.do(http.MethodGet, "/api/v1/agent-instances", nil, nil); code != http.StatusForbidden {
		t.Fatalf("member on an administrator route = %d", code)
	}

	// Without the Console header the cookie is not accepted.
	member.plain = true
	if code := member.do(http.MethodGet, "/api/v1/mailbox", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("cookie without the Console header = %d", code)
	}
}

func TestHubClientLink(t *testing.T) {
	local, _ := hubClientHost(t, false)
	admin := local.browser(t)
	if code := admin.do(http.MethodGet, "/api/v1/hub/client-link", nil, nil); code != http.StatusConflict {
		t.Fatalf("client link with client_api off = %d", code)
	}
	cfg := local.srv.cfg
	cfg.Hub.ClientAPI, cfg.Hub.Addr, cfg.Hub.TLS = true, "0.0.0.0:18766", config.HubTLSAuto
	cfg.Hub.PublicURL, cfg.Hub.PinCertificate = "https://203.0.113.7:443", true
	var response hubClientLinkResponse
	admin.mustOK(http.MethodGet, "/api/v1/hub/client-link", nil, &response)
	if response.HubURL != "https://203.0.113.7:443" || len(response.Fingerprint) != 64 {
		t.Fatalf("client link = %+v", response)
	}
	link, err := url.Parse(response.Link)
	if err != nil || link.Scheme != hubClientLinkScheme || link.Query().Get("url") != response.HubURL || link.Query().Get("fp") != response.Fingerprint {
		t.Fatalf("link = %q, %v", response.Link, err)
	}
	if !memberRouteAllowed(http.MethodGet, "/api/v1/hub/client-link") || strings.Contains(response.Link, "token") {
		t.Fatal("the client link must be open to members and carry no secret")
	}
}
