package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/agentnexus/agentnexus/agent/sdkagent"
	"github.com/agentnexus/agentnexus/core"
	"github.com/agentnexus/agentnexus/framework"
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	kind := strings.TrimSpace(req.Kind)
	if kind == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "framework kind is required"})
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	kind := strings.TrimSpace(req.Kind)
	if kind == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "framework kind is required"})
		return
	}
	writeJSON(w, http.StatusOK, framework.CheckUpdate(r.Context(), kind))
}
