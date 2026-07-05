package server

import (
	"encoding/json"
	"net/http"

	"github.com/agentnexus/agentnexus/store"
)

// proxy.go exposes the local routing (takeover + failover) REST API backed by
// provider.Service.

func (s *Server) handleProxyStatus(w http.ResponseWriter, r *http.Request) {
	if s.proxySvc == nil {
		writeJSON(w, http.StatusOK, map[string]any{"running": false, "tools": []any{}})
		return
	}
	status, err := s.proxySvc.Status(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleProxyTakeover(w http.ResponseWriter, r *http.Request) {
	if s.proxySvc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "local routing unavailable"})
		return
	}
	var req struct {
		Tool    string `json:"tool"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if req.Tool == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing tool"})
		return
	}
	var err error
	if req.Enabled {
		err = s.proxySvc.EnableTakeover(r.Context(), req.Tool)
	} else {
		err = s.proxySvc.DisableTakeover(r.Context(), req.Tool)
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	status, err := s.proxySvc.Status(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleProxyConfigUpdate(w http.ResponseWriter, r *http.Request) {
	if s.proxySvc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "local routing unavailable"})
		return
	}
	var cfg store.ProxyToolConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if cfg.Tool == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing tool"})
		return
	}
	if err := s.proxySvc.SetToolConfig(r.Context(), cfg); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	status, err := s.proxySvc.Status(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleProviderFailover(w http.ResponseWriter, r *http.Request) {
	if s.proxySvc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "local routing unavailable"})
		return
	}
	var req struct {
		ID              string `json:"id"`
		InFailoverQueue bool   `json:"in_failover_queue"`
		SortIndex       int    `json:"sort_index"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if req.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id"})
		return
	}
	if err := s.proxySvc.SetFailoverQueue(r.Context(), req.ID, req.InFailoverQueue, req.SortIndex); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
