package server

import (
	"context"
	"net/http"
	"strings"

	usagepkg "github.com/wangning19940904/AgentMux/usage"
)

// UsageSourceManager is implemented by the usage engine without exposing any
// source credential through the management API.
type UsageSourceManager interface {
	UsageSources(context.Context) []usagepkg.CursorUsageSourceStatus
	UsageSourceAction(context.Context, string, string) (usagepkg.CursorUsageActionResult, error)
}

func (s *Server) handleUsageSources(w http.ResponseWriter, r *http.Request) {
	if s.usageSources == nil {
		writeJSON(w, http.StatusOK, []usagepkg.CursorUsageSourceStatus{})
		return
	}
	writeJSON(w, http.StatusOK, s.usageSources.UsageSources(r.Context()))
}

func (s *Server) handleUsageSourceAction(w http.ResponseWriter, r *http.Request) {
	if s.usageSources == nil {
		writeErr(w, http.StatusServiceUnavailable, "usage source manager unavailable")
		return
	}
	source := strings.TrimSpace(r.PathValue("source"))
	action := strings.TrimSpace(r.PathValue("action"))
	result, err := s.usageSources.UsageSourceAction(r.Context(), source, action)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}
