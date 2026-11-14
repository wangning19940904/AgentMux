package server

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/framework"
)

// frameworkView is one framework's catalog entry plus its live install state
// and whether it is currently registered as a routable runtime.
type frameworkView struct {
	framework.Status
	Registered bool `json:"registered"`
	// Commands previews what install/update will run on this machine after
	// its install settings, so the Console can ask before a custom command.
	Commands *frameworkCommands `json:"commands,omitempty"`
}

type frameworkCommands struct {
	Install *framework.CommandPlan `json:"install,omitempty"`
	Update  *framework.CommandPlan `json:"update,omitempty"`
}

// frameworksResponse is the payload for GET /api/v1/frameworks.
type frameworksResponse struct {
	Prereqs    framework.Prereqs `json:"prereqs"`
	Frameworks []frameworkView   `json:"frameworks"`
}

type frameworkRuntimeSettingsResponse struct {
	Kind         string                           `json:"kind"`
	Defaults     core.RuntimeSettings             `json:"defaults"`
	Capabilities core.RuntimeSettingsCapabilities `json:"capabilities"`
}

func (s *Server) handleFrameworksList(w http.ResponseWriter, r *http.Request) {
	statuses := framework.DetectAll()
	settings := s.frameworkInstallSettings(r.Context())
	views := make([]frameworkView, 0, len(statuses))
	for _, st := range statuses {
		views = append(views, frameworkView{
			Status:     st,
			Registered: core.HasAgent(st.Spec.Kind),
			Commands:   plannedFrameworkCommands(st, settings),
		})
	}
	writeJSON(w, http.StatusOK, frameworksResponse{
		Prereqs:    framework.DetectPrereqs(),
		Frameworks: views,
	})
}

// handleFrameworkRuntimeSettings discovers the model and runtime-setting
// catalogue exposed by the selected CLI's currently signed-in account. It is
// deliberately independent of Provider routing: the Console uses it when an
// Agent runs directly through the CLI's local login state.
func (s *Server) handleFrameworkRuntimeSettings(w http.ResponseWriter, r *http.Request) {
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	if kind == "" {
		writeErr(w, http.StatusBadRequest, "framework kind is required")
		return
	}
	if !core.HasAgent(kind) {
		writeErr(w, http.StatusNotFound, "framework runtime is not registered")
		return
	}
	workDir := strings.TrimSpace(r.URL.Query().Get("work_dir"))
	if workDir == "" {
		workDir, _ = os.Getwd()
	}
	agent, err := core.CreateAgent(kind, map[string]any{})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	defer func() { _ = agent.Stop(r.Context()) }()
	cataloger, ok := agent.(core.RuntimeSettingsCataloger)
	if !ok {
		writeJSON(w, http.StatusOK, frameworkRuntimeSettingsResponse{Kind: kind})
		return
	}
	defaults, capabilities, err := cataloger.RuntimeSettingsCatalog(r.Context(), workDir)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "discover runtime settings: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, frameworkRuntimeSettingsResponse{
		Kind:         kind,
		Defaults:     defaults,
		Capabilities: capabilities,
	})
}

type frameworkTestRequest struct {
	Kind            string `json:"kind"`
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	ServiceTier     string `json:"service_tier,omitempty"`
	// AgentID lends that Agent's environment. Without Kind, the Agent is
	// tested as saved: its runtime and default model settings.
	AgentID        string `json:"agent_id,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

// handleFrameworkTest runs a framework smoke test: it asks the framework,
// through the same runtime Agents use, to reply "ok" with the chosen model.
// It spends one short model call, so it only runs when a user asks for it.
func (s *Server) handleFrameworkTest(w http.ResponseWriter, r *http.Request) {
	var req frameworkTestRequest
	if !decodeJSONInto(w, r, &req) {
		return
	}
	opts := framework.SmokeOptions{
		Kind:            strings.TrimSpace(req.Kind),
		Model:           strings.TrimSpace(req.Model),
		ReasoningEffort: strings.TrimSpace(req.ReasoningEffort),
		ServiceTier:     strings.TrimSpace(req.ServiceTier),
		Timeout:         time.Duration(req.TimeoutSeconds) * time.Second,
	}
	if id := strings.TrimSpace(req.AgentID); id != "" {
		agent, err := s.st.GetAgentInstance(r.Context(), id)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if agent == nil {
			writeErr(w, http.StatusNotFound, "agent not found")
			return
		}
		// Without a kind, test the Agent as saved. With one, test exactly the
		// requested settings (such as an unsaved form) in the Agent's environment.
		if opts.Kind == "" {
			opts.Kind = agent.RuntimeID
			opts.Model = agent.DefaultModel
			opts.ReasoningEffort = agent.DefaultReasoningEffort
			opts.ServiceTier = agent.DefaultServiceTier
		}
		opts.Env = agent.Env
	}
	if opts.Kind == "" {
		writeErr(w, http.StatusBadRequest, "framework kind is required")
		return
	}
	result, err := framework.SmokeTest(r.Context(), opts)
	if errors.Is(err, framework.ErrUnknownFramework) {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.recordFrameworkHealth(r.Context(), result); err != nil {
		s.log.Warn("record framework health", "kind", result.Kind, "err", err)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, result)
}

type frameworkInstallRequest struct {
	Kind                string `json:"kind"`
	Action              string `json:"action"`
	AcknowledgeInternal bool   `json:"acknowledge_internal"`
	// ConfirmedCommand echoes the custom command the user approved.
	ConfirmedCommand string `json:"confirmed_command,omitempty"`
}

func (s *Server) frameworkInstallOptions(r *http.Request, req frameworkInstallRequest) framework.InstallOptions {
	return framework.InstallOptions{
		AcknowledgeInternal: req.AcknowledgeInternal,
		Settings:            s.frameworkInstallSettings(r.Context()),
		ConfirmedCommand:    strings.TrimSpace(req.ConfirmedCommand),
		Forwarded:           requestForwardedFromPeer(r),
	}
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
	res := s.runFrameworkInstall(r.Context(), kind, action, s.frameworkInstallOptions(r, req), nil)
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
	options := s.frameworkInstallOptions(r, req)
	streamInstall(w, r, func(report func(string, string, int)) any {
		return s.runFrameworkInstall(r.Context(), kind, action, options, framework.ProgressFunc(report))
	})
}

func (s *Server) runFrameworkInstall(ctx context.Context, kind, action string, options framework.InstallOptions, progress framework.ProgressFunc) framework.InstallResult {
	var res framework.InstallResult
	switch action {
	case "install":
		res = framework.InstallWithProgressOptions(ctx, kind, options, progress)
	case "update":
		res = framework.UpdateWithProgressOptions(ctx, kind, options, progress)
	case "uninstall":
		res = framework.UninstallWithProgress(ctx, kind, progress)
	default:
		return framework.InstallResult{Kind: kind, Action: action, Error: "action must be install, update, or uninstall"}
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
	writeJSON(w, http.StatusOK, framework.CheckUpdateWithSettings(r.Context(), kind, s.frameworkInstallSettings(r.Context())))
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

func (s *Server) handleFrameworkLogout(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind string `json:"kind"`
	}
	if !decodeJSONInto(w, r, &req) {
		return
	}
	status, err := framework.Logout(r.Context(), req.Kind)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, status)
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
