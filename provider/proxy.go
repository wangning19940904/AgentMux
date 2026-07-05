// Package provider's proxy implements cc-switch's "Local Routing": a local
// HTTP server that Claude Code, Claude Desktop and Codex point at (via live
// config takeover). It injects real credentials, maps models, converts
// protocols where needed, and fails over across the provider queue with a
// per-provider circuit breaker.
package provider

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/agentnexus/agentnexus/core"
	"github.com/agentnexus/agentnexus/store"
)

// ProxyManagedToken is the placeholder credential written into live configs
// under takeover; the proxy injects the real key per request (cc-switch's
// PROXY_MANAGED sentinel).
const ProxyManagedToken = "PROXY_MANAGED"

// DefaultProxyAddr is the local routing listen address (cc-switch defaults to
// 127.0.0.1:15721; AgentNexus claims a nearby port).
const DefaultProxyAddr = "127.0.0.1:15733"

// breaker is a minimal circuit breaker per provider id.
type breaker struct {
	failures atomic.Int32
	openTill atomic.Int64 // unix nano
}

func (b *breaker) available(now time.Time) bool { return b.openTill.Load() <= now.UnixNano() }

func (b *breaker) recordFailure(threshold int32, cooldown time.Duration) bool {
	if b.failures.Add(1) >= threshold {
		b.openTill.Store(time.Now().Add(cooldown).UnixNano())
		b.failures.Store(0)
		return true
	}
	return false
}

func (b *breaker) recordSuccess() {
	b.failures.Store(0)
	b.openTill.Store(0)
}

// ProxyServer is the local routing HTTP server.
type ProxyServer struct {
	log  *slog.Logger
	st   *store.Store
	addr string

	mu       sync.Mutex
	httpSrv  *http.Server
	listener net.Listener

	client   *http.Client
	breakers sync.Map // provider id -> *breaker
}

// NewProxyServer builds the local routing server (not yet listening).
func NewProxyServer(log *slog.Logger, st *store.Store, addr string) *ProxyServer {
	if log == nil {
		log = slog.Default()
	}
	if addr == "" {
		addr = DefaultProxyAddr
	}
	return &ProxyServer{
		log:  log,
		st:   st,
		addr: addr,
		client: &http.Client{
			// No overall timeout: streaming responses run long. The transport
			// bounds connect + first-byte latency instead.
			Transport: &http.Transport{
				ResponseHeaderTimeout: 120 * time.Second,
				Proxy:                 http.ProxyFromEnvironment,
			},
		},
	}
}

// Start begins listening; it is idempotent.
func (s *ProxyServer) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		return nil
	}
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("proxy listen %s: %w", s.addr, err)
	}
	mux := http.NewServeMux()
	s.routes(mux)
	srv := &http.Server{Handler: mux}
	s.listener = ln
	s.httpSrv = srv
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			s.log.Error("proxy server stopped", "err", err)
		}
	}()
	s.log.Info("local routing proxy listening", "addr", ln.Addr().String())
	return nil
}

// Stop shuts the proxy down; it is idempotent.
func (s *ProxyServer) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.httpSrv == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := s.httpSrv.Shutdown(ctx)
	s.httpSrv = nil
	s.listener = nil
	return err
}

// Running reports whether the proxy is listening.
func (s *ProxyServer) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listener != nil
}

// BaseURL returns http://host:port for the running listener (config addr when
// stopped).
func (s *ProxyServer) BaseURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	addr := s.addr
	if s.listener != nil {
		addr = s.listener.Addr().String()
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return fmt.Sprintf("http://%s:%s", host, port)
}

func (s *ProxyServer) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeProxyJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("GET /status", s.handleStatus)
	// Claude Code (Anthropic Messages).
	mux.HandleFunc("POST /v1/messages", func(w http.ResponseWriter, r *http.Request) {
		s.handleAnthropicMessages(w, r, "claudecode", nil)
	})
	mux.HandleFunc("POST /claude/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		s.handleAnthropicMessages(w, r, "claudecode", nil)
	})
	// Claude Desktop gateway (token-authenticated, model-route mapped).
	mux.HandleFunc("GET /claude-desktop/v1/models", s.handleClaudeDesktopModels)
	mux.HandleFunc("POST /claude-desktop/v1/messages", s.handleClaudeDesktopMessages)
	// Codex (OpenAI Chat Completions / Responses).
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		s.handleCodex(w, r, "/chat/completions")
	})
	mux.HandleFunc("POST /chat/completions", func(w http.ResponseWriter, r *http.Request) {
		s.handleCodex(w, r, "/chat/completions")
	})
	mux.HandleFunc("POST /v1/responses", func(w http.ResponseWriter, r *http.Request) {
		s.handleCodex(w, r, "/responses")
	})
	mux.HandleFunc("POST /responses", func(w http.ResponseWriter, r *http.Request) {
		s.handleCodex(w, r, "/responses")
	})
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		s.handleCodex(w, r, "/models")
	})
	mux.HandleFunc("GET /models", func(w http.ResponseWriter, r *http.Request) {
		s.handleCodex(w, r, "/models")
	})
}

