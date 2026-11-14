package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/wangning19940904/AgentMux/config"
	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/hubha"
	linkpkg "github.com/wangning19940904/AgentMux/link"
	pushpkg "github.com/wangning19940904/AgentMux/push"
	remotepkg "github.com/wangning19940904/AgentMux/remote"
	"github.com/wangning19940904/AgentMux/store"
)

// Devices and the hub link.
//
// A machine plays two independent parts. As a hub it pairs devices and holds
// the connections they open to it; its remote Manager then reaches a paired
// device exactly as it reaches an SSH host. As a node it joins one hub and
// keeps a connection to it, down which that hub reaches this machine's API.

// deviceBackend presents paired devices to the remote Manager.
type deviceBackend struct {
	st   *store.Store
	hub  *linkpkg.Hub
	push *pushpkg.Service
	// self returns the device record this hub registered for itself, or "".
	// That record is this machine, not another one to manage, so it is left
	// out; the other hub uses it to accept this one after a failover.
	self func() string
	// hubRoles labels the devices that are hubs ("hub" or "standby"), or
	// returns nil when this hub has no standby.
	hubRoles func(ctx context.Context) map[string]string
}

func (b *deviceBackend) isSelf(id string) bool {
	return b.self != nil && id != "" && id == b.self()
}

func (b *deviceBackend) roles(ctx context.Context) map[string]string {
	if b.hubRoles == nil {
		return nil
	}
	return b.hubRoles(ctx)
}

func (b *deviceBackend) linked(device core.Device, roles map[string]string) remotepkg.LinkedDevice {
	_, online := b.hub.Connection(device.ID)
	return remotepkg.LinkedDevice{ID: device.ID, Name: device.Name, Version: device.Version, Online: online, LastSeenAt: device.LastSeenAt,
		HubRole: roles[device.ID]}
}

// lastReports is when each device last sent the hub its data.
func (b *deviceBackend) lastReports(ctx context.Context) map[string]time.Time {
	latest := map[string]time.Time{}
	collections, err := b.st.ListDeviceCollections(ctx, "")
	if err != nil {
		return latest
	}
	for _, collection := range collections {
		if collection.ReportedAt.After(latest[collection.DeviceID]) {
			latest[collection.DeviceID] = collection.ReportedAt
		}
	}
	return latest
}

func (b *deviceBackend) Devices(ctx context.Context) ([]remotepkg.LinkedDevice, error) {
	devices, err := b.st.ListDevices(ctx)
	if err != nil {
		return nil, err
	}
	latest := b.lastReports(ctx)
	roles := b.roles(ctx)
	out := make([]remotepkg.LinkedDevice, 0, len(devices))
	for _, device := range devices {
		if b.isSelf(device.ID) {
			continue
		}
		linked := b.linked(device, roles)
		if at, ok := latest[device.ID]; ok {
			linked.ReportedAt = &at
		}
		out = append(out, linked)
	}
	return out, nil
}

func (b *deviceBackend) Device(ctx context.Context, id string) (remotepkg.LinkedDevice, bool) {
	if b.isSelf(id) {
		return remotepkg.LinkedDevice{}, false
	}
	device, err := b.st.GetDevice(ctx, id)
	if err != nil || device == nil {
		return remotepkg.LinkedDevice{}, false
	}
	return b.linked(*device, b.roles(ctx)), true
}

const (
	// reconnectGrace is how long after the hub starts, or takes over from
	// another, devices are still expected to come back. They back off while
	// the hub is away, so the first may take most of a minute.
	reconnectGrace = 90 * time.Second
	// reconnectWait bounds how long one request waits for its device.
	reconnectWait = 10 * time.Second
	// recentlyConnected is how close before the start a device must have
	// been connected to be expected back. Devices gone longer fail at once.
	recentlyConnected = 10 * time.Minute
)

