// Package server hosts the HTTP/WS management API and embedded WebUI. CLI,
// WebUI, Wails desktop and the macOS menubar all talk to this one server so
// logic never forks across clients.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/agentnexus/agentnexus/config"
	"github.com/agentnexus/agentnexus/core"
	providerpkg "github.com/agentnexus/agentnexus/provider"
	sessionstore "github.com/agentnexus/agentnexus/sessions"
	"github.com/agentnexus/agentnexus/store"
)

// Server is the management/bridge HTTP server.
type Server struct {
	cfg       *config.Config
	log       *slog.Logger
	st        *store.Store
	provider  core.ProviderManager
	proxySvc  *providerpkg.Service
	usageFn   UsageReporter
	sender    core.Sender
	connect   *core.ConnectService
	presets   any
	memory    core.MemoryStore
	skills    core.SkillManager
	mcp       core.MCPRegistry
	guard     core.Guard
	workspace core.WorkspaceInitializer
	sessions  *sessionstore.Service
	mux       *http.ServeMux
	httpSrv   *http.Server
}

// UsageReporter produces an aggregated usage report for the API.
type UsageReporter func(ctx context.Context, period string, since time.Time) (any, error)

// New builds a server.
func New(cfg *config.Config, log *slog.Logger, st *store.Store, pm core.ProviderManager, usageFn UsageReporter) *Server {
	s := &Server{
		cfg:      cfg,
		log:      log,
		st:       st,
		provider: pm,
		usageFn:  usageFn,
		sessions: sessionstore.New(),
		mux:      http.NewServeMux(),
	}
	s.routes()
	return s
}

// SetSender attaches the engine as the bridge message sender.
func (s *Server) SetSender(sender core.Sender) { s.sender = sender }

// SetPresets attaches the provider presets list exposed by the API.
func (s *Server) SetPresets(presets any) { s.presets = presets }

// SetModules attaches the Memory, Skills, MCP Registry and Guard backends.
// Any of them may be nil; their routes degrade gracefully.
func (s *Server) SetModules(mem core.MemoryStore, sk core.SkillManager, mcp core.MCPRegistry, g core.Guard) {
	s.memory = mem
	s.skills = sk
	s.mcp = mcp
	s.guard = g
}

// SetWorkspaceInitializer attaches the manual and runtime workspace initializer.
func (s *Server) SetWorkspaceInitializer(initializer core.WorkspaceInitializer) {
	s.workspace = initializer
}

// SetSessions attaches the Claude/Codex session manager. Nil disables routes.
func (s *Server) SetSessions(svc *sessionstore.Service) { s.sessions = svc }

// SetProviderService attaches the takeover-aware provider service (local
// routing REST + hot-switch path).
func (s *Server) SetProviderService(svc *providerpkg.Service) {
	s.proxySvc = svc
	if svc != nil {
		s.provider = svc
	}
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/v1/status", s.handleStatus)
	s.mux.HandleFunc("GET /api/v1/platforms", s.handlePlatforms)
	s.mux.HandleFunc("GET /api/v1/agents", s.handleAgents)
	s.mux.HandleFunc("GET /api/v1/agent-instances", s.handleAgentInstancesList)
	s.mux.HandleFunc("POST /api/v1/agent-instances", s.handleAgentInstanceUpsert)
	s.mux.HandleFunc("POST /api/v1/agent-instances/initialize", s.handleAgentInstanceInitialize)
	s.mux.HandleFunc("DELETE /api/v1/agent-instances", s.handleAgentInstanceDelete)
	s.mux.HandleFunc("GET /api/v1/tools", s.handleTools)
	s.mux.HandleFunc("POST /api/v1/tools/cli/install", s.handleCLIInstall)
	s.mux.HandleFunc("GET /api/v1/providers", s.handleProvidersList)
	s.mux.HandleFunc("POST /api/v1/providers", s.handleProviderUpsert)
	s.mux.HandleFunc("DELETE /api/v1/providers", s.handleProviderDelete)
	s.mux.HandleFunc("GET /api/v1/providers/active", s.handleProviderActiveRoutes)
	s.mux.HandleFunc("DELETE /api/v1/providers/active", s.handleProviderClearRoute)
	s.mux.HandleFunc("GET /api/v1/providers/presets", s.handleProviderPresets)
	s.mux.HandleFunc("POST /api/v1/providers/probe", s.handleProviderProbe)
	s.mux.HandleFunc("POST /api/v1/providers/switch", s.handleProviderSwitch)
	s.mux.HandleFunc("POST /api/v1/providers/failover", s.handleProviderFailover)
	s.mux.HandleFunc("GET /api/v1/proxy/status", s.handleProxyStatus)
	s.mux.HandleFunc("GET /api/v1/proxy/traces", s.handleProxyTraces)
	s.mux.HandleFunc("POST /api/v1/proxy/takeover", s.handleProxyTakeover)
	s.mux.HandleFunc("POST /api/v1/proxy/config", s.handleProxyConfigUpdate)
	s.mux.HandleFunc("GET /api/v1/system/claude-3p", s.handleClaude3PStatus)
	s.mux.HandleFunc("POST /api/v1/system/claude-3p", s.handleClaude3PToggle)
	s.mux.HandleFunc("POST /api/v1/system/directories", s.handleSystemDirectoryEnsure)
	s.mux.HandleFunc("GET /api/v1/frameworks", s.handleFrameworksList)
	s.mux.HandleFunc("POST /api/v1/frameworks/install", s.handleFrameworkInstall)
	s.mux.HandleFunc("GET /api/v1/sessions", s.handleSessionsList)
	s.mux.HandleFunc("GET /api/v1/sessions/messages", s.handleSessionMessages)
	s.mux.HandleFunc("POST /api/v1/sessions/resume", s.handleSessionResume)
	s.mux.HandleFunc("DELETE /api/v1/sessions", s.handleSessionDelete)
	s.mux.HandleFunc("GET /api/v1/usage", s.handleUsage)
	s.mux.HandleFunc("POST /api/v1/send", s.handleSend)
	s.mux.HandleFunc("GET /api/v1/channels", s.handleChannelsList)
	s.mux.HandleFunc("POST /api/v1/channels", s.handleChannelUpsert)
	s.mux.HandleFunc("DELETE /api/v1/channels", s.handleChannelDelete)
	s.mux.HandleFunc("POST /api/v1/channels/restart", s.handleChannelRestart)
	s.mux.HandleFunc("POST /api/v1/setup/feishu/begin", s.handleFeishuSetupBegin)
	s.mux.HandleFunc("POST /api/v1/setup/feishu/poll", s.handleFeishuSetupPoll)
	s.mux.HandleFunc("GET /api/v1/triggers", s.handleTriggersList)
	s.mux.HandleFunc("POST /api/v1/triggers", s.handleTriggerUpsert)
	s.mux.HandleFunc("DELETE /api/v1/triggers", s.handleTriggerDelete)
	s.mux.HandleFunc("POST /api/v1/triggers/run", s.handleTriggerRun)
	s.mux.HandleFunc("GET /channel-avatar", s.handleChannelAvatar)
	s.mux.HandleFunc("POST /hook/{id}", s.handleInboundHook)
	s.registerModuleRoutes()
	s.registerWeb(s.mux)
}