func (s *ProxyServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	cfgs, _ := s.st.ListProxyToolConfigs(r.Context())
	writeProxyJSON(w, http.StatusOK, map[string]any{
		"running":  true,
		"base_url": s.BaseURL(),
		"tools":    cfgs,
	})
}

// providerChain resolves the ordered provider candidates for a tool: the
// active provider first, then (when auto-failover is on) the failover queue
// sorted by sort_index (cc-switch provider_router.select_providers).
func (s *ProxyServer) providerChain(ctx context.Context, tool string) ([]*core.Provider, store.ProxyToolConfig) {
	canonical := liveConfigTool(tool)
	cfg, err := s.st.GetProxyToolConfig(ctx, canonical)
	if err != nil {
		s.log.Warn("proxy config read failed", "tool", canonical, "err", err)
	}
	var chain []*core.Provider
	seen := map[string]bool{}
	appendProvider := func(p *core.Provider) {
		if p != nil && !seen[p.ID] {
			seen[p.ID] = true
			chain = append(chain, p)
		}
	}
	for _, key := range activeRouteKeys(canonical) {
		id, ok, err := s.st.ActiveProviderID(ctx, key)
		if err != nil || !ok {
			continue
		}
		if p, err := s.st.GetProvider(ctx, id); err == nil {
			appendProvider(p)
		}
		break
	}
	if cfg.AutoFailover {
		all, err := s.st.ListProviders(ctx)
		if err == nil {
			var queue []*core.Provider
			for _, p := range all {
				if p.InFailoverQueue && providerSupportsTool(p, canonical) {
					queue = append(queue, p)
				}
			}
			sort.Slice(queue, func(i, j int) bool {
				if queue[i].SortIndex != queue[j].SortIndex {
					return queue[i].SortIndex < queue[j].SortIndex
				}
				return queue[i].ID < queue[j].ID
			})
			for _, p := range queue {
				appendProvider(p)
			}
		}
	}
	return chain, cfg
}

// activeRouteKeys lists the active_provider row keys to try for a canonical
// tool (aliases included, e.g. codex-app rows still route codex traffic).
func activeRouteKeys(canonical string) []string {
	switch canonical {
	case "codex":
		return []string{"codex", "codex-app", "codex-desktop"}
	case "claudecode":
		return []string{"claudecode", "claude"}
	default:
		return []string{canonical}
	}
}

func (s *ProxyServer) breakerFor(id string) *breaker {
	if b, ok := s.breakers.Load(id); ok {
		return b.(*breaker)
	}
	b, _ := s.breakers.LoadOrStore(id, &breaker{})
	return b.(*breaker)
}

// hotSwitchAfterFailover records the surviving provider as active, mirroring
// cc-switch's FailoverSwitchManager.try_switch (DB only; live config keeps
// pointing at the proxy).
func (s *ProxyServer) hotSwitchAfterFailover(ctx context.Context, tool string, p *core.Provider) {
	canonical := liveConfigTool(tool)
	if err := s.st.SetActiveProvider(ctx, canonical, p.ID); err != nil {
		s.log.Warn("failover hot-switch failed", "tool", canonical, "provider", p.ID, "err", err)
		return
	}
	s.log.Info("failover switched provider", "tool", canonical, "provider", p.ID, "source", "failover")
}

// ---- Anthropic (Claude Code / Claude Desktop) path ----

