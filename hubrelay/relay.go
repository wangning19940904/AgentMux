package hubrelay

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/wangning19940904/AgentMux/link"
)

// Roles a hub reports in a lease response.
const (
	RolePrimary = "primary"
	RoleStandby = "standby"
	RoleFenced  = "fenced"
	RoleNone    = "none"
)

// ErrUnknownBackend is returned by Switch for a name not in the configuration.
var ErrUnknownBackend = errors.New("hubrelay: unknown backend")

const dialTimeout = 5 * time.Second

// LeaseResponse is a hub's answer to a lease request.
type LeaseResponse struct {
	Role       string `json:"role"`
	Epoch      int64  `json:"epoch"`
	AppliedSeq int64  `json:"applied_seq"`
	Ready      bool   `json:"ready"`
}

type leaseRequest struct {
	Epoch  int64 `json:"epoch"`
	Active bool  `json:"active"`
	TTLms  int64 `json:"ttl_ms"`
}

// Status is a snapshot of the relay for the control endpoint.
type Status struct {
	Epoch      int64           `json:"epoch"`
	Active     string          `json:"active"`
	Forwarding string          `json:"forwarding"`
	GrantedAt  time.Time       `json:"granted_at,omitzero"`
	Backends   []BackendStatus `json:"backends"`
}

// BackendStatus is what the relay last heard from one backend.
type BackendStatus struct {
	Name         string         `json:"name"`
	Addr         string         `json:"addr"`
	LastAttempt  time.Time      `json:"last_attempt,omitzero"`
	LastSuccess  time.Time      `json:"last_success,omitzero"`
	LastOK       bool           `json:"last_ok"`
	LastError    string         `json:"last_error,omitempty"`
	LastResponse *LeaseResponse `json:"last_response,omitempty"`
	Conns        int            `json:"conns"`
}

type persistedState struct {
	Epoch     int64  `json:"epoch"`
	Active    string `json:"active"`
	GrantedAt string `json:"granted_at"`
}

type backend struct {
	name, addr string

	// Guarded by Relay.mu.
	lastAttempt time.Time
	lastSuccess time.Time
	lastOK      bool
	lastErr     string
	lastResp    LeaseResponse
	hasResp     bool
	lastGrant   time.Time // last successful active=true call
	conns       map[*fwdConn]struct{}
}

type fwdConn struct{ client, upstream net.Conn }

func (c *fwdConn) close() {
	c.client.Close()
	c.upstream.Close()
}

// Relay forwards the public port to the backend holding the lease.
type Relay struct {
	cfg     Config
	log     *slog.Logger
	client  *http.Client
	started time.Time

	// opMu serialises lease rounds and the decision parts of Switch, so two
	// grants can never be computed from the same epoch.
	opMu sync.Mutex

	mu        sync.Mutex
	backends  []*backend
	byName    map[string]*backend
	epoch     int64
	active    string    // lease holder per the persisted state
	forward   string    // backend new connections go to; set once a grant succeeds
	grantedAt time.Time // when the current epoch was decided
	// holdUntil keeps the active backend from being granted (and from being
	// failed over) while a previous holder's lease may still be running.
	holdUntil      time.Time
	warnedNoTarget bool
}

