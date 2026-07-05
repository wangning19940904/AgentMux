package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/agentnexus/agentnexus/config"
	"github.com/agentnexus/agentnexus/core"
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

// buildEngine constructs the engine and wires configured projects.
func buildEngine(cfg *config.Config) (*core.Engine, error) {
	var hookList []core.Hook
	for _, h := range cfg.Hooks {
		hookList = append(hookList, core.Hook{
			Event: core.HookEvent(h.Event), Type: h.Type,
			Command: h.Command, URL: h.URL,
		})
	}
	hooks := core.NewHookRunner(logger, hookList)
	eng := core.NewEngine(logger, hooks)

	for _, p := range cfg.Projects {
		ag, err := core.CreateAgent(p.Agent, map[string]any{
			"work_dir": p.WorkDir, "system_prompt": p.SystemPrompt, "env": p.Env,
		})
		if err != nil {
			return nil, err
		}
		var plats []core.Platform
		for _, pc := range p.Platforms {
			typ, _ := pc["type"].(string)
			plat, err := core.CreatePlatform(typ, pc)
			if err != nil {
				return nil, err
			}
			plats = append(plats, plat)
		}
		eng.AddProject(p.Name, p.WorkDir, ag, plats)
	}
	return eng, nil
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

			eng, err := buildEngine(cfg)
			if err != nil {
				return err
			}

			ctx, cancel := signal.NotifyContext(context.Background(),
				os.Interrupt, syscall.SIGTERM)
			defer cancel()

			srv, providerSvc := newServer(cfg, st)
			srv.SetSender(eng)
			if err := providerSvc.RestoreProxyState(ctx); err != nil {
				logger.Warn("local routing restore failed", "err", err)
			}
			defer func() { _ = providerSvc.Proxy().Stop() }()
			go func() { _ = srv.ListenAndServe(ctx) }()

			return eng.Start(ctx)
		},
	}
}
