package remote

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestSSHRetryClassifiesDNSErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  *net.DNSError
		want bool
	}{
		{"temporary resolver failure", &net.DNSError{IsTemporary: true}, true},
		{"resolver timeout", &net.DNSError{IsTimeout: true}, true},
		{"missing hostname", &net.DNSError{IsNotFound: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := retryableSSHError(fmt.Errorf("resolve SSH host: %w", tc.err)); got != tc.want {
				t.Fatalf("retryable = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSSHRetryRecoversAndStopsAtThreeAttempts(t *testing.T) {
	for _, recover := range []bool{true, false} {
		t.Run(fmt.Sprint(recover), func(t *testing.T) {
			calls := 0
			value, err := retrySSH(context.Background(), time.Second, func(context.Context) (int, error) {
				calls++
				if recover && calls == 3 {
					return 42, nil
				}
				return 0, fmt.Errorf("connect: %w", syscall.ECONNRESET)
			})
			if calls != 3 {
				t.Fatalf("attempts=%d", calls)
			}
			if recover {
				if err != nil || value != 42 {
					t.Fatalf("value=%d error=%v", value, err)
				}
			} else {
				var exhausted *sshRetryError
				if !errors.As(err, &exhausted) || exhausted.attempts != 3 || !errors.Is(err, syscall.ECONNRESET) {
					t.Fatalf("error=%v", err)
				}
			}
		})
	}
}

func TestSSHRetryRejectsPermanentFailures(t *testing.T) {
	for _, failure := range []error{
		&UnknownHostKeyError{Fingerprint: "test"}, errors.New("SSH host key changed"), errors.New("Permission denied (publickey)"),
		errors.New("ssh: unable to authenticate"), &ssh.OpenChannelError{Reason: ssh.Prohibited},
		&remoteHTTPError{status: 401}, &remoteHTTPError{status: 403}, context.Canceled,
	} {
		calls := 0
		_, err := retrySSH(context.Background(), time.Second, func(context.Context) (int, error) { calls++; return 0, failure })
		if calls != 1 || !errors.Is(err, failure) {
			t.Errorf("failure=%v calls=%d error=%v", failure, calls, err)
		}
	}
}

func TestSSHRetryReservesDeadlineForRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	calls := 0
	started := time.Now()
	_, err := retrySSH(ctx, 5*time.Second, func(attemptCtx context.Context) (int, error) {
		calls++
		if calls == 1 {
			<-attemptCtx.Done()
			return 0, attemptCtx.Err()
		}
		return 1, nil
	})
	if err != nil || calls != 2 || time.Since(started) >= 600*time.Millisecond {
		t.Fatalf("calls=%d elapsed=%s error=%v", calls, time.Since(started), err)
	}
}

func TestSSHRetryCancellationInterruptsBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan error, 1)
	var calls atomic.Int32
	go func() {
		_, err := retrySSH(ctx, time.Second, func(context.Context) (int, error) {
			if calls.Add(1) == 1 {
				close(started)
			}
			return 0, io.EOF
		})
		done <- err
	}()
	<-started
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || calls.Load() != 1 {
			t.Fatalf("calls=%d error=%v", calls.Load(), err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("backoff ignored cancellation")
	}
}

func TestSSHRetryRecognizesTimedOutSubprocessWithoutRetryingCredentials(t *testing.T) {
	for _, message := range []string{"system SSH command: signal: killed", "Permission denied (publickey)"} {
		calls := 0
		_, err := retrySSH(context.Background(), 10*time.Millisecond, func(ctx context.Context) (int, error) {
			calls++
			if calls == 2 {
				return 1, nil
			}
			<-ctx.Done()
			return 0, errors.New(message)
		})
		if strings.HasPrefix(message, "Permission") {
			if err == nil || calls != 1 {
				t.Fatalf("credentials calls=%d error=%v", calls, err)
			}
		} else if err != nil || calls != 2 {
			t.Fatalf("timeout calls=%d error=%v", calls, err)
		}
	}
}

func TestSSHRetryClassifiesSystemFallbackCause(t *testing.T) {
	for _, failure := range []error{context.DeadlineExceeded, errors.New("Permission denied (publickey)")} {
		calls := 0
		_, err := retrySSH(context.Background(), time.Second, func(context.Context) (int, error) {
			calls++
			if calls == 2 {
				return 1, nil
			}
			return 0, fmt.Errorf("SSH authentication: %w", &sshFallbackError{native: errors.New("unable to authenticate"), fallback: failure})
		})
		if errors.Is(failure, context.DeadlineExceeded) {
			if calls != 2 || err != nil {
				t.Fatalf("fallback timeout calls=%d error=%v", calls, err)
			}
		} else if calls != 1 || err == nil {
			t.Fatalf("fallback credentials calls=%d error=%v", calls, err)
		}
	}
}

func TestSSHConnectionQueueHonorsCancellation(t *testing.T) {
	manager, err := NewManager(filepath.Join(t.TempDir(), "hosts.json"), time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	gate := manager.connectionLock("blocked")
	gate.Lock()
	defer gate.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := manager.client(ctx, Host{ID: "blocked"}); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("connection queue ignored deadline")
	}
}

func TestSSHHandshakeHonorsCallerCancellation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			defer conn.Close()
			close(accepted)
			_, _ = io.Copy(io.Discard, conn)
		}
	}()
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	number, _ := strconv.Atoi(port)
	manager, err := NewManager(filepath.Join(t.TempDir(), "hosts.json"), 5*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, _, err := manager.dial(ctx, Host{Host: "127.0.0.1", Port: number, User: "tester", KeyPath: filepath.Join(t.TempDir(), "missing")}, false)
		done <- err
	}()
	<-accepted
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("native handshake ignored cancellation")
	}
}

