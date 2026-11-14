// Package server hosts the HTTP/WS management API and embedded WebUI. CLI,
// WebUI, Wails desktop and the macOS menubar all talk to this one server so
// logic never forks across clients.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wangning19940904/AgentMux/config"
	"github.com/wangning19940904/AgentMux/contract"
	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/eventrelay"
	"github.com/wangning19940904/AgentMux/internal/hostmetrics"
	linkpkg "github.com/wangning19940904/AgentMux/link"
	"github.com/wangning19940904/AgentMux/mailbox"
	orchestrationpkg "github.com/wangning19940904/AgentMux/orchestration"
	pushpkg "github.com/wangning19940904/AgentMux/push"
	remotepkg "github.com/wangning19940904/AgentMux/remote"
	sessionstore "github.com/wangning19940904/AgentMux/sessions"
	"github.com/wangning19940904/AgentMux/store"
	"github.com/wangning19940904/AgentMux/superagent"
	ttspkg "github.com/wangning19940904/AgentMux/tts"
	"golang.org/x/sync/singleflight"
)

// Server is the management/bridge HTTP server.
type Server struct {
	eventRelay         *eventrelay.Service
	cfg                *config.Config
	version            string
	log                *slog.Logger
	st                 *store.Store
	usageFn            UsageReporter
	usageSources       UsageSourceManager
	sender             core.ChannelDeliverySender
	invoker            core.Invoker
	openAIResponses    *openAIResponseRegistry
	openAIFiles        *openAIFileRegistry
	connect            *core.ConnectService
	memory             core.MemoryStore
	skills             core.SkillManager
	mcp                core.MCPRegistry
	guard              core.Guard
	moduleRuntime      map[string]ModuleRuntimeState
	workspace          core.WorkspaceInitializer
	sessions           *sessionstore.Service
	obs                *observabilityRuntime
	remote             *remotepkg.Manager
	remoteStatusChecks singleflight.Group
	linkHub            *linkpkg.Hub
	push               *pushpkg.Service
	devices            *deviceBackend
	node               *nodeLink
	hubHA              *hubStandby
	superAgents        *superagent.Service
	mail               *mailbox.Service
	feishuDirectory    superagent.FeishuDirectory
	feishuUser         superagent.FeishuUserDirectory
	feishuUserAuthMu   sync.Mutex
	feishuUserAuths    map[string]feishuUserAuthSession
	keepAwake          *keepAwakeManager
	hostMetrics        *hostmetrics.Sampler
	channelPeers       channelPeerClient
	meetingPeers       meetingPeerClient
	ttsModels          *ttspkg.Manager
	channelClaimMu     sync.Mutex
	channelBotCache    channelResourceCache[*channelBotInfo]
	channelAvatarCache channelResourceCache[channelAvatarData]
	orchestrations     *orchestrationpkg.Service
	feishuAutomationMu sync.Mutex
	feishuAutomations  map[string]*feishuAutomationSession
	consoleSessions    *consoleSessionManager
	signIns            *signInLimiter
	fleetSyncMu        sync.Mutex
	fleetSyncPlans     map[string]*fleetSyncPlan
	mux                *http.ServeMux
	httpSrv            *http.Server
}

// UsageReporter produces an aggregated usage report for the API. until is an
// exclusive upper bound.
type UsageReporter func(ctx context.Context, period string, since, until time.Time, location *time.Location) (any, error)

// Dependencies contains the complete HTTP server dependency graph. Production
// composition supplies this once rather than mutating a partially constructed
// server through a sequence of setters.
type Dependencies struct {
	EventRelay     *eventrelay.Service
	Config         *config.Config
	Version        string
	Log            *slog.Logger
	Store          *store.Store
	Usage          UsageReporter
	UsageSources   UsageSourceManager
	Sender         core.ChannelDeliverySender
	Invoker        core.Invoker
	Connect        *core.ConnectService
	Memory         core.MemoryStore
	Skills         core.SkillManager
	MCP            core.MCPRegistry
	Guard          core.Guard
	Workspace      core.WorkspaceInitializer
	ModuleRuntime  map[string]ModuleRuntimeState
	Orchestrations *orchestrationpkg.Service
	// PushSender delivers push notifications; nil sends Web Push.
	PushSender pushpkg.Sender
}

type ModuleRuntimeState struct {
	RuntimeActive bool
	Enforced      bool
}

