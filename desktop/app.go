//go:build desktop
// +build desktop

package main

import (
	"context"

	"github.com/agentnexus/agentnexus/config"
	"github.com/agentnexus/agentnexus/core"
	"github.com/agentnexus/agentnexus/guard"
	"github.com/agentnexus/agentnexus/mcp"
	"github.com/agentnexus/agentnexus/memory"
	"github.com/agentnexus/agentnexus/provider"
	"github.com/agentnexus/agentnexus/server"
	"github.com/agentnexus/agentnexus/skills"
	"github.com/agentnexus/agentnexus/store"
	"github.com/agentnexus/agentnexus/usage"
	"log/slog"
	"os"
	"time"

	_ "github.com/agentnexus/agentnexus/agent"
	_ "github.com/agentnexus/agentnexus/platform"
)

// startup boots the in-process daemon (HTTP API on 127.0.0.1:8765) that the
// embedded WebView talks to, mirroring the CLI's `serve`.
func (a *App) startup(ctx context.Context) {
	a.ctx, a.cancel = context.WithCancel(ctx)
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	cfg, err := config.Load("config.toml")
	if err != nil {
		cfg = config.Default()
	}
	st, err := store.Open(store.DefaultPath())
	if err != nil {
		log.Error("open store", "err", err)
		return
	}
	svc := provider.NewService(log, st, cfg.Provider.ProxyAddr)
	ue := usage.NewEngine(cfg, st, log)
	reporter := func(ctx context.Context, period string, since time.Time) (any, error) {
		return ue.Report(ctx, period, since)
	}
	srv := server.New(cfg, log, st, svc, reporter)
	srv.SetProviderService(svc)
	srv.SetPresets(provider.Presets())
	srv.SetModules(memory.New(st), skills.New(), mcp.New(st), guard.New(st, core.GuardAsk))
	if err := svc.RestoreProxyState(a.ctx); err != nil {
		log.Warn("local routing restore failed", "err", err)
	}
	go func() {
		if err := srv.ListenAndServe(a.ctx); err != nil {
			log.Error("serve desktop API", "err", err)
		}
	}()
	a.startMenuBar(log, cfg.Server.Addr)
}

func (a *App) shutdown(ctx context.Context) {
	if a.cancel != nil {
		a.cancel()
	}
}

// SwitchProvider is bound to the frontend/tray for quick switching.
func (a *App) SwitchProvider(id, tool string) error {
	st, err := store.Open(store.DefaultPath())
	if err != nil {
		return err
	}
	defer st.Close()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	return provider.NewService(log, st, "").Switch(a.ctx, id, tool)
}
