// Package server hosts the HTTP/WS management API and embedded WebUI. CLI,
// WebUI, Wails desktop and the macOS menubar all talk to this one server so
// logic never forks across clients.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/wangning19940904/AgentMux/config"
	"github.com/wangning19940904/AgentMux/core"
	providerpkg "github.com/wangning19940904/AgentMux/provider"
	remotepkg "github.com/wangning19940904/AgentMux/remote"
	sessionstore "github.com/wangning19940904/AgentMux/sessions"
	"github.com/wangning19940904/AgentMux/store"
)

// Server is the management/bridge HTTP server.
type Server struct {
	cfg             *config.Config
	version         string
	log             *slog.Logger
	st              *store.Store
	provider        core.ProviderManager
	proxySvc        *providerpkg.Service
	usageFn         UsageReporter
	sender          core.Sender
	connect         *core.ConnectService
	presets         any
	memory          core.MemoryStore
	skills          core.SkillManager
	mcp             core.MCPRegistry
	guard           core.Guard
	workspace       core.WorkspaceInitializer
	sessions        *sessionstore.Service
	obs             *observabilityRuntime
	providerMonitor *providerMonitor
	remote          *remotepkg.Manager
	keepAwake       *keepAwakeManager
	channelPeers    channelPeerClient
	channelClaimMu  sync.Mutex
	mux             *http.ServeMux
	httpSrv         *http.Server
}

// UsageReporter produces an aggregated usage report for the API. until is an
// exclusive upper bound.
type UsageReporter func(ctx context.Context, period string, since, until time.Time) (any, error)

// New builds a server.
func New(cfg *config.Config, log *slog.Logger, st *store.Store, pm core.ProviderManager, usageFn UsageReporter) *Server {
	s := &Server{
		cfg:       cfg,
		version:   "0.1.0",
		log:       log,
		st:        st,
		provider:  pm,
		usageFn:   usageFn,
		sessions:  sessionstore.New(),
		keepAwake: newKeepAwakeManager(),
		mux:       http.NewServeMux(),
	}
	s.providerMonitor = newProviderMonitor(log, st, pm)
	remoteManager, err := remotepkg.NewManager(
		cfg.Remote.HostsFile,
		time.Duration(cfg.Remote.ConnectTimeoutSeconds)*time.Second,
		log,
	)
	if err != nil {
		if log != nil {
			log.Warn("remote SSH control unavailable", "err", err)
		}
	} else {
		s.remote = remoteManager
		s.channelPeers = &remoteChannelPeerClient{manager: remoteManager}
	}
	s.routes()
	return s
}

