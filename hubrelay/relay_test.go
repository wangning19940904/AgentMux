package hubrelay

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/link"
)

const (
	testSecret = "s3cret"
	testTTL    = 200 * time.Millisecond
	testRenew  = 40 * time.Millisecond
)

type leaseCall struct {
	at  time.Time
	req leaseRequest
}

// fakeHub answers lease requests the way a hub would and serves /whoami so a
// test can tell which hub a forwarded connection reached.
type fakeHub struct {
	name string
	srv  *httptest.Server

	mu    sync.Mutex
	role  string
	epoch int64
	ready bool
	fail  bool
	calls []leaseCall
}

func newFakeHub(t *testing.T, name, role string, epoch int64, ready bool) *fakeHub {
	t.Helper()
	h := &fakeHub{name: name, role: role, epoch: epoch, ready: ready}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /hub/v1/lease", h.lease)
	mux.HandleFunc("GET /whoami", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, name)
	})
	h.srv = httptest.NewTLSServer(mux)
	t.Cleanup(h.srv.Close)
	return h
}

func (h *fakeHub) lease(w http.ResponseWriter, req *http.Request) {
	if req.Header.Get("Authorization") != "Bearer "+testSecret || req.Header.Get("Content-Type") != "application/json" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var lr leaseRequest
	if err := json.NewDecoder(req.Body).Decode(&lr); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.fail {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	h.calls = append(h.calls, leaseCall{at: time.Now(), req: lr})
	switch {
	case lr.Active:
		h.role, h.epoch = RolePrimary, lr.Epoch
	case lr.Epoch > h.epoch && h.role == RolePrimary:
		h.role, h.epoch = RoleStandby, lr.Epoch
	}
	json.NewEncoder(w).Encode(LeaseResponse{Role: h.role, Epoch: h.epoch, Ready: h.ready})
}

func (h *fakeHub) addr() string { return h.srv.Listener.Addr().String() }

func (h *fakeHub) setFail(v bool) {
	h.mu.Lock()
	h.fail = v
	h.mu.Unlock()
}

func (h *fakeHub) leaseCalls() []leaseCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]leaseCall(nil), h.calls...)
}

func (h *fakeHub) firstGrant(epoch int64) (time.Time, bool) {
	for _, c := range h.leaseCalls() {
		if c.req.Active && c.req.Epoch == epoch {
			return c.at, true
		}
	}
	return time.Time{}, false
}

type harness struct {
	relay     *Relay
	addr      string
	ctlAddr   string
	stateFile string
	cancel    context.CancelFunc
	done      chan error
}

func testConfig(t *testing.T, hubs ...*fakeHub) Config {
	cfg := Config{
		StateFile:     filepath.Join(t.TempDir(), "state.json"),
		Secret:        testSecret,
		Fingerprint:   link.Fingerprint(hubs[0].srv.Certificate().Raw),
		LeaseTTL:      testTTL,
		RenewInterval: testRenew,
	}
	for _, h := range hubs {
		cfg.Backends = append(cfg.Backends, Backend{Name: h.name, Addr: h.addr()})
	}
	return cfg
}

func start(t *testing.T, cfg Config) *harness {
	t.Helper()
	r, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctl, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &harness{relay: r, addr: ln.Addr().String(), ctlAddr: ctl.Addr().String(), stateFile: cfg.StateFile, cancel: cancel, done: make(chan error, 1)}
	go func() { h.done <- r.serve(ctx, ln, ctl) }()
	t.Cleanup(func() {
		cancel()
		if err := <-h.done; err != nil {
			t.Errorf("serve: %v", err)
		}
	})
	return h
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (h *harness) forwardingTo(name string) func() bool {
	return func() bool { return h.relay.Status().Forwarding == name }
}

// whoami makes an HTTPS request through the relay and returns the hub's name.
func (h *harness) whoami() (string, error) {
	c := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{
		DisableKeepAlives: true,
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
	}}
	res, err := c.Get("https://" + h.addr + "/whoami")
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	return string(b), err
}

func readState(t *testing.T, path string) persistedState {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var st persistedState
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestInitialSelectionPrefersPrimary(t *testing.T) {
	a := newFakeHub(t, "a", RoleStandby, 3, true)
	b := newFakeHub(t, "b", RolePrimary, 4, true)
	h := start(t, testConfig(t, a, b))
	waitFor(t, 2*time.Second, "forwarding to b", h.forwardingTo("b"))
	st := h.relay.Status()
	if st.Active != "b" || st.Epoch != 5 {
		t.Fatalf("status = %+v, want active b epoch 5", st)
	}
	if ps := readState(t, h.stateFile); ps.Epoch != 5 || ps.Active != "b" || ps.GrantedAt == "" {
		t.Fatalf("persisted state = %+v", ps)
	}
	if info, err := os.Stat(h.stateFile); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("state file mode: %v %v", info.Mode(), err)
	}
}