// Dial opens a stream to the device. Right after the hub starts, its devices
// have not reconnected yet; a request then waits briefly for its device
// instead of failing, so a Console opened with the app does not load every
// machine as offline.
func (b *deviceBackend) Dial(ctx context.Context, id string) (net.Conn, error) {
	conn, err := b.hub.Dial(ctx, id)
	if err == nil || !linkpkg.IsOffline(err) || !b.expectingReconnect(ctx, id) {
		return conn, err
	}
	// Leave the request time to fail as offline rather than time out.
	wait := reconnectWait
	if deadline, ok := ctx.Deadline(); ok {
		wait = min(wait, time.Until(deadline)/2)
	}
	waitCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	if !b.hub.WaitConnected(waitCtx, id) {
		return nil, err
	}
	return b.hub.Dial(ctx, id)
}

func (b *deviceBackend) expectingReconnect(ctx context.Context, id string) bool {
	started := b.hub.StartedAt()
	if time.Since(started) > reconnectGrace {
		return false
	}
	device, err := b.st.GetDevice(ctx, id)
	if err != nil || device == nil || device.LastSeenAt == nil {
		return false
	}
	// A device seen since the start connected to this hub and left: it is
	// offline, not on its way back.
	gap := started.Sub(*device.LastSeenAt)
	return gap > 0 && gap < recentlyConnected
}

// Remove unpairs the device and drops its connection, in that order, so a
// node reconnecting in between is already refused.
func (b *deviceBackend) Remove(ctx context.Context, id string) error {
	if err := b.st.DeleteDevice(ctx, id); err != nil {
		return err
	}
	b.hub.Disconnect(id)
	b.push.ForgetDevice(id)
	return nil
}

// setupLink creates the hub half when there is a store to keep devices in.
func (s *Server) setupLink() {
	s.node = &nodeLink{server: s}
	if s.st == nil {
		return
	}
	s.linkHub = linkpkg.NewHub(s.st, s.log)
	var reports linkpkg.ReportSink = s.st
	if s.push != nil {
		// Machines coming and going, and agents waiting on them, are
		// notified from here (push.Service).
		reports = s.push.ReportSink(s.st, pushDeviceNames{server: s})
	}
	s.linkHub.SetPresenceListener(func(deviceID string, online bool) {
		s.push.DevicePresence(deviceID, online)
		// A machine that came back gets the mail that waited for it.
		if online && s.mail != nil {
			s.mail.Wake()
		}
	})
	s.linkHub.SetUplink(s.setupHubStandby(s.mailUplink(linkpkg.NewReportHandler(reports, s.log))))
	s.devices = &deviceBackend{st: s.st, hub: s.linkHub, push: s.push, self: s.hubPeerDeviceID, hubRoles: s.deviceHubLabels}
	if s.remote != nil {
		s.remote.SetLinkBackend(s.devices)
	}
}

func (s *Server) registerDeviceRoutes() {
	s.mux.HandleFunc("GET /api/v1/devices", s.handleDevicesList)
	s.mux.HandleFunc("POST /api/v1/devices", s.handleDeviceRename)
	s.mux.HandleFunc("DELETE /api/v1/devices", s.handleDeviceDelete)
	s.mux.HandleFunc("POST /api/v1/devices/pairing-codes", s.handleDevicePairingCode)
	s.mux.HandleFunc("GET /api/v1/devices/reports", s.handleDeviceReports)
	s.mux.HandleFunc("GET /api/v1/link", s.handleLinkStatus)
	s.mux.HandleFunc("POST /api/v1/link/join", s.handleLinkJoin)
	s.mux.HandleFunc("POST /api/v1/link/leave", s.handleLinkLeave)
	s.registerHubStandbyRoutes()
	if s.linkHub != nil {
		// Devices that can reach the management port may pair through it. The
		// two paths check their own credentials and sit outside /api/, so the
		// bridge token is not asked of a device that cannot have it.
		s.linkHub.Register(s.mux)
	}
}

type deviceView struct {
	core.Device
	Online         bool       `json:"online"`
	ConnectedSince *time.Time `json:"connected_since,omitempty"`
	RemoteAddr     string     `json:"remote_addr,omitempty"`
	// ReportedAt is when the device last sent the hub its data.
	ReportedAt *time.Time `json:"reported_at,omitempty"`
}

// Device ownership. A device belongs to the namespace of the account that
// created its pairing code. Administrators see every device with its owner;
// a signed-in member sees and manages only their own, and a device that is
// not theirs answers as not found.

