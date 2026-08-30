package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/wangning19940904/AgentMux/core"
	orchestrationpkg "github.com/wangning19940904/AgentMux/orchestration"
)

func (s *Server) handleOrchestrationsList(w http.ResponseWriter, r *http.Request) {
	if s.st == nil {
		writeErr(w, http.StatusServiceUnavailable, "orchestration store is unavailable")
		return
	}
	principal := requestPrincipal(r)
	if id := strings.TrimSpace(r.URL.Query().Get("id")); id != "" {
		item, err := s.st.GetOrchestration(r.Context(), id)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if item == nil {
			writeErr(w, http.StatusNotFound, "orchestration not found")
			return
		}
		level := s.accessLevel(r.Context(), principal, core.ResourceTypeOrchestration, item.ID, item.OwnerTenantID, "")
		if !core.GrantSatisfies(level, core.GrantLevelRead) {
			writeNotVisible(w, "orchestration")
			return
		}
		writeJSON(w, http.StatusOK, item)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	activeOnly := r.URL.Query().Get("active") == "true"
	var items []core.Orchestration
	var err error
	if principal.IsTenant() {
		items, err = s.st.ListOrchestrationsForTenant(r.Context(), activeOnly, limit, principal.TenantID)
	} else {
		items, err = s.st.ListOrchestrations(r.Context(), activeOnly, limit)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleOrchestrationCreate(w http.ResponseWriter, r *http.Request) {
	if s.orchestrations == nil || !s.orchestrations.Available() {
		writeErr(w, http.StatusServiceUnavailable, "orchestration runtime is unavailable")
		return
	}
	var req struct {
		Name           string                   `json:"name"`
		MaxConcurrency int                      `json:"max_concurrency"`
		Tasks          []core.OrchestrationTask `json:"tasks"`
	}
	if !decodeJSONInto(w, r, &req) {
		return
	}
	orchestration, err := orchestrationpkg.Normalize(req.Name, req.MaxConcurrency, req.Tasks)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Every task target must be runnable by the caller, otherwise a tenant
	// could reach a peer's agent through a DAG instead of a direct invocation.
	principal := requestPrincipal(r)
	if principal.IsTenant() {
		orchestration.OwnerTenantID = principal.TenantID
		for _, task := range orchestration.Tasks {
			if !s.authorizeInvocationTarget(w, r, task.AgentID) {
				return
			}
		}
	}
	if err := s.orchestrations.Create(r.Context(), orchestration); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, orchestration)
}

func (s *Server) handleOrchestrationCancel(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r) {
		writeErr(w, http.StatusForbidden, "orchestrations can be cancelled only from the local console")
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if !decodeJSONInto(w, r, &req) {
		return
	}
	req.ID = strings.TrimSpace(req.ID)
	if req.ID == "" {
		writeErr(w, http.StatusBadRequest, "orchestration id is required")
		return
	}
	if s.orchestrations == nil {
		writeErr(w, http.StatusServiceUnavailable, "orchestration runtime is unavailable")
		return
	}
	if err := s.orchestrations.Cancel(r.Context(), req.ID); err != nil {
		switch {
		case errors.Is(err, orchestrationpkg.ErrNotFound):
			writeErr(w, http.StatusNotFound, err.Error())
		case errors.Is(err, orchestrationpkg.ErrTerminal), errors.Is(err, orchestrationpkg.ErrRecovering):
			writeErr(w, http.StatusConflict, err.Error())
		default:
			writeErr(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	writeOK(w)
}
