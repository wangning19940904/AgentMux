package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/wangning19940904/AgentMux/config"
	"github.com/wangning19940904/AgentMux/store"

	// Register all adapters via blank imports (plugin pattern).
	_ "github.com/wangning19940904/AgentMux/agent"
	_ "github.com/wangning19940904/AgentMux/platform"
)

func loadConfig(required bool) (*config.Config, string, error) {
	cfg, path, err := config.LoadResolved(flagConfig)
	if err != nil {
		if !required && config.IsNotFound(err) && flagConfig == "" && os.Getenv(config.EnvPath) == "" {
			return config.Default(), "", nil
		}
		return nil, "", err
	}
	return cfg, path, nil
}

// bootstrap loads config + opens the store. Shared by serve/web/client.
func bootstrap() (*config.Config, *store.Store, error) {
	cfg, st, _, err := bootstrapWithPath()
	return cfg, st, err
}

func bootstrapWithPath() (*config.Config, *store.Store, string, error) {
	return bootstrapWithPathRequired(true)
}

func bootstrapWithPathRequired(required bool) (*config.Config, *store.Store, string, error) {
	cfg, path, err := loadConfig(required)
	if err != nil {
		return nil, nil, "", err
	}
	st, err := openRuntimeStore(cfg)
	if err != nil {
		return nil, nil, "", err
	}
	return cfg, st, path, nil
}

// bootstrapStore opens just the store, tolerating a missing config file. Used
// by commands that only need the DB (provider, usage) so they work without a
// config.toml present.
func bootstrapStore() (*config.Config, *store.Store, error) {
	cfg, _, err := loadConfig(false)
	if err != nil {
		return nil, nil, err
	}
	st, err := openRuntimeStore(cfg)
	if err != nil {
		return nil, nil, err
	}
	return cfg, st, nil
}

func openRuntimeStore(cfg *config.Config) (*store.Store, error) {
	if cfg == nil {
		return nil, fmt.Errorf("database configuration is required")
	}
	lifetime, err := time.ParseDuration(cfg.Database.ConnectionMaxLifetime)
	if err != nil {
		return nil, fmt.Errorf("database.connection_max_lifetime: %w", err)
	}
	url := cfg.Database.URL
	if flagDatabaseURL != "" {
		url = flagDatabaseURL
	}
	return store.OpenPostgres(context.Background(), store.DatabaseConfig{
		URL:                   url,
		MaxOpenConnections:    cfg.Database.MaxOpenConnections,
		MaxIdleConnections:    cfg.Database.MaxIdleConnections,
		ConnectionMaxLifetime: lifetime,
	})
}

type daemonOptions struct {
	addrOverride string
	printConfig  bool
	printReady   bool
	printWebUI   bool
	openWebUI    bool
	allowDefault bool
}

func runDaemon(cmd *cobra.Command, opts daemonOptions) error {
	cfg, st, configPath, err := bootstrapWithPathRequired(!opts.allowDefault)
	if err != nil {
		return err
	}
	defer st.Close()
	if opts.addrOverride != "" {
		cfg.Server.Addr = opts.addrOverride
	}

	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
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

	errCh := make(chan error, 2)
	go func() { errCh <- srv.ListenAndServe(ctx) }()
	go func() { errCh <- eng.Start(ctx) }()
	if err := connectSvc.Start(ctx); err != nil {
		logger.Warn("connect runtime start failed", "err", err)
	}

	if opts.printConfig {
		if configPath != "" {
			cmd.Println("Config:", configPath)
		} else {
			cmd.Println("Config: built-in defaults (run `amux config init` to create one)")
		}
	}
	if opts.printReady {
		cmd.Println("AgentMux client:", cfg.Server.Addr, "(Ctrl-C to stop)")
	}
	if opts.printWebUI || opts.openWebUI {
		url := "http://" + cfg.Server.Addr
		if opts.openWebUI {
			time.Sleep(300 * time.Millisecond)
			if err := openBrowser(url); err != nil {
				logger.Warn("open browser failed", "err", err)
			}
		}
		cmd.Println("WebUI:", url)
	}

	select {
	case err := <-errCh:
		cancel()
		if err != nil && ctx.Err() == nil {
			return err
		}
		return nil
	case <-ctx.Done():
		return nil
	}
}

func serveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Start the bridge daemon (IM gateway + management API)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDaemon(cmd, daemonOptions{})
		},
	}
}
