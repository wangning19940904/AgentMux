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

	"github.com/wangning19940904/AgentMux/config"
	"github.com/wangning19940904/AgentMux/core"
	nativeintegration "github.com/wangning19940904/AgentMux/integrations/native"
	observationpkg "github.com/wangning19940904/AgentMux/observability"
	"github.com/wangning19940904/AgentMux/provider"
	"github.com/wangning19940904/AgentMux/server"
	"github.com/wangning19940904/AgentMux/store"
	"github.com/wangning19940904/AgentMux/tools"
	"github.com/wangning19940904/AgentMux/usage"
)

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
	secrets := ConfiguredSecrets(cfg)
	if st != nil {
		agents, listErr := st.ListAgentInstances(ctx)
		if listErr != nil {
			log.Warn("agent secrets unavailable for observation redaction", "err", listErr)
		} else {
			for _, agent := range agents {
				for _, value := range agent.Env {
					if value != "" {
						secrets = append(secrets, value)
					}
				}
			}
		}
		mcpServers, listErr := st.ListMCPServers(ctx)
		if listErr != nil {
			log.Warn("MCP secrets unavailable for observation redaction", "err", listErr)
		} else {
			for _, definition := range mcpServers {
				for _, value := range definition.Env {
					if value != "" {
						secrets = append(secrets, value)
					}
				}
			}
		}
	}
	observationRuntime, err := observationpkg.NewRuntime(log, cfg.Observability, st, home, secrets)
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
		observationRuntime.Ingest.SetUsageSink(usageEngine.Record)
	}
	srv.SetObservability(cfg.Observability, observationRuntime.Recorder, observationRuntime.Insights, nativeManager, observationRuntime.Ingest)
	// Start runs heavy DB-bound initialization, daily aggregation, and insight
	// materialization off the HTTP startup path.
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
	values := make([]string, 0, 1)
	if cfg.Bridge.Token != "" {
		values = append(values, cfg.Bridge.Token)
	}
	return values
}
