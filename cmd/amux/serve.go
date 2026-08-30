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
	cfg, configPath, err := loadConfig(!opts.allowDefault)
	if err != nil {
		return err
	}
	if opts.addrOverride != "" {
		cfg.Server.Addr = opts.addrOverride
	}
	if err := cfg.ValidateListenSecurity(); err != nil {
		return err
	}
	st, err := openRuntimeStore(cfg)
	if err != nil {
		return err
	}
	defer st.Close()

	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	runtime, err := newRuntime(ctx, cfg, st)
	if err != nil {
		return err
	}
	defer runtime.Stop()
	if err := runtime.Start(); err != nil {
		return err
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

	return runtime.Wait()
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
