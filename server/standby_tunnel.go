package server

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	linkpkg "github.com/wangning19940904/AgentMux/link"
)

// A standby hub keeps the primary's machine list, but the machines link to
// the primary, so the standby cannot open a stream to any of them itself. The
// primary opens one for it and joins the two: every page the standby shows
// then reaches the machines live, as it does on the primary.
//
// Only the standby may do this. It holds a copy of the whole hub and takes
// over from it, so it reaches nothing it could not reach after a failover.

const (
	standbyDialPath = "/uplink/v1/standby/dial/"
	tunnelProtocol  = "agentmux-stream"
	// hubPresenceTTL is how long a standby trusts the primary's list of
	// connected machines before asking again.
	hubPresenceTTL = 10 * time.Second
)

func (s *Server) registerStandbyTunnel(mux *http.ServeMux) {
	mux.HandleFunc("GET "+standbyDialPath+"{id}", s.handleStandbyDial)
}

// handleStandbyDial joins the standby to a stream to one of the machines.
func (s *Server) handleStandbyDial(w http.ResponseWriter, r *http.Request) {
	source, ok := s.relaySource(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if !s.isPrimaryHub(ctx) {
		writeErr(w, http.StatusConflict, "this machine is not the primary hub")
		return
	}
	if !s.isStandbyDevice(ctx, source.ID) {
		writeErr(w, http.StatusForbidden, "only the standby hub may open streams through this hub")
		return
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), tunnelProtocol) {
		writeErr(w, http.StatusBadRequest, "upgrade to "+tunnelProtocol+" required")
		return
	}
	targetID := strings.TrimSpace(r.PathValue("id"))
	var target net.Conn
	if s.ownDeviceIDs()[targetID] {
		target = s.localAPIStream()
	} else {
		if device, err := s.st.GetDevice(ctx, targetID); err != nil || device == nil || s.devices == nil {
			writeErr(w, http.StatusNotFound, "no such machine")
			return
		}
		conn, err := s.devices.Dial(ctx, targetID)
		if err != nil {
			status := http.StatusBadGateway
			if linkpkg.IsOffline(err) {
				status = http.StatusServiceUnavailable
			}
			writeErr(w, status, err.Error())
			return
		}
		target = conn
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		_ = target.Close()
		writeErr(w, http.StatusInternalServerError, "this connection cannot carry a stream")
		return
	}
	conn, buffered, err := hijacker.Hijack()
	if err != nil {
		_ = target.Close()
		return
	}
	_, err = io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: "+tunnelProtocol+"\r\n\r\n")
	if err != nil {
		_ = conn.Close()
		_ = target.Close()
		return
	}
	joinStreams(&bufferedConn{Conn: conn, reader: buffered.Reader}, target)
}

func (s *Server) isStandbyDevice(ctx context.Context, id string) bool {
	if s.standbyForTest != "" {
		return id == s.standbyForTest
	}
	role, err := s.st.DeviceHubRole(ctx, id)
	return err == nil && role == "standby"
}

// localAPIStream serves this machine's own API on one end of a pipe, as a
// machine serves the streams its hub opens.
func (s *Server) localAPIStream() net.Conn {
	token := ""
	if s.cfg.Bridge.Enabled {
		token = s.cfg.Bridge.Token
	}
	near, far := net.Pipe()
	server := &http.Server{
		Handler:           linkpkg.LocalAPIHandler(linkpkg.LoopbackAddr(s.cfg.Server.Addr), token),
		ReadHeaderTimeout: 30 * time.Second,
	}
	go func() { _ = server.Serve(&oneConnListener{conn: far}) }()
	return near
}

// joinStreams copies both ways until either side ends.
func joinStreams(a, b net.Conn) {
	done := make(chan struct{}, 2)
	copyHalf := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		done <- struct{}{}
	}
	go copyHalf(a, b)
	go copyHalf(b, a)
	<-done
	_ = a.Close()
	_ = b.Close()
	<-done
}