// New builds a relay from cfg and the state file it names.
func New(cfg Config, log *slog.Logger) (*Relay, error) {
	if err := cfg.normalize(); err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}
	r := &Relay{
		cfg:     cfg,
		log:     log,
		started: time.Now(),
		byName:  make(map[string]*backend, len(cfg.Backends)),
	}
	for _, b := range cfg.Backends {
		be := &backend{name: b.Name, addr: b.Addr, conns: make(map[*fwdConn]struct{})}
		r.backends = append(r.backends, be)
		r.byName[b.Name] = be
	}
	pin := cfg.Fingerprint
	r.client = &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{
			ServerName: "agentmux-hub",
			// The hub has no CA-signed certificate; the pin below is the
			// only check, as it is for devices.
			InsecureSkipVerify: true,
			VerifyConnection: func(cs tls.ConnectionState) error {
				if len(cs.PeerCertificates) == 0 {
					return errors.New("hubrelay: backend presented no certificate")
				}
				if got := link.Fingerprint(cs.PeerCertificates[0].Raw); got != pin {
					return fmt.Errorf("hubrelay: backend certificate fingerprint %s does not match the pin", got)
				}
				return nil
			},
		},
		ForceAttemptHTTP2:   false,
		MaxIdleConnsPerHost: 1,
	}}
	if err := r.loadState(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Relay) loadState() error {
	raw, err := os.ReadFile(r.cfg.StateFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("hubrelay: read state: %w", err)
	}
	var st persistedState
	if err := json.Unmarshal(raw, &st); err != nil {
		return fmt.Errorf("hubrelay: parse state: %w", err)
	}
	r.epoch = st.Epoch
	if st.GrantedAt != "" {
		if t, err := time.Parse(time.RFC3339Nano, st.GrantedAt); err == nil {
			r.grantedAt = t
		}
	}
	if _, ok := r.byName[st.Active]; ok {
		r.active = st.Active
		// The previous relay process may have crashed between deciding this
		// epoch and the old holder's lease running out; wait that out.
		r.holdUntil = r.grantedAt.Add(r.cfg.LeaseTTL)
	} else if st.Active != "" {
		r.log.Warn("hubrelay: state names an unknown backend; choosing again", "active", st.Active)
	}
	return nil
}

// persistLocked writes the state atomically. Callers hold r.mu.
func (r *Relay) persistLocked() error {
	raw, err := json.Marshal(persistedState{
		Epoch:     r.epoch,
		Active:    r.active,
		GrantedAt: r.grantedAt.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return err
	}
	dir := filepath.Dir(r.cfg.StateFile)
	f, err := os.CreateTemp(dir, ".relay-state-*")
	if err != nil {
		return fmt.Errorf("hubrelay: write state: %w", err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return fmt.Errorf("hubrelay: write state: %w", err)
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return fmt.Errorf("hubrelay: write state: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("hubrelay: write state: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("hubrelay: write state: %w", err)
	}
	if err := os.Rename(tmp, r.cfg.StateFile); err != nil {
		return fmt.Errorf("hubrelay: write state: %w", err)
	}
	return nil
}

// Run serves the public listener, the control listener and the lease loop
// until ctx is done.
func (r *Relay) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", r.cfg.Listen)
	if err != nil {
		return fmt.Errorf("hubrelay: listen: %w", err)
	}
	ctl, err := net.Listen("tcp", r.cfg.Control)
	if err != nil {
		ln.Close()
		return fmt.Errorf("hubrelay: control listen: %w", err)
	}
	return r.serve(ctx, ln, ctl)
}

func (r *Relay) serve(ctx context.Context, ln, ctl net.Listener) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	srv := &http.Server{Handler: r.controlHandler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		errc <- r.acceptLoop(ctx, ln)
	}()
	go func() {
		defer wg.Done()
		if err := srv.Serve(ctl); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- fmt.Errorf("hubrelay: control: %w", err)
		}
	}()
	go func() {
		defer wg.Done()
		r.leaseLoop(ctx)
	}()

	var err error
	select {
	case <-ctx.Done():
	case err = <-errc:
	}
	cancel()
	ln.Close()
	srv.Close()
	r.closeConns("")
	wg.Wait()
	return err
}

