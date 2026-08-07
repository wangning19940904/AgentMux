package server

import (
	"net/http"
	"strings"

	"github.com/wangning19940904/AgentMux/agent/sdkagent"
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
	Kind   string `json:"kind"`
	Action string `json:"action"`
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
	var res framework.InstallResult
	switch action {
	case "install":
		res = framework.Install(r.Context(), kind)
	case "update":
		res = framework.Update(r.Context(), kind)
	default:
		res = framework.InstallResult{Kind: kind, Action: action, Error: "action must be install or update"}
	}
	if !res.OK {
		// Surface the install log/error but keep a 200 envelope so the client
		// can render the log; the ok flag conveys success.
		writeJSON(w, http.StatusOK, res)
		return
	}

	// Register the freshly-installed SDK framework so it becomes routable in the
	// current process without requiring a daemon restart. Register() is
	// idempotent and only picks up frameworks detected as installed.
	sdkagent.Register()

	writeJSON(w, http.StatusOK, res)
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
