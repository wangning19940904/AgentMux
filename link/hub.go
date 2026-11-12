package link

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"

	"github.com/wangning19940904/AgentMux/core"
)

const (
	pairBodyLimit    = 16 << 10
	pairAttempts     = 10
	pairAttemptSpan  = time.Minute
	lastSeenInterval = time.Minute
)

// Registry is the hub's record of paired devices.
type Registry interface {
	// RedeemDevicePairingCode pairs one device with a valid, unused code and
	// returns it with its token. An invalid code returns (nil, "", nil).
	RedeemDevicePairingCode(ctx context.Context, code string, info core.DeviceInfo) (*core.Device, string, error)
	// AuthenticateDevice resolves a token, or returns (nil, nil).
	AuthenticateDevice(ctx context.Context, token string) (*core.Device, error)
	// TouchDevice records that a device was connected at seen. A zero info
	// leaves what the device said about itself unchanged.
	TouchDevice(ctx context.Context, id string, info core.DeviceInfo, seen time.Time) error
}

// Connection describes a device's live connection.
type Connection struct {
	Since      time.Time
	RemoteAddr string
}

type hubSession struct {
	mux   *yamux.Session
	since time.Time
	addr  string
}

// Hub accepts connections from paired devices and opens streams to them.
type Hub struct {
	registry Registry
	log      *slog.Logger
	upgrader websocket.Upgrader

	mu       sync.Mutex
	sessions map[string]*hubSession
	attempts map[string][]time.Time
	uplink   http.Handler
	presence func(deviceID string, online bool)
}

// SetPresenceListener is told when a device connects (online) and when its
// last connection ends. A reconnect that replaces a live connection, and a
// device dropped by Disconnect, are not changes. It runs on the connection's
// goroutine and must return quickly.
func (h *Hub) SetPresenceListener(listener func(deviceID string, online bool)) {
	h.mu.Lock()
	h.presence = listener
	h.mu.Unlock()
}

// SetUplink sets what answers the requests devices send up their links, such
// as their reports. DeviceID names the sender in each request's context.
func (h *Hub) SetUplink(handler http.Handler) {
	h.mu.Lock()
	h.uplink = handler
	h.mu.Unlock()
}

// serveUplink answers the streams a device opens, until its session ends.
func (h *Hub) serveUplink(deviceID string, mux *yamux.Session) {
	h.mu.Lock()
	handler := h.uplink
	h.mu.Unlock()
	if handler == nil {
		handler = http.NotFoundHandler()
	}
	server := &http.Server{
		Handler: handler, ReadHeaderTimeout: 30 * time.Second,
		BaseContext: func(net.Listener) context.Context {
			return context.WithValue(context.Background(), deviceContextKey{}, deviceID)
		},
	}
	_ = server.Serve(mux)
	_ = server.Close()
}

func NewHub(registry Registry, log *slog.Logger) *Hub {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Hub{
		registry: registry, log: log,
		// Devices authenticate with a header a browser cannot set on a
		// WebSocket, so the origin says nothing more.
		upgrader: websocket.Upgrader{HandshakeTimeout: 15 * time.Second, CheckOrigin: func(*http.Request) bool { return true }},
		sessions: map[string]*hubSession{},
		attempts: map[string][]time.Time{},
	}
}

// Handler serves the two endpoints a node calls. They carry their own
// credentials, so it can be exposed without the management API.
func (h *Hub) Handler() http.Handler {
	mux := http.NewServeMux()
	h.Register(mux)
	return mux
}

// Register adds the node endpoints to an existing mux.
func (h *Hub) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST "+PairPath, h.handlePair)
	mux.HandleFunc("GET "+ConnectPath, h.handleConnect)
}

type pairRequest struct {
	Code string          `json:"code"`
	Info core.DeviceInfo `json:"info"`
}

type pairResponse struct {
	DeviceID string `json:"device_id"`
	Name     string `json:"name"`
	Token    string `json:"token"`
}

func (h *Hub) handlePair(w http.ResponseWriter, r *http.Request) {
	if !h.allowPairAttempt(remoteIP(r), time.Now()) {
		writeLinkError(w, http.StatusTooManyRequests, "too many pairing attempts; wait a minute")
		return
	}
	var request pairRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, pairBodyLimit)).Decode(&request); err != nil {
		writeLinkError(w, http.StatusBadRequest, "invalid pairing request")
		return
	}
	device, token, err := h.registry.RedeemDevicePairingCode(r.Context(), request.Code, cleanInfo(request.Info))
	if err != nil {
		h.log.Warn("device pairing failed", "err", err)
		writeLinkError(w, http.StatusInternalServerError, "the hub could not pair this device")
		return
	}
	if device == nil {
		writeLinkError(w, http.StatusUnauthorized, "the pairing code is wrong, already used, or expired")
		return
	}
	h.log.Info("device paired", "device", device.ID, "name", device.Name)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(pairResponse{DeviceID: device.ID, Name: device.Name, Token: token})
}