func TestInitialSelectionPrefersHighestEpoch(t *testing.T) {
	a := newFakeHub(t, "a", RoleStandby, 5, true)
	b := newFakeHub(t, "b", RoleFenced, 6, true)
	h := start(t, testConfig(t, a, b))
	waitFor(t, 2*time.Second, "forwarding to b", h.forwardingTo("b"))
	if st := h.relay.Status(); st.Active != "b" || st.Epoch != 7 {
		t.Fatalf("status = %+v, want active b epoch 7", st)
	}
}

func TestRenewalOnlyGrantsActive(t *testing.T) {
	a := newFakeHub(t, "a", RolePrimary, 1, true)
	b := newFakeHub(t, "b", RoleStandby, 1, true)
	h := start(t, testConfig(t, a, b))
	waitFor(t, 2*time.Second, "forwarding to a", h.forwardingTo("a"))
	waitFor(t, 2*time.Second, "renewals", func() bool { return len(a.leaseCalls()) >= 5 })

	epoch := h.relay.Status().Epoch
	grants := 0
	for _, c := range a.leaseCalls() {
		if c.req.Active {
			grants++
			if c.req.Epoch != epoch || c.req.TTLms != testTTL.Milliseconds() {
				t.Fatalf("grant to a = %+v, want epoch %d ttl %d", c.req, epoch, testTTL.Milliseconds())
			}
		}
	}
	if grants < 3 {
		t.Fatalf("a got %d grants, want renewals", grants)
	}
	for _, c := range b.leaseCalls() {
		if c.req.Active {
			t.Fatalf("standby b was granted: %+v", c.req)
		}
	}
}

