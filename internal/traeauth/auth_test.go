package traeauth

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func writeAuth(t *testing.T, home string, expires, refreshed time.Time) {
	t.Helper()
	path := filepath.Join(home, "cli", "auth.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"auth_mode": "trae", "last_refresh": refreshed, "trae": map[string]any{"expires_at": expires, "access_token": "secret-must-not-escape"}})
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	// Give independent credential revisions distinct stamps on all filesystems.
	if err := os.Chtimes(path, refreshed, refreshed); err != nil {
		t.Fatal(err)
	}
}

func newTestManager(now *time.Time, refresh func(context.Context, map[string]string) error) *manager {
	return &manager{entries: make(map[string]*entry), now: func() time.Time { return *now }, refresh: refresh}
}

func TestRefreshCoalescesConcurrentChannels(t *testing.T) {
	now := time.Now()
	home := t.TempDir()
	extra := map[string]string{"TRAE_HOME": home}
	writeAuth(t, home, now.Add(5*time.Minute), now.Add(-time.Hour))
	var calls atomic.Int32
	m := newTestManager(&now, func(ctx context.Context, env map[string]string) error {
		calls.Add(1)
		if env["TRAE_HOME"] != home {
			t.Error("refresh did not receive the Agent environment")
		}
		writeAuth(t, home, now.Add(2*time.Hour), now)
		return nil
	})
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := m.ensure(context.Background(), extra); err != nil {
				t.Errorf("ensure: %v", err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("refresh calls = %d, want 1", calls.Load())
	}
}

func TestRefreshBackoffAndExternalLoginRecovery(t *testing.T) {
	now := time.Now()
	home := t.TempDir()
	extra := map[string]string{"TRAE_HOME": home}
	writeAuth(t, home, now.Add(-time.Minute), now.Add(-time.Hour))
	calls := 0
	m := newTestManager(&now, func(context.Context, map[string]string) error { calls++; return ErrLoginRequired })
	for range 3 {
		if err := m.ensure(context.Background(), extra); !errors.Is(err, ErrLoginRequired) {
			t.Fatalf("expired credentials error = %v", err)
		}
	}
	if calls != 1 {
		t.Fatalf("refresh storm: %d calls", calls)
	}
	now = now.Add(time.Minute)
	_ = m.ensure(context.Background(), extra)
	if calls != 2 {
		t.Fatalf("did not retry after backoff: %d calls", calls)
	}
	// Interactive login replaces the credential before the backoff ends.
	writeAuth(t, home, now.Add(2*time.Hour), now)
	if err := m.ensure(context.Background(), extra); err != nil {
		t.Fatalf("new login remained blocked: %v", err)
	}
	// A different login already near expiry must also bypass old backoff.
	writeAuth(t, home, now.Add(5*time.Minute), now.Add(time.Second))
	_ = m.ensure(context.Background(), extra)
	if calls != 3 {
		t.Fatalf("new credential revision reused cached failure: %d calls", calls)
	}
}

func TestValidTokenSurvivesTransientFailureAndRetriesAtExpiry(t *testing.T) {
	now := time.Now()
	home := t.TempDir()
	extra := map[string]string{"TRAE_HOME": home}
	writeAuth(t, home, now.Add(30*time.Second), now.Add(-time.Hour))
	calls := 0
	m := newTestManager(&now, func(context.Context, map[string]string) error { calls++; return ErrRefreshUnavailable })
	if err := m.ensure(context.Background(), extra); err != nil {
		t.Fatalf("valid token blocked by transient refresh failure: %v", err)
	}
	now = now.Add(30 * time.Second)
	if err := m.ensure(context.Background(), extra); !errors.Is(err, ErrRefreshUnavailable) || calls != 2 {
		t.Fatalf("must retry at expiry before backoff, calls=%d err=%v", calls, err)
	}
}

func TestSuccessfulRPCWithoutRenewalDoesNotAcceptExpiredToken(t *testing.T) {
	now := time.Now()
	home := t.TempDir()
	writeAuth(t, home, now.Add(-time.Minute), now.Add(-time.Hour))
	m := newTestManager(&now, func(context.Context, map[string]string) error { return nil })
	if err := m.ensure(context.Background(), map[string]string{"TRAE_HOME": home}); !errors.Is(err, ErrRefreshUnavailable) {
		t.Fatalf("unchanged expired token accepted: %v", err)
	}
}

func TestRevokedCredentialIsNotAcceptedBeforeRecordedExpiry(t *testing.T) {
	now := time.Now()
	home := t.TempDir()
	extra := map[string]string{"TRAE_HOME": home}
	writeAuth(t, home, now.Add(10*time.Minute), now.Add(-time.Hour))
	calls := 0
	m := newTestManager(&now, func(context.Context, map[string]string) error { calls++; return ErrLoginRequired })
	for range 2 {
		if err := m.ensure(context.Background(), extra); !errors.Is(err, ErrLoginRequired) {
			t.Fatalf("revoked credential accepted: %v", err)
		}
	}
	if calls != 1 || !m.needsLogin(extra) {
		t.Fatalf("revocation not cached, calls=%d", calls)
	}
	writeAuth(t, home, now.Add(time.Hour), now)
	if m.needsLogin(extra) {
		t.Fatal("new login inherited old rejection")
	}
}

func TestRefreshSkipsHealthyMissingAndNonTraeCredentials(t *testing.T) {
	now := time.Now()
	home := t.TempDir()
	extra := map[string]string{"TRAE_HOME": home}
	m := newTestManager(&now, func(context.Context, map[string]string) error { t.Fatal("unexpected refresh"); return nil })
	if err := m.ensure(context.Background(), extra); err != nil {
		t.Fatal(err)
	}
	writeAuth(t, home, now.Add(time.Hour), now)
	if err := m.ensure(context.Background(), extra); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "cli", "auth.json"), []byte(`{"auth_mode":"apikey","OPENAI_API_KEY":"private"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.ensure(context.Background(), extra); err != nil {
		t.Fatal(err)
	}
}

func TestCancelledRefreshDoesNotPoisonNextAttempt(t *testing.T) {
	now := time.Now()
	home := t.TempDir()
	writeAuth(t, home, now.Add(-time.Minute), now.Add(-time.Hour))
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	m := newTestManager(&now, func(context.Context, map[string]string) error {
		calls++
		if calls == 1 {
			cancel()
			return context.Canceled
		}
		writeAuth(t, home, now.Add(time.Hour), now)
		return nil
	})
	extra := map[string]string{"TRAE_HOME": home}
	if err := m.ensure(ctx, extra); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := m.ensure(context.Background(), extra); err != nil || calls != 2 {
		t.Fatalf("cancellation cached: calls=%d err=%v", calls, err)
	}
}

func TestCredentialHomesAreIsolated(t *testing.T) {
	now := time.Now()
	for _, overrides := range []map[string]string{{"TRAE_HOME": t.TempDir()}, {"TRAE_HOME": "", "HOME": t.TempDir()}} {
		home := overrides["TRAE_HOME"]
		if home == "" {
			home = filepath.Join(overrides["HOME"], ".trae")
		}
		writeAuth(t, home, now.Add(time.Hour), now)
		meta, err := ReadMetadata(overrides)
		if err != nil || !meta.Managed || !meta.ExpiresAt.Equal(now.Add(time.Hour)) {
			t.Fatalf("metadata with overrides = %+v, %v", meta, err)
		}
	}
}
