package server

import (
	"context"
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
		serviceUnavailable(w, "memory")
		return
	}
	scope := r.URL.Query().Get("scope")
	query := r.URL.Query().Get("q")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	res, err := s.memory.Search(r.Context(), scope, query, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleMemoryPut(w http.ResponseWriter, r *http.Request) {
	if s.memory == nil {
		serviceUnavailable(w, "memory")
		return
	}
	e, ok := decodeJSON[core.MemoryEntry](w, r)
	if !ok {
		return
	}
	id, err := s.memory.Put(r.Context(), &e)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

func (s *Server) handleMemoryDelete(w http.ResponseWriter, r *http.Request) {
	if s.memory == nil {
		serviceUnavailable(w, "memory")
		return
	}
	id, ok := requireQuery(w, r, "id")
	if !ok {
		return
	}
	if err := s.memory.Delete(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w)
}

func (s *Server) handleSkillsList(w http.ResponseWriter, r *http.Request) {
	if s.skills == nil {
		serviceUnavailable(w, "skills")
		return
	}
	res, err := s.skills.List(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
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
		serviceUnavailable(w, "skills marketplace")
		return
	}
	res, err := mgr.Marketplace(r.Context(), r.URL.Query().Get("q"), r.URL.Query().Get("source"), r.URL.Query().Get("category"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleSkillInstall(w http.ResponseWriter, r *http.Request) {
	mgr, mgrOK := s.skills.(marketplaceSkillManager)
	if !mgrOK || mgr == nil {
		serviceUnavailable(w, "skills marketplace")
		return
	}
	req, ok := decodeJSON[skillpkg.InstallRequest](w, r)
	if !ok {
		return
	}
	res, err := mgr.InstallMarketplace(r.Context(), req)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleSkillToggle(w http.ResponseWriter, r *http.Request) {
	if s.skills == nil {
		serviceUnavailable(w, "skills")
		return
	}
	type toggleRequest struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	req, ok := decodeJSON[toggleRequest](w, r)
	if !ok {
		return
	}
	if err := s.skills.SetEnabled(r.Context(), req.Name, req.Enabled); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w)
}

func (s *Server) handleMCPList(w http.ResponseWriter, r *http.Request) {
	if s.mcp == nil {
		serviceUnavailable(w, "mcp registry")
		return
	}
	res, err := s.mcp.List(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleMCPUpsert(w http.ResponseWriter, r *http.Request) {
	if s.mcp == nil {
		serviceUnavailable(w, "mcp registry")
		return
	}
	m, ok := decodeJSON[core.MCPServer](w, r)
	if !ok {
		return
	}
	if err := s.mcp.Upsert(r.Context(), &m); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, &m)
}

func (s *Server) handleMCPDelete(w http.ResponseWriter, r *http.Request) {
	if s.mcp == nil {
		serviceUnavailable(w, "mcp registry")
		return
	}
	name, ok := requireQuery(w, r, "name")
	if !ok {
		return
	}
	if err := s.mcp.Delete(r.Context(), name); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w)
}

func (s *Server) handleGuardPolicies(w http.ResponseWriter, r *http.Request) {
	if s.st == nil {
		serviceUnavailable(w, "guard store")
		return
	}
	res, err := s.st.ListGuardPolicies(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleGuardEvaluate(w http.ResponseWriter, r *http.Request) {
	if s.guard == nil {
		serviceUnavailable(w, "guard")
		return
	}
	req, ok := decodeJSON[core.GuardRequest](w, r)
	if !ok {
		return
	}
	decision, err := s.guard.Evaluate(r.Context(), &req)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"decision": string(decision)})
}
