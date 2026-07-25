package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/wangning19940904/AgentMux/core"
	providerpkg "github.com/wangning19940904/AgentMux/provider"
)

const claudeDesktopTool = "claude-desktop"

type claude3PToggleRequest struct {
	Enabled    bool   `json:"enabled"`
	ProviderID string `json:"provider_id,omitempty"`
}

func (s *Server) handleClaude3PStatus(w http.ResponseWriter, r *http.Request) {
	p, _ := s.defaultClaudeDesktopProvider(r.Context())
	status, err := providerpkg.ClaudeDesktopStatus(p)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	attachClaude3PProvider(&status, p)
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleClaude3PToggle(w http.ResponseWriter, r *http.Request) {
	var req claude3PToggleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	if req.Enabled {
		p, err := s.claudeDesktopProvider(r.Context(), req.ProviderID)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if err := s.provider.Switch(r.Context(), p.ID, claudeDesktopTool); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		status, err := providerpkg.ClaudeDesktopStatus(p)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		attachClaude3PProvider(&status, p)
		writeJSON(w, http.StatusOK, status)
		return
	}

	p, _ := s.defaultClaudeDesktopProvider(r.Context())
	status, err := providerpkg.DisableClaudeDesktopConfig(p)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if s.st != nil {
		if err := s.st.ClearActiveProvider(r.Context(), claudeDesktopTool); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	}
	attachClaude3PProvider(&status, p)
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) claudeDesktopProvider(ctx context.Context, id string) (*core.Provider, error) {
	if s.provider == nil {
		return nil, fmt.Errorf("provider manager unavailable")
	}
	if id != "" {
		p, err := s.provider.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if p == nil {
			return nil, fmt.Errorf("provider %q not found", id)
		}
		return p, nil
	}
	p, err := s.defaultClaudeDesktopProvider(ctx)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, fmt.Errorf("no %s provider configured", claudeDesktopTool)
	}
	return p, nil
}

func (s *Server) defaultClaudeDesktopProvider(ctx context.Context) (*core.Provider, error) {
	if s.provider == nil {
		return nil, nil
	}
	active, err := s.provider.Active(ctx, claudeDesktopTool)
	if err != nil {
		return nil, err
	}
	if active != nil {
		return active, nil
	}
	// No explicit claude-desktop route: fall back to any provider that can
	// serve an Anthropic client (native anthropic or proxy-convertible).
	providers, err := s.provider.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range providers {
		return p, nil
	}
	return nil, nil
}

func attachClaude3PProvider(status *providerpkg.ClaudeDesktopConfigStatus, p *core.Provider) {
	if status == nil || p == nil {
		return
	}
	status.ProviderID = p.ID
	status.ProviderName = p.Name
}
