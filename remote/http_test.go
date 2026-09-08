package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type httpTestClient struct {
	fallbackRemoteClient
	dials  atomic.Int32
	closes atomic.Int32
}

func (c *httpTestClient) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	c.dials.Add(1)
	return c.fallbackRemoteClient.DialContext(ctx, network, address)
}

func (c *httpTestClient) Close() error { c.closes.Add(1); return nil }

func newHTTPTestManager(t *testing.T, handler http.HandlerFunc, timeout time.Duration) (*Manager, Host, *httpTestClient) {
	t.Helper()
	apiServer := httptest.NewServer(handler)
	t.Cleanup(apiServer.Close)
	m, err := NewManager(filepath.Join(t.TempDir(), "hosts.json"), timeout, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	view, err := m.Upsert(Host{
		Name: "test-http", Host: "192.0.2.1", Port: 22, User: "tester",
		RemoteAddr: apiServer.Listener.Addr().String(), HostKeyFingerprint: "SHA256:trusted",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	host := mustStoredHost(t, m, view.ID)
	client := &httpTestClient{}
	m.cache(host, client)
	return m, host, client
}

func closeHTTPResponse(t *testing.T, w http.ResponseWriter, partial bool) {
	t.Helper()
	conn, buffer, err := w.(http.Hijacker).Hijack()
	if err != nil {
		t.Error(err)
		return
	}
	defer conn.Close()
	if partial {
		_, _ = buffer.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\n{\"partial\":")
		_ = buffer.Flush()
	}
}

func TestDoHTTPRetriesCompleteGETRead(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprintf("partial-body=%v", partial), func(t *testing.T) {
			var calls atomic.Int32
			m, host, client := newHTTPTestManager(t, func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if string(body) != `{"filter":"active"}` || r.Header.Get("Authorization") != "Bearer test" {
					t.Errorf("retry lost request body or headers")
				}
				if calls.Add(1) == 1 {
					closeHTTPResponse(t, w, partial)
					return
				}
				_, _ = io.WriteString(w, `{"ok":true}`)
			}, time.Second)
			req, _ := http.NewRequest("GET", "http://"+host.RemoteAddr+"/api/v1/providers", strings.NewReader(`{"filter":"active"}`))
			req.Header.Set("Authorization", "Bearer test")
			status, body, err := m.DoHTTP(host.ID, req, 1024)
			if err != nil || status != 200 || string(body) != `{"ok":true}` || calls.Load() != 2 || client.dials.Load() != 2 {
				t.Fatalf("status=%d body=%s err=%v calls=%d dials=%d", status, body, err, calls.Load(), client.dials.Load())
			}
			if client.closes.Load() != 0 || m.cached(host) != client {
				t.Fatal("one HTTP channel failure invalidated the shared SSH client")
			}
		})
	}
}

func TestDoHTTPRetryBoundsAndNonReplayableRequests(t *testing.T) {
	for _, tc := range []struct {
		name, method string
		body         io.Reader
		wantCalls    int32
	}{
		{name: "GET stops after three attempts", method: "GET", wantCalls: 3},
		{name: "POST is never replayed", method: "POST", body: strings.NewReader(`{"enabled":true}`), wantCalls: 1},
		{name: "PUT is never replayed", method: "PUT", wantCalls: 1},
		{name: "DELETE is never replayed", method: "DELETE", wantCalls: 1},
		{name: "GET with streaming body is not replayed", method: "GET", body: io.NopCloser(strings.NewReader("stream")), wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			m, host, client := newHTTPTestManager(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				calls.Add(1)
				closeHTTPResponse(t, w, false)
			}, time.Second)
			req, _ := http.NewRequest(tc.method, "http://"+host.RemoteAddr+"/api/v1/providers", tc.body)
			_, _, err := m.DoHTTP(host.ID, req, 1024)
			if !errors.Is(err, io.EOF) || calls.Load() != tc.wantCalls || client.dials.Load() != tc.wantCalls {
				t.Fatalf("err=%v calls=%d dials=%d", err, calls.Load(), client.dials.Load())
			}
			if tc.wantCalls == 3 && !strings.Contains(err.Error(), "after 3 SSH attempts") {
				t.Fatalf("missing exhausted retry context: %v", err)
			}
		})
	}
}

func TestDoHTTPPreservesHTTPErrorAndResponseLimit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		limit  int64
	}{
		{name: "authorization failure", status: 401, limit: 1024},
		{name: "body size limit", status: 200, limit: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			m, host, _ := newHTTPTestManager(t, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, `{"error":"denied"}`)
			}, time.Second)
			req, _ := http.NewRequest("GET", "http://"+host.RemoteAddr+"/api/v1/providers", nil)
			status, body, err := m.DoHTTP(host.ID, req, tc.limit)
			if tc.status == 401 && (err != nil || status != 401 || string(body) != `{"error":"denied"}`) {
				t.Fatalf("status=%d body=%s err=%v", status, body, err)
			}
			if tc.limit == 4 && (err == nil || !strings.Contains(err.Error(), "response exceeds 4 bytes")) {
				t.Fatalf("response limit not enforced: %v", err)
			}
			if calls.Load() != 1 {
				t.Fatalf("permanent failure was retried %d times", calls.Load())
			}
		})
	}
}

func TestDoHTTPBoundsStalledBodyBeforeRetry(t *testing.T) {
	var calls atomic.Int32
	m, host, _ := newHTTPTestManager(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Content-Length", "100")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		_, _ = io.WriteString(w, `[]`)
	}, 80*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+host.RemoteAddr+"/api/v1/providers", nil)
	started := time.Now()
	status, body, err := m.DoHTTP(host.ID, req, 1024)
	if err != nil || status != 200 || string(body) != "[]" || calls.Load() != 2 || time.Since(started) >= time.Second {
		t.Fatalf("status=%d body=%s err=%v calls=%d elapsed=%s", status, body, err, calls.Load(), time.Since(started))
	}
}

func TestDoHTTPCancellationStopsReadAndRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	m, host, _ := newHTTPTestManager(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Length", "100")
		w.(http.Flusher).Flush()
		cancel()
		<-r.Context().Done()
	}, time.Second)
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+host.RemoteAddr+"/api/v1/providers", nil)
	_, _, err := m.DoHTTP(host.ID, req, 1024)
	if !errors.Is(err, context.Canceled) || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}
