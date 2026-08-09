//go:build desktop
// +build desktop

package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/bootstrap"
	"github.com/wangning19940904/AgentMux/config"
	"github.com/wangning19940904/AgentMux/provider"
	"github.com/wangning19940904/AgentMux/store"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	_ "github.com/wangning19940904/AgentMux/agent"
	_ "github.com/wangning19940904/AgentMux/platform"
)

// startup boots the in-process daemon (HTTP API on 127.0.0.1:8765) that the
// embedded WebView talks to, mirroring the CLI's `serve`.
func (a *App) startup(ctx context.Context) {
	a.ctx, a.cancel = context.WithCancel(ctx)
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	a.ensureLaunchAtLoginDefault(log)

	cfg, err := config.Load("config.toml")
	if err != nil {
		cfg = config.Default()
	}
	a.setAPITarget(cfg.Server.Addr)
	a.startMenuBar(log, cfg.Server.Addr)
	go a.runDesktopBackend(log, cfg)
}

const desktopStoreRetryInterval = 2 * time.Second

// runDesktopBackend keeps the native shell useful when PostgreSQL and
// AgentMux are both launched at login. Service startup order is not stable on
// macOS, so a failed first connection must not permanently strand the WebView
// behind the asset proxy's "desktop API is starting" response.
func (a *App) runDesktopBackend(log *slog.Logger, cfg *config.Config) {
	st, err := waitForDesktopStore(a.ctx, cfg, desktopStoreRetryInterval, log, openDesktopStore)
	if err != nil {
		return
	}
	defer st.Close()

	srv, svc, ue := bootstrap.NewServer(log, cfg, st, version)
	if eng, connectSvc, err := bootstrap.AttachRuntime(a.ctx, log, cfg, st, srv, svc, ue, false); err != nil {
		log.Error("build engine", "err", err)
	} else {
		go func() {
			if err := eng.Start(a.ctx); err != nil {
				log.Error("engine stopped", "err", err)
			}
		}()
		if err := connectSvc.Start(a.ctx); err != nil {
			log.Warn("connect runtime start failed", "err", err)
		}
	}
	if err := svc.RestoreProxyState(a.ctx); err != nil {
		log.Warn("local routing restore failed", "err", err)
	}
	if err := srv.ListenAndServe(a.ctx); err != nil && a.ctx.Err() == nil {
		log.Error("serve desktop API", "err", err)
	}
}

func waitForDesktopStore(
	ctx context.Context,
	cfg *config.Config,
	retryInterval time.Duration,
	log *slog.Logger,
	open func(context.Context, *config.Config) (*store.Store, error),
) (*store.Store, error) {
	if retryInterval <= 0 {
		retryInterval = desktopStoreRetryInterval
	}
	for {
		st, err := open(ctx, cfg)
		if err == nil {
			return st, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if log != nil {
			log.Warn("desktop database unavailable; retrying", "err", err, "retry_in", retryInterval)
		}
		timer := time.NewTimer(retryInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (a *App) shutdown(ctx context.Context) {
	if a.cancel != nil {
		a.cancel()
	}
}

// SwitchProvider is bound to the frontend/tray for quick switching.
func (a *App) SwitchProvider(id, tool string) error {
	cfg, cfgErr := config.Load("config.toml")
	if cfgErr != nil {
		cfg = config.Default()
	}
	st, err := openDesktopStore(a.ctx, cfg)
	if err != nil {
		return err
	}
	defer st.Close()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	return provider.NewService(log, st, "").Switch(a.ctx, id, tool)
}

func openDesktopStore(ctx context.Context, cfg *config.Config) (*store.Store, error) {
	lifetime, err := time.ParseDuration(cfg.Database.ConnectionMaxLifetime)
	if err != nil {
		return nil, err
	}
	return store.OpenPostgres(ctx, store.DatabaseConfig{
		URL:                   cfg.Database.URL,
		MaxOpenConnections:    cfg.Database.MaxOpenConnections,
		MaxIdleConnections:    cfg.Database.MaxIdleConnections,
		ConnectionMaxLifetime: lifetime,
	})
}

// SelectDirectory opens the native system directory picker for desktop users.
func (a *App) SelectDirectory(defaultDirectory string) (string, error) {
	return wailsruntime.OpenDirectoryDialog(a.ctx, wailsruntime.OpenDialogOptions{
		Title:                      "Select work directory",
		DefaultDirectory:           existingDirectoryForDialog(defaultDirectory),
		CanCreateDirectories:       true,
		ResolvesAliases:            true,
		TreatPackagesAsDirectories: true,
	})
}

func existingDirectoryForDialog(raw string) string {
	path := strings.TrimSpace(raw)
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "~") {
		if path == "~" || strings.HasPrefix(path, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				if path == "~" {
					path = home
				} else {
					path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
				}
			}
		}
	}
	if abs, err := filepath.Abs(os.ExpandEnv(path)); err == nil {
		path = abs
	}
	for {
		info, err := os.Stat(path)
		if err == nil {
			if info.IsDir() {
				return path
			}
			return filepath.Dir(path)
		}
		parent := filepath.Dir(path)
		if parent == path || parent == "." {
			return ""
		}
		path = parent
	}
}
