package link

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"

	"github.com/wangning19940904/AgentMux/core"
)

const (
	minBackoff     = time.Second
	maxBackoff     = time.Minute
	stableDuration = 30 * time.Second
)

// Pair exchanges a join token's one-time code for this device's membership.
func Pair(ctx context.Context, join JoinToken, info core.DeviceInfo) (State, error) {
	body, _ := json.Marshal(pairRequest{Code: join.Code, Info: info})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, join.HubURL+PairPath, bytes.NewReader(body))
	if err != nil {
		return State{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, TLSClientConfig: clientTLS(join.Fingerprint)},
	}
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		return State{}, fmt.Errorf("reach the hub at %s: %w", join.HubURL, err)
	}
	defer response.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(response.Body, pairBodyLimit))
	if response.StatusCode != http.StatusOK {
		return State{}, fmt.Errorf("the hub refused to pair: %s", linkErrorMessage(payload, response.StatusCode))
	}
	var paired pairResponse
	if err := json.Unmarshal(payload, &paired); err != nil || paired.Token == "" {
		return State{}, errors.New("the hub's pairing answer could not be read; is this address an AgentMux hub?")
	}
	return State{
		HubURL: join.HubURL, Fingerprint: join.Fingerprint, DeviceID: paired.DeviceID,
		Token: paired.Token, Name: paired.Name, JoinedAt: time.Now().UTC(),
	}, nil
}

func linkErrorMessage(payload []byte, status int) string {
	var envelope struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(payload, &envelope) == nil && envelope.Error != "" {
		return envelope.Error
	}
	return http.StatusText(status)
}

// ClientStatus is a node's view of its connection to the hub.
type ClientStatus struct {
	HubURL    string     `json:"hub_url"`
	DeviceID  string     `json:"device_id"`
	Name      string     `json:"name,omitempty"`
	Connected bool       `json:"connected"`
	Since     *time.Time `json:"since,omitempty"`
	Revoked   bool       `json:"revoked,omitempty"`
	LastError string     `json:"last_error,omitempty"`
	// Reports is what this node has sent the hub about itself.
	Reports []ReportStatus `json:"reports,omitempty"`
}

// Client keeps a node connected to its hub and serves the hub's requests.
type Client struct {
	State State
	Info  core.DeviceInfo
	// Handler answers the hub. LocalAPIHandler is the usual one.
	Handler http.Handler
	Log     *slog.Logger
	// Reports are what the node tells the hub about itself. Nil means
	// DefaultReports; an empty list reports nothing.
	Reports        []Report
	ReportInterval time.Duration

	mu           sync.Mutex
	status       ClientStatus
	reportStatus reportStatuses
	session      *yamux.Session
}

// Uplink returns an HTTP client whose requests travel up the link to the
// hub, which answers them knowing which device asked. Requests fail with
// ErrOffline while the node is not connected. Use any host in the URL.
func (c *Client) Uplink() *http.Client {
	return &http.Client{Timeout: time.Minute, Transport: &http.Transport{
		Proxy: nil, DisableKeepAlives: true,
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			c.mu.Lock()
			session := c.session
			c.mu.Unlock()
			if session == nil {
				return nil, ErrOffline
			}
			return session.Open()
		},
	}}
}

// Status reports whether the node is connected now.
func (c *Client) Status() ClientStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	status := c.status
	status.HubURL, status.DeviceID, status.Name = c.State.HubURL, c.State.DeviceID, c.State.Name
	status.Reports = c.reportStatusList()
	return status
}

func (c *Client) setStatus(update func(*ClientStatus)) {
	c.mu.Lock()
	update(&c.status)
	c.mu.Unlock()
}