func (h *Hub) handleConnect(w http.ResponseWriter, r *http.Request) {
	if version := r.Header.Get(headerProtocol); version != ProtocolVersion {
		writeLinkError(w, http.StatusUpgradeRequired, "this device speaks link protocol "+version+"; the hub speaks "+ProtocolVersion)
		return
	}
	token, _ := strings.CutPrefix(strings.TrimSpace(r.Header.Get("Authorization")), "Bearer ")
	device, err := h.registry.AuthenticateDevice(r.Context(), strings.TrimSpace(token))
	if err != nil {
		h.log.Warn("device lookup failed", "err", err)
		writeLinkError(w, http.StatusServiceUnavailable, "the hub could not check this device")
		return
	}
	if device == nil {
		writeLinkError(w, http.StatusUnauthorized, ErrRevoked.Error())
		return
	}
	ws, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	mux, err := yamux.Client(&wsStream{ws: ws}, muxConfig())
	if err != nil {
		_ = ws.Close()
		return
	}
	session := &hubSession{mux: mux, since: time.Now(), addr: remoteIP(r)}
	h.attach(device.ID, session)
	go h.serveUplink(device.ID, mux)
	h.log.Info("device connected", "device", device.ID, "name", device.Name, "addr", session.addr)
	// The request's context ends with the upgrade, so the bookkeeping that
	// follows the connection's life uses its own.
	touch := func(info core.DeviceInfo) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := h.registry.TouchDevice(ctx, device.ID, info, time.Now()); err != nil {
			h.log.Warn("record device activity failed", "device", device.ID, "err", err)
		}
	}
	touch(decodeInfo(r.Header.Get(headerInfo)))
	ticker := time.NewTicker(lastSeenInterval)
	defer ticker.Stop()
	for {
		select {
		case <-mux.CloseChan():
			h.detach(device.ID, session)
			touch(core.DeviceInfo{})
			h.log.Info("device disconnected", "device", device.ID, "name", device.Name)
			return
		case <-ticker.C:
			touch(core.DeviceInfo{})
		}
	}
}

// attach makes session the device's connection. A device has one: a node that
// reconnects before the hub noticed the old connection die replaces it.
func (h *Hub) attach(id string, session *hubSession) {
	h.mu.Lock()
	previous := h.sessions[id]
	h.sessions[id] = session
	presence := h.presence
	h.mu.Unlock()
	if previous != nil {
		_ = previous.mux.Close()
	} else if presence != nil {
		presence(id, true)
	}
}

func (h *Hub) detach(id string, session *hubSession) {
	h.mu.Lock()
	removed := h.sessions[id] == session
	if removed {
		delete(h.sessions, id)
	}
	presence := h.presence
	h.mu.Unlock()
	if removed && presence != nil {
		presence(id, false)
	}
}

// Dial opens a stream to the device. The node serves HTTP on it.
func (h *Hub) Dial(ctx context.Context, id string) (net.Conn, error) {
	h.mu.Lock()
	session := h.sessions[id]
	h.mu.Unlock()
	if session == nil {
		return nil, ErrOffline
	}
	type opened struct {
		conn net.Conn
		err  error
	}
	result := make(chan opened, 1)
	go func() {
		conn, err := session.mux.Open()
		result <- opened{conn, err}
	}()
	select {
	case stream := <-result:
		if stream.err != nil {
			return nil, ErrOffline
		}
		return stream.conn, nil
	case <-ctx.Done():
		go func() {
			if stream := <-result; stream.conn != nil {
				_ = stream.conn.Close()
			}
		}()
		return nil, ctx.Err()
	}
}

// Connection reports the device's live connection, if it has one.
func (h *Hub) Connection(id string) (Connection, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	session := h.sessions[id]
	if session == nil || session.mux.IsClosed() {
		return Connection{}, false
	}
	return Connection{Since: session.since, RemoteAddr: session.addr}, true
}

// Disconnect drops the device's connection, for a device that was revoked.
func (h *Hub) Disconnect(id string) {
	h.mu.Lock()
	session := h.sessions[id]
	delete(h.sessions, id)
	h.mu.Unlock()
	if session != nil {
		_ = session.mux.Close()
	}
}

// Close drops every connection.
func (h *Hub) Close() {
	h.mu.Lock()
	sessions := h.sessions
	h.sessions = map[string]*hubSession{}
	h.mu.Unlock()
	for _, session := range sessions {
		_ = session.mux.Close()
	}
}

// allowPairAttempt limits guesses at a pairing code from one address.
func (h *Hub) allowPairAttempt(addr string, now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	recent := h.attempts[addr][:0]
	for _, at := range h.attempts[addr] {
		if now.Sub(at) < pairAttemptSpan {
			recent = append(recent, at)
		}
	}
	if len(recent) >= pairAttempts {
		h.attempts[addr] = recent
		return false
	}
	h.attempts[addr] = append(recent, now)
	if len(h.attempts) > 4096 {
		for key, times := range h.attempts {
			if len(times) == 0 || now.Sub(times[len(times)-1]) >= pairAttemptSpan {
				delete(h.attempts, key)
			}
		}
	}
	return true
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func writeLinkError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// IsOffline reports whether err means the device has no connection.
func IsOffline(err error) bool { return errors.Is(err, ErrOffline) }
