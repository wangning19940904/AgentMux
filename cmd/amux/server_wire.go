package main

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/wangning19940904/AgentMux/config"
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

	"github.com/wangning19940904/AgentMux/core"
)

// newServer wires the management server with provider + usage backends plus
// the Memory, Skills, MCP Registry and Guard modules. The returned provider
// service owns the local routing proxy (takeover + failover).
func newServer(cfg *config.Config, st *store.Store) (*server.Server, *provider.Service, *usage.Engine) {
	svc := provider.NewService(logger, st, cfg.Provider.ProxyAddr)
	eng := usage.NewEngine(cfg, st, logger)
	reporter := func(ctx context.Context, period string, since, until time.Time) (any, error) {
		return eng.ReportRange(ctx, period, since, until)
	}
	srv := server.New(cfg, logger, st, svc, reporter)
	srv.SetVersion(version)
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
// wires both onto the server. Shared by `amux serve` and `amux web` so
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
		// Start runs heavy DB-bound initialization (legacy import, daily
		// aggregation, insight materialization). On large stores this can take
		// tens of seconds while it contends for the single SQLite connection, so
		// run it off the startup path to let the HTTP server bind immediately.
		go func() {
			if runtimeErr := observationRuntime.Start(ctx); runtimeErr != nil && ctx.Err() == nil {
				logger.Warn("observability runtime start failed", "err", runtimeErr)
			}
		}()
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
