package server

import (
	"net/http"
	"strconv"
	"strings"
)

func (s *Server) handleFeedbackList(w http.ResponseWriter, r *http.Request) {
	if s.st == nil {
		writeErr(w, http.StatusServiceUnavailable, "feedback store is unavailable")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := s.st.ListChannelFeedback(r.Context(), strings.TrimSpace(r.URL.Query().Get("channel_id")), strings.TrimSpace(r.URL.Query().Get("task_id")), limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	counts := map[string]int{"positive": 0, "progress": 0, "negative": 0}
	for _, item := range items {
		counts[item.Semantic]++
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "counts": counts, "total": len(items)})
}

func (s *Server) handleFeedbackDetail(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r) {
		writeErr(w, http.StatusForbidden, "feedback details can be edited only from the local console")
		return
	}
	if s.st == nil {
		writeErr(w, http.StatusServiceUnavailable, "feedback store is unavailable")
		return
	}
	var req struct {
		ID      string `json:"id"`
		Reason  string `json:"reason"`
		Comment string `json:"comment"`
	}
	if !decodeJSONInto(w, r, &req) {
		return
	}
	req.ID = strings.TrimSpace(req.ID)
	req.Reason = strings.TrimSpace(req.Reason)
	req.Comment = strings.TrimSpace(req.Comment)
	if req.ID == "" {
		writeErr(w, http.StatusBadRequest, "feedback id is required")
		return
	}
	if len([]rune(req.Reason)) > 200 || len([]rune(req.Comment)) > 1000 {
		writeErr(w, http.StatusBadRequest, "feedback detail is too long")
		return
	}
	updated, err := s.st.UpdateChannelFeedbackDetail(r.Context(), req.ID, req.Reason, req.Comment)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !updated {
		writeErr(w, http.StatusNotFound, "feedback not found")
		return
	}
	writeOK(w)
}
