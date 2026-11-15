package link

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/wangning19940904/AgentMux/core"
)

// The hub's half of the passive link: pairing with a node from its invite,
// and keeping a connection to every node that listens.

const (
	passiveDialTimeout   = 10 * time.Second
	passiveSyncInterval  = 30 * time.Second
	passiveGateInterval  = 2 * time.Second
	passiveRequestLimit  = 64 << 10
	passiveRateLimitWait = time.Minute
)

// errPassiveRateLimited means the node is holding this hub off after failed
// attempts.
var errPassiveRateLimited = errors.New("the node is refusing this hub for a minute after failed attempts")

func passiveClient(addr, fingerprint string) *http.Client {
	return &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		// Nodes are dialed directly: a proxy would see the pinned connection
		// only as bytes anyway, and most hubs dial nodes on their own network.
		Proxy:               nil,
		DialContext:         (&net.Dialer{Timeout: passiveDialTimeout}).DialContext,
		TLSClientConfig:     pinnedTLS(addr, fingerprint),
		TLSHandshakeTimeout: passiveDialTimeout, DisableKeepAlives: true,
	}}
}

// passiveCall sends one request to a node at addr and decodes its answer.
func passiveCall(ctx context.Context, addr, fingerprint, method, path, authorization string, body, output any) error {
	var payload io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, "https://"+addr+path, payload)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", authorization)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := passiveClient(addr, fingerprint).Do(request)
	if err != nil {
		var mismatch *FingerprintMismatchError
		if errors.As(err, &mismatch) {
			return mismatch
		}
		return fmt.Errorf("reach the node at %s: %w", addr, err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(response.Body, passiveRequestLimit))
	switch {
	case response.StatusCode == http.StatusTooManyRequests:
		return errPassiveRateLimited
	case response.StatusCode != http.StatusOK:
		return fmt.Errorf("the node at %s refused: %s", addr, linkErrorMessage(data, response.StatusCode))
	}
	if err := json.Unmarshal(data, output); err != nil {
		return fmt.Errorf("the node at %s answered something else than an AgentMux node would", addr)
	}
	return nil
}

// PassiveInfo asks the node an invite names who it is, trying its addresses
// in order, without using the invite up. It returns the address that
// answered. A node presenting another key than the invite's is never asked.
func PassiveInfo(ctx context.Context, invite Invite) (core.DeviceInfo, string, error) {
	var failures []error
	for _, addr := range invite.Addrs {
		var answer passiveInfoResponse
		err := passiveCall(ctx, addr, invite.Fingerprint, http.MethodGet, PassiveInfoPath, "Pairing "+invite.Secret, nil, &answer)
		if err == nil {
			return cleanInfo(answer.Info), addr, nil
		}
		if IsFingerprintMismatch(err) || ctx.Err() != nil {
			// The invite is for another machine than the one answering; no
			// other address is tried, and nothing is paired.
			return core.DeviceInfo{}, "", err
		}
		failures = append(failures, err)
	}
	return core.DeviceInfo{}, "", errors.Join(failures...)
}

// PassivePair gives the node at addr the hub's credential for it, using the
// invite's secret up. deviceID and name are what the hub calls the node.
func PassivePair(ctx context.Context, invite Invite, addr, credential, deviceID, name string) (core.DeviceInfo, error) {
	var answer passiveInfoResponse
	err := passiveCall(ctx, addr, invite.Fingerprint, http.MethodPost, PassivePairPath, "",
		passivePairRequest{Secret: invite.Secret, Credential: credential, DeviceID: deviceID, Name: name}, &answer)
	return cleanInfo(answer.Info), err
}