// SetVersion exposes the build version through the status endpoint.
func (s *Server) SetVersion(value string) {
	if strings.TrimSpace(value) != "" {
		s.version = strings.TrimSpace(value)
	}
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

// SetProviderService attaches the takeover-aware provider service (local
// routing REST + hot-switch path).
func (s *Server) SetProviderService(svc *providerpkg.Service) {
	s.proxySvc = svc
	if svc != nil {
		s.provider = svc
		if s.providerMonitor != nil {
			s.providerMonitor.provider = svc
		}
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
	s.mux.HandleFunc("POST /api/v1/tools/cli/install/stream", s.handleCLIInstallStream)
	s.mux.HandleFunc("POST /api/v1/tools/cli/check", s.handleCLICheck)
	s.mux.HandleFunc("POST /api/v1/tools/cli/skills/sync", s.handleCLISkillSync)
	s.mux.HandleFunc("POST /api/v1/tools/cli/skills/sync/stream", s.handleCLISkillSyncStream)
	s.mux.HandleFunc("GET /api/v1/tools/cli/auth", s.handleCLIAuthStatus)
	s.mux.HandleFunc("POST /api/v1/tools/cli/auth/login", s.handleCLIAuthLogin)
	s.mux.HandleFunc("GET /api/v1/tools/cli/auth/login", s.handleCLIAuthLoginStatus)
	s.mux.HandleFunc("POST /api/v1/tools/cli/auth/login/cancel", s.handleCLIAuthLoginCancel)
	s.mux.HandleFunc("GET /api/v1/providers", s.handleProvidersList)
	s.mux.HandleFunc("POST /api/v1/providers", s.handleProviderUpsert)
	s.mux.HandleFunc("DELETE /api/v1/providers", s.handleProviderDelete)
	s.mux.HandleFunc("GET /api/v1/providers/active", s.handleProviderActiveRoutes)
	s.mux.HandleFunc("DELETE /api/v1/providers/active", s.handleProviderClearRoute)
	s.mux.HandleFunc("GET /api/v1/providers/presets", s.handleProviderPresets)
	s.mux.HandleFunc("POST /api/v1/providers/probe", s.handleProviderProbe)
	s.mux.HandleFunc("GET /api/v1/providers/monitor", s.handleProviderMonitorGet)
	s.mux.HandleFunc("PUT /api/v1/providers/monitor", s.handleProviderMonitorPut)
	s.mux.HandleFunc("POST /api/v1/providers/monitor/run", s.handleProviderMonitorRun)
	s.mux.HandleFunc("DELETE /api/v1/providers/monitor/alerts", s.handleProviderMonitorAlertDismiss)
	s.mux.HandleFunc("POST /api/v1/providers/switch", s.handleProviderSwitch)
	s.mux.HandleFunc("POST /api/v1/providers/failover", s.handleProviderFailover)
	s.mux.HandleFunc("GET /api/v1/proxy/status", s.handleProxyStatus)
	s.mux.HandleFunc("GET /api/v1/proxy/traces", s.handleProxyTraces)
	s.mux.HandleFunc("POST /api/v1/proxy/takeover", s.handleProxyTakeover)
	s.mux.HandleFunc("POST /api/v1/proxy/config", s.handleProxyConfigUpdate)
	s.mux.HandleFunc("GET /api/v1/system/claude-3p", s.handleClaude3PStatus)
	s.mux.HandleFunc("POST /api/v1/system/claude-3p", s.handleClaude3PToggle)
	s.mux.HandleFunc("GET /api/v1/system/directories", s.handleSystemDirectoryList)
	s.mux.HandleFunc("POST /api/v1/system/directories", s.handleSystemDirectoryEnsure)
	s.mux.HandleFunc("GET /api/v1/system/keep-awake", s.handleKeepAwakeGet)
	s.mux.HandleFunc("PUT /api/v1/system/keep-awake", s.handleKeepAwakePut)
	s.mux.HandleFunc("GET /api/v1/frameworks", s.handleFrameworksList)
	s.mux.HandleFunc("GET /api/v1/frameworks/auth", s.handleFrameworkAuthStatus)
	s.mux.HandleFunc("POST /api/v1/frameworks/install", s.handleFrameworkInstall)
	s.mux.HandleFunc("POST /api/v1/frameworks/install/stream", s.handleFrameworkInstallStream)
	s.mux.HandleFunc("POST /api/v1/frameworks/check", s.handleFrameworkCheck)
	s.mux.HandleFunc("POST /api/v1/frameworks/login", s.handleFrameworkLogin)
	s.mux.HandleFunc("POST /api/v1/frameworks/login/complete", s.handleFrameworkLoginComplete)
	s.mux.HandleFunc("GET /api/v1/sessions", s.handleSessionsList)
	s.mux.HandleFunc("GET /api/v1/sessions/messages", s.handleSessionMessages)
	s.mux.HandleFunc("POST /api/v1/sessions/messages", s.handleSessionMessageSend)
	s.mux.HandleFunc("POST /api/v1/sessions/resume", s.handleSessionResume)
	s.mux.HandleFunc("POST /api/v1/sessions/stop", s.handleSessionStop)
	s.mux.HandleFunc("DELETE /api/v1/sessions", s.handleSessionDelete)
	s.mux.HandleFunc("GET /api/v1/usage", s.handleUsage)
	s.mux.HandleFunc("GET /api/v1/menubar/settings", s.handleMenubarSettingsGet)
	s.mux.HandleFunc("PUT /api/v1/menubar/settings", s.handleMenubarSettingsPut)
	s.mux.HandleFunc("POST /api/v1/send", s.handleSend)
	s.mux.HandleFunc("GET /api/v1/channels", s.handleChannelsList)
	s.mux.HandleFunc("POST /api/v1/channels", s.handleChannelUpsert)
	s.mux.HandleFunc("POST /api/v1/channels/validate", s.handleChannelValidate)
	s.mux.HandleFunc("DELETE /api/v1/channels", s.handleChannelDelete)
	s.mux.HandleFunc("POST /api/v1/channels/restart", s.handleChannelRestart)
	s.mux.HandleFunc("GET /api/v1/channel-conversations", s.handleChannelConversations)
	s.mux.HandleFunc("POST /api/v1/channel-conversations/bind", s.handleChannelConversationBind)
	s.mux.HandleFunc("POST /api/v1/channel-conversations/open", s.handleChannelConversationOpen)
	s.mux.HandleFunc("GET /api/v1/channel-tasks", s.handleChannelTasks)
	s.mux.HandleFunc("GET /api/v1/channel-interactions", s.handleChannelInteractions)
	s.mux.HandleFunc("POST /api/v1/channel-interactions/respond", s.handleChannelInteractionRespond)
	s.mux.HandleFunc("POST /api/v1/setup/feishu/begin", s.handleFeishuSetupBegin)
	s.mux.HandleFunc("POST /api/v1/setup/feishu/poll", s.handleFeishuSetupPoll)
	s.registerRemoteRoutes()
	s.mux.HandleFunc("GET /api/v1/triggers", s.handleTriggersList)
	s.mux.HandleFunc("POST /api/v1/triggers", s.handleTriggerUpsert)
	s.mux.HandleFunc("DELETE /api/v1/triggers", s.handleTriggerDelete)
	s.mux.HandleFunc("POST /api/v1/triggers/run", s.handleTriggerRun)
	s.mux.HandleFunc("GET /channel-avatar", s.handleChannelAvatar)
	s.mux.HandleFunc("POST /hook/{id}", s.handleInboundHook)
	s.registerModuleRoutes()
	s.registerObservabilityRoutes()
	s.registerWeb(s.mux)
}

// ListenAndServe starts the HTTP server until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context) error {
	defer s.keepAwake.Stop()
	if s.remote != nil {
		defer s.remote.Close()
	}
	s.httpSrv = &http.Server{
		Addr:    s.cfg.Server.Addr,
		Handler: s.withAuth(s.mux),
	}
	if s.providerMonitor != nil {
		go s.providerMonitor.Run(ctx)
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
		if isObservabilityPath(r.URL.Path) {
			s.applyObservabilityCORS(w, r)
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			if !s.authorizeObservabilityRequest(w, r) {
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if len(r.URL.Path) >= 5 && r.URL.Path[:5] == "/api/" {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
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

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"projects": len(s.cfg.Projects),
		"version":  s.version,
	})
}

func (s *Server) handlePlatforms(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, core.RegisteredPlatforms())
}

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, availableAgentRuntimes())
}

