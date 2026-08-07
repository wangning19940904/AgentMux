package server

import (
	"net/http"
	"strconv"

	"github.com/wangning19940904/AgentMux/store"
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
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleProxyTraces(w http.ResponseWriter, r *http.Request) {
	if s.st == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	traces, err := s.st.QueryProxyTraces(
		r.Context(),
		r.URL.Query().Get("tool"),
		r.URL.Query().Get("session_id"),
		limit,
	)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, traces)
}

func (s *Server) handleProxyTakeover(w http.ResponseWriter, r *http.Request) {
	if s.proxySvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "local routing unavailable")
		return
	}
	var req struct {
		Tool    string `json:"tool"`
		Enabled bool   `json:"enabled"`
	}
	if !decodeJSONInto(w, r, &req) {
		return
	}
	if req.Tool == "" {
		writeErr(w, http.StatusBadRequest, "missing tool")
		return
	}
	var err error
	if req.Enabled {
		err = s.proxySvc.EnableTakeover(r.Context(), req.Tool)
	} else {
		err = s.proxySvc.DisableTakeover(r.Context(), req.Tool)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	status, err := s.proxySvc.Status(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleProxyConfigUpdate(w http.ResponseWriter, r *http.Request) {
	if s.proxySvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "local routing unavailable")
		return
	}
	var cfg store.ProxyToolConfig
	if !decodeJSONInto(w, r, &cfg) {
		return
	}
	if cfg.Tool == "" {
		writeErr(w, http.StatusBadRequest, "missing tool")
		return
	}
	if err := s.proxySvc.SetToolConfig(r.Context(), cfg); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	status, err := s.proxySvc.Status(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleProviderFailover(w http.ResponseWriter, r *http.Request) {
	if s.proxySvc == nil {
		writeErr(w, http.StatusServiceUnavailable, "local routing unavailable")
		return
	}
	var req struct {
		ID              string `json:"id"`
		InFailoverQueue bool   `json:"in_failover_queue"`
		SortIndex       int    `json:"sort_index"`
	}
	if !decodeJSONInto(w, r, &req) {
		return
	}
	if req.ID == "" {
		writeErr(w, http.StatusBadRequest, "missing id")
		return
	}
	if err := s.proxySvc.SetFailoverQueue(r.Context(), req.ID, req.InFailoverQueue, req.SortIndex); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w)
}
