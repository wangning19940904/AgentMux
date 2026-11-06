package server

import (
	"net/http"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/internal/hostmetrics"
)

// systemMetricsResponse is one machine's health for the Console's machine
// status page. The Console reads it from every machine through the fleet
// query endpoint, so it must stay cheap and read-only.
type systemMetricsResponse struct {
	hostmetrics.Snapshot
	AgentMuxVersion string `json:"agentmux_version"`
	// Sessions is absent when this process has no channel engine.
	Sessions *core.SessionActivity `json:"sessions,omitempty"`
}

func (s *Server) handleSystemMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	response := systemMetricsResponse{
		Snapshot:        s.hostMetrics.Snapshot(r.Context()),
		AgentMuxVersion: s.version,
	}
	if reporter, ok := s.sender.(core.SessionActivityReporter); ok {
		activity := reporter.SessionActivity()
		response.Sessions = &activity
	}
	writeJSON(w, http.StatusOK, response)
}
