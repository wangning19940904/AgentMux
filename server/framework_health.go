package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/wangning19940904/AgentMux/framework"
	"github.com/wangning19940904/AgentMux/framework/health"
)

// newFrameworkHealth builds the health service over this machine's store.
func newFrameworkHealth(s *Server) *health.Service {
	opts := health.Options{Log: s.log}
	if s.st != nil {
		opts.Store = s.st
	}
	return health.New(opts)
}

// FrameworkHealth is the health of the frameworks on this machine. The
// runtime subscribes its engine sink so real runs feed it.
func (s *Server) FrameworkHealth() *health.Service {
	return s.frameworkHealth
}

// frameworkHealthResponse is GET /api/v1/frameworks/health. Results are the
// latest test or check per framework and model, newest first, as before;
// records are the health of each framework; checks are the periodic check
// settings.
type frameworkHealthResponse struct {
	Results []framework.SmokeResult `json:"results"`
	Records []health.Record         `json:"records"`
	Checks  health.Settings         `json:"checks"`
}

// handleFrameworkHealth returns this machine's framework health, optionally
// narrowed to one framework.
func (s *Server) handleFrameworkHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	results, err := s.frameworkHealth.Results(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	records, err := s.frameworkHealth.Records(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	checks, err := s.frameworkHealth.Settings(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if kind := strings.TrimSpace(r.URL.Query().Get("kind")); kind != "" {
		results = filterByKind(results, func(result framework.SmokeResult) string { return result.Kind }, kind)
		records = filterByKind(records, func(record health.Record) string { return record.Kind }, kind)
		if len(records) == 0 {
			record, err := s.frameworkHealth.Status(ctx, kind)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			records = []health.Record{record}
		}
	}
	if results == nil {
		results = []framework.SmokeResult{}
	}
	if records == nil {
		records = []health.Record{}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, frameworkHealthResponse{Results: results, Records: records, Checks: checks})
}

func filterByKind[T any](items []T, kindOf func(T) string, kind string) []T {
	var kept []T
	for _, item := range items {
		if kindOf(item) == kind {
			kept = append(kept, item)
		}
	}
	return kept
}

// handleFrameworkHealthRecheck checks one framework now. Concurrent calls
// share one check, a check is not repeated within 30 seconds, and a usage
// limit is not checked before its known reset: those return the record as
// it is.
func (s *Server) handleFrameworkHealthRecheck(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind string `json:"kind"`
	}
	if !decodeJSONInto(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Kind) == "" {
		writeErr(w, http.StatusBadRequest, "framework kind is required")
		return
	}
	record, err := s.frameworkHealth.Recheck(r.Context(), req.Kind)
	if errors.Is(err, framework.ErrUnknownFramework) {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, record)
}

func (s *Server) handleFrameworkHealthSettingsGet(w http.ResponseWriter, r *http.Request) {
	settings, err := s.frameworkHealth.Settings(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, settings)
}

// handleFrameworkHealthSettingsPut turns periodic checks on or off and sets
// their interval for this machine.
func (s *Server) handleFrameworkHealthSettingsPut(w http.ResponseWriter, r *http.Request) {
	var req health.Settings
	if !decodeJSONInto(w, r, &req) {
		return
	}
	if req.IntervalMinutes != 0 && (req.IntervalMinutes < health.MinIntervalMinutes || req.IntervalMinutes > health.MaxIntervalMinutes) {
		writeErr(w, http.StatusBadRequest, "interval_minutes must be between 5 and 1440")
		return
	}
	settings, err := s.frameworkHealth.SetSettings(r.Context(), req)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.log.Info("framework health checks updated", "enabled", settings.Enabled, "interval_minutes", settings.IntervalMinutes)
	writeJSON(w, http.StatusOK, settings)
}
