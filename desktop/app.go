//go:build desktop
// +build desktop

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/wangning19940904/AgentMux/config"
	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/guard"
	nativeintegration "github.com/wangning19940904/AgentMux/integrations/native"
	"github.com/wangning19940904/AgentMux/mcp"
	"github.com/wangning19940904/AgentMux/memory"
	observationpkg "github.com/wangning19940904/AgentMux/observability"
	"github.com/wangning19940904/AgentMux/provider"
	"github.com/wangning19940904/AgentMux/server"
	"github.com/wangning19940904/AgentMux/skills"
	"github.com/wangning19940904/AgentMux/store"
	"github.com/wangning19940904/AgentMux/usage"
	"github.com/wangning19940904/AgentMux/workspace"
	"log/slog"
	"time"

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

	svc := provider.NewService(log, st, cfg.Provider.ProxyAddr)
	ue := usage.NewEngine(cfg, st, log)
	reporter := func(ctx context.Context, period string, since, until time.Time) (any, error) {
		return ue.ReportRange(ctx, period, since, until)
	}
	initializer := workspace.New()
	srv := server.New(cfg, log, st, svc, reporter)
	srv.SetProviderService(svc)
	srv.SetPresets(provider.Presets())
	srv.SetModules(memory.New(st), skills.New(), mcp.New(st), guard.New(st, core.GuardAsk))
	srv.SetWorkspaceInitializer(initializer)
	go ue.Start(a.ctx, time.Duration(cfg.Observability.BackfillDays)*24*time.Hour)
	var observationRuntime *observationpkg.Runtime
	if cfg.Observability.Enabled {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			log.Error("resolve observability home", "err", homeErr)
		} else if runtimeValue, runtimeErr := observationpkg.NewRuntime(log, cfg.Observability, st, home, desktopConfiguredSecrets(cfg)); runtimeErr != nil {
			log.Error("build observability runtime", "err", runtimeErr)
		} else {
			observationRuntime = runtimeValue
			var nativeManager *nativeintegration.Manager
			if manager, managerErr := nativeintegration.NewManager(nativeintegration.Options{HomeDir: home}); managerErr != nil {
				log.Warn("native observation integrations unavailable", "err", managerErr)
			} else {
				nativeManager = manager
			}
			srv.SetObservability(cfg.Observability, runtimeValue.Recorder, runtimeValue.Insights, nativeManager, runtimeValue.Ingest)
			// Heavy DB-bound initialization; run off the startup path so the
			// desktop HTTP API binds immediately even on large stores.
			go func() {
				if runtimeErr := runtimeValue.Start(a.ctx); runtimeErr != nil && a.ctx.Err() == nil {
					log.Warn("start observability runtime", "err", runtimeErr)
				}
			}()
		}
	}
	if eng, err := server.BuildEngine(log, cfg, initializer); err != nil {
		log.Error("build engine", "err", err)
	} else {
		eng.SetConversationStore(st)
		if observationRuntime != nil {
			eng.SetObservationBus(observationRuntime.Bus)
			eng.SetUsageSink(ue.Record)
			eng.SetObservationChildTelemetry(core.ObservationChildTelemetry{
				Endpoint: observationpkg.LocalOTLPEndpoint(cfg.Server.Addr), Token: observationRuntime.IngestToken,
				CaptureContent: cfg.Observability.CaptureContent == "full",
			})
			svc.Proxy().SetTraceCostEstimator(ue.ProxyCost)
			svc.Proxy().SetTraceObserver(func(ctx context.Context, trace core.ProxyTrace, requestBody, responseBody []byte) error {
				return errors.Join(ue.RecordProxy(ctx, trace), observationRuntime.ObserveProxyTrace(ctx, trace, requestBody, responseBody))
			})
		}
		connectSvc := core.NewConnectService(log, eng, st)
		srv.SetSender(eng)
		srv.SetConnect(connectSvc)
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

func desktopConfiguredSecrets(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	values := []string{cfg.Bridge.Token}
	for _, project := range cfg.Projects {
		for _, value := range project.Env {
			if value != "" {
				values = append(values, value)
			}
		}
	}
	return values
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