// visibleDevices lists the devices a principal may see.
func (s *Server) visibleDevices(ctx context.Context, principal *core.Principal) ([]core.Device, error) {
	if principal.IsTenant() {
		return s.st.ListDevicesForTenant(ctx, principal.TenantID)
	}
	devices, err := s.st.ListDevices(ctx)
	if err != nil || len(devices) == 0 {
		return devices, err
	}
	// This hub's own record is this machine, not a paired one.
	if self := s.hubPeerDeviceID(); self != "" {
		devices = slices.DeleteFunc(devices, func(device core.Device) bool { return device.ID == self })
	}
	names, err := s.st.TenantNames(ctx)
	if err != nil {
		return nil, err
	}
	for index := range devices {
		devices[index].OwnerTenantName = names[devices[index].OwnerTenantID]
	}
	return devices, nil
}

// authorizeDevice resolves a device the principal may manage. It writes the
// response and returns nil when the device is missing or not theirs.
func (s *Server) authorizeDevice(w http.ResponseWriter, r *http.Request, id string) *core.Device {
	device, err := s.st.GetDevice(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return nil
	}
	principal := requestPrincipal(r)
	if device == nil || (principal.IsTenant() && device.OwnerTenantID != principal.TenantID) {
		writeNotVisible(w, "device")
		return nil
	}
	return device
}

