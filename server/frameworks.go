package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/framework"
)

// frameworkView is one framework's catalog entry plus its live install state
// and whether it is currently registered as a routable runtime.
type frameworkView struct {
	framework.Status
	Registered bool `json:"registered"`
}

// frameworksResponse is the payload for GET /api/v1/frameworks.
type frameworksResponse struct {
	Prereqs    framework.Prereqs `json:"prereqs"`
	Frameworks []frameworkView   `json:"frameworks"`
}

func (s *Server) handleFrameworksList(w http.ResponseWriter, r *http.Request) {
	statuses := framework.DetectAll()
	views := make([]frameworkView, 0, len(statuses))
	for _, st := range statuses {
		views = append(views, frameworkView{
			Status:     st,
			Registered: core.HasAgent(st.Spec.Kind),
		})
	}
	writeJSON(w, http.StatusOK, frameworksResponse{
		Prereqs:    framework.DetectPrereqs(),
		Frameworks: views,
	})
}

type frameworkInstallRequest struct {
	Kind                string `json:"kind"`
	Action              string `json:"action"`
	AcknowledgeInternal bool   `json:"acknowledge_internal"`
}

func (s *Server) handleFrameworkInstall(w http.ResponseWriter, r *http.Request) {
	var req frameworkInstallRequest
	if !decodeJSONInto(w, r, &req) {
		return
	}
	kind := strings.TrimSpace(req.Kind)
	if kind == "" {
		writeErr(w, http.StatusBadRequest, "framework kind is required")
		return
	}

	action := strings.TrimSpace(req.Action)
	if action == "" {
		action = "install"
	}
	res := s.runFrameworkInstall(r.Context(), kind, action, req.AcknowledgeInternal, nil)
	if !res.OK {
		// Surface the install log/error but keep a 200 envelope so the client
		// can render the log; the ok flag conveys success.
		writeJSON(w, http.StatusOK, res)
		return
	}

	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleFrameworkInstallStream(w http.ResponseWriter, r *http.Request) {
	var req frameworkInstallRequest
	if !decodeJSONInto(w, r, &req) {
		return
	}
	kind := strings.TrimSpace(req.Kind)
	if kind == "" {
		writeErr(w, http.StatusBadRequest, "framework kind is required")
		return
	}
	action := strings.TrimSpace(req.Action)
	if action == "" {
		action = "install"
	}
	streamInstall(w, r, func(report func(string, string, int)) any {
		return s.runFrameworkInstall(r.Context(), kind, action, req.AcknowledgeInternal, framework.ProgressFunc(report))
	})
}

func (s *Server) runFrameworkInstall(ctx context.Context, kind, action string, acknowledgeInternal bool, progress framework.ProgressFunc) framework.InstallResult {
	var res framework.InstallResult
	switch action {
	case "install":
		res = framework.InstallWithProgressOptions(ctx, kind, framework.InstallOptions{AcknowledgeInternal: acknowledgeInternal}, progress)
	case "update":
		res = framework.UpdateWithProgress(ctx, kind, progress)
	default:
		return framework.InstallResult{Kind: kind, Action: action, Error: "action must be install or update"}
	}
	return res
}

func (s *Server) handleFrameworkCheck(w http.ResponseWriter, r *http.Request) {
	var req frameworkInstallRequest
	if !decodeJSONInto(w, r, &req) {
		return
	}
	kind := strings.TrimSpace(req.Kind)
	if kind == "" {
		writeErr(w, http.StatusBadRequest, "framework kind is required")
		return
	}
	writeJSON(w, http.StatusOK, framework.CheckUpdate(r.Context(), kind))
}

func (s *Server) handleFrameworkAuthStatus(w http.ResponseWriter, r *http.Request) {
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	if kind == "" {
		writeErr(w, http.StatusBadRequest, "framework kind is required")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, framework.CheckAuth(r.Context(), kind))
}

func (s *Server) handleFrameworkLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind string `json:"kind"`
	}
	if !decodeJSONInto(w, r, &req) {
		return
	}
	result, err := framework.StartLogin(req.Kind)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleFrameworkLoginStatus(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sessionID == "" {
		writeErr(w, http.StatusBadRequest, "login session id is required")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, framework.GetLoginSession(sessionID))
}

func (s *Server) handleFrameworkLoginCancel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID string `json:"session_id"`
	}
	if !decodeJSONInto(w, r, &req) {
		return
	}
	if err := framework.CancelLogin(req.SessionID); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeOK(w)
}

func (s *Server) handleFrameworkLoginComplete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID string `json:"session_id"`
		Code      string `json:"code"`
	}
	if !decodeJSONInto(w, r, &req) {
		return
	}
	if err := framework.CompleteLogin(req.SessionID, req.Code); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeOK(w)
}
