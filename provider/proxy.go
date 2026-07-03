// Package provider's proxy implements a local reverse proxy with hot
// provider-switching, automatic failover across a provider chain, a per-target
// circuit breaker, and background health monitoring — mirroring cc-switch's
// "Proxy & Failover" feature.
package provider

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

// Upstream is a single proxy target derived from a provider.
type Upstream struct {
	ProviderID string
	BaseURL    string
	APIKeyEnv  string

	// circuit breaker state
	failures atomic.Int32
	openTill atomic.Int64 // unix nano; >now means open (skip)
}

// Proxy is a failover-aware reverse proxy over an ordered upstream chain.
type Proxy struct {
	log       *slog.Logger
	mu        sync.RWMutex
	chain     []*Upstream
	threshold int32
	cooldown  time.Duration
	client    *http.Client
}

// NewProxy builds a proxy. threshold failures trips the breaker; cooldown is
// how long a tripped upstream is skipped.
func NewProxy(log *slog.Logger, threshold int, cooldown time.Duration) *Proxy {
	if log == nil {
		log = slog.Default()
	}
	if threshold <= 0 {
		threshold = 3
	}
	if cooldown <= 0 {
		cooldown = 30 * time.Second
	}
	return &Proxy{
		log:       log,
		threshold: int32(threshold),
		cooldown:  cooldown,
		client:    &http.Client{Timeout: 120 * time.Second},
	}
}

// SetUpstreams hot-swaps the chain from providers.
func (p *Proxy) SetUpstreams(ups []*Upstream) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.chain = ups
}

// available returns upstreams whose breaker is closed, in order.
func (p *Proxy) available() []*Upstream {
	p.mu.RLock()
	defer p.mu.RUnlock()
	now := time.Now().UnixNano()
	var out []*Upstream
	for _, u := range p.chain {
		if u.openTill.Load() > now {
			continue
		}
		out = append(out, u)
	}
	return out
}

// ServeHTTP proxies the request, failing over across the chain on errors.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ups := p.available()
	if len(ups) == 0 {
		http.Error(w, "no healthy upstream", http.StatusBadGateway)
		return
	}
	var lastErr error
	for _, u := range ups {
		ok := p.forward(w, r, u)
		if ok {
			u.failures.Store(0)
			return
		}
		lastErr = fmt.Errorf("upstream %s failed", u.ProviderID)
		if u.failures.Add(1) >= p.threshold {
			u.openTill.Store(time.Now().Add(p.cooldown).UnixNano())
			p.log.Warn("circuit opened", "provider", u.ProviderID, "cooldown", p.cooldown)
		}
	}
	http.Error(w, "all upstreams failed: "+lastErr.Error(), http.StatusBadGateway)
}

// forward attempts a single upstream; returns true on a 2xx/3xx response that
// it has already written to w.
func (p *Proxy) forward(w http.ResponseWriter, r *http.Request, u *Upstream) bool {
	target, err := url.Parse(u.BaseURL)
	if err != nil {
		return false
	}
	rp := httputil.NewSingleHostReverseProxy(target)
	failed := false
	rp.Transport = p.client.Transport
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		failed = true
	}
	rp.ModifyResponse = func(resp *http.Response) error {
		if resp.StatusCode >= 500 {
			failed = true
			return fmt.Errorf("upstream %d", resp.StatusCode)
		}
		return nil
	}
	// Buffer so a failed upstream does not leak a partial body before failover.
	bw := &bufferedWriter{ResponseWriter: w}
	rp.ServeHTTP(bw, r)
	if failed {
		return false
	}
	bw.flush()
	return true
}

// HealthCheck pings each upstream's base URL and resets breakers that recover.
func (p *Proxy) HealthCheck(ctx context.Context) {
	p.mu.RLock()
	chain := append([]*Upstream(nil), p.chain...)
	p.mu.RUnlock()
	for _, u := range chain {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.BaseURL, nil)
		if err != nil {
			continue
		}
		resp, err := p.client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			u.failures.Store(0)
			u.openTill.Store(0)
		}
	}
}

// bufferedWriter delays writing the body so failover can substitute upstreams.
type bufferedWriter struct {
	http.ResponseWriter
	status int
	buf    []byte
}

func (b *bufferedWriter) WriteHeader(code int) { b.status = code }
func (b *bufferedWriter) Write(p []byte) (int, error) {
	b.buf = append(b.buf, p...)
	return len(p), nil
}
func (b *bufferedWriter) flush() {
	if b.status != 0 {
		b.ResponseWriter.WriteHeader(b.status)
	}
	_, _ = b.ResponseWriter.Write(b.buf)
}
