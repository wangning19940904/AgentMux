package remote

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Dialing a machine reached more than one way: the path the chooser
// (paths.go) picks is tried first, and the next usable one at once when it
// fails. Machines with a single path never get here.

// candidates describes a machine's paths to the chooser.
func (m *Manager) candidates(ctx context.Context, entries []pathEntry) []pathCandidate {
	out := make([]pathCandidate, 0, len(entries))
	for _, entry := range entries {
		out = append(out, pathCandidate{key: entry.key(), via: entry.via, available: m.pathAvailable(ctx, entry) == ""})
	}
	return out
}

// noPathError explains why none of a machine's paths can be tried now.
func noPathError(group *machineGroup, wait time.Duration) error {
	name := group.primary().name()
	if wait > 0 {
		return fmt.Errorf("every way of reaching %s failed just now; trying again in %s", name, wait.Round(time.Second))
	}
	return fmt.Errorf("%s cannot be reached any way now", name)
}

// dialPath opens a connection to the machine's API through one path.
func (m *Manager) dialPath(ctx context.Context, entry pathEntry, network string) (net.Conn, error) {
	switch entry.via {
	case PathLocal:
		_, dial := m.local()
		if dial == nil {
			return nil, errors.New("this machine's API is not available")
		}
		return dial(ctx)
	case PathSSH:
		// One attempt: when it fails another path is tried at once, rather
		// than this one again.
		conn, _, err := m.dialOnce(ctx, entry.host, network)
		return conn, err
	case PathLink:
		return m.dialLinked(ctx, entry.device)
	}
	return nil, fmt.Errorf("a %s path carries requests, not connections", entry.via)
}

// dialMachine opens a connection through the machine's best path, moving to
// the next at once when one fails.
func (m *Manager) dialMachine(ctx context.Context, group *machineGroup, network string) (net.Conn, error) {
	conn, _, err := m.dialMachineVia(ctx, group, network, group.dialable())
	return conn, err
}

func (m *Manager) dialMachineVia(ctx context.Context, group *machineGroup, network string, entries []pathEntry) (net.Conn, pathEntry, error) {
	keys, wait := m.paths.order(group.key, m.candidates(ctx, entries))
	if len(keys) == 0 {
		return nil, pathEntry{}, noPathError(group, wait)
	}
	var failures []error
	for _, key := range keys {
		entry, _ := group.entry(key)
		conn, err := m.dialPath(ctx, entry, network)
		if err == nil {
			m.paths.connected(group.key, key)
			return conn, entry, nil
		}
		if ctx.Err() != nil {
			// The caller gave up; that says nothing about the path.
			return nil, pathEntry{}, err
		}
		m.paths.failed(key, err)
		failures = append(failures, fmt.Errorf("%s: %w", entry.via, err))
	}
	return nil, pathEntry{}, errors.Join(failures...)
}

// machineRoundTrip sends one request to the machine through its best path,
// relays included. A path that cannot be connected to is left for the next
// one before anything is sent; a relay can be followed by another path only
// when the request body can be sent again.
func (m *Manager) machineRoundTrip(group *machineGroup, request *http.Request) (*http.Response, error) {
	ctx := request.Context()
	keys, wait := m.paths.order(group.key, m.candidates(ctx, group.entries))
	if len(keys) == 0 {
		closeBody(request)
		return nil, noPathError(group, wait)
	}
	replayable := request.Body == nil || request.Body == http.NoBody || request.GetBody != nil
	if !replayable {
		// A body that cannot be sent again goes to the relay only when no
		// connection opened first, since a relay that fails cannot hand
		// the request on.
		var direct, relayed []string
		for _, key := range keys {
			if strings.HasPrefix(key, PathRelay+":") {
				relayed = append(relayed, key)
			} else {
				direct = append(direct, key)
			}
		}
		keys = append(direct, relayed...)
	}
	var failures []error
	for _, key := range keys {
		entry, _ := group.entry(key)
		if entry.via == PathRelay {
			relay := m.relayBackend()
			if relay == nil {
				continue
			}
			out, err := rewindRequest(request)
			if err != nil {
				return nil, err
			}
			response, err := relay.RoundTrip(entry.id, out)
			if err == nil {
				m.paths.connected(group.key, key)
				return response, nil
			}
			if ctx.Err() != nil {
				return nil, err
			}
			m.paths.failed(key, err)
			failures = append(failures, fmt.Errorf("%s: %w", entry.via, err))
			if !replayable {
				return nil, errors.Join(failures...)
			}
			continue
		}
		conn, err := m.dialPath(ctx, entry, "tcp")
		if err != nil {
			if ctx.Err() != nil {
				closeBody(request)
				return nil, err
			}
			m.paths.failed(key, err)
			failures = append(failures, fmt.Errorf("%s: %w", entry.via, err))
			continue
		}
		out, err := rewindRequest(request)
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		response, err := connTransport(conn).RoundTrip(out)
		if err != nil {
			if ctx.Err() == nil {
				// It connected but did not deliver: try another next time.
				m.paths.failed(key, err)
			}
			return nil, fmt.Errorf("%s: %w", entry.via, err)
		}
		m.paths.connected(group.key, key)
		return response, nil
	}
	closeBody(request)
	if len(failures) == 0 {
		return nil, noPathError(group, 0)
	}
	return nil, errors.Join(failures...)
}