func TestSSHInitialConnectionRetriesAreSharedAcrossCallers(t *testing.T) {
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(private, "")
	if err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(key, pem.EncodeToMemory(block), 0600); err != nil {
		t.Fatal(err)
	}
	serverConfig := &ssh.ServerConfig{NoClientAuth: true}
	serverConfig.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var connections atomic.Int32
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			if connections.Add(1) <= 2 {
				conn.Close()
			} else {
				go serveSSHConnection(conn, serverConfig)
			}
		}
	}()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `{"ok":true}`) }))
	defer api.Close()
	manager, err := NewManager(filepath.Join(t.TempDir(), "hosts.json"), time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	number, _ := strconv.Atoi(port)
	saved, err := manager.Upsert(Host{Name: "recover", Host: host, Port: number, User: "tester", KeyPath: key, HostKeyFingerprint: ssh.FingerprintSHA256(signer.PublicKey()), RemoteAddr: api.Listener.Addr().String()}, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	results := make(chan error, 6)
	for range 6 {
		go func() {
			conn, err := manager.DialContext(ctx, saved.ID, "tcp")
			if conn != nil {
				conn.Close()
			}
			results <- err
		}()
	}
	for range 6 {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
	if connections.Load() != 3 {
		t.Fatalf("opened %d SSH connections, expected two failures and one shared recovery", connections.Load())
	}
}

func TestSSHStatusRetriesUnavailableResponsesButNotAuthorization(t *testing.T) {
	for _, status := range []int{503, 401} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var requests atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if requests.Add(1) < 3 || status == 401 {
					w.WriteHeader(status)
					return
				}
				_, _ = io.WriteString(w, `{"ok":true}`)
			}))
			defer api.Close()
			manager, host := cachedHTTPTestManager(t, api.Listener.Addr().String())
			result, err := manager.Status(context.Background(), host.ID)
			if status == 503 {
				if err != nil || !result.OK || requests.Load() != 3 {
					t.Fatalf("result=%+v error=%v calls=%d", result, err, requests.Load())
				}
			} else if err == nil || requests.Load() != 1 {
				t.Fatalf("error=%v calls=%d", err, requests.Load())
			}
		})
	}
}

func TestSSHTunnelRetryDoesNotReplayHTTPWrites(t *testing.T) {
	var writes atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		writes.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	}))
	defer api.Close()
	manager, host := cachedHTTPTestManager(t, api.Listener.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	transport := &http.Transport{DialContext: func(_ context.Context, network, _ string) (net.Conn, error) {
		return manager.DialContext(ctx, host.ID, network)
	}, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	req, _ := http.NewRequestWithContext(ctx, "POST", api.URL, strings.NewReader(`{"action":"install"}`))
	if response, err := transport.RoundTrip(req); err == nil {
		response.Body.Close()
		t.Fatal("missing response unexpectedly succeeded")
	}
	if writes.Load() != 1 {
		t.Fatalf("write executed %d times", writes.Load())
	}
}

func cachedHTTPTestManager(t *testing.T, address string) (*Manager, Host) {
	t.Helper()
	manager, err := NewManager(filepath.Join(t.TempDir(), "hosts.json"), time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	saved, err := manager.Upsert(Host{Name: "http", Host: "127.0.0.1", Port: 22, User: "tester", RemoteAddr: address, HostKeyFingerprint: "pinned"}, false)
	if err != nil {
		t.Fatal(err)
	}
	host := mustStoredHost(t, manager, saved.ID)
	manager.cache(host, &fallbackRemoteClient{})
	return manager, host
}
