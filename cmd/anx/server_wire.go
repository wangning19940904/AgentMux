package main

import (
	"context"
	"time"

	"github.com/agentnexus/agentnexus/config"
	"github.com/agentnexus/agentnexus/guard"
	"github.com/agentnexus/agentnexus/mcp"
	"github.com/agentnexus/agentnexus/memory"
	"github.com/agentnexus/agentnexus/provider"
	"github.com/agentnexus/agentnexus/server"
	"github.com/agentnexus/agentnexus/skills"
	"github.com/agentnexus/agentnexus/store"
	"github.com/agentnexus/agentnexus/usage"

	"github.com/agentnexus/agentnexus/core"
)

// newServer wires the management server with provider + usage backends plus
// the Memory, Skills, MCP Registry and Guard modules.
func newServer(cfg *config.Config, st *store.Store) *server.Server {
	pm := provider.NewManager(st)
	eng := usage.NewEngine(cfg, st, logger)
	reporter := func(ctx context.Context, period string, since time.Time) (any, error) {
		return eng.Report(ctx, period, since)
	}
	srv := server.New(cfg, logger, st, pm, reporter)
	srv.SetPresets(provider.Presets())
	srv.SetModules(
		memory.New(st),
		skills.New(),
		mcp.New(st),
		guard.New(st, core.GuardAsk),
	)
	return srv
}
