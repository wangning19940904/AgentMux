package server

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/superagent"
)

const maxGitHubWebhookBytes = 25 << 20 // GitHub's documented payload cap.

// SetSuperAgents attaches the Super Agent runtime.
func (s *Server) SetSuperAgents(service *superagent.Service) { s.superAgents = service }

// RemoteHosts exposes the SSH host manager to the Super Agent worker fleet. It
// returns an untyped nil when remote control is unavailable.
func (s *Server) RemoteHosts() superagent.HostDialer {
	if s.remote == nil {
		return nil
	}
	return s.remote
}

func (s *Server) registerSuperAgentRoutes() {
	s.mux.HandleFunc("GET /api/v1/super-agents", s.handleSuperAgentsList)
	s.mux.HandleFunc("POST /api/v1/super-agents", s.handleSuperAgentUpsert)
	s.mux.HandleFunc("DELETE /api/v1/super-agents", s.handleSuperAgentDelete)
	s.mux.HandleFunc("GET /api/v1/super-agents/activity", s.handleSuperAgentActivity)
	s.mux.HandleFunc("POST /api/v1/super-agents/setup", s.handleSuperAgentSetup)
	s.mux.HandleFunc("POST /api/v1/super-agents/trigger", s.handleSuperAgentTrigger)
	s.mux.HandleFunc("POST /api/v1/super-agents/tickets/stop", s.handleSuperAgentTicketAction(func(r *http.Request, id string) error {
		return s.superAgents.StopTicket(r.Context(), id)
	}))
	s.mux.HandleFunc("POST /api/v1/super-agents/tickets/retry", s.handleSuperAgentTicketAction(func(r *http.Request, id string) error {
		return s.superAgents.RetryTicket(r.Context(), id)
	}))
	// Outside /api: GitHub cannot present a bridge token. Each delivery proves
	// itself with the HMAC signature of its Source's webhook secret instead.
	s.mux.HandleFunc("POST /hook/super-agent/github/{source}", s.handleSuperAgentGitHubHook)
}

func (s *Server) requireSuperAgents(w http.ResponseWriter) bool {
	if s.superAgents == nil {
		writeErr(w, http.StatusServiceUnavailable, "super agent runtime unavailable")
		return false
	}
	return true
}

func (s *Server) handleSuperAgentsList(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgents(w) {
		return
	}
	items, err := s.superAgents.List(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleSuperAgentUpsert(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgents(w) {
		return
	}
	agent, ok := decodeJSON[core.SuperAgent](w, r)
	if !ok {
		return
	}
	saved, err := s.superAgents.Save(r.Context(), agent)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleSuperAgentDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgents(w) {
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	if err := s.superAgents.Delete(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleSuperAgentActivity(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgents(w) {
		return
	}
	activity, err := s.superAgents.Activity(r.Context(), strings.TrimSpace(r.URL.Query().Get("id")))
	if err != nil {
		writeSuperAgentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, activity)
}

func (s *Server) handleSuperAgentSetup(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgents(w) {
		return
	}
	body, ok := decodeJSON[superagent.SetupRequest](w, r)
	if !ok {
		return
	}
	result, err := s.superAgents.Setup(r.Context(), body)
	if err != nil {
		writeSuperAgentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleSuperAgentTrigger(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgents(w) {
		return
	}
	body, ok := decodeJSON[struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}](w, r)
	if !ok {
		return
	}
	trigger, err := s.superAgents.TriggerManually(r.Context(), body.ID, body.Text)
	if err != nil {
		writeSuperAgentError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, trigger)
}

func (s *Server) handleSuperAgentTicketAction(action func(*http.Request, string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.requireSuperAgents(w) {
			return
		}
		body, ok := decodeJSON[struct {
			ID string `json:"id"`
		}](w, r)
		if !ok {
			return
		}
		if err := action(r, strings.TrimSpace(body.ID)); err != nil {
			writeSuperAgentError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func (s *Server) handleSuperAgentGitHubHook(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgents(w) {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxGitHubWebhookBytes))
	if err != nil {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}
	err = s.superAgents.HandleGitHubWebhook(r.Context(), r.PathValue("source"), superagent.GitHubDelivery{
		Event:      r.Header.Get("X-GitHub-Event"),
		DeliveryID: r.Header.Get("X-GitHub-Delivery"),
		Signature:  r.Header.Get("X-Hub-Signature-256"),
		Body:       body,
	})
	switch {
	case errors.Is(err, superagent.ErrNotFound):
		http.Error(w, "webhook source not found", http.StatusNotFound)
	case errors.Is(err, superagent.ErrUnauthorized):
		http.Error(w, "invalid signature", http.StatusUnauthorized)
	case err != nil:
		if s.log != nil {
			s.log.Error("super agent webhook failed", "err", err)
		}
		http.Error(w, "webhook processing failed", http.StatusInternalServerError)
	default:
		writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
	}
}

func writeSuperAgentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, superagent.ErrNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, superagent.ErrConflict):
		writeErr(w, http.StatusConflict, err.Error())
	default:
		writeErr(w, http.StatusBadRequest, err.Error())
	}
}
