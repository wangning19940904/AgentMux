package server

import (
	"net/http"
	"strings"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/framework"
	toolpkg "github.com/wangning19940904/AgentMux/tools"
)

type toolsResponse struct {
	CLI         []toolpkg.CLIStatus    `json:"cli"`
	Bundles     []toolpkg.BundleStatus `json:"bundles"`
	Frameworks  []frameworkView        `json:"frameworks"`
	Skills      []core.Skill           `json:"skills"`
	MCP         []core.MCPServer       `json:"mcp"`
	Marketplace any                    `json:"marketplace"`
}

func (s *Server) handleTools(w http.ResponseWriter, r *http.Request) {
	var resp toolsResponse
	resp.CLI = toolpkg.DetectCLIs(r.Context())
	resp.Bundles = toolpkg.DetectBundles(r.Context())
	statuses := framework.DetectAll()
	resp.Frameworks = make([]frameworkView, 0, len(statuses))
	for _, st := range statuses {
		resp.Frameworks = append(resp.Frameworks, frameworkView{
			Status:     st,
			Registered: core.HasAgent(st.Spec.Kind),
		})
	}
	if s.skills != nil {
		if items, err := s.skills.List(r.Context()); err == nil {
			resp.Skills = items
		}
	}
	if s.mcp != nil {
		if items, err := s.mcp.List(r.Context()); err == nil {
			resp.MCP = items
		}
	}
	if mgr, ok := s.skills.(marketplaceSkillManager); ok && mgr != nil {
		if items, err := mgr.Marketplace(r.Context(), "", "", ""); err == nil {
			resp.Marketplace = items
		}
	}
	if resp.Marketplace == nil {
		resp.Marketplace = []any{}
	}
	writeJSON(w, http.StatusOK, resp)
}

type cliInstallRequest struct {
	ID                  string `json:"id"`
	Action              string `json:"action"`
	AcknowledgeInternal bool   `json:"acknowledge_internal"`
}

func (s *Server) handleCLIInstall(w http.ResponseWriter, r *http.Request) {
	var req cliInstallRequest
	if !decodeJSONInto(w, r, &req) {
		return
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		writeErr(w, http.StatusBadRequest, "cli id is required")
		return
	}
	res := toolpkg.InstallCLIWithOptions(r.Context(), id, strings.TrimSpace(req.Action), toolpkg.CLIInstallOptions{AcknowledgeInternal: req.AcknowledgeInternal})
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleCLIInstallStream(w http.ResponseWriter, r *http.Request) {
	var req cliInstallRequest
	if !decodeJSONInto(w, r, &req) {
		return
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		writeErr(w, http.StatusBadRequest, "cli id is required")
		return
	}
	streamInstall(w, r, func(report func(string, string, int)) any {
		return toolpkg.InstallCLIWithProgressOptions(r.Context(), id, strings.TrimSpace(req.Action), toolpkg.CLIInstallOptions{AcknowledgeInternal: req.AcknowledgeInternal}, toolpkg.ProgressFunc(report))
	})
}

type bundleInstallRequest struct {
	ID                  string `json:"id"`
	AcknowledgeInternal bool   `json:"acknowledge_internal"`
}

func (s *Server) handleBundleInstall(w http.ResponseWriter, r *http.Request) {
	var req bundleInstallRequest
	if !decodeJSONInto(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.ID) == "" {
		writeErr(w, http.StatusBadRequest, "bundle id is required")
		return
	}
	res := toolpkg.InstallBundle(r.Context(), req.ID, toolpkg.BundleInstallOptions{AcknowledgeInternal: req.AcknowledgeInternal})
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleBundleInstallStream(w http.ResponseWriter, r *http.Request) {
	var req bundleInstallRequest
	if !decodeJSONInto(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.ID) == "" {
		writeErr(w, http.StatusBadRequest, "bundle id is required")
		return
	}
	streamInstall(w, r, func(report func(string, string, int)) any {
		return toolpkg.InstallBundleWithProgress(
			r.Context(), req.ID,
			toolpkg.BundleInstallOptions{AcknowledgeInternal: req.AcknowledgeInternal},
			toolpkg.ProgressFunc(report),
		)
	})
}

func (s *Server) handleCLICheck(w http.ResponseWriter, r *http.Request) {
	var req cliInstallRequest
	if !decodeJSONInto(w, r, &req) {
		return
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		writeErr(w, http.StatusBadRequest, "cli id is required")
		return
	}
	res := toolpkg.CheckCLIUpdate(r.Context(), id)
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleCLISkillSync(w http.ResponseWriter, r *http.Request) {
	var req cliInstallRequest
	if !decodeJSONInto(w, r, &req) {
		return
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		writeErr(w, http.StatusBadRequest, "cli id is required")
		return
	}
	res := toolpkg.SyncCLILinkedSkills(r.Context(), id)
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleCLISkillSyncStream(w http.ResponseWriter, r *http.Request) {
	var req cliInstallRequest
	if !decodeJSONInto(w, r, &req) {
		return
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		writeErr(w, http.StatusBadRequest, "cli id is required")
		return
	}
	streamInstall(w, r, func(report func(string, string, int)) any {
		return toolpkg.SyncCLILinkedSkillsWithProgress(r.Context(), id, toolpkg.ProgressFunc(report))
	})
}

func (s *Server) handleCLIAuthStatus(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		writeErr(w, http.StatusBadRequest, "cli id is required")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, toolpkg.CheckCLIAuth(r.Context(), id))
}

func (s *Server) handleCLIAuthLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID    string `json:"id"`
		Force bool   `json:"force"`
	}
	if !decodeJSONInto(w, r, &req) {
		return
	}
	result, err := toolpkg.StartCLIAuth(req.ID, req.Force)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleCLIAuthLoginStatus(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sessionID == "" {
		writeErr(w, http.StatusBadRequest, "login session id is required")
		return
	}
	result, ok := toolpkg.GetCLIAuthSession(sessionID)
	if !ok {
		writeErr(w, http.StatusNotFound, "CLI login session was not found")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleCLIAuthLoginCancel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SessionID string `json:"session_id"`
	}
	if !decodeJSONInto(w, r, &req) {
		return
	}
	if err := toolpkg.CancelCLIAuthSession(req.SessionID); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeOK(w)
}
