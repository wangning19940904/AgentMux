// Package server hosts the HTTP/WS management API and embedded WebUI. CLI,
// WebUI, Wails desktop and the macOS menubar all talk to this one server so
// logic never forks across clients.
package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/agentnexus/agentnexus/config"
	"github.com/agentnexus/agentnexus/core"
	"github.com/agentnexus/agentnexus/store"
)

// Server is the management/bridge HTTP server.
type Server struct {
	cfg      *config.Config
	log      *slog.Logger
	st       *store.Store
	provider core.ProviderManager
	usageFn  UsageReporter
	sender   core.Sender
	presets  any
	memory   core.MemoryStore
	skills   core.SkillManager
	mcp      core.MCPRegistry
	guard    core.Guard
	mux      *http.ServeMux
	httpSrv  *http.Server
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

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/v1/status", s.handleStatus)
	s.mux.HandleFunc("GET /api/v1/platforms", s.handlePlatforms)
	s.mux.HandleFunc("GET /api/v1/agents", s.handleAgents)
	s.mux.HandleFunc("GET /api/v1/providers", s.handleProvidersList)
	s.mux.HandleFunc("POST /api/v1/providers", s.handleProviderUpsert)
	s.mux.HandleFunc("GET /api/v1/providers/presets", s.handleProviderPresets)
	s.mux.HandleFunc("POST /api/v1/providers/switch", s.handleProviderSwitch)
	s.mux.HandleFunc("GET /api/v1/usage", s.handleUsage)
	s.mux.HandleFunc("POST /api/v1/send", s.handleSend)
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
	writeJSON(w, http.StatusOK, ps)
}

func (s *Server) handleProviderUpsert(w http.ResponseWriter, r *http.Request) {
	var p core.Provider
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := s.provider.Upsert(r.Context(), &p); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, &p)
}

func (s *Server) handleProviderPresets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.presets)
}

func (s *Server) handleProviderSwitch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID   string `json:"id"`
		Tool string `json:"tool"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := s.provider.Switch(r.Context(), req.ID, req.Tool); err != nil {
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
