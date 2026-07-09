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
	"github.com/agentnexus/agentnexus/workspace"

	"github.com/agentnexus/agentnexus/core"
)

// newServer wires the management server with provider + usage backends plus
// the Memory, Skills, MCP Registry and Guard modules. The returned provider
// service owns the local routing proxy (takeover + failover).
func newServer(cfg *config.Config, st *store.Store) (*server.Server, *provider.Service) {
	svc := provider.NewService(logger, st, cfg.Provider.ProxyAddr)
	eng := usage.NewEngine(cfg, st, logger)
	reporter := func(ctx context.Context, period string, since time.Time) (any, error) {
		return eng.Report(ctx, period, since)
	}
	srv := server.New(cfg, logger, st, svc, reporter)
	srv.SetProviderService(svc)
	srv.SetPresets(provider.Presets())
	srv.SetModules(
		memory.New(st),
		skills.New(),
		mcp.New(st),
		guard.New(st, core.GuardAsk),
	)
	srv.SetWorkspaceInitializer(workspace.New())
	return srv, svc
}

// attachRuntime builds the Engine plus the channels & triggers runtime and
// wires both onto the server. Shared by `anx serve` and `anx web` so
// console-managed channels and cron triggers run in either mode.
func attachRuntime(cfg *config.Config, st *store.Store, srv *server.Server) (*core.Engine, *core.ConnectService, error) {
	initializer := workspace.New()
	eng, err := server.BuildEngine(logger, cfg, initializer)
	if err != nil {
		return nil, nil, err
	}
	srv.SetWorkspaceInitializer(initializer)
	eng.SetConversationStore(st)
	connectSvc := core.NewConnectService(logger, eng, st)
	srv.SetSender(eng)
	srv.SetConnect(connectSvc)
	return eng, connectSvc, nil
}