// Run connects and reconnects until ctx ends or the hub revokes the device.
func (c *Client) Run(ctx context.Context) error {
	if c.Log == nil {
		c.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	backoff := minBackoff
	for {
		started := time.Now()
		err := c.connect(ctx)
		c.setStatus(func(status *ClientStatus) {
			status.Connected, status.Since = false, nil
			if err != nil {
				status.LastError = err.Error()
			}
		})
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, ErrRevoked) {
			c.setStatus(func(status *ClientStatus) { status.Revoked = true })
			c.Log.Warn("hub link stopped: the hub no longer accepts this device", "hub", c.State.HubURL)
			return err
		}
		if time.Since(started) >= stableDuration {
			backoff = minBackoff
		}
		c.Log.Warn("hub link lost; reconnecting", "hub", c.State.HubURL, "in", backoff.String(), "err", err)
		// Jitter keeps a hub restart from being met by every node at once.
		wait := time.Duration(float64(backoff) * (0.5 + rand.Float64()/2))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

func (c *Client) connect(ctx context.Context) error {
	target, err := url.Parse(c.State.HubURL + ConnectPath)
	if err != nil {
		return err
	}
	if target.Scheme == "https" {
		target.Scheme = "wss"
	} else {
		target.Scheme = "ws"
	}
	dialer := websocket.Dialer{
		Proxy: http.ProxyFromEnvironment, HandshakeTimeout: 20 * time.Second,
		TLSClientConfig: clientTLS(c.State.Fingerprint),
	}
	header := http.Header{}
	header.Set("Authorization", "Bearer "+c.State.Token)
	header.Set(headerProtocol, ProtocolVersion)
	header.Set(headerInfo, encodeInfo(c.Info))
	ws, response, err := dialer.DialContext(ctx, target.String(), header)
	if err != nil {
		if response != nil {
			payload, _ := io.ReadAll(io.LimitReader(response.Body, pairBodyLimit))
			_ = response.Body.Close()
			if response.StatusCode == http.StatusUnauthorized {
				return ErrRevoked
			}
			return fmt.Errorf("the hub answered %d: %s", response.StatusCode, linkErrorMessage(payload, response.StatusCode))
		}
		return err
	}
	session, err := yamux.Server(&wsStream{ws: ws}, muxConfig())
	if err != nil {
		_ = ws.Close()
		return err
	}
	now := time.Now()
	c.setStatus(func(status *ClientStatus) {
		status.Connected, status.Since, status.LastError, status.Revoked = true, &now, "", false
	})
	c.Log.Info("hub link connected", "hub", c.State.HubURL, "device", c.State.DeviceID)
	c.mu.Lock()
	c.session = session
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.session == session {
			c.session = nil
		}
		c.mu.Unlock()
	}()

	// Each stream the hub opens is one connection to this server.
	server := &http.Server{Handler: c.Handler, ReadHeaderTimeout: 30 * time.Second}
	stop := context.AfterFunc(ctx, func() { _ = session.Close() })
	defer stop()
	// Reporting lasts as long as this connection: a new connection starts
	// again from what the hub then holds.
	reportCtx, stopReports := context.WithCancel(ctx)
	reported := make(chan struct{})
	go func() {
		defer close(reported)
		c.report(reportCtx, session)
	}()
	err = server.Serve(session)
	stopReports()
	_ = server.Close()
	_ = session.Close()
	<-reported
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		err = errors.New("the connection closed")
	}
	return err
}

// LoopbackAddr is where a server listening on addr answers on this machine:
// a listen-on-everything host becomes loopback.
func LoopbackAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// LocalAPIHandler forwards the hub's requests to this node's own API on addr.
// The hub does not hold the node's API token: the node adds it here, so a hub
// reaches the node only while the node chooses to stay connected.
func LocalAPIHandler(addr, token string) http.Handler {
	target := &url.URL{Scheme: "http", Host: addr}
	proxy := &httputil.ReverseProxy{
		Transport:     &http.Transport{Proxy: nil, MaxIdleConnsPerHost: 8, IdleConnTimeout: 60 * time.Second},
		FlushInterval: -1,
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(target)
			request.Out.Host = addr
			request.Out.Header.Del("Cookie")
			// Observability has its own credentials, which the caller carries.
			if !strings.HasPrefix(request.Out.URL.Path, "/api/v1/observability/") {
				request.Out.Header.Del("Authorization")
				if token != "" {
					request.Out.Header.Set("Authorization", "Bearer "+token)
				}
			}
			request.Out.Header.Set("X-AgentMux-Hub-Link", "1")
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeLinkError(w, http.StatusBadGateway, "this device's AgentMux API did not answer: "+err.Error())
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") && !strings.HasPrefix(r.URL.Path, "/v1/") {
			writeLinkError(w, http.StatusNotFound, "only the AgentMux API is reachable through the hub link")
			return
		}
		proxy.ServeHTTP(w, r)
	})
}
