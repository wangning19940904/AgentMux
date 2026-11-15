package link

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/wangning19940904/AgentMux/core"
)

const (
	passiveStateFile = "passive-link.json"
	passiveCertFile  = "passive-cert.pem"
	passiveKeyFile   = "passive-key.pem"
	// passiveBodyWait bounds how long a pairing request may take to arrive,
	// so a peer that never authenticates cannot hold a connection open.
	passiveBodyWait = 20 * time.Second
)

// PassiveState is a node's passive membership: where it listens, which hub
// credential it accepts (as a hash), and the pending invite's secret (as a
// hash). It lives beside the node's other link files, readable by its owner
// only, and is separate from State: a node is in one mode at a time.
type PassiveState struct {
	Listen string `json:"listen"`
	// Addrs are what invites tell the hub to dial; empty means this
	// machine's own addresses at the listening port.
	Addrs            []string   `json:"addrs,omitempty"`
	DeviceID         string     `json:"device_id,omitempty"`
	Name             string     `json:"name,omitempty"`
	CredentialSHA256 string     `json:"credential_sha256,omitempty"`
	PairedAt         *time.Time `json:"paired_at,omitempty"`
	SecretSHA256     string     `json:"secret_sha256,omitempty"`
	SecretExpires    *time.Time `json:"secret_expires,omitempty"`
}

// Paired reports whether a hub holds a credential this node accepts.
func (s PassiveState) Paired() bool { return s.CredentialSHA256 != "" }

// PassiveStatePath is where a node keeps its passive membership.
func PassiveStatePath(dir string) (string, error) {
	dir, err := StateDir(dir)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, passiveStateFile), nil
}

// LoadPassiveState reads the passive membership. A missing file means the
// node has never been told to listen.
func LoadPassiveState(path string) (PassiveState, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return PassiveState{}, false, nil
	}
	if err != nil {
		return PassiveState{}, false, err
	}
	var state PassiveState
	if err := json.Unmarshal(data, &state); err != nil {
		return PassiveState{}, false, fmt.Errorf("read %s: %w", path, err)
	}
	return state, true, nil
}

// SavePassiveState replaces the passive membership atomically, owner-only.
func SavePassiveState(path string, state PassiveState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(path, append(data, '\n'))
}

// LoadOrCreatePassiveIdentity returns the key a listening node presents, and
// its SPKI fingerprint, creating it on first use in the state directory. Hubs
// pin the fingerprint, so the key is never replaced while it can be read.
func LoadOrCreatePassiveIdentity(dir string) (tls.Certificate, string, error) {
	dir, err := StateDir(dir)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	// The certificate names nothing: anyone who reaches the port sees it.
	cert, err := loadOrCreateIdentity(dir, passiveCertFile, passiveKeyFile, &x509.Certificate{
		Subject: pkix.Name{CommonName: "AgentMux node"},
	})
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("read the node's link key in %s: %w", dir, err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return tls.Certificate{}, "", err
	}
	cert.Leaf = leaf
	return cert, SPKIFingerprint(leaf), nil
}

