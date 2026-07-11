package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/agentnexus/agentnexus/config"
	"github.com/agentnexus/agentnexus/store"
	"github.com/spf13/cobra"

	// Register all adapters via blank imports (plugin pattern).
	_ "github.com/agentnexus/agentnexus/agent"
	_ "github.com/agentnexus/agentnexus/platform"
)

func dbPath() string {
	if flagDB != "" {
		return flagDB
	}
	return store.DefaultPath()
}

// bootstrap loads config + opens the store. Shared by serve/web.
func bootstrap() (*config.Config, *store.Store, error) {
	cfg, err := config.Load(flagConfig)
	if err != nil {
		return nil, nil, err
	}
	st, err := store.Open(dbPath())
	if err != nil {
		return nil, nil, err
	}
	return cfg, st, nil
}

// bootstrapStore opens just the store, tolerating a missing config file. Used
// by commands that only need the DB (provider, usage) so they work without a
// config.toml present.
func bootstrapStore() (*config.Config, *store.Store, error) {
	cfg, err := config.Load(flagConfig)
	if err != nil {
		cfg = config.Default()
	}
	st, err := store.Open(dbPath())
	if err != nil {
		return nil, nil, err
	}
	return cfg, st, nil
}

func serveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Start the bridge daemon (IM gateway + management API)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, st, err := bootstrap()
			if err != nil {
				return err
			}
			defer st.Close()

			ctx, cancel := signal.NotifyContext(context.Background(),
				os.Interrupt, syscall.SIGTERM)
			defer cancel()

			srv, providerSvc, usageEngine := newServer(cfg, st)
			eng, connectSvc, err := attachRuntime(ctx, cfg, st, srv, providerSvc, usageEngine)
			if err != nil {
				return err
			}
			if err := providerSvc.RestoreProxyState(ctx); err != nil {
				logger.Warn("local routing restore failed", "err", err)
			}
			defer func() { _ = providerSvc.Proxy().Stop() }()
			go func() { _ = srv.ListenAndServe(ctx) }()
			if err := connectSvc.Start(ctx); err != nil {
				logger.Warn("connect runtime start failed", "err", err)
			}

			return eng.Start(ctx)
		},
	}
}