// New builds a server from one explicit dependency set.
func New(deps Dependencies) *Server {
	cfg, log, st := deps.Config, deps.Log, deps.Store
	if log == nil {
		log = slog.Default()
	}
	s := &Server{
		eventRelay:        deps.EventRelay,
		cfg:               cfg,
		version:           "0.1.0",
		log:               log,
		st:                st,
		usageFn:           deps.Usage,
		usageSources:      deps.UsageSources,
		sender:            deps.Sender,
		invoker:           deps.Invoker,
		connect:           deps.Connect,
		memory:            deps.Memory,
		skills:            deps.Skills,
		mcp:               deps.MCP,
		guard:             deps.Guard,
		moduleRuntime:     deps.ModuleRuntime,
		workspace:         deps.Workspace,
		sessions:          sessionstore.New(),
		keepAwake:         newKeepAwakeManager(),
		hostMetrics:       hostmetrics.New(hostmetrics.Options{}),
		ttsModels:         ttspkg.NewManager("", log),
		openAIResponses:   newOpenAIResponseRegistry(),
		openAIFiles:       newOpenAIFileRegistry(),
		orchestrations:    deps.Orchestrations,
		feishuAutomations: map[string]*feishuAutomationSession{},
		feishuUserAuths:   map[string]feishuUserAuthSession{},
		consoleSessions:   newConsoleSessionManager(),
		signIns:           newSignInLimiter(),
		fleetSyncPlans:    map[string]*fleetSyncPlan{},
		mux:               http.NewServeMux(),
	}
	if strings.TrimSpace(deps.Version) != "" {
		s.version = strings.TrimSpace(deps.Version)
	}
	if s.orchestrations == nil && st != nil && deps.Invoker != nil {
		s.orchestrations = orchestrationpkg.New(st, deps.Invoker)
	}
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
		peer := &remoteChannelPeerClient{manager: remoteManager}
		s.channelPeers = peer
		s.meetingPeers = peer
	}
	s.setupPush(deps.PushSender)
	s.setupLink()
	s.setupMailbox()
	s.routes()
	if s.orchestrations != nil {
		go s.orchestrations.Recover()
	}
	return s
}

// SetVersion exposes the build version through the status endpoint.
func (s *Server) SetVersion(value string) {
	if strings.TrimSpace(value) != "" {
		s.version = strings.TrimSpace(value)
	}
}

// SetUsageSourceManager attaches consent-gated collectors such as Cursor.
func (s *Server) SetUsageSourceManager(manager UsageSourceManager) {
	s.usageSources = manager
}

// SetInvoker attaches the direct Agent execution service. It is separate from
// Sender because an invocation runs an Agent and returns its result; it does
// not publish a message to a channel.
func (s *Server) SetInvoker(invoker core.Invoker) {
	s.invoker = invoker
	if invoker != nil && s.st != nil {
		s.orchestrations = orchestrationpkg.New(s.st, invoker)
		go s.orchestrations.Recover()
	}
}

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

// ListenAndServe starts the HTTP server until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context) error {
	if s == nil || s.cfg == nil {
		return fmt.Errorf("server config is required")
	}
	if err := s.cfg.ValidateListenSecurity(); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", s.cfg.Server.Addr)
	if err != nil {
		return err
	}
	return s.Serve(ctx, listener)
}

// Serve uses the API listener reserved before daemon initialization.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	if listener == nil {
		return fmt.Errorf("server listener is required")
	}
	defer listener.Close()
	if s == nil || s.cfg == nil {
		return fmt.Errorf("server config is required")
	}
	if err := s.cfg.ValidateListenSecurity(); err != nil {
		return err
	}
	defer s.keepAwake.Stop()
	defer s.hostMetrics.Close()
	if s.remote != nil {
		defer s.remote.Close()
	}
	httpSrv := &http.Server{
		Addr:    s.cfg.Server.Addr,
		Handler: s.withAuth(s.mux),
	}
	s.httpSrv = httpSrv
	// The provider monitor no longer runs: AgentMux does not manage providers,
	// and frameworks are checked on demand by their own "reply ok" test. Its
	// last snapshot stays readable until the provider module is removed.
	stopped := make(chan struct{})
	defer close(stopped)
	if err := s.startHubListener(ctx); err != nil {
		return err
	}
	s.node.run(ctx)
	if err := s.startHubStandby(ctx); err != nil {
		return err
	}
	if s.mail != nil {
		go s.mail.Run(ctx)
	}
	go func() {
		select {
		case <-ctx.Done():
			shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = httpSrv.Shutdown(shutCtx)
		case <-stopped:
		}
	}()
	s.log.Info("server listening", "addr", listener.Addr().String())
	if err := httpSrv.Serve(listener); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// withAuth enforces credentials on management and OpenAI-compatible API
// routes when the bridge is on. When the bridge is off, legacy unauthenticated
// local access remains available, but an explicitly supplied credential is
// still resolved, so a signed-in member keeps member scope instead of
// silently inheriting administrator access.
func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isObservabilityPath(r.URL.Path) {
			s.applyObservabilityCORS(w, r)
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			// Observability carries agent transcripts and runs on its own
			// credential model. It is administrator-only, so a member is
			// rejected outright rather than falling through to that model.
			if principal := s.resolvePrincipal(r); principal.IsMember() {
				denyMemberRoute(w, r, principal)
				return
			}
			if !s.authorizeObservabilityRequest(w, r) {
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if isBridgeAPIPath(r.URL.Path) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, OpenAI-Organization, OpenAI-Project, X-AgentMux-Agent-ID, X-AgentMux-Console, X-Stainless-Lang, X-Stainless-Package-Version, X-Stainless-OS, X-Stainless-Arch, X-Stainless-Runtime, X-Stainless-Runtime-Version, X-Stainless-Async")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		if isBridgeAPIPath(r.URL.Path) && !isSelfAuthenticatingPath(r.URL.Path) &&
			(s.cfg.Bridge.Enabled || hasExplicitCredential(r)) {
			principal := s.resolvePrincipal(r)
			if principal == nil {
				if strings.HasPrefix(r.URL.Path, "/v1/") {
					writeOpenAIError(w, http.StatusUnauthorized, "invalid or missing API key", "authentication_error", nil, "invalid_api_key")
				} else {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
				}
				return
			}
			// A member reaches only their own devices, messages and account;
			// everything else is reserved for administrators.
			if !principal.Admin && !(principal.IsMember() && memberRouteAllowed(r.Method, r.URL.Path)) {
				denyMemberRoute(w, r, principal)
				return
			}
			r = r.WithContext(withPrincipal(r.Context(), principal))
		}
		next.ServeHTTP(w, r)
	})
}

