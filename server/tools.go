package server

import (
	"net/http"
	"strings"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/framework"
	toolpkg "github.com/wangning19940904/AgentMux/tools"
)

type toolsResponse struct {
	CLI         []toolpkg.CLIStatus `json:"cli"`
	Frameworks  []frameworkView     `json:"frameworks"`
	Skills      []core.Skill        `json:"skills"`
	MCP         []core.MCPServer    `json:"mcp"`
	Marketplace any                 `json:"marketplace"`
}

func (s *Server) handleTools(w http.ResponseWriter, r *http.Request) {
	var resp toolsResponse
	resp.CLI = toolpkg.DetectCLIs(r.Context())
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
	ID     string `json:"id"`
	Action string `json:"action"`
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
	res := toolpkg.InstallCLI(r.Context(), id, strings.TrimSpace(req.Action))
	writeJSON(w, http.StatusOK, res)
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
