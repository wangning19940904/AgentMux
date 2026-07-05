// Package provider implements LLM provider management ported from cc-switch:
// CRUD over the SQLite SSOT plus atomic switching that writes a tool's live
// config (e.g. ~/.claude/settings.json).
package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/agentnexus/agentnexus/core"
	"github.com/agentnexus/agentnexus/store"
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
func (m *Manager) Switch(ctx context.Context, id, tool string) error {
	p, err := m.st.GetProvider(ctx, id)
	if err != nil {
		return err
	}
	if p == nil {
		return fmt.Errorf("provider %q not found", id)
	}
	supported := false
	for _, t := range p.Tools {
		if t == tool {
			supported = true
			break
		}
	}
	if !supported {
		return fmt.Errorf("provider %q does not support tool %q", id, tool)
	}
	if err := WriteLiveConfig(tool, p); err != nil {
		return fmt.Errorf("write live config: %w", err)
	}
	return m.st.SetActiveProvider(ctx, tool, id)
}

// Clear removes the active provider route for a tool.
func (m *Manager) Clear(ctx context.Context, tool string) error {
	if strings.TrimSpace(tool) == "" {
		return fmt.Errorf("missing tool")
	}
	return m.st.ClearActiveProvider(ctx, tool)
}

// providerJSON is the on-disk JSON shape (also used by presets import).
type providerJSON struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Category       string            `json:"category,omitempty"`
	BaseURL        string            `json:"base_url"`
	Model          string            `json:"model"`
	Tools          []string          `json:"tools"`
	Extra          map[string]string `json:"extra,omitempty"`
	SettingsConfig map[string]any    `json:"settings_config,omitempty"`
	Meta           core.ProviderMeta `json:"meta,omitempty"`
}

// MarshalProvider renders a provider as compact JSON (used by export).
func MarshalProvider(p *core.Provider) ([]byte, error) {
	return json.Marshal(providerJSON{
		ID: p.ID, Name: p.Name, Category: p.Category, BaseURL: p.BaseURL,
		Model: p.Model, Tools: p.Tools, Extra: p.Extra,
		SettingsConfig: p.SettingsConfig, Meta: p.Meta,
	})
}

// JoinTools serializes a tools slice for storage.
func JoinTools(tools []string) string { return strings.Join(tools, ",") }

// SplitTools parses a stored tools string.
func SplitTools(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
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

// BuildUpstreams converts a provider chain into proxy upstreams.
func BuildUpstreams(providers []*core.Provider) []*Upstream {
	out := make([]*Upstream, 0, len(providers))
	for _, p := range providers {
		out = append(out, &Upstream{
			ProviderID: p.ID,
			BaseURL:    p.BaseURL,
			APIKeyEnv:  p.APIKeyEnv,
		})
	}
	return out
}
