//go:build desktop
// +build desktop

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/agentnexus/agentnexus/config"
	"github.com/agentnexus/agentnexus/core"
	"github.com/agentnexus/agentnexus/guard"
	nativeintegration "github.com/agentnexus/agentnexus/integrations/native"
	"github.com/agentnexus/agentnexus/mcp"
	"github.com/agentnexus/agentnexus/memory"
	observationpkg "github.com/agentnexus/agentnexus/observability"
	"github.com/agentnexus/agentnexus/provider"
	"github.com/agentnexus/agentnexus/server"
	"github.com/agentnexus/agentnexus/skills"
	"github.com/agentnexus/agentnexus/store"
	"github.com/agentnexus/agentnexus/usage"
	"github.com/agentnexus/agentnexus/workspace"
	"log/slog"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

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
			if runtimeErr := runtimeValue.Start(a.ctx); runtimeErr != nil {
				log.Warn("start observability runtime", "err", runtimeErr)
			}
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
	go func() {
		if err := srv.ListenAndServe(a.ctx); err != nil {
			log.Error("serve desktop API", "err", err)
		}
	}()
	a.startMenuBar(log, cfg.Server.Addr)
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
	st, err := store.Open(store.DefaultPath())
	if err != nil {
		return err
	}
	defer st.Close()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	return provider.NewService(log, st, "").Switch(a.ctx, id, tool)
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