func (s *ProxyServer) handleAnthropicMessages(w http.ResponseWriter, r *http.Request, tool string, mapModel func(*core.Provider, map[string]any) error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 200<<20))
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "read body: "+err.Error())
		return
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "invalid JSON body")
		return
	}
	chain, cfg := s.providerChain(r.Context(), tool)
	if len(chain) == 0 {
		writeAnthropicError(w, http.StatusBadGateway, "api_error", "no provider routed for "+tool)
		return
	}
	var lastErr error
	now := time.Now()
	for i, p := range chain {
		br := s.breakerFor(p.ID)
		if len(chain) > 1 && !br.available(now) {
			continue
		}
		reqBody := cloneJSONMap(parsed)
		if mapModel != nil {
			if err := mapModel(p, reqBody); err != nil {
				writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
				return
			}
		} else if model := stringValue(reqBody["model"]); model != "" {
			reqBody["model"] = mapClaudeTierModel(model, p)
		}
		ok, retryable, err := s.forwardAnthropic(w, r, p, reqBody)
		if ok {
			br.recordSuccess()
			if i > 0 && cfg.AutoFailover {
				s.hotSwitchAfterFailover(r.Context(), tool, p)
			}
			return
		}
		lastErr = err
		if br.recordFailure(int32(cfg.FailureThreshold), time.Duration(cfg.CooldownSeconds)*time.Second) {
			s.log.Warn("circuit opened", "provider", p.ID, "cooldown_s", cfg.CooldownSeconds)
		}
		if !retryable {
			return
		}
	}
	msg := "all providers failed"
	if lastErr != nil {
		msg += ": " + lastErr.Error()
	}
	writeAnthropicError(w, http.StatusBadGateway, "api_error", msg)
}

// forwardAnthropic sends one Anthropic-format client request to provider p,
// converting to the provider's API format when needed. Returns ok=true when
// the response has been written to w; retryable=false means the response was
// already streamed (or is a client error) so failover must stop.
func (s *ProxyServer) forwardAnthropic(w http.ResponseWriter, r *http.Request, p *core.Provider, body map[string]any) (ok, retryable bool, err error) {
	format := p.Meta.APIFormat
	if format == "" {
		format = "anthropic"
	}
	stream, _ := body["stream"].(bool)
	switch format {
	case "anthropic":
		payload, _ := json.Marshal(body)
		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, joinURL(p.BaseURL, "/v1/messages"), bytes.NewReader(payload))
		if err != nil {
			return false, true, err
		}
		copyProxyHeaders(req.Header, r.Header)
		req.Header.Set("Content-Type", "application/json")
		if req.Header.Get("anthropic-version") == "" {
			req.Header.Set("anthropic-version", "2023-06-01")
		}
		key := providerAPIKey(p)
		if key != "" {
			req.Header.Set("x-api-key", key)
			req.Header.Set("Authorization", "Bearer "+key)
		}
		resp, err := s.client.Do(req)
		if err != nil {
			return false, true, err
		}
		defer resp.Body.Close()
		if isFailoverStatus(resp.StatusCode) {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			return false, true, fmt.Errorf("%s: upstream %d: %s", p.ID, resp.StatusCode, truncate(string(b), 300))
		}
		relayResponse(w, resp)
		return true, false, nil
	case "openai_chat":
		chatBody, err := anthropicToChatRequest(body, upstreamModelFor(p, stringValue(body["model"])))
		if err != nil {
			return false, true, err
		}
		payload, _ := json.Marshal(chatBody)
		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, joinURL(p.BaseURL, "/chat/completions"), bytes.NewReader(payload))
		if err != nil {
			return false, true, err
		}
		req.Header.Set("Content-Type", "application/json")
		if key := providerAPIKey(p); key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		resp, err := s.client.Do(req)
		if err != nil {
			return false, true, err
		}
		defer resp.Body.Close()
		if isFailoverStatus(resp.StatusCode) || resp.StatusCode >= 400 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			return false, isFailoverStatus(resp.StatusCode), fmt.Errorf("%s: upstream %d: %s", p.ID, resp.StatusCode, truncate(string(b), 300))
		}
		requestModel := stringValue(body["model"])
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			if err := chatStreamToAnthropicSSE(resp.Body, w, requestModel); err != nil {
				s.log.Warn("stream conversion aborted", "provider", p.ID, "err", err)
			}
			return true, false, nil
		}
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			return false, false, err
		}
		var chatResp map[string]any
		if err := json.Unmarshal(raw, &chatResp); err != nil {
			return false, true, fmt.Errorf("%s: invalid upstream JSON", p.ID)
		}
		writeProxyJSON(w, http.StatusOK, chatToAnthropicResponse(chatResp, requestModel))
		return true, false, nil
	default:
		return false, true, fmt.Errorf("provider %s api_format %q not supported for anthropic clients", p.ID, format)
	}
}

// upstreamModelFor picks the upstream model for converted requests: tier
// mapping first, then the provider default model, then the client model.
func upstreamModelFor(p *core.Provider, clientModel string) string {
	mapped := mapClaudeTierModel(clientModel, p)
	if mapped != clientModel {
		return mapped
	}
	if p.Model != "" {
		return p.Model
	}
	return clientModel
}

