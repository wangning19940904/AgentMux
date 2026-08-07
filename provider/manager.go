// Package provider implements LLM provider management ported from cc-switch:
// CRUD over the SQLite SSOT plus atomic switching that writes a tool's live
// config (e.g. ~/.claude/settings.json).
package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
)

// Manager implements core.ProviderManager backed by the store.
type Manager struct {
	st *store.Store
}

// NewManager builds a provider Manager.
func NewManager(st *store.Store) *Manager { return &Manager{st: st} }

var _ core.ProviderManager = (*Manager)(nil)

// List returns all providers.
func (m *Manager) List(ctx context.Context) ([]*core.Provider, error) {
	return m.st.ListProviders(ctx)
}

// Get returns one provider by id.
func (m *Manager) Get(ctx context.Context, id string) (*core.Provider, error) {
	return m.st.GetProvider(ctx, id)
}

// Upsert inserts or updates a provider.
func (m *Manager) Upsert(ctx context.Context, p *core.Provider) error {
	now := time.Now()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	p.UpdatedAt = now
	return m.st.UpsertProvider(ctx, p)
}

// Delete removes a provider.
func (m *Manager) Delete(ctx context.Context, id string) error {
	p, err := m.st.GetProvider(ctx, id)
	if err != nil {
		return err
	}
	if p != nil {
		if err := DeleteProviderAPIKey(p.APIKeyEnv); err != nil {
			return err
		}
	}
	return m.st.DeleteProvider(ctx, id)
}

// ActiveRoutes returns every active tool -> provider route.
func (m *Manager) ActiveRoutes(ctx context.Context) ([]core.ProviderRoute, error) {
	return m.st.ActiveProviderRoutes(ctx)
}

// Active returns the enabled provider for a tool.
func (m *Manager) Active(ctx context.Context, tool string) (*core.Provider, error) {
	id, ok, err := m.st.ActiveProviderID(ctx, tool)
	if err != nil || !ok {
		return nil, err
	}
	return m.st.GetProvider(ctx, id)
}

// Switch enables provider id for tool and writes the tool's live config.
// The previous active provider (if any) is consulted so its leftover keys
// are cleaned from the live file, mirroring cc-switch's switch_normal.
func (m *Manager) Switch(ctx context.Context, id, tool string) error {
	return m.switchProvider(ctx, id, tool, core.ProviderMeta{}, false)
}

func (m *Manager) SwitchRoute(ctx context.Context, route core.ProviderRoute) error {
	return m.switchProvider(ctx, route.ProviderID, route.Tool, route.Meta, true)
}

func (m *Manager) switchProvider(ctx context.Context, id, tool string, routeMeta core.ProviderMeta, writeRouteMeta bool) error {
	p, err := m.st.GetProvider(ctx, id)
	if err != nil {
		return err
	}
	if p == nil {
		return fmt.Errorf("provider %q not found", id)
	}
	effective := core.ProviderWithRouteMeta(p, routeMeta)
	if err := validateDirectSwitch(effective, tool); err != nil {
		return err
	}
	var prev *core.Provider
	if prevID, ok, err := m.st.ActiveProviderID(ctx, tool); err == nil && ok && prevID != id {
		prev, _ = m.st.GetProvider(ctx, prevID)
	}
	if err := m.writeLive(tool, effective, prev); err != nil {
		return fmt.Errorf("write live config: %w", err)
	}
	if writeRouteMeta {
		return m.st.SetActiveProviderRoute(ctx, core.ProviderRoute{Tool: tool, ProviderID: id, Meta: routeMeta})
	}
	return m.st.SetActiveProvider(ctx, tool, id)
}

// validateDirectSwitch rejects live-config writes that would break the tool:
// protocol-converting combos only work through local routing takeover.
func validateDirectSwitch(p *core.Provider, tool string) error {
	format := p.Meta.APIFormat
	switch liveConfigTool(tool) {
	case "claudecode", "claude-desktop":
		if format == "openai_chat" || format == "openai_responses" || format == "gemini_native" {
			return fmt.Errorf("provider %q speaks %s; enable local routing takeover for %s to use it", p.ID, format, tool)
		}
	case "codex":
		if format == "anthropic" {
			return fmt.Errorf("provider %q speaks anthropic; codex needs an openai_chat/openai_responses endpoint", p.ID)
		}
	}
	return nil
}

// writeLive is the live-config write step of a switch; the takeover layer
// overrides it when the tool is proxied (hot switch keeps live untouched).
func (m *Manager) writeLive(tool string, p, prev *core.Provider) error {
	return WriteLiveConfigForSwitch(tool, p, prev)
}

// Clear removes the active provider route for a tool.
func (m *Manager) Clear(ctx context.Context, tool string) error {
	if strings.TrimSpace(tool) == "" {
		return fmt.Errorf("missing tool")
	}
	return m.st.ClearActiveProvider(ctx, tool)
}

// ImportPreset creates a provider from a built-in preset id.
func (m *Manager) ImportPreset(ctx context.Context, presetID string) (*core.Provider, error) {
	p := PresetByID(presetID)
	if p == nil {
		return nil, fmt.Errorf("unknown preset %q", presetID)
	}
	if err := m.Upsert(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}
