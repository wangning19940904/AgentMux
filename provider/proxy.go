// Package provider's proxy implements cc-switch's "Local Routing": a local
// HTTP server that Claude Code, Claude Desktop and Codex point at (via live
// config takeover). It injects real credentials, maps models, converts
// protocols where needed, and fails over across the provider queue with a
// per-provider circuit breaker.
package provider

import (
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

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
)

// ProxyManagedToken is the placeholder credential written into live configs
// under takeover; the proxy injects the real key per request (cc-switch's
// PROXY_MANAGED sentinel).
const ProxyManagedToken = "PROXY_MANAGED"

// DefaultProxyAddr is the local routing listen address (cc-switch defaults to
// 127.0.0.1:15721; AgentMux claims a nearby port).
const DefaultProxyAddr = "127.0.0.1:15733"

// claudeCodeModelPrefix makes every provider model eligible for Claude Code's
// gateway discovery filter. Claude Code only adds ids beginning with
// "claude" or "anthropic" to /model, so the proxy exposes a prefixed alias
// and maps it back to the provider's original id before forwarding.
const claudeCodeModelPrefix = "claude-"

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

	traceMu       sync.RWMutex
	traceObserver func(context.Context, core.ProxyTrace, []byte, []byte) error
	traceCost     func(core.ProxyTrace) float64
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

// SetTraceObserver attaches the live observability bridge. Request/response
// bodies are bounded captures and must be redacted/encrypted by the observer.
func (s *ProxyServer) SetTraceObserver(observer func(context.Context, core.ProxyTrace, []byte, []byte) error) {
	s.traceMu.Lock()
	s.traceObserver = observer
	s.traceMu.Unlock()
}

func (s *ProxyServer) SetTraceCostEstimator(estimator func(core.ProxyTrace) float64) {
	s.traceMu.Lock()
	s.traceCost = estimator
	s.traceMu.Unlock()
}

func (s *ProxyServer) estimateTraceCost(trace core.ProxyTrace) float64 {
	s.traceMu.RLock()
	estimator := s.traceCost
	s.traceMu.RUnlock()
	if estimator == nil {
		return 0
	}
	return estimator(trace)
}

