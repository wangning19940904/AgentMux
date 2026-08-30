package main

import (
	"context"

	"github.com/wangning19940904/AgentMux/bootstrap"
	"github.com/wangning19940904/AgentMux/config"
	"github.com/wangning19940904/AgentMux/store"
)

func newRuntime(ctx context.Context, cfg *config.Config, st *store.Store) (*bootstrap.Runtime, error) {
	return bootstrap.NewRuntime(ctx, logger, cfg, st, version, true)
}