func (r *Relay) leaseLoop(ctx context.Context) {
	t := time.NewTicker(r.cfg.RenewInterval)
	defer t.Stop()
	for {
		r.round(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// round renews or demotes every backend, then picks or replaces the active one.
func (r *Relay) round(ctx context.Context) {
	r.opMu.Lock()
	defer r.opMu.Unlock()
	if ctx.Err() != nil {
		return
	}

	r.mu.Lock()
	epoch, active, hold := r.epoch, r.active, time.Now().Before(r.holdUntil)
	r.mu.Unlock()

	var wg sync.WaitGroup
	for _, b := range r.backends {
		isActive := b.name == active
		if isActive && hold {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.lease(ctx, b, epoch, isActive)
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return
	}
	r.decide(ctx)
}

// decide chooses a first backend, or fails over from one whose lease has
// certainly expired on its own.
func (r *Relay) decide(ctx context.Context) {
	r.mu.Lock()
	now := time.Now()
	if r.active == "" {
		target, maxEpoch := r.chooseLocked("", false)
		if target == nil {
			r.mu.Unlock()
			return
		}
		prevEpoch, prevGranted := r.epoch, r.grantedAt
		r.epoch = max(r.epoch, maxEpoch) + 1
		r.active, r.grantedAt = target.name, now
		if err := r.persistLocked(); err != nil {
			r.epoch, r.active, r.grantedAt = prevEpoch, "", prevGranted
			r.mu.Unlock()
			r.log.Error("hubrelay: cannot persist the first grant", "err", err)
			return
		}
		epoch := r.epoch
		r.mu.Unlock()
		r.log.Info("hubrelay: granting the first lease", "backend", target.name, "epoch", epoch)
		r.lease(ctx, target, epoch, true)
		return
	}
	if now.Before(r.holdUntil) {
		r.mu.Unlock()
		return
	}
	cur := r.byName[r.active]
	last := cur.lastSuccess
	if last.Before(r.started) {
		// A previous relay process may have renewed it just before exiting.
		last = r.started
	}
	if now.Sub(last) <= r.cfg.LeaseTTL+r.cfg.RenewInterval {
		r.warnedNoTarget = false
		r.mu.Unlock()
		return
	}
	target, _ := r.chooseLocked(cur.name, true)
	if target == nil {
		if !r.warnedNoTarget {
			r.warnedNoTarget = true
			r.log.Warn("hubrelay: active backend is unreachable and no other backend is ready; keeping it", "backend", cur.name, "last_success", cur.lastSuccess)
		}
		r.mu.Unlock()
		return
	}
	prevEpoch, prevGranted := r.epoch, r.grantedAt
	r.epoch++
	r.active, r.grantedAt = target.name, now
	if err := r.persistLocked(); err != nil {
		r.epoch, r.active, r.grantedAt = prevEpoch, cur.name, prevGranted
		r.mu.Unlock()
		r.log.Error("hubrelay: cannot persist failover; not granting", "err", err)
		return
	}
	r.warnedNoTarget = false
	epoch := r.epoch
	r.mu.Unlock()
	r.log.Warn("hubrelay: failing over", "from", cur.name, "to", target.name, "epoch", epoch, "last_success", cur.lastSuccess)
	r.lease(ctx, target, epoch, true)
}

// chooseLocked returns the best candidate other than exclude among backends
// whose latest call succeeded: role primary first, then the highest reported
// epoch, then config order. Without requireReady a backend reporting primary
// qualifies even if not ready (it already serves). It also returns the highest
// epoch any backend reported in its latest successful response.
func (r *Relay) chooseLocked(exclude string, requireReady bool) (*backend, int64) {
	var cands []*backend
	var maxEpoch int64
	for _, b := range r.backends {
		if !b.lastOK {
			continue
		}
		maxEpoch = max(maxEpoch, b.lastResp.Epoch)
		if b.name == exclude {
			continue
		}
		if b.lastResp.Ready || (!requireReady && b.lastResp.Role == RolePrimary) {
			cands = append(cands, b)
		}
	}
	if len(cands) == 0 {
		return nil, maxEpoch
	}
	sort.SliceStable(cands, func(i, j int) bool {
		pi, pj := cands[i].lastResp.Role == RolePrimary, cands[j].lastResp.Role == RolePrimary
		if pi != pj {
			return pi
		}
		return cands[i].lastResp.Epoch > cands[j].lastResp.Epoch
	})
	return cands[0], maxEpoch
}

// lease makes one lease call and records the outcome. A successful grant to
// the active backend at the current epoch moves forwarding to it.
func (r *Relay) lease(ctx context.Context, b *backend, epoch int64, active bool) (LeaseResponse, error) {
	cctx, cancel := context.WithTimeout(ctx, r.cfg.RenewInterval)
	defer cancel()
	resp, err := r.callLease(cctx, b.addr, leaseRequest{Epoch: epoch, Active: active, TTLms: r.cfg.LeaseTTL.Milliseconds()})

	var stale []*fwdConn
	r.mu.Lock()
	now := time.Now()
	b.lastAttempt = now
	b.lastOK = err == nil
	if err != nil {
		b.lastErr = err.Error()
	} else {
		b.lastErr = ""
		b.lastSuccess = now
		b.lastResp, b.hasResp = resp, true
		if active {
			b.lastGrant = now
			if r.active == b.name && r.epoch == epoch && r.forward != b.name {
				prev := r.forward
				r.forward = b.name
				stale = r.takeConnsLocked(b.name)
				r.log.Info("hubrelay: forwarding to backend", "backend", b.name, "epoch", epoch, "previous", prev, "closing", len(stale))
			}
		}
	}
	r.mu.Unlock()
	for _, c := range stale {
		c.close()
	}
	if err != nil {
		r.log.Debug("hubrelay: lease call failed", "backend", b.name, "active", active, "epoch", epoch, "err", err)
	}
	return resp, err
}

func (r *Relay) callLease(ctx context.Context, addr string, req leaseRequest) (LeaseResponse, error) {
	body, _ := json.Marshal(req)
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+addr+"/hub/v1/lease", bytes.NewReader(body))
	if err != nil {
		return LeaseResponse{}, err
	}
	hreq.Header.Set("Authorization", "Bearer "+r.cfg.Secret)
	hreq.Header.Set("Content-Type", "application/json")
	res, err := r.client.Do(hreq)
	if err != nil {
		return LeaseResponse{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
		return LeaseResponse{}, fmt.Errorf("lease: HTTP %d", res.StatusCode)
	}
	var out LeaseResponse
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<16)).Decode(&out); err != nil {
		return LeaseResponse{}, fmt.Errorf("lease: decode response: %w", err)
	}
	return out, nil
}

// Switch moves the lease to name by hand. The current holder is demoted
// first, and name is granted only once the old lease has been given up or has
// certainly expired, so the two hubs never both hold one.
func (r *Relay) Switch(ctx context.Context, name string) error {
	r.opMu.Lock()
	r.mu.Lock()
	target := r.byName[name]
	if target == nil {
		r.mu.Unlock()
		r.opMu.Unlock()
		return fmt.Errorf("%w %q", ErrUnknownBackend, name)
	}
	if r.active == name {
		r.mu.Unlock()
		r.opMu.Unlock()
		return nil
	}
	if !target.lastOK || !target.lastResp.Ready || time.Since(target.lastSuccess) > r.cfg.LeaseTTL {
		r.mu.Unlock()
		r.opMu.Unlock()
		return fmt.Errorf("hubrelay: backend %q has not reported ready recently", name)
	}
	old := r.active
	prevEpoch, prevGranted := r.epoch, r.grantedAt
	r.epoch++
	r.active, r.grantedAt = name, time.Now()
	if err := r.persistLocked(); err != nil {
		r.epoch, r.active, r.grantedAt = prevEpoch, old, prevGranted
		r.mu.Unlock()
		r.opMu.Unlock()
		return err
	}
	epoch := r.epoch
	// Hold the target until the old lease is dealt with; the loop must not
	// grant it, nor fail it over, in the meantime.
	r.holdUntil = time.Now().Add(r.cfg.LeaseTTL)
	r.mu.Unlock()
	r.log.Info("hubrelay: switching", "from", old, "to", name, "epoch", epoch)

	holdUntil := time.Now()
	if old != "" {
		ob := r.byName[old]
		resp, err := r.lease(ctx, ob, epoch, false)
		if err != nil || resp.Role == RolePrimary {
			r.mu.Lock()
			last := ob.lastGrant
			if last.Before(r.started) {
				last = r.started
			}
			holdUntil = last.Add(r.cfg.LeaseTTL)
			r.mu.Unlock()
			r.log.Info("hubrelay: old primary did not acknowledge; waiting for its lease to expire", "backend", old, "until", holdUntil)
		}
	}
	r.mu.Lock()
	r.holdUntil = holdUntil
	r.mu.Unlock()
	r.opMu.Unlock()

	if d := time.Until(holdUntil); d > 0 {
		t := time.NewTimer(d)
		select {
		case <-ctx.Done():
			t.Stop()
			// The loop grants the target once the hold passes.
			return ctx.Err()
		case <-t.C:
		}
	}

	r.opMu.Lock()
	defer r.opMu.Unlock()
	r.mu.Lock()
	superseded := r.active != name || r.epoch != epoch
	r.mu.Unlock()
	if superseded {
		return fmt.Errorf("hubrelay: switch to %q superseded", name)
	}
	if _, err := r.lease(ctx, target, epoch, true); err != nil {
		return fmt.Errorf("hubrelay: grant %q: %w", name, err)
	}
	return nil
}

// Status returns a snapshot of the relay.
func (r *Relay) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := Status{Epoch: r.epoch, Active: r.active, Forwarding: r.forward, GrantedAt: r.grantedAt}
	for _, b := range r.backends {
		bs := BackendStatus{
			Name: b.name, Addr: b.addr,
			LastAttempt: b.lastAttempt, LastSuccess: b.lastSuccess,
			LastOK: b.lastOK, LastError: b.lastErr, Conns: len(b.conns),
		}
		if b.hasResp {
			resp := b.lastResp
			bs.LastResponse = &resp
		}
		st.Backends = append(st.Backends, bs)
	}
	return st
}

func (r *Relay) acceptLoop(ctx context.Context, ln net.Listener) error {
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			return fmt.Errorf("hubrelay: accept: %w", err)
		}
		go r.forwardConn(ctx, c)
	}
}

