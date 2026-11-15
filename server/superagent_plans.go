package server

import (
	"net/http"
	"strings"

	"github.com/wangning19940904/AgentMux/core"
)

// Plans an agent's brain proposed, and the approvals its requesters asked to
// have remembered (superagent/plans.go). Beta routes.
func (s *Server) registerSuperAgentPlanRoutes() {
	s.mux.HandleFunc("GET /api/v1/super-agents/plans", s.handleSuperAgentPlans)
	s.mux.HandleFunc("POST /api/v1/super-agents/plans/approve", s.handleSuperAgentPlanDecision(true))
	s.mux.HandleFunc("POST /api/v1/super-agents/plans/decline", s.handleSuperAgentPlanDecision(false))
	s.mux.HandleFunc("GET /api/v1/super-agents/trust", s.handleSuperAgentTrust)
	s.mux.HandleFunc("DELETE /api/v1/super-agents/trust", s.handleSuperAgentTrustRevoke)
}

type superAgentPlansResponse struct {
	Plans []core.SuperAgentPlan  `json:"plans"`
	Trust []core.SuperAgentTrust `json:"trust"`
}

func (s *Server) handleSuperAgentPlans(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgents(w) {
		return
	}
	agentID := strings.TrimSpace(r.URL.Query().Get("agent_id"))
	plans, err := s.superAgents.Plans(r.Context(), agentID)
	if err != nil {
		writeSuperAgentError(w, err)
		return
	}
	trust, err := s.superAgents.Trust(r.Context(), agentID)
	if err != nil {
		writeSuperAgentError(w, err)
		return
	}
	if plans == nil {
		plans = []core.SuperAgentPlan{}
	}
	if trust == nil {
		trust = []core.SuperAgentTrust{}
	}
	writeJSON(w, http.StatusOK, superAgentPlansResponse{Plans: plans, Trust: trust})
}

// handleSuperAgentPlanDecision approves or declines a pending plan. Approving
// starts its Tickets; remember also keeps the requester's approval of work up
// to the plan's tier. A plan that is no longer pending is 409.
func (s *Server) handleSuperAgentPlanDecision(approve bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.requireSuperAgents(w) {
			return
		}
		body, ok := decodeJSON[struct {
			AgentID  string `json:"agent_id"`
			PlanID   string `json:"plan_id"`
			Remember bool   `json:"remember"`
			Note     string `json:"note"`
		}](w, r)
		if !ok {
			return
		}
		by := "Console"
		if principal := principalFrom(r.Context()); principal != nil && principal.Account != nil {
			by = firstNonEmptyString(principal.Account.DisplayName, principal.Account.Login)
		}
		var plan core.SuperAgentPlan
		var err error
		if approve {
			plan, err = s.superAgents.ApprovePlan(r.Context(), strings.TrimSpace(body.AgentID), strings.TrimSpace(body.PlanID), body.Remember, by)
		} else {
			plan, err = s.superAgents.DeclinePlan(r.Context(), strings.TrimSpace(body.AgentID), strings.TrimSpace(body.PlanID), body.Note, by)
		}
		if err != nil {
			writeSuperAgentError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, plan)
	}
}

func (s *Server) handleSuperAgentTrust(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgents(w) {
		return
	}
	trust, err := s.superAgents.Trust(r.Context(), strings.TrimSpace(r.URL.Query().Get("agent_id")))
	if err != nil {
		writeSuperAgentError(w, err)
		return
	}
	if trust == nil {
		trust = []core.SuperAgentTrust{}
	}
	writeJSON(w, http.StatusOK, trust)
}

func (s *Server) handleSuperAgentTrustRevoke(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgents(w) {
		return
	}
	query := r.URL.Query()
	if err := s.superAgents.RevokeTrust(r.Context(), strings.TrimSpace(query.Get("agent_id")), strings.TrimSpace(query.Get("requester"))); err != nil {
		writeSuperAgentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