func TestFailoverAfterLeaseExpires(t *testing.T) {
	a := newFakeHub(t, "a", RolePrimary, 1, true)
	b := newFakeHub(t, "b", RoleStandby, 1, true)
	h := start(t, testConfig(t, a, b))
	waitFor(t, 2*time.Second, "forwarding to a", h.forwardingTo("a"))
	if got, err := h.whoami(); err != nil || got != "a" {
		t.Fatalf("whoami = %q, %v; want a", got, err)
	}

	// A device connection held open through the relay to a.
	held, err := tls.Dial("tcp", h.addr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	waitFor(t, time.Second, "tracked conn", func() bool { return h.relay.Status().Backends[0].Conns == 1 })

	epoch := h.relay.Status().Epoch
	failedAt := time.Now()
	a.setFail(true)

	time.Sleep(testTTL / 2)
	if st := h.relay.Status(); st.Active != "a" || st.Epoch != epoch {
		t.Fatalf("failed over too early: %+v", st)
	}

	waitFor(t, 3*time.Second, "forwarding to b", h.forwardingTo("b"))
	grantAt, ok := b.firstGrant(epoch + 1)
	if !ok {
		t.Fatalf("b was not granted epoch %d", epoch+1)
	}
	if elapsed := grantAt.Sub(failedAt); elapsed < testTTL {
		t.Fatalf("b granted %s after a failed, before a's lease (%s) could expire", elapsed, testTTL)
	}
	if ps := readState(t, h.stateFile); ps.Epoch != epoch+1 || ps.Active != "b" {
		t.Fatalf("persisted state = %+v", ps)
	}

	held.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := held.Read(make([]byte, 1)); err == nil {
		t.Fatal("read on the old connection succeeded")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("connection to the old backend was not closed")
	}
	if got, err := h.whoami(); err != nil || got != "b" {
		t.Fatalf("whoami = %q, %v; want b", got, err)
	}

	// The old primary comes back: it is only told it is not primary.
	a.setFail(false)
	n := len(a.leaseCalls())
	waitFor(t, 2*time.Second, "a polled again", func() bool { return len(a.leaseCalls()) >= n+3 })
	for _, c := range a.leaseCalls()[n:] {
		if c.req.Active || c.req.Epoch != epoch+1 {
			t.Fatalf("recovered a got %+v", c.req)
		}
	}
	if st := h.relay.Status(); st.Active != "b" {
		t.Fatalf("active = %q after a recovered", st.Active)
	}
}

func TestNotReadyStandbyIsNeverPromoted(t *testing.T) {
	a := newFakeHub(t, "a", RolePrimary, 1, true)
	b := newFakeHub(t, "b", RoleStandby, 1, false)
	h := start(t, testConfig(t, a, b))
	waitFor(t, 2*time.Second, "forwarding to a", h.forwardingTo("a"))
	epoch := h.relay.Status().Epoch
	a.setFail(true)
	time.Sleep(3 * (testTTL + testRenew))
	if st := h.relay.Status(); st.Active != "a" || st.Epoch != epoch {
		t.Fatalf("status = %+v, want a kept at epoch %d", st, epoch)
	}
	for _, c := range b.leaseCalls() {
		if c.req.Active {
			t.Fatalf("not-ready b was granted: %+v", c.req)
		}
	}
}

func TestWrongFingerprintIsFailure(t *testing.T) {
	a := newFakeHub(t, "a", RolePrimary, 1, true)
	cfg := testConfig(t, a)
	cfg.Fingerprint = strings.Repeat("ab", 32)
	h := start(t, cfg)
	waitFor(t, 2*time.Second, "a failed attempt", func() bool {
		b := h.relay.Status().Backends[0]
		return !b.LastAttempt.IsZero()
	})
	b := h.relay.Status().Backends[0]
	if b.LastOK || !strings.Contains(b.LastError, "fingerprint") {
		t.Fatalf("backend status = %+v, want fingerprint failure", b)
	}
	if len(a.leaseCalls()) != 0 {
		t.Fatal("lease request reached a hub with the wrong certificate")
	}
	if st := h.relay.Status(); st.Active != "" {
		t.Fatalf("active = %q with no verified backend", st.Active)
	}
	if _, err := h.whoami(); err == nil {
		t.Fatal("relay forwarded with no active backend")
	}
}

func TestFingerprintAcceptsColons(t *testing.T) {
	a := newFakeHub(t, "a", RolePrimary, 1, true)
	cfg := testConfig(t, a)
	fp := strings.ToUpper(cfg.Fingerprint)
	var parts []string
	for i := 0; i < len(fp); i += 2 {
		parts = append(parts, fp[i:i+2])
	}
	cfg.Fingerprint = strings.Join(parts, ":")
	h := start(t, cfg)
	waitFor(t, 2*time.Second, "forwarding to a", h.forwardingTo("a"))
}

func TestSwitchByHand(t *testing.T) {
	a := newFakeHub(t, "a", RolePrimary, 1, true)
	b := newFakeHub(t, "b", RoleStandby, 1, true)
	h := start(t, testConfig(t, a, b))
	waitFor(t, 2*time.Second, "forwarding to a", h.forwardingTo("a"))
	waitFor(t, 2*time.Second, "b polled", func() bool { return len(b.leaseCalls()) > 0 })
	epoch := h.relay.Status().Epoch

	res, err := http.Post(fmt.Sprintf("http://%s/relay/v1/switch?to=b", h.ctlAddr), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("switch: HTTP %d", res.StatusCode)
	}
	if st := h.relay.Status(); st.Active != "b" || st.Forwarding != "b" || st.Epoch != epoch+1 {
		t.Fatalf("status = %+v", st)
	}
	demoted, granted := false, false
	for _, c := range a.leaseCalls() {
		if !c.req.Active && c.req.Epoch == epoch+1 {
			demoted = true
		}
	}
	if _, ok := b.firstGrant(epoch + 1); ok {
		granted = true
	}
	if !demoted || !granted {
		t.Fatalf("demoted a: %v, granted b: %v", demoted, granted)
	}
	if err := h.relay.Switch(context.Background(), "nope"); !errors.Is(err, ErrUnknownBackend) {
		t.Fatalf("switch to unknown = %v", err)
	}
	res, err = http.Get(fmt.Sprintf("http://%s/relay/v1/status", h.ctlAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var st Status
	if err := json.NewDecoder(res.Body).Decode(&st); err != nil || st.Active != "b" || len(st.Backends) != 2 {
		t.Fatalf("status endpoint = %+v, %v", st, err)
	}
}

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "secret"), []byte("  abc\n"), 0o600)
	path := filepath.Join(dir, "relay.toml")
	os.WriteFile(path, []byte(`
state_file = "/var/lib/agentmux-relay/state.json"
secret_file = "secret"
fingerprint = "AB:CD"
lease_ttl = "20s"

[[backends]]
name = "mac"
addr = "127.0.0.1:18766"

[[backends]]
name = "macmini"
addr = "127.0.0.1:18767"
`), 0o600)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Secret != "abc" || cfg.Fingerprint != "abcd" || cfg.LeaseTTL != 20*time.Second ||
		cfg.RenewInterval != 5*time.Second || cfg.Listen != "0.0.0.0:443" || cfg.Control != "127.0.0.1:18700" || len(cfg.Backends) != 2 {
		t.Fatalf("cfg = %+v", cfg)
	}

	bad := func(body string) {
		t.Helper()
		os.WriteFile(path, []byte(body), 0o600)
		if _, err := LoadConfig(path); err == nil {
			t.Fatalf("accepted %q", body)
		}
	}
	base := "state_file='s'\nsecret='x'\nfingerprint='ab'\n"
	bad(base)
	bad(base + "[[backends]]\nname='a'\naddr='x:1'\n[[backends]]\nname='a'\naddr='x:2'\n")
	bad(base + "renew_interval='8s'\n[[backends]]\nname='a'\naddr='x:1'\n")
	bad(base + "lisen='x'\n[[backends]]\nname='a'\naddr='x:1'\n")
}