// dialViaHub opens a stream to a machine through the primary hub, for a
// standby. It fails as offline when this machine follows no hub.
func (s *Server) dialViaHub(ctx context.Context, id string) (net.Conn, error) {
	uplink := s.HubRelay().Uplink()
	if uplink == nil || s.isPrimaryHub(ctx) {
		return nil, linkpkg.ErrOffline
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://hub"+standbyDialPath+url.PathEscape(id), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", tunnelProtocol)
	response, err := uplink.RoundTrip(request)
	if err != nil {
		return nil, linkpkg.ErrOffline
	}
	if response.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		_ = response.Body.Close()
		if response.StatusCode == http.StatusServiceUnavailable {
			return nil, linkpkg.ErrOffline
		}
		return nil, fmt.Errorf("the hub refused the stream: HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	stream, ok := response.Body.(io.ReadWriteCloser)
	if !ok {
		_ = response.Body.Close()
		return nil, errors.New("the hub's stream cannot be written to")
	}
	return &streamConn{ReadWriteCloser: stream}, nil
}

// hubPresence says which machines are connected to the primary hub, for a
// standby. It is nil on the primary and on a machine that follows no hub.
func (s *Server) hubPresence(ctx context.Context) map[string]bool {
	uplink := s.HubRelay().Uplink()
	if uplink == nil || s.isPrimaryHub(ctx) {
		return nil
	}
	s.fleet.mu.Lock()
	fresh := time.Since(s.fleet.presenceAt) < hubPresenceTTL
	presence := s.fleet.presence
	s.fleet.mu.Unlock()
	if fresh {
		return presence
	}
	// Right after the server starts, the link to the hub is not up yet;
	// asking at once would list every machine as offline until the answer
	// expires.
	if starting := s.node.startingUplink(); starting != nil {
		waitCtx, cancel := reconnectContext(ctx)
		starting.WaitConnected(waitCtx)
		cancel()
	}
	_, _ = s.fetchHubFleet(ctx, uplink)
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	return s.fleet.presence
}

// deviceOnline says whether a machine is connected to this hub or, on a
// standby, to the primary.
func (s *Server) deviceOnline(ctx context.Context, id string) bool {
	if s.linkHub != nil {
		if _, online := s.linkHub.Connection(id); online {
			return true
		}
	}
	return s.hubPresence(ctx)[id]
}

// streamConn is a stream the hub joined, as a connection.
type streamConn struct{ io.ReadWriteCloser }

func (*streamConn) LocalAddr() net.Addr              { return tunnelAddr{} }
func (*streamConn) RemoteAddr() net.Addr             { return tunnelAddr{} }
func (*streamConn) SetDeadline(time.Time) error      { return nil }
func (*streamConn) SetReadDeadline(time.Time) error  { return nil }
func (*streamConn) SetWriteDeadline(time.Time) error { return nil }

type tunnelAddr struct{}

func (tunnelAddr) Network() string { return tunnelProtocol }
func (tunnelAddr) String() string  { return "hub" }

// bufferedConn reads first what the HTTP server had already read.
type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

// oneConnListener hands out one connection, then waits until closed.
type oneConnListener struct {
	mu     sync.Mutex
	conn   net.Conn
	closed chan struct{}
	once   sync.Once
}

func (l *oneConnListener) done() chan struct{} {
	l.once.Do(func() { l.closed = make(chan struct{}) })
	return l.closed
}

func (l *oneConnListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	conn := l.conn
	l.conn = nil
	l.mu.Unlock()
	if conn != nil {
		return &closeNotifyConn{Conn: conn, listener: l}, nil
	}
	<-l.done()
	return nil, net.ErrClosed
}

func (l *oneConnListener) Close() error {
	l.done()
	l.mu.Lock()
	defer l.mu.Unlock()
	select {
	case <-l.closed:
	default:
		close(l.closed)
	}
	return nil
}

func (l *oneConnListener) Addr() net.Addr { return tunnelAddr{} }

// closeNotifyConn closes its listener with it, so the server serving the one
// connection stops.
type closeNotifyConn struct {
	net.Conn
	listener *oneConnListener
	once     sync.Once
}

func (c *closeNotifyConn) Close() error {
	c.once.Do(func() { _ = c.listener.Close() })
	return c.Conn.Close()
}
