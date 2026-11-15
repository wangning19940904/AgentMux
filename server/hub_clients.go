package server

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/wangning19940904/AgentMux/hubha"
)

// Clients on the hub listener.
//
// The hub listener ([hub] addr) is the one port of a hub that other machines
// can reach, often through a relay. It always serves device pairing and
// device connections. With [hub] client_api it also serves the bridge API
// under /api/v1/, so a phone app can reach the hub without the Console port.
//
// Requests there are never trusted for where they come from: a relay reaches
// the hub through an SSH reverse tunnel, so every remote caller arrives from
// 127.0.0.1. Each request is therefore marked as a hub client, its address is
// replaced with one that is not loopback, and it must carry an account
// session or the bridge token even on a machine whose bridge is off.

// hubClientRemoteHost replaces the caller's host on the hub listener. It is
// not an IP address, so no loopback check can mistake it for this machine.
const hubClientRemoteHost = "hub-client"

// hubClientLinkScheme is the URL a phone app scans to learn where the hub
// is and which certificate to expect there.
const hubClientLinkScheme = "agentmux"

type hubClientKey struct{}

// isHubClient reports whether a request arrived on the hub listener.
func isHubClient(r *http.Request) bool {
	marked, _ := r.Context().Value(hubClientKey{}).(bool)
	return marked
}

// hubListenerHandler is everything the hub listener serves.
func (s *Server) hubListenerHandler() http.Handler {
	mux := http.NewServeMux()
	s.linkHub.Register(mux)
	if s.hubHA != nil {
		// The relay in front of two hubs reaches each through this listener.
		mux.HandleFunc("POST "+hubha.LeasePath, s.hubHA.controller.ServeLease)
	}
	if s.cfg.Hub.ClientAPI {
		mux.Handle("/api/v1/", s.hubClientAPI(s.withAuth(s.mux)))
	}
	return mux
}

// hubClientAPI marks requests as coming from a hub client and keeps them
// away from what only this machine may use.
func (s *Server) hubClientAPI(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Observability has its own credential model, built for the loopback
		// Console; it is not offered to remote clients.
		if isObservabilityPath(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), hubClientKey{}, true))
		_, port, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			port = "0"
		}
		r.RemoteAddr = net.JoinHostPort(hubClientRemoteHost, port)
		next.ServeHTTP(w, r)
	})
}

type hubClientLinkResponse struct {
	HubURL      string `json:"hub_url"`
	Fingerprint string `json:"fingerprint,omitempty"`
	// Link is what the Console shows as a QR code for a phone to scan.
	Link string `json:"link"`
	// Reach says who can dial HubURL: "public" for any network, "private"
	// for a phone on the same network or VPN as the hub.
	Reach   string `json:"reach"`
	Warning string `json:"warning,omitempty"`
}

// Reasons a phone link is refused, for the Console to explain.
const (
	hubClientLinkDisabled = "disabled" // [hub] addr or client_api is not set
	hubClientLinkLoopback = "loopback" // the address only works on the hub itself
	hubClientLinkInsecure = "insecure" // http://; the app only signs in over https
)

func writeHubClientLinkRefusal(w http.ResponseWriter, reason, message string) {
	writeJSON(w, http.StatusConflict, map[string]string{"error": message, "reason": reason})
}

// handleHubClientLink tells a signed-in person where a phone reaches this
// hub. Nothing in it is secret: the phone still has to sign in.
//
// The hub needs no public IP of its own: public_url can name a relay, a
// tunnel or a forwarded port. What it cannot be is an address the phone
// never reaches, so a loopback or plain http address is refused rather than
// shown as a code that fails after scanning.
func (s *Server) handleHubClientLink(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.cfg.Hub.ClientAPI || s.cfg.Hub.Addr == "" {
		writeHubClientLinkRefusal(w, hubClientLinkDisabled, "phones cannot reach this hub yet; set [hub] addr and [hub] client_api = true")
		return
	}
	address, err := s.hubJoinAddress("", nil)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	parsed, err := url.Parse(address.url)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	host := parsed.Hostname()
	switch {
	case isLoopbackHost(host):
		writeHubClientLinkRefusal(w, hubClientLinkLoopback, "the hub's address "+address.url+" only works on this machine; set [hub] public_url to an address phones can reach, such as a relay or tunnel")
		return
	case parsed.Scheme != "https":
		writeHubClientLinkRefusal(w, hubClientLinkInsecure, "the phone app signs in only over https, and the hub's address is "+address.url+"; use [hub] tls = \"auto\" or an https public_url")
		return
	}
	query := url.Values{"url": {address.url}}
	if address.fingerprint != "" {
		query.Set("fp", address.fingerprint)
	}
	link := url.URL{Scheme: hubClientLinkScheme, Host: "hub", RawQuery: query.Encode()}
	response := hubClientLinkResponse{
		HubURL: strings.TrimRight(address.url, "/"), Fingerprint: address.fingerprint,
		Link: link.String(), Reach: "public", Warning: address.warning,
	}
	if privateHost(host) {
		response.Reach = "private"
		response.Warning = "Phones reach " + host + " only on the same network or VPN as the hub. Set [hub] public_url to a relay or tunnel to connect from anywhere."
	}
	writeJSON(w, http.StatusOK, response)
}

// privateHost reports whether host is only reachable from a local network
// or VPN: private, shared (CGNAT, as Tailscale uses) and link-local
// addresses, and mDNS .local names.
func privateHost(host string) bool {
	if strings.HasSuffix(strings.ToLower(strings.TrimSuffix(host, ".")), ".local") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return true
	}
	_, shared, _ := net.ParseCIDR("100.64.0.0/10")
	return shared.Contains(ip)
}