// rewindRequest returns a copy of the request whose body can be sent, from
// the start when the request can produce it again.
func rewindRequest(request *http.Request) (*http.Request, error) {
	out := request.Clone(request.Context())
	if request.GetBody != nil {
		body, err := request.GetBody()
		if err != nil {
			return nil, fmt.Errorf("reopen request body: %w", err)
		}
		out.Body = body
	}
	return out, nil
}

func closeBody(request *http.Request) {
	if request.Body != nil {
		_ = request.Body.Close()
	}
}

// connTransport sends one request over a connection already open.
func connTransport(conn net.Conn) *http.Transport {
	var once sync.Once
	return &http.Transport{
		Proxy: nil, DisableKeepAlives: true,
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			var out net.Conn
			once.Do(func() { out = conn })
			if out == nil {
				return nil, errors.New("the connection was already used")
			}
			return out, nil
		},
	}
}

// Transport returns a transport for requests to a machine, by any of its
// entries' ids or its UID. Each request goes through the machine's best path,
// the hub's relay included; the relay carries only the worker API. A machine
// with one path is reached through DialContext, as always.
func (m *Manager) Transport(id string) http.RoundTripper {
	return roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if group, ok := m.machine(request.Context(), id); ok && len(group.entries) > 1 {
			return m.machineRoundTrip(group, request)
		}
		transport := &http.Transport{
			Proxy: nil, DisableKeepAlives: true,
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return m.DialContext(ctx, id, network)
			},
		}
		return transport.RoundTrip(request)
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

// probePath checks one path with the machine's status endpoint and records
// how it went. Relays carry only the worker API, so the hub's word that the
// device is connected stands for them.
func (m *Manager) probePath(ctx context.Context, group *machineGroup, entry pathEntry) (map[string]any, time.Duration, error) {
	if reason := m.pathAvailable(ctx, entry); reason != "" {
		return nil, 0, errors.New(reason)
	}
	if entry.via == PathRelay {
		return nil, 0, nil
	}
	host := entry.host
	if entry.via != PathSSH {
		host = Host{ID: entry.id, Name: entry.name(), RemoteAddr: linkedRemoteAddr}
	}
	started := time.Now()
	conn, err := m.dialPath(ctx, entry, "tcp")
	if err == nil {
		var status map[string]any
		status, err = requestStatusVia(ctx, connTransport(conn), host)
		if err == nil {
			elapsed := time.Since(started)
			m.paths.measured(entry.key(), elapsed)
			if entry.via == PathSSH {
				m.learnMachineUID(entry.id, status)
			}
			return status, elapsed, nil
		}
	}
	if ctx.Err() == nil {
		m.paths.failed(entry.key(), err)
	}
	return nil, 0, err
}

type probeResult struct {
	entry   pathEntry
	status  map[string]any
	elapsed time.Duration
	err     error
}

// probeMachine checks every path of a machine at once. When nothing has
// used the machine yet, or the chooser now prefers another path, the best
// path that answered becomes the active one, so the list shows the path a
// request would take.
func (m *Manager) probeMachine(ctx context.Context, group *machineGroup) []probeResult {
	results := make([]probeResult, len(group.entries))
	var wg sync.WaitGroup
	for index, entry := range group.entries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			probeCtx, cancel := context.WithTimeout(ctx, pathProbeTimeout)
			defer cancel()
			status, elapsed, err := m.probePath(probeCtx, group, entry)
			results[index] = probeResult{entry: entry, status: status, elapsed: elapsed, err: err}
		}()
	}
	wg.Wait()
	keys, _ := m.paths.order(group.key, m.candidates(ctx, group.entries))
	for _, key := range keys {
		for _, result := range results {
			if result.entry.key() == key && result.err == nil {
				m.paths.connected(group.key, key)
				return results
			}
		}
	}
	return results
}

// machineStatus answers Status for a machine with several paths: every path
// is checked, and the answer is the one through the path in use.
func (m *Manager) machineStatus(ctx context.Context, group *machineGroup) (TestResult, error) {
	results := m.probeMachine(ctx, group)
	active := m.paths.activePath(group.key)
	var failures []error
	for _, result := range results {
		if result.err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", result.entry.via, result.err))
		}
	}
	for _, result := range results {
		if result.entry.key() == active && result.err == nil && result.status != nil {
			primary := group.primary()
			return TestResult{
				OK: true, Name: primary.name(), LatencyMS: result.elapsed.Milliseconds(),
				HostKeyFingerprint: primary.host.HostKeyFingerprint, Status: result.status,
			}, nil
		}
	}
	if len(failures) == 0 {
		return TestResult{}, noPathError(group, 0)
	}
	return TestResult{}, errors.Join(failures...)
}

// RunPathProbes checks the paths of every machine that has more than one,
// every interval, until ctx ends. It keeps round trips current, notices a
// path that stopped working before a request needs it, and moves a machine
// back to a better path once that has lasted the hold period.
func (m *Manager) RunPathProbes(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = PathProbeInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		index := m.buildMachines(ctx)
		for _, group := range index.groups {
			if len(group.entries) > 1 && ctx.Err() == nil {
				m.probeMachine(ctx, group)
			}
		}
	}
}
