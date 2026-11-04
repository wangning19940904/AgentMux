package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/superagent"
)

const maxGitHubWebhookBytes = 25 << 20 // GitHub's documented payload cap.

// SetSuperAgents attaches the Super Agent runtime and the Feishu directory its
// Source form reads groups and people from.
func (s *Server) SetSuperAgents(service *superagent.Service, directory superagent.FeishuDirectory) {
	s.superAgents = service
	s.feishuDirectory = directory
}

// RemoteHosts exposes the SSH host manager to the Super Agent worker fleet. It
// returns an untyped nil when remote control is unavailable.
func (s *Server) RemoteHosts() superagent.HostDialer {
	if s.remote == nil {
		return nil
	}
	return s.remote
}

// AgentRuntimeAvailable reports whether an Agent can run on this framework here.
func AgentRuntimeAvailable(id string) bool {
	return knownAgentRuntime(id) && agentRuntimeAvailable(id)
}

func (s *Server) registerSuperAgentRoutes() {
	s.mux.HandleFunc("GET /api/v1/super-agents", s.handleSuperAgentsList)
	s.mux.HandleFunc("POST /api/v1/super-agents", s.handleSuperAgentUpsert)
	s.mux.HandleFunc("DELETE /api/v1/super-agents", s.handleSuperAgentDelete)
	s.mux.HandleFunc("GET /api/v1/super-agents/routers", s.handleSuperAgentRoutersList)
	s.mux.HandleFunc("POST /api/v1/super-agents/routers", s.handleSuperAgentRouterUpsert)
	s.mux.HandleFunc("DELETE /api/v1/super-agents/routers", s.handleSuperAgentRouterDelete)
	s.mux.HandleFunc("GET /api/v1/super-agents/activity", s.handleSuperAgentActivity)
	s.mux.HandleFunc("POST /api/v1/super-agents/setup", s.handleSuperAgentSetup)
	s.mux.HandleFunc("POST /api/v1/super-agents/trigger", s.handleSuperAgentTrigger)
	s.mux.HandleFunc("POST /api/v1/super-agents/tickets/stop", s.handleSuperAgentTicketAction(func(r *http.Request, id string) error {
		return s.superAgents.StopTicket(r.Context(), id)
	}))
	s.mux.HandleFunc("POST /api/v1/super-agents/tickets/retry", s.handleSuperAgentTicketAction(func(r *http.Request, id string) error {
		return s.superAgents.RetryTicket(r.Context(), id)
	}))
	s.mux.HandleFunc("GET /api/v1/super-agents/feishu/chats", s.handleFeishuDirectory(func(r *http.Request, channelID string) (any, error) {
		return s.feishuDirectory.Chats(r.Context(), channelID)
	}))
	s.mux.HandleFunc("GET /api/v1/super-agents/feishu/members", s.handleFeishuDirectory(func(r *http.Request, channelID string) (any, error) {
		return s.feishuDirectory.Members(r.Context(), channelID, splitQueryList(r.URL.Query().Get("chat_ids")))
	}))
	// POST keeps the email addresses being looked up out of URLs and access logs.
	s.mux.HandleFunc("POST /api/v1/super-agents/feishu/users", s.handleFeishuDirectory(func(r *http.Request, channelID string) (any, error) {
		var body struct {
			OpenIDs []string `json:"open_ids"`
			Emails  []string `json:"emails"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
			return nil, errBadDirectoryRequest
		}
		return s.feishuDirectory.Users(r.Context(), channelID, body.OpenIDs, body.Emails)
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

func (s *Server) handleSuperAgentRoutersList(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgents(w) {
		return
	}
	list, err := s.superAgents.ListRouters(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleSuperAgentRouterUpsert(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgents(w) {
		return
	}
	router, ok := decodeJSON[superagent.Router](w, r)
	if !ok {
		return
	}
	saved, err := s.superAgents.SaveRouter(r.Context(), router)
	if err != nil {
		writeSuperAgentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleSuperAgentRouterDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireSuperAgents(w) {
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	if err := s.superAgents.DeleteRouter(r.Context(), id); err != nil {
		writeSuperAgentError(w, err)
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

var errBadDirectoryRequest = errors.New("invalid JSON body")

// handleFeishuDirectory checks the channel before asking Feishu, so a wrong id
// is a 4xx here and only Feishu's own refusals, such as a missing app scope,
// come back as 502 with Feishu's message.
func (s *Server) handleFeishuDirectory(lookup func(*http.Request, string) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.feishuDirectory == nil || s.st == nil {
			writeErr(w, http.StatusServiceUnavailable, "feishu directory unavailable")
			return
		}
		channelID := strings.TrimSpace(r.URL.Query().Get("channel_id"))
		if channelID == "" {
			writeErr(w, http.StatusBadRequest, "channel_id is required")
			return
		}
		channel, err := s.st.GetChannel(r.Context(), channelID)
		switch {
		case err != nil:
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		case channel == nil:
			writeErr(w, http.StatusNotFound, "channel not found")
			return
		case channel.Type != "feishu" && channel.Type != "lark":
			writeErr(w, http.StatusBadRequest, "channel is not a Feishu channel")
			return
		}
		result, err := lookup(r, channelID)
		switch {
		case errors.Is(err, errBadDirectoryRequest):
			writeErr(w, http.StatusBadRequest, err.Error())
		case err != nil:
			writeErr(w, http.StatusBadGateway, err.Error())
		default:
			writeJSON(w, http.StatusOK, result)
		}
	}
}

func splitQueryList(value string) []string {
	var items []string
	for _, item := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' }) {
		if !slices.Contains(items, item) {
			items = append(items, item)
		}
	}
	return items
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