func (s *Server) handleProvidersList(w http.ResponseWriter, r *http.Request) {
	if s.provider == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	ps, err := s.provider.List(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, p := range ps {
		annotateProviderAPIKey(p)
	}
	writeJSON(w, http.StatusOK, ps)
}

func (s *Server) handleProviderUpsert(w http.ResponseWriter, r *http.Request) {
	if s.provider == nil {
		writeErr(w, http.StatusServiceUnavailable, "provider manager unavailable")
		return
	}
	p, ok := decodeJSON[core.Provider](w, r)
	if !ok {
		return
	}
	if err := normalizeProviderAPIKeyForSave(&p); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.provider.Upsert(r.Context(), &p); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	annotateProviderAPIKey(&p)
	writeJSON(w, http.StatusOK, &p)
}

func (s *Server) handleProviderDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := requireQuery(w, r, "id")
	if !ok {
		return
	}
	if s.provider == nil {
		writeErr(w, http.StatusServiceUnavailable, "provider manager unavailable")
		return
	}
	if err := s.provider.Delete(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w)
}

func (s *Server) handleProviderActiveRoutes(w http.ResponseWriter, r *http.Request) {
	if s.provider == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	routes, err := s.provider.ActiveRoutes(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	for i := range routes {
		annotateProviderRouteAPIKey(&routes[i])
	}
	writeJSON(w, http.StatusOK, routes)
}

func (s *Server) handleProviderClearRoute(w http.ResponseWriter, r *http.Request) {
	if s.provider == nil {
		writeErr(w, http.StatusServiceUnavailable, "provider manager unavailable")
		return
	}
	tool, ok := requireQuery(w, r, "tool")
	if !ok {
		return
	}
	if err := s.provider.Clear(r.Context(), tool); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w)
}

func (s *Server) handleProviderPresets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.presets)
}

func (s *Server) handleProviderSwitch(w http.ResponseWriter, r *http.Request) {
	if s.provider == nil {
		writeErr(w, http.StatusServiceUnavailable, "provider manager unavailable")
		return
	}
	var req struct {
		ID            string            `json:"id"`
		Tool          string            `json:"tool"`
		Meta          core.ProviderMeta `json:"meta"`
		LocalTakeover *bool             `json:"local_takeover,omitempty"`
	}
	if !decodeJSONInto(w, r, &req) {
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
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w)
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Project string `json:"project"`
		Text    string `json:"text"`
	}
	if !decodeJSONInto(w, r, &req) {
		return
	}
	if s.sender == nil {
		writeErr(w, http.StatusServiceUnavailable, "no sender wired")
		return
	}
	if err := s.sender.SendToProject(r.Context(), req.Project, req.Text); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w)
}

func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	period := r.URL.Query().Get("period")
	if period == "" {
		period = "daily"
	}
	since, err := parseUsageDate(r.URL.Query().Get("from"), false)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	until, err := parseUsageDate(r.URL.Query().Get("to"), true)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !since.IsZero() && !until.IsZero() && !since.Before(until) {
		writeErr(w, http.StatusBadRequest, "usage date range must start on or before the end date")
		return
	}
	if s.usageFn == nil {
		writeJSON(w, http.StatusOK, map[string]any{})
		return
	}
	rep, err := s.usageFn(r.Context(), period, since, until)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func parseUsageDate(value string, inclusiveEnd bool) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.ParseInLocation("2006-01-02", value, time.Local)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid usage date %q; expected YYYY-MM-DD", value)
	}
	if inclusiveEnd {
		parsed = parsed.AddDate(0, 0, 1)
	}
	return parsed, nil
}