// hasExplicitCredential distinguishes the bridge-disabled legacy mode from a
// caller that is deliberately asking to run under a bearer or Console-session
// identity. Invalid explicit credentials must fail instead of falling back to
// the open administrator scope.
func hasExplicitCredential(r *http.Request) bool {
	if strings.TrimSpace(r.Header.Get("Authorization")) != "" {
		return true
	}
	_, err := r.Cookie(consoleSessionCookie)
	return err == nil
}

func isBridgeAPIPath(path string) bool {
	return strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/v1/")
}

// isSelfAuthenticatingPath lists endpoints that cannot require a credential:
// the open account paths report whether sign-in is needed and perform it, and
// resolve the caller's credential themselves.
func isSelfAuthenticatingPath(path string) bool {
	return authOpenPaths[path]
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":               true,
		"version":          s.version,
		"contract_version": contract.Version,
	})
}

func (s *Server) handlePlatforms(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, core.RegisteredPlatforms())
}

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, availableAgentRuntimes())
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ChannelID       string   `json:"channel_id"`
		ConversationKey string   `json:"conversation_key"`
		Text            string   `json:"text"`
		Images          []string `json:"images"`
		Files           []string `json:"files"`
	}
	if !decodeJSONInto(w, r, &req) {
		return
	}
	if s.sender == nil {
		writeErr(w, http.StatusServiceUnavailable, "no sender wired")
		return
	}
	if !isLoopbackRequest(r) {
		writeErr(w, http.StatusForbidden, "channel delivery is only available on loopback")
		return
	}
	if strings.TrimSpace(req.ChannelID) == "" || strings.TrimSpace(req.ConversationKey) == "" {
		writeErr(w, http.StatusBadRequest, "channel_id and conversation_key are required")
		return
	}
	if s.st != nil {
		if _, authorized := s.lookupChannel(w, r, req.ChannelID); !authorized {
			return
		}
	}
	images, err := readChannelDeliveryFiles(req.Images, 10<<20)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid image: "+err.Error())
		return
	}
	files, err := readChannelDeliveryFiles(req.Files, 30<<20)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid file: "+err.Error())
		return
	}
	delivery := core.ChannelDelivery{
		ChannelID:       req.ChannelID,
		ConversationKey: req.ConversationKey,
		Text:            req.Text,
		Images:          images,
		Files:           files,
	}
	if err := s.sender.SendToChannel(r.Context(), delivery); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeOK(w)
}

func readChannelDeliveryFiles(paths []string, maxBytes int64) ([]core.ChannelDeliveryFile, error) {
	if len(paths) > 8 {
		return nil, fmt.Errorf("at most 8 attachments are allowed")
	}
	files := make([]core.ChannelDeliveryFile, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" || !filepath.IsAbs(path) {
			return nil, fmt.Errorf("attachment path must be absolute")
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%q is not a regular file", path)
		}
		if info.Size() > maxBytes {
			return nil, fmt.Errorf("%q exceeds the %d MiB limit", path, maxBytes>>20)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		files = append(files, core.ChannelDeliveryFile{Name: filepath.Base(path), Data: data})
	}
	return files, nil
}

func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	period := r.URL.Query().Get("period")
	if period == "" {
		period = "daily"
	}
	location := time.Local
	if timezone := strings.TrimSpace(r.URL.Query().Get("timezone")); timezone != "" {
		var err error
		location, err = time.LoadLocation(timezone)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid usage timezone")
			return
		}
	}
	since, err := parseUsageDateInLocation(r.URL.Query().Get("from"), false, location)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	until, err := parseUsageDateInLocation(r.URL.Query().Get("to"), true, location)
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
	rep, err := s.usageFn(r.Context(), period, since, until, location)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func parseUsageDateInLocation(value string, inclusiveEnd bool, location *time.Location) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	if location == nil {
		location = time.Local
	}
	parsed, err := time.ParseInLocation("2006-01-02", value, location)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid usage date %q; expected YYYY-MM-DD", value)
	}
	if inclusiveEnd {
		parsed = parsed.AddDate(0, 0, 1)
	}
	return parsed, nil
}
