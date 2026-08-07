package main

import (
	"context"

	"github.com/wangning19940904/AgentMux/bootstrap"
	"github.com/wangning19940904/AgentMux/config"
	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/provider"
	"github.com/wangning19940904/AgentMux/server"
	"github.com/wangning19940904/AgentMux/store"
	"github.com/wangning19940904/AgentMux/usage"
)

// newServer wires the management server via the shared bootstrap package.
func newServer(cfg *config.Config, st *store.Store) (*server.Server, *provider.Service, *usage.Engine) {
	return bootstrap.NewServer(logger, cfg, st, version)
}

// attachRuntime builds the Engine plus the channels & triggers runtime and
// wires both onto the server. Shared by `amux serve` and `amux web` so
// console-managed channels and cron triggers run in either mode.
func attachRuntime(ctx context.Context, cfg *config.Config, st *store.Store, srv *server.Server, providerService *provider.Service, usageEngine *usage.Engine) (*core.Engine, *core.ConnectService, error) {
	return bootstrap.AttachRuntime(ctx, logger, cfg, st, srv, providerService, usageEngine, true)
}