func (r *Relay) forwardConn(ctx context.Context, client net.Conn) {
	r.mu.Lock()
	b := r.byName[r.forward]
	r.mu.Unlock()
	if b == nil {
		// No hub holds a lease yet; refusing is better than a dead end.
		client.Close()
		return
	}
	d := net.Dialer{Timeout: dialTimeout}
	up, err := d.DialContext(ctx, "tcp", b.addr)
	if err != nil {
		r.log.Debug("hubrelay: dial backend failed", "backend", b.name, "err", err)
		client.Close()
		return
	}
	fc := &fwdConn{client: client, upstream: up}
	r.mu.Lock()
	if r.forward != b.name {
		// Forwarding moved while dialling.
		r.mu.Unlock()
		fc.close()
		return
	}
	b.conns[fc] = struct{}{}
	r.mu.Unlock()

	done := make(chan struct{}, 2)
	pipe := func(dst, src net.Conn) {
		io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		} else {
			dst.Close()
		}
		done <- struct{}{}
	}
	go pipe(up, client)
	go pipe(client, up)
	<-done
	<-done
	fc.close()
	r.mu.Lock()
	delete(b.conns, fc)
	r.mu.Unlock()
}

// takeConnsLocked removes and returns forwarded connections to every backend
// except keep ("" takes all). Callers hold r.mu and close them after unlocking.
func (r *Relay) takeConnsLocked(keep string) []*fwdConn {
	var out []*fwdConn
	for _, b := range r.backends {
		if b.name == keep {
			continue
		}
		for c := range b.conns {
			out = append(out, c)
			delete(b.conns, c)
		}
	}
	return out
}

func (r *Relay) closeConns(keep string) {
	r.mu.Lock()
	cs := r.takeConnsLocked(keep)
	r.mu.Unlock()
	for _, c := range cs {
		c.close()
	}
}

// controlHandler serves the local-only status and switch endpoints.
func (r *Relay) controlHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /relay/v1/status", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, r.Status())
	})
	mux.HandleFunc("POST /relay/v1/switch", func(w http.ResponseWriter, req *http.Request) {
		to := req.URL.Query().Get("to")
		if to == "" {
			http.Error(w, "missing ?to=<backend>", http.StatusBadRequest)
			return
		}
		if err := r.Switch(req.Context(), to); err != nil {
			code := http.StatusConflict
			if errors.Is(err, ErrUnknownBackend) {
				code = http.StatusNotFound
			}
			http.Error(w, err.Error(), code)
			return
		}
		writeJSON(w, http.StatusOK, r.Status())
	})
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