func (s *ProxyServer) observeTrace(ctx context.Context, trace core.ProxyTrace, requestBody, responseBody []byte) error {
	s.traceMu.RLock()
	observer := s.traceObserver
	s.traceMu.RUnlock()
	if observer == nil {
		return nil
	}
	return observer(ctx, trace, requestBody, responseBody)
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
	mux.HandleFunc("GET /claude/v1/models", s.handleClaudeCodeModels)
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
	mux.HandleFunc("GET /v1/models", s.handleRootModels)
	mux.HandleFunc("GET /models", func(w http.ResponseWriter, r *http.Request) {
		s.handleCodex(w, r, "/models")
	})
	// Gemini CLI (generateContent / streamGenerateContent). The model rides in
	// the path (/v1beta/models/<model>:generateContent); the wildcard captures
	// it and the handler infers stream mode from the method suffix.
	mux.HandleFunc("POST /v1beta/models/{model}", s.handleGemini)
	mux.HandleFunc("POST /v1/models/{model}", s.handleGemini)
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
		route, ok, err := s.st.ActiveProviderRoute(ctx, key)
		if err != nil || !ok {
			continue
		}
		if p, err := s.st.GetProvider(ctx, route.ProviderID); err == nil {
			appendProvider(core.ProviderWithRouteMeta(p, route.Meta))
		}
		break
	}
	if cfg.AutoFailover {
		all, err := s.st.ListProviders(ctx)
		if err == nil {
			var queue []*core.Provider
			for _, p := range all {
				if p.InFailoverQueue {
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
	stream, _ := parsed["stream"].(bool)
	s.forwardChain(w, r, parsed, forwardOpts{
		tool:        tool,
		clientProto: protoAnthropic,
		stream:      stream,
		mapModel:    mapModel,
		writeErr: func(w http.ResponseWriter, code int, message string) {
			writeAnthropicError(w, code, "api_error", message)
		},
	})
}

// handleRootModels preserves compatibility with takeover configs written
// before Claude Code received its dedicated /claude prefix. New Claude
// configs use /claude/v1/models, while Codex continues to use /v1/models.
func (s *ProxyServer) handleRootModels(w http.ResponseWriter, r *http.Request) {
	if looksLikeClaudeModelsRequest(r) {
		s.handleClaudeCodeModels(w, r)
		return
	}
	s.handleCodex(w, r, "/models")
}

func looksLikeClaudeModelsRequest(r *http.Request) bool {
	if strings.TrimSpace(r.Header.Get("anthropic-version")) != "" ||
		strings.TrimSpace(r.Header.Get("x-api-key")) != "" {
		return true
	}
	return strings.Contains(strings.ToLower(r.UserAgent()), "claude")
}

// handleClaudeCodeModels exposes the active route's configured model catalog
// in Anthropic's Models API shape. Claude Code consumes this endpoint when
// CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY is enabled.
func (s *ProxyServer) handleClaudeCodeModels(w http.ResponseWriter, r *http.Request) {
	chain, _ := s.providerChain(r.Context(), "claudecode")
	if len(chain) == 0 {
		writeAnthropicError(w, http.StatusBadGateway, "api_error", "no provider routed for claudecode")
		return
	}
	routes := claudeCodeModelRoutes(chain[0])
	data := make([]any, 0, len(routes))
	for _, route := range routes {
		data = append(data, map[string]any{
			"id":           route.ID,
			"type":         "model",
			"display_name": route.DisplayName,
			"created_at":   "2025-01-01T00:00:00Z",
		})
	}
	var firstID, lastID any
	if len(routes) > 0 {
		firstID = routes[0].ID
		lastID = routes[len(routes)-1].ID
	}
	writeProxyJSON(w, http.StatusOK, map[string]any{
		"data": data, "has_more": false, "first_id": firstID, "last_id": lastID,
	})
}

type claudeCodeModelRoute struct {
	ID            string
	DisplayName   string
	UpstreamModel string
}

func claudeCodeModelRoutes(p *core.Provider) []claudeCodeModelRoute {
	if p == nil {
		return nil
	}
	seen := map[string]bool{}
	models := claudeCodeModelOptions(p)
	routes := make([]claudeCodeModelRoute, 0, len(p.Meta.ClaudeDesktopModels)+len(models))
	add := func(route claudeCodeModelRoute) {
		route.ID = strings.TrimSpace(route.ID)
		route.UpstreamModel = strings.TrimSpace(route.UpstreamModel)
		if route.ID == "" || route.UpstreamModel == "" || seen[strings.ToLower(route.ID)] {
			return
		}
		if route.DisplayName == "" {
			route.DisplayName = route.ID
		}
		seen[strings.ToLower(route.ID)] = true
		routes = append(routes, route)
	}
	// The route editor stores friendly Claude aliases in the shared model-map
	// shape. Honor those aliases for Claude Code as well as Claude Desktop so a
	// saved route is not merely decorative.
	for _, model := range p.Meta.ClaudeDesktopModels {
		id := model.ID
		if id == "" {
			id = model.Name
		}
		upstream := model.UpstreamModel
		if upstream == "" {
			upstream = mapClaudeTierModel(id, p)
		}
		display := model.DisplayName
		if display == "" {
			display = id
		}
		add(claudeCodeModelRoute{ID: id, DisplayName: display, UpstreamModel: upstream})
	}
	for _, model := range models {
		add(claudeCodeModelRoute{
			ID:            claudeCodeModelPrefix + model,
			DisplayName:   model,
			UpstreamModel: model,
		})
	}
	return routes
}

func claudeCodeUpstreamModel(p *core.Provider, clientModel string) (string, bool) {
	alias := strings.TrimSpace(clientModel)
	if strings.HasSuffix(strings.ToLower(alias), "[1m]") {
		alias = strings.TrimSpace(alias[:len(alias)-len("[1m]")])
	}
	for _, route := range claudeCodeModelRoutes(p) {
		if strings.EqualFold(route.ID, alias) {
			return route.UpstreamModel, true
		}
	}
	return "", false
}

func claudeCodeModelOptions(p *core.Provider) []string {
	if p == nil {
		return nil
	}
	seen := map[string]bool{}
	models := make([]string, 0, len(p.Meta.SupportedModels)+1)
	add := func(model string) {
		model = strings.TrimSpace(model)
		if model == "" || seen[model] {
			return
		}
		seen[model] = true
		models = append(models, model)
	}
	for _, model := range core.ProviderModelOptions(p) {
		add(model)
	}
	return models
}

// upstreamModelFor picks the upstream model for converted requests: a
// discovered Claude Code alias first, then tier mapping, then an explicitly
// advertised provider model, the provider default model, and finally the
// client model.
func upstreamModelFor(p *core.Provider, clientModel string) string {
	if upstream, ok := claudeCodeUpstreamModel(p, clientModel); ok {
		return upstream
	}
	mapped := mapClaudeTierModel(clientModel, p)
	if mapped != clientModel {
		return mapped
	}
	for _, model := range claudeCodeModelOptions(p) {
		if model == clientModel {
			return clientModel
		}
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
	route, ok, err := s.st.ActiveProviderRoute(ctx, "claude-desktop")
	if err != nil || !ok {
		return nil
	}
	p, err := s.st.GetProvider(ctx, route.ProviderID)
	if err != nil {
		return nil
	}
	return core.ProviderWithRouteMeta(p, route.Meta)
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
	for _, route := range routes {
		if route.UpstreamModel != "" && strings.EqualFold(strings.TrimSpace(route.UpstreamModel), normalized) {
			body["model"] = route.UpstreamModel
			return nil
		}
	}
	// Role-keyword fallback (sonnet/opus/haiku/fable), mirroring cc-switch.
	for _, role := range []string{"opus", "haiku", "fable", "sonnet"} {
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

// handleCodex serves OpenAI-format traffic (Codex CLI + Desktop). Chat and
// Responses requests now translate through the IR hub, so an upstream speaking
// a different protocol (Anthropic, Gemini, or the other OpenAI wire) can serve
// the request. /models stays a same-protocol passthrough.
func (s *ProxyServer) handleCodex(w http.ResponseWriter, r *http.Request, endpoint string) {
	if endpoint == "/models" {
		s.codexModelsPassthrough(w, r)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 200<<20))
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	clientProto := protoOpenAIChat
	if endpoint == "/responses" {
		clientProto = protoResponses
	}
	stream, _ := parsed["stream"].(bool)
	s.forwardChain(w, r, parsed, forwardOpts{
		tool:        "codex",
		clientProto: clientProto,
		stream:      stream,
		writeErr:    writeOpenAIError,
	})
}

// codexModelsPassthrough relays a GET /models to the active codex provider
// verbatim (no body to translate).
func (s *ProxyServer) codexModelsPassthrough(w http.ResponseWriter, r *http.Request) {
	chain, _ := s.providerChain(r.Context(), "codex")
	if len(chain) == 0 {
		writeOpenAIError(w, http.StatusBadGateway, "no provider routed for codex")
		return
	}
	p := chain[0]
	if issue := providerAPIKeyIssue(p); issue != "" {
		writeOpenAIError(w, http.StatusBadGateway, fmt.Sprintf("%s: %s", p.ID, issue))
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, joinURL(p.BaseURL, "/models"), nil)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return
	}
	copyProxyHeaders(req.Header, r.Header)
	if key := providerAPIKey(p); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	_ = relayResponse(w, resp)
}

// ---- Gemini CLI path ----

// handleGemini serves Gemini CLI generateContent traffic. The model and stream
// mode ride in the path (…/models/<model>:generateContent); the body is
// translated through the IR hub so any upstream protocol can serve it.
func (s *ProxyServer) handleGemini(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 200<<20))
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	// Path segment is "<model>:generateContent" or "<model>:streamGenerateContent".
	seg := r.PathValue("model")
	model, method, _ := strings.Cut(seg, ":")
	if model != "" {
		parsed["model"] = model
	}
	stream := strings.Contains(strings.ToLower(method), "stream")
	s.forwardChain(w, r, parsed, forwardOpts{
		tool:        "gemini",
		clientProto: protoGemini,
		stream:      stream,
		writeErr:    writeOpenAIError,
	})
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
func relayResponse(w http.ResponseWriter, resp *http.Response) error {
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
				return werr
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
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