// mapClaudeTierModel maps claude-* client models onto the provider's tiered
// model overrides (sonnet/opus/haiku), leaving custom ids untouched.
func mapClaudeTierModel(model string, p *core.Provider) string {
	lower := strings.ToLower(model)
	if !strings.HasPrefix(lower, "claude-") && !strings.HasPrefix(lower, "anthropic/claude-") {
		return model
	}
	switch {
	case strings.Contains(lower, "haiku"):
		if p.Meta.ClaudeHaikuModel != "" {
			return p.Meta.ClaudeHaikuModel
		}
	case strings.Contains(lower, "opus"):
		if p.Meta.ClaudeOpusModel != "" {
			return p.Meta.ClaudeOpusModel
		}
	case strings.Contains(lower, "sonnet"):
		if p.Meta.ClaudeSonnetModel != "" {
			return p.Meta.ClaudeSonnetModel
		}
	}
	return model
}

// ---- Claude Desktop gateway ----

func (s *ProxyServer) authClaudeDesktop(w http.ResponseWriter, r *http.Request) bool {
	token, err := s.st.GetOrCreateGatewayToken(r.Context())
	if err != nil {
		writeAnthropicError(w, http.StatusInternalServerError, "api_error", "gateway token unavailable")
		return false
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if got == "" {
		got = r.Header.Get("x-api-key")
	}
	if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
		writeAnthropicError(w, http.StatusUnauthorized, "authentication_error", "invalid gateway token")
		return false
	}
	return true
}

func (s *ProxyServer) activeClaudeDesktopProvider(ctx context.Context) *core.Provider {
	id, ok, err := s.st.ActiveProviderID(ctx, "claude-desktop")
	if err != nil || !ok {
		return nil
	}
	p, err := s.st.GetProvider(ctx, id)
	if err != nil {
		return nil
	}
	return p
}

func (s *ProxyServer) handleClaudeDesktopModels(w http.ResponseWriter, r *http.Request) {
	if !s.authClaudeDesktop(w, r) {
		return
	}
	p := s.activeClaudeDesktopProvider(r.Context())
	if p == nil {
		writeAnthropicError(w, http.StatusBadGateway, "api_error", "no provider routed for claude-desktop")
		return
	}
	data := []any{}
	for _, m := range claudeDesktopRouteModels(p) {
		id := m.ID
		if id == "" {
			id = m.Name
		}
		if id == "" {
			continue
		}
		display := m.DisplayName
		if display == "" {
			display = id
		}
		data = append(data, map[string]any{
			"id": id, "type": "model", "display_name": display,
			"created_at": "2025-01-01T00:00:00Z",
		})
	}
	writeProxyJSON(w, http.StatusOK, map[string]any{"data": data, "has_more": false, "first_id": nil, "last_id": nil})
}

func (s *ProxyServer) handleClaudeDesktopMessages(w http.ResponseWriter, r *http.Request) {
	if !s.authClaudeDesktop(w, r) {
		return
	}
	s.handleAnthropicMessages(w, r, "claude-desktop", mapClaudeDesktopRequestModel)
}

// mapClaudeDesktopRequestModel maps the Desktop-visible route id onto the
// provider's real upstream model. Unknown routes are a hard error (cc-switch
// map_proxy_request_model parity: no silent default).
func mapClaudeDesktopRequestModel(p *core.Provider, body map[string]any) error {
	requested := strings.TrimSpace(stringValue(body["model"]))
	if requested == "" {
		return fmt.Errorf("missing model")
	}
	normalized := strings.TrimSuffix(strings.ToLower(requested), "[1m]")
	normalized = strings.TrimSpace(normalized)
	routes := claudeDesktopRouteModels(p)
	for _, route := range routes {
		id := route.ID
		if id == "" {
			id = route.Name
		}
		if id == "" {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(id), normalized) {
			upstream := route.UpstreamModel
			if upstream == "" {
				upstream = mapClaudeTierModel(id, p)
			}
			body["model"] = upstream
			return nil
		}
	}
	// Role-keyword fallback (sonnet/opus/haiku), mirroring cc-switch.
	for _, role := range []string{"sonnet", "opus", "haiku"} {
		if !strings.Contains(normalized, role) {
			continue
		}
		for _, route := range routes {
			if strings.Contains(strings.ToLower(route.ID+route.Name), role) {
				if route.UpstreamModel != "" {
					body["model"] = route.UpstreamModel
					return nil
				}
			}
		}
	}
	return fmt.Errorf("model route %q is not configured for provider %s", requested, p.ID)
}