// ListenAndServe starts the HTTP server until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context) error {
	s.httpSrv = &http.Server{
		Addr:    s.cfg.Server.Addr,
		Handler: s.withAuth(s.mux),
	}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.httpSrv.Shutdown(shutCtx)
	}()
	s.log.Info("server listening", "addr", s.cfg.Server.Addr)
	if err := s.httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// withAuth enforces the bridge token on /api/ routes when the bridge is on.
func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Path) >= 5 && r.URL.Path[:5] == "/api/" {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		if s.cfg.Bridge.Enabled && len(r.URL.Path) >= 5 && r.URL.Path[:5] == "/api/" {
			tok := r.Header.Get("Authorization")
			if tok != "Bearer "+s.cfg.Bridge.Token {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"projects": len(s.cfg.Projects),
		"version":  "0.1.0",
	})
}

func (s *Server) handlePlatforms(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, core.RegisteredPlatforms())
}

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, core.RegisteredAgents())
}

func (s *Server) handleProvidersList(w http.ResponseWriter, r *http.Request) {
	if s.provider == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	ps, err := s.provider.List(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	for _, p := range ps {
		annotateProviderAPIKey(p)
	}
	writeJSON(w, http.StatusOK, ps)
}

func (s *Server) handleProviderUpsert(w http.ResponseWriter, r *http.Request) {
	if s.provider == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "provider manager unavailable"})
		return
	}
	var p core.Provider
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := normalizeProviderAPIKeyForSave(&p); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := s.provider.Upsert(r.Context(), &p); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	annotateProviderAPIKey(&p)
	writeJSON(w, http.StatusOK, &p)
}

func (s *Server) handleProviderDelete(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id"})
		return
	}
	if s.provider == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "provider manager unavailable"})
		return
	}
	if err := s.provider.Delete(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleProviderActiveRoutes(w http.ResponseWriter, r *http.Request) {
	if s.provider == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	routes, err := s.provider.ActiveRoutes(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	for i := range routes {
		annotateProviderRouteAPIKey(&routes[i])
	}
	writeJSON(w, http.StatusOK, routes)
}

func (s *Server) handleProviderClearRoute(w http.ResponseWriter, r *http.Request) {
	if s.provider == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "provider manager unavailable"})
		return
	}
	tool := r.URL.Query().Get("tool")
	if tool == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing tool"})
		return
	}
	if err := s.provider.Clear(r.Context(), tool); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleProviderPresets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.presets)
}

func (s *Server) handleProviderSwitch(w http.ResponseWriter, r *http.Request) {
	if s.provider == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "provider manager unavailable"})
		return
	}
	var req struct {
		ID            string            `json:"id"`
		Tool          string            `json:"tool"`
		Meta          core.ProviderMeta `json:"meta"`
		LocalTakeover *bool             `json:"local_takeover,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	route := core.ProviderRoute{
		Tool:       req.Tool,
		ProviderID: req.ID,
		Meta:       req.Meta,
	}
	var err error
	if req.LocalTakeover != nil && s.proxySvc != nil {
		err = s.proxySvc.SwitchRouteWithLocalTakeover(r.Context(), route, *req.LocalTakeover)
	} else if req.LocalTakeover != nil && *req.LocalTakeover {
		err = fmt.Errorf("local routing unavailable")
	} else {
		err = s.provider.SwitchRoute(r.Context(), route)
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Project string `json:"project"`
		Text    string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if s.sender == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no sender wired"})
		return
	}
	if err := s.sender.SendToProject(r.Context(), req.Project, req.Text); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	period := r.URL.Query().Get("period")
	if period == "" {
		period = "daily"
	}
	if s.usageFn == nil {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	rep, err := s.usageFn(r.Context(), period, time.Time{})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, rep)
}
