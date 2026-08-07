package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/wangning19940904/AgentMux/core"
	skillpkg "github.com/wangning19940904/AgentMux/skills"
)

// registerModuleRoutes wires the Memory, Skills, MCP Registry and Guard
// endpoints. Backends may be nil; handlers respond with empty data or 503.
func (s *Server) registerModuleRoutes() {
	s.mux.HandleFunc("GET /api/v1/modules", s.handleModules)

	s.mux.HandleFunc("GET /api/v1/memory", s.handleMemorySearch)
	s.mux.HandleFunc("POST /api/v1/memory", s.handleMemoryPut)
	s.mux.HandleFunc("DELETE /api/v1/memory", s.handleMemoryDelete)

	s.mux.HandleFunc("GET /api/v1/skills", s.handleSkillsList)
	s.mux.HandleFunc("GET /api/v1/skills/marketplace", s.handleSkillsMarketplace)
	s.mux.HandleFunc("POST /api/v1/skills/install", s.handleSkillInstall)
	s.mux.HandleFunc("POST /api/v1/skills/toggle", s.handleSkillToggle)

	s.mux.HandleFunc("GET /api/v1/mcp", s.handleMCPList)
	s.mux.HandleFunc("POST /api/v1/mcp", s.handleMCPUpsert)
	s.mux.HandleFunc("DELETE /api/v1/mcp", s.handleMCPDelete)

	s.mux.HandleFunc("GET /api/v1/guard/policies", s.handleGuardPolicies)
	s.mux.HandleFunc("POST /api/v1/guard/evaluate", s.handleGuardEvaluate)
}

// handleModules reports which control-plane modules are registered/active.
func (s *Server) handleModules(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"connect": core.RegisteredPlatforms(),
		"router":  core.RegisteredAgents(),
		"ledger":  s.cfg.Usage.Sources,
		"memory":  core.RegisteredMemories(),
		"skills":  core.RegisteredSkillManagers(),
		"mcp":     core.RegisteredMCPRegistries(),
		"guard":   core.RegisteredGuards(),
		"active": map[string]bool{
			"memory": s.memory != nil,
			"skills": s.skills != nil,
			"mcp":    s.mcp != nil,
			"guard":  s.guard != nil,
		},
	})
}

func (s *Server) handleMemorySearch(w http.ResponseWriter, r *http.Request) {
	if s.memory == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	scope := r.URL.Query().Get("scope")
	query := r.URL.Query().Get("q")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	res, err := s.memory.Search(r.Context(), scope, query, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleMemoryPut(w http.ResponseWriter, r *http.Request) {
	if s.memory == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "memory not enabled"})
		return
	}
	var e core.MemoryEntry
	if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	id, err := s.memory.Put(r.Context(), &e)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

func (s *Server) handleMemoryDelete(w http.ResponseWriter, r *http.Request) {
	if s.memory == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "memory not enabled"})
		return
	}
	id := r.URL.Query().Get("id")
	if err := s.memory.Delete(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleSkillsList(w http.ResponseWriter, r *http.Request) {
	if s.skills == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	res, err := s.skills.List(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

type marketplaceSkillManager interface {
	Marketplace(ctx context.Context, query, source, category string) ([]skillpkg.MarketplaceSkill, error)
	InstallMarketplace(ctx context.Context, req skillpkg.InstallRequest) (*core.Skill, error)
}

func (s *Server) handleSkillsMarketplace(w http.ResponseWriter, r *http.Request) {
	mgr, ok := s.skills.(marketplaceSkillManager)
	if !ok || mgr == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	res, err := mgr.Marketplace(r.Context(), r.URL.Query().Get("q"), r.URL.Query().Get("source"), r.URL.Query().Get("category"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleSkillInstall(w http.ResponseWriter, r *http.Request) {
	mgr, ok := s.skills.(marketplaceSkillManager)
	if !ok || mgr == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "skills marketplace not enabled"})
		return
	}
	var req skillpkg.InstallRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	res, err := mgr.InstallMarketplace(r.Context(), req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleSkillToggle(w http.ResponseWriter, r *http.Request) {
	if s.skills == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "skills not enabled"})
		return
	}
	var req struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := s.skills.SetEnabled(r.Context(), req.Name, req.Enabled); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleMCPList(w http.ResponseWriter, r *http.Request) {
	if s.mcp == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	res, err := s.mcp.List(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleMCPUpsert(w http.ResponseWriter, r *http.Request) {
	if s.mcp == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "mcp registry not enabled"})
		return
	}
	var m core.MCPServer
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := s.mcp.Upsert(r.Context(), &m); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, &m)
}

func (s *Server) handleMCPDelete(w http.ResponseWriter, r *http.Request) {
	if s.mcp == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "mcp registry not enabled"})
		return
	}
	name := r.URL.Query().Get("name")
	if err := s.mcp.Delete(r.Context(), name); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleGuardPolicies(w http.ResponseWriter, r *http.Request) {
	if s.st == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	res, err := s.st.ListGuardPolicies(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleGuardEvaluate(w http.ResponseWriter, r *http.Request) {
	if s.guard == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "guard not enabled"})
		return
	}
	var req core.GuardRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	decision, err := s.guard.Evaluate(r.Context(), &req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"decision": string(decision)})
}
