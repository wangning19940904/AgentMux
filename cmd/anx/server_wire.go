package main

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/agentnexus/agentnexus/config"
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

	"github.com/agentnexus/agentnexus/core"
)

// newServer wires the management server with provider + usage backends plus
// the Memory, Skills, MCP Registry and Guard modules. The returned provider
// service owns the local routing proxy (takeover + failover).
func newServer(cfg *config.Config, st *store.Store) (*server.Server, *provider.Service, *usage.Engine) {
	svc := provider.NewService(logger, st, cfg.Provider.ProxyAddr)
	eng := usage.NewEngine(cfg, st, logger)
	reporter := func(ctx context.Context, period string, since time.Time) (any, error) {
		return eng.Report(ctx, period, since)
	}
	srv := server.New(cfg, logger, st, svc, reporter)
	srv.SetProviderService(svc)
	srv.SetPresets(provider.Presets())
	srv.SetModules(
		memory.New(st),
		skills.New(),
		mcp.New(st),
		guard.New(st, core.GuardAsk),
	)
	srv.SetWorkspaceInitializer(workspace.New())
	return srv, svc, eng
}

// attachRuntime builds the Engine plus the channels & triggers runtime and
// wires both onto the server. Shared by `anx serve` and `anx web` so
// console-managed channels and cron triggers run in either mode.
func attachRuntime(ctx context.Context, cfg *config.Config, st *store.Store, srv *server.Server, providerService *provider.Service, usageEngine *usage.Engine) (*core.Engine, *core.ConnectService, error) {
	initializer := workspace.New()
	eng, err := server.BuildEngine(logger, cfg, initializer)
	if err != nil {
		return nil, nil, err
	}
	srv.SetWorkspaceInitializer(initializer)
	eng.SetConversationStore(st)
	if usageEngine != nil {
		backfill := time.Duration(cfg.Observability.BackfillDays) * 24 * time.Hour
		go usageEngine.Start(ctx, backfill)
	}
	if cfg.Observability.Enabled {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return nil, nil, homeErr
		}
		observationRuntime, runtimeErr := observationpkg.NewRuntime(logger, cfg.Observability, st, home, configuredSecrets(cfg))
		if runtimeErr != nil {
			return nil, nil, runtimeErr
		}
		var nativeManager *nativeintegration.Manager
		if manager, managerErr := nativeintegration.NewManager(nativeintegration.Options{HomeDir: home}); managerErr != nil {
			logger.Warn("native observation integrations unavailable", "err", managerErr)
		} else {
			nativeManager = manager
		}
		eng.SetObservationBus(observationRuntime.Bus)
		eng.SetObservationChildTelemetry(core.ObservationChildTelemetry{
			Endpoint: observationpkg.LocalOTLPEndpoint(cfg.Server.Addr), Token: observationRuntime.IngestToken,
			CaptureContent: cfg.Observability.CaptureContent == "full",
		})
		if providerService != nil && providerService.Proxy() != nil {
			if usageEngine != nil {
				providerService.Proxy().SetTraceCostEstimator(usageEngine.ProxyCost)
			}
			providerService.Proxy().SetTraceObserver(func(ctx context.Context, trace core.ProxyTrace, requestBody, responseBody []byte) error {
				var usageErr error
				if usageEngine != nil {
					usageErr = usageEngine.RecordProxy(ctx, trace)
				}
				return errors.Join(usageErr, observationRuntime.ObserveProxyTrace(ctx, trace, requestBody, responseBody))
			})
		}
		if usageEngine != nil {
			eng.SetUsageSink(usageEngine.Record)
		}
		srv.SetObservability(cfg.Observability, observationRuntime.Recorder, observationRuntime.Insights, nativeManager, observationRuntime.Ingest)
		if runtimeErr := observationRuntime.Start(ctx); runtimeErr != nil {
			return nil, nil, runtimeErr
		}
	}
	connectSvc := core.NewConnectService(logger, eng, st)
	srv.SetSender(eng)
	srv.SetConnect(connectSvc)
	return eng, connectSvc, nil
}

func configuredSecrets(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	values := make([]string, 0, 1+len(cfg.Projects)*2)
	if cfg.Bridge.Token != "" {
		values = append(values, cfg.Bridge.Token)
	}
	for _, project := range cfg.Projects {
		for _, value := range project.Env {
			if value != "" {
				values = append(values, value)
			}
		}
	}
	return values
}
