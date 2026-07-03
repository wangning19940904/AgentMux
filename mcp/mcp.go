// Package mcp implements AgentNexus MCP Registry: registration, orchestration
// and distribution of Model Context Protocol server configurations. The
// default "store" registry persists server definitions in the SQLite SSOT so
// they can be rendered into per-tool MCP config files.
package mcp

import (
	"context"
	"fmt"

	"github.com/agentnexus/agentnexus/core"
	"github.com/agentnexus/agentnexus/store"
)

func init() {
	core.RegisterMCPRegistry("store", func(cfg map[string]any) (core.MCPRegistry, error) {
		return &Registry{}, nil
	})
}

// Registry implements core.MCPRegistry backed by the store.
type Registry struct {
	st *store.Store
}

var _ core.MCPRegistry = (*Registry)(nil)

// New builds a store-backed MCP registry.
func New(st *store.Store) *Registry { return &Registry{st: st} }

// Name returns the registry id.
func (r *Registry) Name() string { return "store" }

// List returns all registered MCP servers.
func (r *Registry) List(ctx context.Context) ([]core.MCPServer, error) {
	return r.st.ListMCPServers(ctx)
}

// Upsert inserts or updates an MCP server definition.
func (r *Registry) Upsert(ctx context.Context, s *core.MCPServer) error {
	if s.Name == "" {
		return fmt.Errorf("mcp: server name is required")
	}
	if s.Transport == "" {
		s.Transport = "stdio"
	}
	return r.st.UpsertMCPServer(ctx, s)
}

// Delete removes a server by name.
func (r *Registry) Delete(ctx context.Context, name string) error {
	return r.st.DeleteMCPServer(ctx, name)
}