// dialPassive opens the multiplexed connection to a node that listens,
// trying its addresses in order. A key mismatch at an address is never
// connected; when no address connects, the mismatch is the error.
func dialPassive(ctx context.Context, target core.PassiveLink, device *core.Device) (*websocket.Conn, string, core.DeviceInfo, error) {
	header := http.Header{}
	header.Set("Authorization", "Bearer "+target.Credential)
	header.Set(headerProtocol, ProtocolVersion)
	header.Set(headerDeviceID, device.ID)
	header.Set(headerDeviceName, url.QueryEscape(device.Name))
	var failures []error
	var mismatch error
	for _, addr := range target.Addrs {
		dialer := websocket.Dialer{
			NetDialContext:   (&net.Dialer{Timeout: passiveDialTimeout}).DialContext,
			HandshakeTimeout: 20 * time.Second,
			TLSClientConfig:  pinnedTLS(addr, target.Fingerprint),
		}
		ws, response, err := dialer.DialContext(ctx, "wss://"+addr+PassiveConnectPath, header)
		if err == nil {
			return ws, addr, decodeInfo(response.Header.Get(headerInfo)), nil
		}
		if ctx.Err() != nil {
			return nil, "", core.DeviceInfo{}, ctx.Err()
		}
		if response != nil {
			payload, _ := io.ReadAll(io.LimitReader(response.Body, pairBodyLimit))
			_ = response.Body.Close()
			switch response.StatusCode {
			case http.StatusUnauthorized:
				// The node answered with its pinned key and refused the
				// credential: no other address will take it either.
				return nil, "", core.DeviceInfo{}, ErrPassiveRefused
			case http.StatusTooManyRequests:
				failures = append(failures, fmt.Errorf("%s: %w", addr, errPassiveRateLimited))
				continue
			}
			failures = append(failures, fmt.Errorf("%s answered %d: %s", addr, response.StatusCode, linkErrorMessage(payload, response.StatusCode)))
			continue
		}
		var pinned *FingerprintMismatchError
		if errors.As(err, &pinned) {
			mismatch = pinned
			continue
		}
		failures = append(failures, fmt.Errorf("%s: %w", addr, err))
	}
	if mismatch != nil {
		return nil, "", core.DeviceInfo{}, mismatch
	}
	if len(failures) == 0 {
		return nil, "", core.DeviceInfo{}, errors.New("the node has no address to dial")
	}
	return nil, "", core.DeviceInfo{}, errors.Join(failures...)
}

// PassiveSource is the hub's record of devices that listen.
type PassiveSource interface {
	// PassiveLinks lists every device the hub dials.
	PassiveLinks(ctx context.Context) ([]core.PassiveLink, error)
	// GetDevice returns a paired device, or (nil, nil) once it is removed.
	GetDevice(ctx context.Context, id string) (*core.Device, error)
}

// Passive dial states.
const (
	PassiveConnecting = "connecting"
	PassiveConnected  = "connected"
	// PassiveWaiting is between attempts after a failure.
	PassiveWaiting = "waiting"
	// PassiveStandby is a hub that does not hold the lease: it does not dial.
	PassiveStandby = "standby"
	// PassiveStopped is a hard failure: the node's key did not match, or it
	// refused the credential. Only adding the node again restarts it.
	PassiveStopped = "stopped"
)

// PassiveDialStatus is how the hub's connection to one listening device
// stands.
type PassiveDialStatus struct {
	State     string     `json:"state"`
	Addr      string     `json:"addr,omitempty"`
	LastError string     `json:"last_error,omitempty"`
	NextDial  *time.Time `json:"next_dial_at,omitempty"`
}

type passiveLoop struct {
	target core.PassiveLink
	cancel context.CancelFunc
	done   chan struct{}
}

// PassiveDialer keeps the hub connected to every device that listens. Only a
// hub that admits devices (Hub.SetGate) dials: a standby hub, or a primary
// whose lease lapsed, waits. Once a connection is up it is the hub's like
// any other (Hub.serveSession).
type PassiveDialer struct {
	hub    *Hub
	source PassiveSource

	syncInterval time.Duration
	gateInterval time.Duration

	mu      sync.Mutex
	loops   map[string]*passiveLoop
	status  map[string]PassiveDialStatus
	refresh chan struct{}
}