// PassiveStatus is a listening node's view of its link.
type PassiveStatus struct {
	Listening   bool     `json:"listening"`
	Listen      string   `json:"listen,omitempty"`
	ListenError string   `json:"listen_error,omitempty"`
	Addrs       []string `json:"addrs,omitempty"`
	Fingerprint string   `json:"fingerprint,omitempty"`
	Paired      bool     `json:"paired"`
	// DeviceID and Name are what the hub that paired calls this node.
	DeviceID      string     `json:"device_id,omitempty"`
	Name          string     `json:"name,omitempty"`
	PairedAt      *time.Time `json:"paired_at,omitempty"`
	InviteExpires *time.Time `json:"invite_expires_at,omitempty"`
	Connected     bool       `json:"connected"`
	Since         *time.Time `json:"since,omitempty"`
	LastHubAt     *time.Time `json:"last_hub_at,omitempty"`
	LastHubAddr   string     `json:"last_hub_addr,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	// Reports is what this node has sent the hub about itself.
	Reports []ReportStatus `json:"reports,omitempty"`
}

// PassiveNode listens for the hub that paired with it and serves that hub's
// connection the way Client serves one it opened.
type PassiveNode struct {
	// Dir is the link state directory, as StateDir takes it.
	Dir  string
	Info core.DeviceInfo
	// Handler answers the hub. LocalAPIHandler is the usual one.
	Handler        http.Handler
	Log            *slog.Logger
	Reports        []Report
	ReportInterval time.Duration

	mu          sync.Mutex
	opened      bool
	ctx         context.Context
	statePath   string
	state       PassiveState
	cert        tls.Certificate
	fingerprint string
	server      *http.Server
	stopServer  func() bool
	listening   string
	bound       string
	listenErr   string
	client      *Client
	failures    *attemptLimiter
	upgrader    websocket.Upgrader
	// session ends the hub's current connection; generation counts them,
	// so a connection that was replaced does not mark the node offline.
	session    context.CancelFunc
	generation int
	lastHubAt  *time.Time
	lastHubAdr string
	now        func() time.Time
}

// Open reads the node's key and passive state, creating the key on first
// use. It does not listen; Serve does. ctx bounds every connection served.
func (n *PassiveNode) Open(ctx context.Context) error {
	if n.Log == nil {
		n.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	cert, fingerprint, err := LoadOrCreatePassiveIdentity(n.Dir)
	if err != nil {
		return err
	}
	path, err := PassiveStatePath(n.Dir)
	if err != nil {
		return err
	}
	state, _, err := LoadPassiveState(path)
	if err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.ctx, n.statePath, n.state, n.cert, n.fingerprint = ctx, path, state, cert, fingerprint
	if n.now == nil {
		n.now = time.Now
	}
	n.failures = newAttemptLimiter(passiveFailures, passiveFailureSpan)
	n.upgrader = websocket.Upgrader{HandshakeTimeout: 15 * time.Second, CheckOrigin: func(*http.Request) bool { return true }}
	n.client = &Client{
		State: State{DeviceID: state.DeviceID, Name: state.Name}, Info: n.Info, Handler: n.Handler, Log: n.Log,
		Reports: n.Reports, ReportInterval: n.ReportInterval,
	}
	n.opened = true
	return nil
}

// State returns the passive membership as it is now.
func (n *PassiveNode) State() PassiveState {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.state
}

// Fingerprint is the SPKI fingerprint of the key this node presents.
func (n *PassiveNode) Fingerprint() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.fingerprint
}

// Serve listens on addr, remembering it, and keeps listening until Close or
// the context given to Open ends. A live hub connection survives a move to
// another address.
func (n *PassiveNode) Serve(addr string) error {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		addr = DefaultPassiveListen
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return fmt.Errorf("listen address %q must be host:port: %w", addr, err)
	}
	n.mu.Lock()
	if !n.opened {
		n.mu.Unlock()
		return errors.New("the passive link is not open")
	}
	if n.server != nil && n.listening == addr {
		n.mu.Unlock()
		return nil
	}
	previous, stopPrevious := n.server, n.stopServer
	n.server, n.stopServer = nil, nil
	n.mu.Unlock()
	if previous != nil {
		stopPrevious()
		_ = previous.Close()
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		n.mu.Lock()
		n.listenErr = err.Error()
		n.mu.Unlock()
		return fmt.Errorf("listen for the hub on %s: %w", addr, err)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.state.Listen != addr {
		next := n.state
		next.Listen = addr
		if err := SavePassiveState(n.statePath, next); err != nil {
			_ = listener.Close()
			return err
		}
		n.state = next
	}
	cert := n.cert
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
		// The hub's connection is a WebSocket, which needs HTTP/1.1.
		NextProtos:   []string{"http/1.1"},
		Certificates: []tls.Certificate{cert},
	}
	// Requests carry no body worth waiting for, and the connection itself is
	// hijacked, so only the header and the TLS handshake are timed here.
	server := &http.Server{Handler: n.routes(), ReadHeaderTimeout: 10 * time.Second, ErrorLog: nil}
	n.server, n.listening, n.bound, n.listenErr = server, addr, listener.Addr().String(), ""
	ctx := n.ctx
	go func() {
		err := server.Serve(tls.NewListener(listener, tlsConfig))
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			n.Log.Error("passive link listener stopped", "addr", addr, "err", err)
			n.mu.Lock()
			if n.server == server {
				n.listenErr = err.Error()
			}
			n.mu.Unlock()
		}
	}()
	// The listener ends with the node's context, or when replaced or closed
	// (server.Close below), whichever comes first.
	n.stopServer = context.AfterFunc(ctx, func() { _ = server.Close() })
	n.Log.Info("passive link listening for the hub", "addr", addr, "fingerprint", n.fingerprint)
	return nil
}

// ListenAddr is where the node listens now, as bound (with the real port
// when it was asked for port 0), or "" when it does not.
func (n *PassiveNode) ListenAddr() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.server == nil {
		return ""
	}
	return n.bound
}

// Invite makes a new one-time secret, replacing any earlier one, and returns
// the invite a hub adds this node with. addrs override where the hub dials;
// without them the stored ones, or this machine's own addresses, are used.
// The paired hub, if any, keeps working until another hub uses the invite.
func (n *PassiveNode) Invite(addrs []string, port string) (Invite, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.opened {
		return Invite{}, errors.New("the passive link is not open")
	}
	next := n.state
	if len(addrs) > 0 {
		normalized, err := NormalizePassiveAddrs(addrs)
		if err != nil {
			return Invite{}, err
		}
		next.Addrs = normalized
	}
	advertised := next.Addrs
	if len(advertised) == 0 {
		if port == "" {
			_, port, _ = net.SplitHostPort(firstNonEmpty(n.bound, next.Listen))
		}
		advertised = LocalPassiveAddrs(next.Listen, port)
		if len(advertised) == 0 {
			return Invite{}, errors.New("this machine has no address another machine can reach; give the hub's way in with --addr")
		}
	}
	secret := randomToken(16)
	expires := n.now().Add(PassiveSecretTTL).UTC()
	next.SecretSHA256, next.SecretExpires = secretHash(secret), &expires
	if err := SavePassiveState(n.statePath, next); err != nil {
		return Invite{}, err
	}
	n.state = next
	return Invite{Addrs: advertised, Fingerprint: n.fingerprint, Secret: secret, Expires: expires.Unix()}, nil
}

// Close stops listening and drops the hub's connection. The membership stays,
// so a node that listens again accepts the same hub.
func (n *PassiveNode) Close() {
	n.mu.Lock()
	server, stopServer, session := n.server, n.stopServer, n.session
	n.server, n.stopServer, n.session, n.listening, n.bound = nil, nil, nil, "", ""
	n.generation++
	client := n.client
	n.mu.Unlock()
	if server != nil {
		stopServer()
		_ = server.Close()
	}
	if session != nil {
		session()
	}
	if client != nil {
		client.setStatus(func(status *ClientStatus) { status.Connected, status.Since = false, nil })
	}
}

// Forget stops listening and removes the passive membership. The key stays,
// so a later invite carries the same fingerprint.
func (n *PassiveNode) Forget() error {
	n.Close()
	n.mu.Lock()
	defer n.mu.Unlock()
	n.state = PassiveState{}
	if n.statePath == "" {
		return nil
	}
	err := os.Remove(n.statePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Uplink is the client for requests up the hub's connection, as
// Client.Uplink.
func (n *PassiveNode) Uplink() *http.Client {
	n.mu.Lock()
	client := n.client
	n.mu.Unlock()
	if client == nil {
		return nil
	}
	return client.Uplink()
}

// Status reports whether the node listens and whether its hub is connected.
func (n *PassiveNode) Status() PassiveStatus {
	n.mu.Lock()
	state, client := n.state, n.client
	status := PassiveStatus{
		Listening: n.server != nil, Listen: firstNonEmpty(n.listening, state.Listen), ListenError: n.listenErr,
		Addrs: state.Addrs, Fingerprint: n.fingerprint, Paired: state.Paired(),
		DeviceID: state.DeviceID, Name: state.Name, PairedAt: state.PairedAt,
		LastHubAt: n.lastHubAt, LastHubAddr: n.lastHubAdr,
	}
	if state.SecretSHA256 != "" && state.SecretExpires != nil && n.now().Before(*state.SecretExpires) {
		status.InviteExpires = state.SecretExpires
	}
	n.mu.Unlock()
	if client != nil {
		link := client.Status()
		status.Connected, status.Since, status.LastError, status.Reports = link.Connected, link.Since, link.LastError, link.Reports
	}
	return status
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// routes are everything the listening port answers. Anything else is 404.
func (n *PassiveNode) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+PassiveInfoPath, n.handleInfo)
	mux.HandleFunc("POST "+PassivePairPath, n.handlePair)
	mux.HandleFunc("GET "+PassiveConnectPath, n.handleConnect)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeLinkError(w, http.StatusNotFound, "not found")
	})
	return mux
}

// heldOff answers a source that used up its failed attempts, before it is
// checked again, even with the right secret.
func (n *PassiveNode) heldOff(w http.ResponseWriter, source string) bool {
	if !n.failures.blocked(source, n.now()) {
		return false
	}
	w.Header().Set("Retry-After", fmt.Sprint(int(passiveFailureSpan.Seconds())))
	writeLinkError(w, http.StatusTooManyRequests, "too many failed attempts from this address; wait a minute")
	return true
}

func (n *PassiveNode) refuse(w http.ResponseWriter, r *http.Request, source, message string) {
	n.failures.record(source, n.now())
	n.Log.Warn("passive link: refused", "from", source, "path", r.URL.Path)
	writeLinkError(w, http.StatusUnauthorized, message)
}

// secretValid checks the invite's secret. The caller holds mu.
func (n *PassiveNode) secretValid(secret string) bool {
	state := n.state
	return state.SecretExpires != nil && n.now().Before(*state.SecretExpires) && sameHash(secret, state.SecretSHA256)
}

// credentialValid checks the paired hub's credential.
func (n *PassiveNode) credentialValid(credential string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return sameHash(credential, n.state.CredentialSHA256)
}

type passiveInfoResponse struct {
	Info   core.DeviceInfo `json:"info"`
	Paired bool            `json:"paired"`
}

// handleInfo tells a hub holding the invite, or the paired hub, who this node
// is, without using the invite up: the hub checks the machine is not already
// paired the other way before it pairs.
func (n *PassiveNode) handleInfo(w http.ResponseWriter, r *http.Request) {
	source := limiterSource(remoteIP(r))
	if n.heldOff(w, source) {
		return
	}
	scheme, value, _ := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
	value = strings.TrimSpace(value)
	n.mu.Lock()
	allowed := (scheme == "Pairing" && n.secretValid(value)) || (scheme == "Bearer" && sameHash(value, n.state.CredentialSHA256))
	paired := n.state.Paired()
	n.mu.Unlock()
	if !allowed {
		n.refuse(w, r, source, "the pairing secret or credential is wrong, already used, or expired")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(passiveInfoResponse{Info: cleanInfo(n.Info), Paired: paired})
}

type passivePairRequest struct {
	Secret     string `json:"secret"`
	Credential string `json:"credential"`
	DeviceID   string `json:"device_id"`
	Name       string `json:"name"`
}

// handlePair trades the invite's secret for the hub's credential. The node
// keeps only the credential's hash. Pairing replaces the hub paired before:
// its credential stops working and its connection closes.
func (n *PassiveNode) handlePair(w http.ResponseWriter, r *http.Request) {
	source := limiterSource(remoteIP(r))
	if n.heldOff(w, source) {
		return
	}
	_ = http.NewResponseController(w).SetReadDeadline(n.now().Add(passiveBodyWait))
	var request passivePairRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, pairBodyLimit)).Decode(&request); err != nil {
		writeLinkError(w, http.StatusBadRequest, "invalid pairing request")
		return
	}
	if !validCredential(request.Credential) {
		writeLinkError(w, http.StatusBadRequest, "the hub sent no usable credential")
		return
	}
	now := n.now().UTC()
	n.mu.Lock()
	if !n.secretValid(strings.TrimSpace(request.Secret)) {
		n.mu.Unlock()
		n.refuse(w, r, source, "the pairing secret is wrong, already used, or expired; make a new invite on the node")
		return
	}
	next := n.state
	next.CredentialSHA256 = secretHash(request.Credential)
	next.SecretSHA256, next.SecretExpires = "", nil
	next.DeviceID, next.Name, next.PairedAt = boundString(request.DeviceID, 64), boundString(request.Name, 120), &now
	if err := SavePassiveState(n.statePath, next); err != nil {
		n.mu.Unlock()
		n.Log.Warn("passive link: pairing could not be saved", "err", err)
		writeLinkError(w, http.StatusInternalServerError, "the node could not save the pairing")
		return
	}
	n.state = next
	previous := n.session
	n.session = nil
	n.generation++
	client := n.client
	n.mu.Unlock()
	if previous != nil {
		previous()
	}
	client.mu.Lock()
	client.State.DeviceID, client.State.Name = next.DeviceID, next.Name
	client.mu.Unlock()
	n.Log.Info("passive link paired", "device", next.DeviceID, "from", source)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(passiveInfoResponse{Info: cleanInfo(n.Info), Paired: true})
}

// handleConnect takes the paired hub's connection. A newer connection
// replaces an older one, as a node that reconnects replaces its own.
func (n *PassiveNode) handleConnect(w http.ResponseWriter, r *http.Request) {
	source := limiterSource(remoteIP(r))
	if n.heldOff(w, source) {
		return
	}
	if version := r.Header.Get(headerProtocol); version != ProtocolVersion {
		writeLinkError(w, http.StatusUpgradeRequired, "this hub speaks link protocol "+version+"; the node speaks "+ProtocolVersion)
		return
	}
	credential, _ := strings.CutPrefix(strings.TrimSpace(r.Header.Get("Authorization")), "Bearer ")
	if !n.credentialValid(strings.TrimSpace(credential)) {
		n.refuse(w, r, source, ErrPassiveRefused.Error())
		return
	}
	header := http.Header{}
	header.Set(headerInfo, encodeInfo(n.Info))
	ws, err := n.upgrader.Upgrade(w, r, header)
	if err != nil {
		return
	}
	now := n.now()
	from := remoteIP(r)
	n.mu.Lock()
	previous := n.session
	ctx, cancel := context.WithCancel(n.ctx)
	n.session = cancel
	n.generation++
	generation := n.generation
	n.lastHubAt, n.lastHubAdr = &now, from
	client := n.client
	n.mu.Unlock()
	if previous != nil {
		previous()
	}
	if id := boundString(r.Header.Get(headerDeviceID), 64); id != "" {
		name, _ := url.QueryUnescape(r.Header.Get(headerDeviceName))
		n.learnDevice(id, boundString(name, 120))
	}
	n.Log.Info("passive link: the hub connected", "from", from)
	err = client.serveSession(ctx, ws)
	cancel()
	n.mu.Lock()
	latest := n.generation == generation
	if latest {
		n.session = nil
	}
	n.mu.Unlock()
	if latest {
		client.setStatus(func(status *ClientStatus) {
			status.Connected, status.Since = false, nil
			if err != nil && !errors.Is(err, context.Canceled) {
				status.LastError = err.Error()
			}
		})
	}
	n.Log.Info("passive link: the hub disconnected", "from", from, "err", err)
}

// learnDevice records what the hub that connected calls this node: the hub
// names it only once it has recorded the pairing.
func (n *PassiveNode) learnDevice(id, name string) {
	n.mu.Lock()
	client := n.client
	if n.state.DeviceID != id || n.state.Name != name {
		next := n.state
		next.DeviceID, next.Name = id, name
		if err := SavePassiveState(n.statePath, next); err != nil {
			n.Log.Warn("passive link: the device name could not be saved", "err", err)
		} else {
			n.state = next
		}
	}
	n.mu.Unlock()
	client.mu.Lock()
	client.State.DeviceID, client.State.Name = id, name
	client.mu.Unlock()
}

func boundString(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) > limit {
		value = value[:limit]
	}
	return value
}

// LocalPassiveAddrs guesses where a hub can reach a node listening on listen:
// that host when it is a specific one, otherwise this machine's own
// addresses at port, private IPv4 first, then public IPv4, then global IPv6.
// Loopback, link-local, point-to-point (VPN) interfaces and the addresses
// proxy tunnels make up are left out.
func LocalPassiveAddrs(listen, port string) []string {
	host, listenPort, err := net.SplitHostPort(listen)
	if port == "" {
		port = listenPort
	}
	if port == "" || port == "0" {
		return nil
	}
	if err == nil && host != "" {
		if ip := net.ParseIP(host); ip == nil || !ip.IsUnspecified() {
			return []string{net.JoinHostPort(host, port)}
		}
	}
	type candidate struct {
		ip   net.IP
		rank int
	}
	var found []candidate
	interfaces, _ := net.Interfaces()
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&(net.FlagLoopback|net.FlagPointToPoint) != 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			network, ok := addr.(*net.IPNet)
			if !ok || !network.IP.IsGlobalUnicast() {
				continue
			}
			ip := network.IP
			switch {
			case ip.To4() != nil && proxyTunnelNet.Contains(ip):
				continue
			case ip.To4() != nil && ip.IsPrivate():
				found = append(found, candidate{ip, 0})
			case ip.To4() != nil:
				found = append(found, candidate{ip, 1})
			default:
				found = append(found, candidate{ip, 2})
			}
		}
	}
	sort.SliceStable(found, func(a, b int) bool { return found[a].rank < found[b].rank })
	var out []string
	for _, candidate := range found {
		out = append(out, net.JoinHostPort(candidate.ip.String(), port))
		if len(out) == 8 {
			break
		}
	}
	return out
}

// proxyTunnelNet is 198.18.0.0/15, which proxy tunnels use for made-up
// addresses.
var proxyTunnelNet = &net.IPNet{IP: net.IPv4(198, 18, 0, 0), Mask: net.CIDRMask(15, 32)}
