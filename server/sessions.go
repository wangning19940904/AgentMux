package server

import (
	"encoding/json"
	"net/http"

	sessionstore "github.com/agentnexus/agentnexus/sessions"
)

func (s *Server) handleSessionsList(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	items, err := s.sessions.List(r.Context(), r.URL.Query().Get("provider"), r.URL.Query().Get("surface"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleSessionMessages(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	req := sessionstore.ResumeRequest{
		ProviderID: r.URL.Query().Get("provider"),
		Surface:    r.URL.Query().Get("surface"),
		SessionID:  r.URL.Query().Get("session_id"),
		SourcePath: r.URL.Query().Get("source_path"),
		ProjectDir: r.URL.Query().Get("project_dir"),
	}
	items, err := s.sessions.Messages(r.Context(), req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleSessionResume(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "sessions not enabled"})
		return
	}
	var req sessionstore.ResumeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	res, err := s.sessions.Resume(r.Context(), req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleSessionDelete(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "sessions not enabled"})
		return
	}
	req := sessionstore.ResumeRequest{
		ProviderID: r.URL.Query().Get("provider"),
		Surface:    r.URL.Query().Get("surface"),
		SessionID:  r.URL.Query().Get("session_id"),
		SourcePath: r.URL.Query().Get("source_path"),
	}
	if err := s.sessions.Delete(r.Context(), req); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
