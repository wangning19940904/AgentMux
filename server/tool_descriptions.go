package server

import (
	"context"
	"errors"
	"net/http"
	"os"
	"slices"
	"strings"
	"unicode/utf8"

	toolpkg "github.com/wangning19940904/AgentMux/tools"
)

type editableSkillManager interface {
	SetDescription(context.Context, string, string) error
}

func (s *Server) handleToolDescription(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind        string  `json:"kind"`
		ID          string  `json:"id"`
		Description *string `json:"description"`
	}
	if !decodeJSONInto(w, r, &req) {
		return
	}
	req.ID = strings.TrimSpace(req.ID)
	if req.ID == "" || req.Description == nil {
		writeErr(w, http.StatusBadRequest, "tool id and description are required")
		return
	}
	description := strings.TrimSpace(*req.Description)
	if utf8.RuneCountInString(description) > 4000 {
		writeErr(w, http.StatusBadRequest, "description must be at most 4000 characters")
		return
	}
	var err error
	switch req.Kind {
	case "cli":
		if _, ok := toolpkg.LookupCLI(req.ID); !ok {
			writeErr(w, http.StatusNotFound, "unknown CLI")
			return
		}
		if s.st == nil {
			serviceUnavailable(w, "tool descriptions")
			return
		}
		err = toolpkg.SetCLIDescription(r.Context(), s.st, req.ID, description)
	case "skill":
		mgr, ok := s.skills.(editableSkillManager)
		if !ok {
			serviceUnavailable(w, "skill descriptions")
			return
		}
		err = mgr.SetDescription(r.Context(), req.ID, description)
	default:
		writeErr(w, http.StatusBadRequest, "kind must be cli or skill")
		return
	}
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, os.ErrNotExist) {
			status = http.StatusNotFound
		}
		writeErr(w, status, err.Error())
		return
	}
	// Refresh only agents using this tool. Existing turns keep their generation.
	warning := ""
	if err := s.refreshToolAgents(r.Context(), req.Kind, req.ID); err != nil {
		warning = err.Error()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "description": description, "warning": warning})
}

func (s *Server) refreshToolAgents(ctx context.Context, kind, id string) error {
	if s.st == nil || s.connect == nil {
		return nil
	}
	agents, err := s.st.ListAgentInstances(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, agent := range agents {
		selected := agent.CLIs
		if kind == "skill" {
			selected = agent.Skills
		}
		if slices.Contains(selected, id) {
			failures = append(failures, s.connect.RestartChannelsForAgent(ctx, agent.ID))
		}
	}
	return errors.Join(failures...)
}
