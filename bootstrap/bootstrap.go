// Package bootstrap wires the AgentMux daemon: management server, provider
// service, usage engine, observability runtime and the channels & triggers
// runtime. It is shared by the CLI (`amux serve` / `amux web`) and the Wails
// desktop shell so both assemble exactly the same backend.
package bootstrap

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/wangning19940904/AgentMux/config"
	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/historysearch"
	nativeintegration "github.com/wangning19940904/AgentMux/integrations/native"
	observationpkg "github.com/wangning19940904/AgentMux/observability"
	"github.com/wangning19940904/AgentMux/server"
	"github.com/wangning19940904/AgentMux/store"
	"github.com/wangning19940904/AgentMux/tools"
	"github.com/wangning19940904/AgentMux/usage"
)

// cliNotes resolves managed-CLI catalog descriptions for prompt injection.
func cliNotes(ctx context.Context, st tools.DescriptionStore, ids []string) []core.CLINote {
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
		note, err := tools.CLIDescription(ctx, st, spec)
		if err != nil {
			slog.Warn("load CLI description for prompt", "cli", id, "err", err)
			continue
		}
		notes = append(notes, core.CLINote{Name: name, Note: note})
	}
	return notes
}

// attachObservability builds the observation runtime and fans it out to the
// engine, the provider proxy and the server's observability API.
func attachObservability(ctx context.Context, log *slog.Logger, cfg *config.Config, st *store.Store, srv *server.Server, eng *core.Engine, usageEngine *usage.Engine, history *historysearch.Service) error {
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
	if usageEngine != nil {
		eng.SetUsageSink(usageEngine.Record)
		observationRuntime.Ingest.SetUsageSink(usageEngine.Record)
	}
	srv.SetObservability(cfg.Observability, observationRuntime.Recorder, observationRuntime.Insights, nativeManager, observationRuntime.Ingest)
	attachHistorySearch(history, cfg.Observability, observationRuntime)
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
// attachHistorySearch indexes the turns the engine records, under the same
// conditions the recorder keeps their content: capture_content "full" and a
// secure key. Otherwise only memory is searchable, and a search says why.
func attachHistorySearch(history *historysearch.Service, cfg config.ObservabilityConfig, runtime *observationpkg.Runtime) {
	if history == nil {
		return
	}
	recorder := runtime.Recorder
	history.AddSecrets(recorder.KnownSecrets())
	switch {
	case cfg.CaptureContent != "full":
		history.DisableOriginals("observability.capture_content is not \"full\", so past turns are not indexed")
	case recorder.MetadataOnly():
		history.DisableOriginals("past turns are not indexed: " + recorder.MetadataOnlyReason())
	default:
		history.EnableOriginals(time.Duration(cfg.ContentRetentionDays)*24*time.Hour, recorder)
		runtime.Bus.Subscribe("history-search-index", history.Observe)
	}
}

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
