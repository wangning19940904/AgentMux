// Package traeauth renews TRAE-managed credentials through the native CLI.
// AgentMux reads expiry metadata only; the CLI owns tokens and their storage.
package traeauth

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const RefreshInterval = 5 * time.Minute
const refreshAhead = 15 * time.Minute

var ErrLoginRequired = errors.New("TRAE 登录已失效，自动续期失败；请在该机器的 AgentMux「框架 → TRAE CLI」中重新登录")
var ErrRefreshUnavailable = errors.New("TRAE 登录自动续期暂时失败，请稍后重试；若持续失败，请在该机器的 AgentMux「框架 → TRAE CLI」中重新登录")

type Metadata struct {
	Managed     bool
	ExpiresAt   time.Time
	LastRefresh time.Time
	stamp       string
}

// ReadMetadata respects the same HOME/TRAE_HOME overrides as the Agent.
// Unrecognized credential formats are left to the native CLI.
func ReadMetadata(extra map[string]string) (Metadata, error) {
	path, err := authPath(extra)
	if err != nil {
		return Metadata{}, err
	}
	return readMetadata(path)
}

func authPath(extra map[string]string) (string, error) {
	value := func(key string) string {
		if v, ok := extra[key]; ok {
			return v
		}
		return os.Getenv(key)
	}
	home := strings.TrimSpace(value("TRAE_HOME"))
	if home == "" {
		userHome := value("HOME")
		if userHome == "" {
			var err error
			userHome, err = os.UserHomeDir()
			if err != nil {
				return "", err
			}
		}
		home = filepath.Join(userHome, ".trae")
	}
	path, err := filepath.Abs(filepath.Join(home, "cli", "auth.json"))
	if err == nil {
		if resolved, resolveErr := filepath.EvalSymlinks(path); resolveErr == nil {
			path = resolved
		}
	}
	return path, err
}

func readMetadata(path string) (Metadata, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return Metadata{}, nil
	}
	if err != nil {
		return Metadata{}, err
	}
	defer f.Close()
	var payload struct {
		AuthMode    string    `json:"auth_mode"`
		LastRefresh time.Time `json:"last_refresh"`
		Trae        struct {
			ExpiresAt time.Time `json:"expires_at"`
		} `json:"trae"`
	}
	if err := json.NewDecoder(f).Decode(&payload); err != nil {
		return Metadata{}, err
	}
	meta := Metadata{Managed: payload.AuthMode == "trae", ExpiresAt: payload.Trae.ExpiresAt, LastRefresh: payload.LastRefresh}
	if info, err := f.Stat(); err == nil {
		meta.stamp = info.ModTime().UTC().Format(time.RFC3339Nano)
	}
	return meta, nil
}

type entry struct {
	gate        chan struct{}
	stamp       string
	nextAttempt time.Time
	failures    int
	err         error
}

type manager struct {
	mu      sync.Mutex
	entries map[string]*entry
	now     func() time.Time
	refresh func(context.Context, map[string]string) error
}

var defaultManager = &manager{entries: make(map[string]*entry), now: time.Now, refresh: refreshNative}

// Ensure renews credentials near expiry. Concurrent channels sharing a login
// share one refresh, and transient failures back off without blocking turns
// that still have valid credentials. No model turn or browser login is started.
func Ensure(ctx context.Context, extra map[string]string) error {
	return defaultManager.ensure(ctx, extra)
}

// NeedsLogin exposes a definitive rejection for the current credential
// revision without leaking CLI output or delaying a status request.
func NeedsLogin(extra map[string]string) bool {
	return defaultManager.needsLogin(extra)
}

func (m *manager) needsLogin(extra map[string]string) bool {
	path, err := authPath(extra)
	if err != nil {
		return false
	}
	meta, err := readMetadata(path)
	if err != nil || !meta.Managed {
		return false
	}
	m.mu.Lock()
	e := m.entries[path]
	m.mu.Unlock()
	if e == nil {
		return false
	}
	select {
	case e.gate <- struct{}{}:
		defer func() { <-e.gate }()
		return e.stamp == meta.stamp && errors.Is(e.err, ErrLoginRequired)
	default:
		return false
	}
}

func (m *manager) ensure(ctx context.Context, extra map[string]string) error {
	path, err := authPath(extra)
	if err != nil {
		return nil // Native CLI owns unknown credential layouts.
	}
	meta, err := readMetadata(path)
	if err != nil || !meta.Managed || meta.ExpiresAt.IsZero() || meta.ExpiresAt.After(m.now().Add(refreshAhead)) {
		return nil
	}
	m.mu.Lock()
	e := m.entries[path]
	if e == nil {
		e = &entry{gate: make(chan struct{}, 1)}
		m.entries[path] = e
	}
	m.mu.Unlock()
	select {
	case e.gate <- struct{}{}:
		defer func() { <-e.gate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	// Another channel or an interactive login may have replaced the file.
	meta, err = readMetadata(path)
	if err != nil || !meta.Managed || meta.ExpiresAt.IsZero() || meta.ExpiresAt.After(m.now().Add(refreshAhead)) {
		return nil
	}
	if e.stamp == meta.stamp && m.now().Before(e.nextAttempt) {
		if meta.ExpiresAt.After(m.now()) && !errors.Is(e.err, ErrLoginRequired) {
			return nil
		}
		return e.err
	}
	if e.stamp != meta.stamp {
		e.failures = 0
	}
	refreshErr := m.refresh(ctx, extra)
	if ctx.Err() != nil {
		return ctx.Err() // A cancelled caller must not poison the retry cache.
	}
	after, readErr := readMetadata(path)
	if readErr == nil && after.Managed && after.ExpiresAt.After(m.now()) {
		meta = after
		if after.ExpiresAt.After(m.now().Add(refreshAhead)) {
			e.failures, e.err = 0, nil
			e.stamp = ""
			return nil
		}
	}
	// A successful RPC alone does not prove renewal. Some CLI versions return
	// an account even when their underlying refresh failed or was skipped.
	if refreshErr == nil {
		refreshErr = ErrRefreshUnavailable
	}
	e.err, e.stamp = refreshErr, meta.stamp
	e.failures++
	delay := time.Minute * time.Duration(1<<min(e.failures-1, 4))
	e.nextAttempt = m.now().Add(delay)
	if meta.ExpiresAt.After(m.now()) && !errors.Is(refreshErr, ErrLoginRequired) {
		if meta.ExpiresAt.Before(e.nextAttempt) {
			e.nextAttempt = meta.ExpiresAt
		}
		return nil
	}
	return refreshErr
}

func commandEnv(extra map[string]string) []string {
	env := os.Environ()
	for key, value := range extra {
		env = append(env, key+"="+value)
	}
	return env
}