// ---- Codex path ----

// handleCodex proxies OpenAI-format traffic (Codex CLI + Desktop). Same-format
// passthrough only: the endpoint the client calls must match the provider's
// wire API (the takeover writer keeps them in sync).
func (s *ProxyServer) handleCodex(w http.ResponseWriter, r *http.Request, endpoint string) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 200<<20))
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	chain, cfg := s.providerChain(r.Context(), "codex")
	if len(chain) == 0 {
		writeOpenAIError(w, http.StatusBadGateway, "no provider routed for codex")
		return
	}
	var lastErr error
	now := time.Now()
	for i, p := range chain {
		br := s.breakerFor(p.ID)
		if len(chain) > 1 && !br.available(now) {
			continue
		}
		wire := codexWireAPI(p)
		if endpoint == "/chat/completions" && wire != "chat" {
			lastErr = fmt.Errorf("provider %s wire_api %s cannot serve %s", p.ID, wire, endpoint)
			continue
		}
		if endpoint == "/responses" && wire != "responses" {
			lastErr = fmt.Errorf("provider %s wire_api %s cannot serve %s", p.ID, wire, endpoint)
			continue
		}
		req, err := http.NewRequestWithContext(r.Context(), r.Method, joinURL(p.BaseURL, endpoint), bytes.NewReader(body))
		if err != nil {
			lastErr = err
			continue
		}
		copyProxyHeaders(req.Header, r.Header)
		if len(body) > 0 {
			req.Header.Set("Content-Type", "application/json")
		}
		if key := providerAPIKey(p); key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		resp, err := s.client.Do(req)
		if err != nil {
			lastErr = err
			if br.recordFailure(int32(cfg.FailureThreshold), time.Duration(cfg.CooldownSeconds)*time.Second) {
				s.log.Warn("circuit opened", "provider", p.ID, "cooldown_s", cfg.CooldownSeconds)
			}
			continue
		}
		if isFailoverStatus(resp.StatusCode) {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			lastErr = fmt.Errorf("%s: upstream %d: %s", p.ID, resp.StatusCode, truncate(string(b), 300))
			if br.recordFailure(int32(cfg.FailureThreshold), time.Duration(cfg.CooldownSeconds)*time.Second) {
				s.log.Warn("circuit opened", "provider", p.ID, "cooldown_s", cfg.CooldownSeconds)
			}
			continue
		}
		br.recordSuccess()
		if i > 0 && cfg.AutoFailover {
			s.hotSwitchAfterFailover(r.Context(), "codex", p)
		}
		relayResponse(w, resp)
		resp.Body.Close()
		return
	}
	msg := "all providers failed"
	if lastErr != nil {
		msg += ": " + lastErr.Error()
	}
	writeOpenAIError(w, http.StatusBadGateway, msg)
}

// ---- shared helpers ----

// isFailoverStatus marks upstream statuses that justify trying the next
// provider (5xx, rate limits, auth failures on this upstream).
func isFailoverStatus(code int) bool {
	return code >= 500 || code == http.StatusTooManyRequests ||
		code == http.StatusUnauthorized || code == http.StatusForbidden
}

// copyProxyHeaders copies client headers, dropping hop-by-hop and credential
// headers (the proxy injects its own auth).
func copyProxyHeaders(dst, src http.Header) {
	skip := map[string]bool{
		"authorization": true, "x-api-key": true, "host": true,
		"connection": true, "keep-alive": true, "proxy-authenticate": true,
		"proxy-authorization": true, "te": true, "trailer": true,
		"transfer-encoding": true, "upgrade": true, "content-length": true,
		"accept-encoding": true,
	}
	for k, vs := range src {
		if skip[strings.ToLower(k)] {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

// relayResponse streams an upstream response to the client verbatim.
func relayResponse(w http.ResponseWriter, resp *http.Response) {
	for k, vs := range resp.Header {
		lk := strings.ToLower(k)
		if lk == "connection" || lk == "transfer-encoding" || lk == "keep-alive" {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

func joinURL(base, endpoint string) string {
	return strings.TrimRight(strings.TrimSpace(base), "/") + endpoint
}

func cloneJSONMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func writeProxyJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeAnthropicError(w http.ResponseWriter, code int, errType, message string) {
	writeProxyJSON(w, code, map[string]any{
		"type":  "error",
		"error": map[string]any{"type": errType, "message": message},
	})
}

func writeOpenAIError(w http.ResponseWriter, code int, message string) {
	writeProxyJSON(w, code, map[string]any{
		"error": map[string]any{"message": message, "type": "api_error"},
	})
}