// NewPassiveDialer makes the hub's dialer for devices that listen. Run starts
// it.
func (h *Hub) NewPassiveDialer(source PassiveSource) *PassiveDialer {
	return &PassiveDialer{
		hub: h, source: source,
		syncInterval: passiveSyncInterval, gateInterval: passiveGateInterval,
		loops: map[string]*passiveLoop{}, status: map[string]PassiveDialStatus{},
		refresh: make(chan struct{}, 1),
	}
}

// Refresh reads the list of listening devices again now, as after one was
// added or removed.
func (d *PassiveDialer) Refresh() {
	select {
	case d.refresh <- struct{}{}:
	default:
	}
}

// Status reports the hub's connection to a listening device.
func (d *PassiveDialer) Status(deviceID string) (PassiveDialStatus, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	status, ok := d.status[deviceID]
	return status, ok
}

func (d *PassiveDialer) setStatus(deviceID string, status PassiveDialStatus) {
	d.mu.Lock()
	if _, live := d.loops[deviceID]; live {
		d.status[deviceID] = status
	}
	d.mu.Unlock()
}

// Run keeps one dialing loop per listening device until ctx ends. The list is
// read again every half minute, and at once after Refresh. A standby hub
// reads it too, from its copy of hub data, so it can dial the moment it
// takes over.
func (d *PassiveDialer) Run(ctx context.Context) {
	ticker := time.NewTicker(d.syncInterval)
	defer ticker.Stop()
	for {
		d.sync(ctx)
		select {
		case <-ctx.Done():
			d.stopAll()
			return
		case <-ticker.C:
		case <-d.refresh:
		}
	}
}

func samePassiveTarget(a, b core.PassiveLink) bool {
	return a.Fingerprint == b.Fingerprint && a.Credential == b.Credential && slices.Equal(a.Addrs, b.Addrs)
}

