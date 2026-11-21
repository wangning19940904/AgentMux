package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/framework"
	"github.com/wangning19940904/AgentMux/framework/health"
)

// turnSchedulerSetting keeps this machine's turn scheduling settings.
const turnSchedulerSetting = "turn_scheduler_v1"

// newTurnScheduler builds the scheduler every agent turn on this machine is
// admitted by (docs/architecture/usage-aware-scheduling.md), with the saved
// settings. It is off until an administrator turns it on.
func newTurnScheduler(s *Server) *core.TurnScheduler {
	settings := core.DefaultTurnSchedulerSettings()
	if s.st != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		raw, ok, err := s.st.GetSetting(ctx, turnSchedulerSetting)
		cancel()
		switch {
		case err != nil:
			s.log.Warn("load turn scheduler settings", "err", err)
		case ok:
			if err := json.Unmarshal([]byte(raw), &settings); err != nil {
				s.log.Warn("decode turn scheduler settings", "err", err)
				settings = core.DefaultTurnSchedulerSettings()
			}
		}
	}
	return core.NewTurnScheduler(core.TurnSchedulerOptions{
		Settings: settings, Limits: s.frameworkUsageLimit, Classify: classifyUsageLimit, Log: s.log,
	})
}

// TurnScheduler is this machine's turn scheduler; the runtime installs it in
// the engine.
func (s *Server) TurnScheduler() *core.TurnScheduler { return s.turnScheduler }

// frameworkUsageLimit reports a framework's usage limit as its health
// record knows it.
func (s *Server) frameworkUsageLimit(kind string) (time.Time, bool) {
	if s.frameworkHealth == nil {
		return time.Time{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	record, err := s.frameworkHealth.Status(ctx, kind)
	if err != nil || record.State != health.StateUsageLimit || record.ResetsAt == nil {
		return time.Time{}, false
	}
	return *record.ResetsAt, true
}

// classifyUsageLimit reads a framework's usage-limit message and the reset it
// names.
func classifyUsageLimit(text string, now time.Time) (time.Time, bool) {
	if !framework.IsUsageLimit(text) {
		return time.Time{}, false
	}
	resetsAt, _ := framework.UsageLimitReset(text, now)
	return resetsAt, true
}

// withRequestedTurnClass applies an invocation's X-AgentMux-Turn-Class. A
// caller can only lower its turn from the default, interactive, so the
// header needs no permission of its own.
func withRequestedTurnClass(r *http.Request) *http.Request {
	class, ok := core.ParseTurnClass(r.Header.Get(core.TurnClassHeader))
	if !ok || class <= core.TurnInteractive {
		return r
	}
	return r.WithContext(core.WithTurnClass(r.Context(), class))
}

// handleTurnScheduler is GET /api/v1/frameworks/scheduling: the settings and,
// per framework account in use, its running and waiting turns and any hold.
func (s *Server) handleTurnScheduler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, s.turnScheduler.Snapshot())
}

// handleTurnSchedulerSettingsPut saves and applies this machine's settings.
func (s *Server) handleTurnSchedulerSettingsPut(w http.ResponseWriter, r *http.Request) {
	var req core.TurnSchedulerSettings
	if !decodeJSONInto(w, r, &req) {
		return
	}
	if req.MaxConcurrent < 0 || req.MaxConcurrent > 64 {
		writeErr(w, http.StatusBadRequest, "max_concurrent must be between 0 and 64")
		return
	}
	if req.InteractiveHeadroom < 0 || req.InteractiveHeadroom > 16 {
		writeErr(w, http.StatusBadRequest, "interactive_headroom must be between 0 and 16")
		return
	}
	req = req.Normalized()
	if s.st != nil {
		raw, err := json.Marshal(req)
		if err == nil {
			err = s.st.SetSetting(r.Context(), turnSchedulerSetting, string(raw))
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	settings := s.turnScheduler.Configure(req)
	s.log.Info("turn scheduler updated", "enabled", settings.Enabled,
		"max_concurrent", settings.MaxConcurrent, "interactive_headroom", settings.InteractiveHeadroom)
	writeJSON(w, http.StatusOK, settings)
}