// handleDeviceReports lists what the hub holds of each device's data.
func (s *Server) handleDeviceReports(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.linkHub == nil {
		serviceUnavailable(w, "device pairing")
		return
	}
	deviceID := strings.TrimSpace(r.URL.Query().Get("device_id"))
	principal := requestPrincipal(r)
	if deviceID != "" && s.authorizeDevice(w, r, deviceID) == nil {
		return
	}
	collections, err := s.st.ListDeviceCollections(r.Context(), deviceID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if principal.IsTenant() && deviceID == "" {
		owned, err := s.visibleDevices(r.Context(), principal)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		mine := make(map[string]bool, len(owned))
		for _, device := range owned {
			mine[device.ID] = true
		}
		filtered := collections[:0]
		for _, collection := range collections {
			if mine[collection.DeviceID] {
				filtered = append(filtered, collection)
			}
		}
		collections = filtered
	}
	if collections == nil {
		collections = []core.DeviceCollection{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"collections": collections})
}

// reportedResponse is the hub's copy of what an offline linked device's API
// last answered at path. Only reads have one, and only a device that is not
// connected is answered from it: a connected device answers for itself.
func (s *Server) reportedResponse(ctx context.Context, deviceID, method, path string) ([]byte, time.Time, bool) {
	if s.linkHub == nil || method != http.MethodGet {
		return nil, time.Time{}, false
	}
	if _, online := s.linkHub.Connection(deviceID); online {
		return nil, time.Time{}, false
	}
	payload, at, ok, err := s.st.DeviceReportedResponse(ctx, deviceID, path)
	if err != nil {
		s.log.Warn("read device report failed", "device", deviceID, "path", path, "err", err)
		return nil, time.Time{}, false
	}
	return payload, at, ok
}

func (s *Server) handleDevicesList(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.linkHub == nil {
		serviceUnavailable(w, "device pairing")
		return
	}
	devices, err := s.visibleDevices(r.Context(), requestPrincipal(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	latest := s.devices.lastReports(r.Context())
	views := make([]deviceView, 0, len(devices))
	for _, device := range devices {
		view := deviceView{Device: device}
		if at, ok := latest[device.ID]; ok {
			view.ReportedAt = &at
		}
		if connection, ok := s.linkHub.Connection(device.ID); ok {
			view.Online, view.ConnectedSince, view.RemoteAddr = true, &connection.Since, connection.RemoteAddr
		}
		views = append(views, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": views})
}

func (s *Server) handleDeviceRename(w http.ResponseWriter, r *http.Request) {
	if s.linkHub == nil {
		serviceUnavailable(w, "device pairing")
		return
	}
	var req struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if !decodeJSONInto(w, r, &req) {
		return
	}
	req.ID, req.Name = strings.TrimSpace(req.ID), strings.TrimSpace(req.Name)
	if req.ID == "" || req.Name == "" || len(req.Name) > 120 {
		writeErr(w, http.StatusBadRequest, "id and a name of at most 120 characters are required")
		return
	}
	if s.authorizeDevice(w, r, req.ID) == nil {
		return
	}
	if err := s.st.RenameDevice(r.Context(), req.ID, req.Name); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w)
}

func (s *Server) handleDeviceDelete(w http.ResponseWriter, r *http.Request) {
	if s.linkHub == nil {
		serviceUnavailable(w, "device pairing")
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	if requestPrincipal(r).IsTenant() && s.authorizeDevice(w, r, id) == nil {
		return
	}
	if err := s.devices.Remove(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w)
}

type pairingCodeResponse struct {
	Code        string    `json:"code"`
	ExpiresAt   time.Time `json:"expires_at"`
	HubURL      string    `json:"hub_url"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	JoinToken   string    `json:"join_token"`
	Command     string    `json:"command"`
	Warning     string    `json:"warning,omitempty"`
}

func (s *Server) handleDevicePairingCode(w http.ResponseWriter, r *http.Request) {
	if s.linkHub == nil {
		serviceUnavailable(w, "device pairing")
		return
	}
	var req struct {
		Name string `json:"name"`
		// HubURL overrides the address devices dial, for a tunnel or proxy
		// set up for this one device. Pin says that address still presents
		// the hub's own certificate.
		HubURL string `json:"hub_url"`
		Pin    *bool  `json:"pin"`
	}
	if r.ContentLength != 0 && !decodeJSONInto(w, r, &req) {
		return
	}
	address, err := s.hubJoinAddress(req.HubURL, req.Pin)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// The device will belong to whoever asked for the code: a signed-in
	// account's namespace, or nobody (administrators only) for the bridge
	// token and the loopback administrator.
	code, err := s.st.CreateOwnedDevicePairingCode(r.Context(), req.Name, requestPrincipal(r).OwnerTenantID())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	token := linkpkg.JoinToken{HubURL: address.url, Code: code.Code, Fingerprint: address.fingerprint}.String()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, pairingCodeResponse{
		Code: code.Code, ExpiresAt: code.ExpiresAt, HubURL: address.url, Fingerprint: address.fingerprint,
		JoinToken: token, Command: "amux join " + token, Warning: address.warning,
	})
}

type hubAddress struct {
	url         string
	fingerprint string
	warning     string
}

// hubJoinAddress decides what a join token tells a device to dial, and
// whether to pin the hub's own certificate there.
func (s *Server) hubJoinAddress(override string, pin *bool) (hubAddress, error) {
	hub := s.cfg.Hub
	var raw string
	var pinned bool
	switch {
	case strings.TrimSpace(override) != "":
		raw, pinned = override, pin != nil && *pin
	case hub.PublicURL != "":
		raw, pinned = hub.PublicURL, hub.PinCertificate
	case hub.Addr != "":
		scheme := "https"
		if hub.TLS == config.HubTLSOff {
			scheme = "http"
		}
		raw, pinned = scheme+"://"+reachableAddr(hub.Addr), hub.TLS == config.HubTLSAuto
	default:
		raw = "http://" + reachableAddr(s.cfg.Server.Addr)
	}
	if pin != nil && strings.TrimSpace(override) == "" {
		pinned = *pin
	}
	address, err := linkpkg.NormalizeHubURL(raw)
	if err != nil {
		return hubAddress{}, err
	}
	out := hubAddress{url: address}
	parsed, _ := url.Parse(address)
	loopback := isLoopbackHost(parsed.Hostname())
	if pinned {
		if parsed.Scheme != "https" {
			return hubAddress{}, errors.New("a pinned certificate needs an https:// hub address")
		}
		_, fingerprint, err := s.hubCertificate()
		if err != nil {
			return hubAddress{}, err
		}
		out.fingerprint = fingerprint
	}
	switch {
	case loopback:
		out.warning = "This address only works on this machine. Set [hub] addr or public_url so other machines can reach the hub."
	case parsed.Scheme == "http":
		out.warning = "Devices send their token unencrypted over http. Use this address only on a network you trust."
	}
	return out, nil
}

// reachableAddr replaces a listen-on-everything host with this machine's
// outbound address, which is the best guess at what another machine dials.
func reachableAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = outboundIP()
	}
	return net.JoinHostPort(host, port)
}

// outboundIP guesses the address other machines reach this one at. Interface
// addresses come first: asking the routing table, as the fallback does, names
// a VPN or proxy tunnel when one holds the default route, and nothing outside
// this machine can dial that.
func outboundIP() string {
	interfaces, _ := net.Interfaces()
	for _, candidate := range interfaces {
		if candidate.Flags&net.FlagUp == 0 || candidate.Flags&(net.FlagLoopback|net.FlagPointToPoint) != 0 {
			continue
		}
		addrs, _ := candidate.Addrs()
		for _, addr := range addrs {
			network, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			if ip := network.IP.To4(); ip != nil && ip.IsGlobalUnicast() && !benchmarkNet.Contains(ip) {
				return ip.String()
			}
		}
	}
	// A UDP "connection" sends nothing; it only asks the routing table which
	// local address faces the outside.
	conn, err := net.Dial("udp", "192.0.2.1:9")
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		return addr.IP.String()
	}
	return "127.0.0.1"
}

// benchmarkNet is 198.18.0.0/15, which proxy tunnels use for made-up
// addresses.
var benchmarkNet = &net.IPNet{IP: net.IPv4(198, 18, 0, 0), Mask: net.CIDRMask(15, 32)}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) linkStateDir() (string, error) {
	return linkpkg.StateDir(s.cfg.Hub.StateDir)
}

// hubCertificate returns the certificate devices see: the configured files,
// or the hub's own, created on first use.
func (s *Server) hubCertificate() (tls.Certificate, string, error) {
	if s.cfg.Hub.TLS == config.HubTLSFiles {
		cert, err := tls.LoadX509KeyPair(s.cfg.Hub.CertFile, s.cfg.Hub.KeyFile)
		if err != nil {
			return tls.Certificate{}, "", fmt.Errorf("read hub certificate: %w", err)
		}
		return cert, linkpkg.Fingerprint(cert.Certificate[0]), nil
	}
	dir, err := s.linkStateDir()
	if err != nil {
		return tls.Certificate{}, "", err
	}
	return linkpkg.LoadOrCreateCertificate(dir)
}

// startHubListener opens the device-only listener when [hub] addr is set. It
// fails loudly: a hub nobody can reach should not start as if it were fine.
func (s *Server) startHubListener(ctx context.Context) error {
	if s.cfg.Hub.Addr == "" {
		return nil
	}
	if s.linkHub == nil {
		return errors.New("hub.addr is set but there is no database to keep paired devices in")
	}
	listener, err := net.Listen("tcp", s.cfg.Hub.Addr)
	if err != nil {
		return fmt.Errorf("hub.addr: %w", err)
	}
	mux := http.NewServeMux()
	s.linkHub.Register(mux)
	if s.hubHA != nil {
		// The relay in front of two hubs reaches each through this listener.
		mux.HandleFunc("POST "+hubha.LeasePath, s.hubHA.controller.ServeLease)
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 15 * time.Second}
	secure := s.cfg.Hub.TLS != config.HubTLSOff
	if secure {
		if _, _, err := s.hubCertificate(); err != nil {
			_ = listener.Close()
			return err
		}
		// The device connection is a WebSocket, which needs HTTP/1.1. The
		// certificate is read per handshake: a standby hub replaces it with
		// the primary's while running.
		server.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"},
			GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
				cert, _, err := s.hubCertificate()
				return &cert, err
			}}
	}
	go func() {
		<-ctx.Done()
		_ = server.Close()
		s.linkHub.Close()
	}()
	go func() {
		var err error
		if secure {
			err = server.ServeTLS(listener, "", "")
		} else {
			err = server.Serve(listener)
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("hub listener stopped", "addr", s.cfg.Hub.Addr, "err", err)
		}
	}()
	s.log.Info("hub listening for devices", "addr", s.cfg.Hub.Addr, "tls", s.cfg.Hub.TLS)
	return nil
}

// nodeLink is this machine's membership of a hub.
type nodeLink struct {
	server *Server
	mu     sync.Mutex
	ctx    context.Context
	client *linkpkg.Client
	cancel context.CancelFunc
	// reportInterval overrides how often the node looks for changes to
	// report; zero is the default.
	reportInterval time.Duration
}

func (n *nodeLink) statePath() (string, error) {
	return linkpkg.StatePath(n.server.cfg.Hub.StateDir)
}

// run starts the connection kept in the membership file, if there is one,
// and stops it when ctx ends.
func (n *nodeLink) run(ctx context.Context) {
	n.mu.Lock()
	n.ctx = ctx
	n.mu.Unlock()
	path, err := n.statePath()
	if err != nil {
		return
	}
	state, ok, err := linkpkg.LoadState(path)
	if err != nil {
		n.server.log.Warn("hub membership could not be read", "path", path, "err", err)
		return
	}
	if ok {
		n.start(state)
	}
}

func (n *nodeLink) start(state linkpkg.State) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.cancel != nil {
		n.cancel()
	}
	parent := n.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	s := n.server
	token := ""
	if s.cfg.Bridge.Enabled {
		token = s.cfg.Bridge.Token
	}
	client := &linkpkg.Client{
		State: state, Info: linkpkg.LocalDeviceInfo(state.Name, s.version),
		Handler: linkpkg.LocalAPIHandler(linkpkg.LoopbackAddr(s.cfg.Server.Addr), token), Log: s.log,
		ReportInterval: n.reportInterval,
	}
	n.client, n.cancel = client, cancel
	go func() { _ = client.Run(ctx) }()
}

func (n *nodeLink) stop() {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.cancel != nil {
		n.cancel()
	}
	n.client, n.cancel = nil, nil
}

// uplink is the client for requests up this machine's link, or nil without
// one.
func (n *nodeLink) uplink() *http.Client {
	n.mu.Lock()
	client := n.client
	n.mu.Unlock()
	if client == nil {
		return nil
	}
	return client.Uplink()
}

func (n *nodeLink) status() (linkpkg.ClientStatus, bool) {
	n.mu.Lock()
	client := n.client
	n.mu.Unlock()
	if client == nil {
		return linkpkg.ClientStatus{}, false
	}
	return client.Status(), true
}

type linkStatusResponse struct {
	Joined bool `json:"joined"`
	linkpkg.ClientStatus
}

func (s *Server) handleLinkStatus(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	status, joined := s.node.status()
	writeJSON(w, http.StatusOK, linkStatusResponse{Joined: joined, ClientStatus: status})
}

// handleLinkJoin makes this machine a device of a hub. Whoever runs that hub
// can then use this machine's AgentMux as its administrator does, so the
// route is the administrator's alone.
func (s *Server) handleLinkJoin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token   string `json:"token"`
		Name    string `json:"name"`
		Replace bool   `json:"replace"`
	}
	if !decodeJSONInto(w, r, &req) {
		return
	}
	join, err := linkpkg.ParseJoinToken(req.Token)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	path, err := s.node.statePath()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if existing, ok, _ := linkpkg.LoadState(path); ok && !req.Replace {
		writeErr(w, http.StatusConflict, "this machine already belongs to the hub at "+existing.HubURL+"; leave it first")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	state, err := linkpkg.Pair(ctx, join, linkpkg.LocalDeviceInfo(req.Name, s.version))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if err := linkpkg.SaveState(path, state); err != nil {
		writeErr(w, http.StatusInternalServerError, "paired, but the membership could not be saved: "+err.Error())
		return
	}
	s.node.start(state)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "hub_url": state.HubURL, "device_id": state.DeviceID, "name": state.Name})
}

// handleLinkLeave disconnects from the hub and forgets it. The hub keeps the
// device in its list, offline, until someone removes it there.
func (s *Server) handleLinkLeave(w http.ResponseWriter, _ *http.Request) {
	path, err := s.node.statePath()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.node.stop()
	if err := linkpkg.RemoveState(path); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w)
}