func (d *PassiveDialer) sync(ctx context.Context) {
	readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	targets, err := d.source.PassiveLinks(readCtx)
	cancel()
	if err != nil {
		d.hub.log.Warn("read listening devices failed", "err", err)
		return
	}
	wanted := make(map[string]core.PassiveLink, len(targets))
	for _, target := range targets {
		wanted[target.DeviceID] = target
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for id, loop := range d.loops {
		target, ok := wanted[id]
		if ok && samePassiveTarget(loop.target, target) {
			continue
		}
		// Removed, or added again with a new key, credential or address.
		loop.cancel()
		delete(d.loops, id)
		delete(d.status, id)
	}
	for id, target := range wanted {
		if _, running := d.loops[id]; running {
			continue
		}
		loopCtx, cancel := context.WithCancel(ctx)
		loop := &passiveLoop{target: target, cancel: cancel, done: make(chan struct{})}
		d.loops[id] = loop
		d.status[id] = PassiveDialStatus{State: PassiveConnecting}
		go func() {
			defer close(loop.done)
			d.dialLoop(loopCtx, target)
		}()
	}
}

func (d *PassiveDialer) stopAll() {
	d.mu.Lock()
	loops := d.loops
	d.loops = map[string]*passiveLoop{}
	d.mu.Unlock()
	for _, loop := range loops {
		loop.cancel()
		<-loop.done
	}
}

// dialLoop connects to one device and reconnects with backoff, as a node
// reconnects to its hub (Client.Run), until ctx ends or a hard failure.
func (d *PassiveDialer) dialLoop(ctx context.Context, target core.PassiveLink) {
	log := d.hub.log
	backoff := minBackoff
	for ctx.Err() == nil {
		// Only the hub holding the lease dials. A standby or fenced hub
		// waits, and takes over within a moment of being admitted.
		if err := d.hub.admits(); err != nil {
			d.setStatus(target.DeviceID, PassiveDialStatus{State: PassiveStandby, LastError: err.Error()})
			if !sleepContext(ctx, d.gateInterval) {
				return
			}
			backoff = minBackoff
			continue
		}
		readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		device, err := d.source.GetDevice(readCtx, target.DeviceID)
		cancel()
		if err == nil && device == nil {
			return
		}
		if err == nil {
			d.setStatus(target.DeviceID, PassiveDialStatus{State: PassiveConnecting})
			var ws *websocket.Conn
			var addr string
			var info core.DeviceInfo
			ws, addr, info, err = dialPassive(ctx, target, device)
			if err == nil {
				// The gate may have closed while dialing.
				if gateErr := d.hub.admits(); gateErr != nil {
					_ = ws.Close()
					continue
				}
				started := time.Now()
				d.setStatus(target.DeviceID, PassiveDialStatus{State: PassiveConnected, Addr: addr})
				// The session ends with the loop: when the device is added
				// again with new details, or the hub stops.
				stop := context.AfterFunc(ctx, func() { _ = ws.Close() })
				d.hub.serveSession(device, ws, trimAddr(addr), info)
				stop()
				if time.Since(started) >= stableDuration {
					backoff = minBackoff
				}
				err = errors.New("the connection closed")
			}
		}
		if ctx.Err() != nil {
			return
		}
		if IsFingerprintMismatch(err) || errors.Is(err, ErrPassiveRefused) {
			log.Error("passive link stopped", "device", target.DeviceID, "err", err)
			d.setStatus(target.DeviceID, PassiveDialStatus{State: PassiveStopped, LastError: err.Error()})
			return
		}
		wait := time.Duration(float64(backoff) * (0.5 + rand.Float64()/2))
		if errors.Is(err, errPassiveRateLimited) {
			wait = max(wait, passiveRateLimitWait)
		}
		next := time.Now().Add(wait)
		d.setStatus(target.DeviceID, PassiveDialStatus{State: PassiveWaiting, LastError: err.Error(), NextDial: &next})
		log.Warn("passive link lost; dialing again", "device", target.DeviceID, "in", wait.Round(time.Millisecond).String(), "err", err)
		if !sleepContext(ctx, wait) {
			return
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

func sleepContext(ctx context.Context, wait time.Duration) bool {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// PassiveMachines is a Registry that knows which machines the hub dials. A
// machine is paired one way at a time, so a node that dials in to pair is
// refused when the hub already dials the same machine.
type PassiveMachines interface {
	// PassiveDeviceForMachine returns the listening device that is this
	// machine, or "".
	PassiveDeviceForMachine(ctx context.Context, machineUID string) (string, error)
}

// passiveConflict explains why a machine that dials in cannot pair, or "".
func (h *Hub) passiveConflict(ctx context.Context, info core.DeviceInfo) (string, error) {
	checker, ok := h.registry.(PassiveMachines)
	if !ok || info.MachineUID == "" {
		return "", nil
	}
	id, err := checker.PassiveDeviceForMachine(ctx, info.MachineUID)
	if err != nil || id == "" {
		return "", err
	}
	// The device is not named: the code has not been checked yet.
	return "this machine is already paired with the hub through a passive link (the hub dials it); " +
		"a machine is paired one way at a time: remove that device on the hub first", nil
}

// trimAddr is the host of a dialed address, for logs and the device list.
func trimAddr(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return strings.TrimSpace(addr)
	}
	return host
}

// Admits reports why this hub does not take devices now (a standby, or a
// primary whose lease lapsed), or nil when it does.
func (h *Hub) Admits() error { return h.admits() }

// RunPairedHook runs what follows pairing a device (SetPairedHook), for a
// device paired other than through the pairing endpoint, as a listening one
// is.
func (h *Hub) RunPairedHook(ctx context.Context) {
	h.mu.Lock()
	paired := h.paired
	h.mu.Unlock()
	if paired != nil {
		paired(ctx)
	}
}
