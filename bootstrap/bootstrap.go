// Package bootstrap wires the AgentMux daemon: management server, provider
// service, usage engine, observability runtime and the channels & triggers
// runtime. It is shared by the CLI (`amux serve` / `amux web`) and the Wails
// desktop shell so both assemble exactly the same backend.
package bootstrap

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

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
	"github.com/wangning19940904/AgentMux/tools"
	"github.com/wangning19940904/AgentMux/usage"
	"github.com/wangning19940904/AgentMux/workspace"
)

// NewServer wires the management server with provider + usage backends plus
// the Memory, Skills, MCP Registry and Guard modules. The returned provider
// service owns the local routing proxy (takeover + failover).
func NewServer(log *slog.Logger, cfg *config.Config, st *store.Store, version string) (*server.Server, *provider.Service, *usage.Engine) {
	svc := provider.NewService(log, st, cfg.Provider.ProxyAddr)
	eng := usage.NewEngine(cfg, st, log)
	reporter := func(ctx context.Context, period string, since, until time.Time) (any, error) {
		return eng.ReportRange(ctx, period, since, until)
	}
	srv := server.New(cfg, log, st, svc, reporter)
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

// AttachRuntime builds the Engine plus the channels & triggers runtime and
// wires both — together with the observability runtime — onto the server.
// With strictObservability, an observability init failure aborts startup;
// otherwise it is logged and the daemon continues without observation (the
// desktop shell must stay useful when e.g. the keychain is unavailable at
// login).
func AttachRuntime(ctx context.Context, log *slog.Logger, cfg *config.Config, st *store.Store, srv *server.Server, providerService *provider.Service, usageEngine *usage.Engine, strictObservability bool) (*core.Engine, *core.ConnectService, error) {
	initializer := workspace.New()
	eng, err := server.BuildEngine(log, cfg, initializer)
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
		if obsErr := attachObservability(ctx, log, cfg, st, srv, eng, providerService, usageEngine); obsErr != nil {
			if strictObservability {
				return nil, nil, obsErr
			}
			log.Error("observability unavailable; continuing without it", "err", obsErr)
		}
	}
	connectSvc := core.NewConnectService(log, eng, st)
	connectSvc.SetCLINoteResolver(cliNotes)
	srv.SetSender(eng)
	srv.SetInvoker(connectSvc)
	srv.SetConnect(connectSvc)
	return eng, connectSvc, nil
}

// cliNotes resolves managed-CLI catalog descriptions for prompt injection.
func cliNotes(ids []string) []core.CLINote {
	var notes []core.CLINote
	for _, id := range ids {
		spec, ok := tools.LookupCLI(id)
		if !ok {
			continue
		}
		name := spec.Name
		if name == "" {
			name = spec.ID
		}
		notes = append(notes, core.CLINote{Name: name, Note: spec.Note})
	}
	return notes
}

// attachObservability builds the observation runtime and fans it out to the
// engine, the provider proxy and the server's observability API.
func attachObservability(ctx context.Context, log *slog.Logger, cfg *config.Config, st *store.Store, srv *server.Server, eng *core.Engine, providerService *provider.Service, usageEngine *usage.Engine) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	observationRuntime, err := observationpkg.NewRuntime(log, cfg.Observability, st, home, ConfiguredSecrets(cfg))
	if err != nil {
		return err
	}
	var nativeManager *nativeintegration.Manager
	if manager, managerErr := nativeintegration.NewManager(nativeintegration.Options{HomeDir: home}); managerErr != nil {
		log.Warn("native observation integrations unavailable", "err", managerErr)
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
			log.Warn("observability runtime start failed", "err", runtimeErr)
		}
	}()
	return nil
}

// ConfiguredSecrets collects config values that must never appear in
// persisted observation content.
func ConfiguredSecrets(cfg *config.Config) []string {
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
