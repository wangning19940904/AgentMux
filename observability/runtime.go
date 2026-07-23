package observability

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	"github.com/agentnexus/agentnexus/config"
	"github.com/agentnexus/agentnexus/core"
	"github.com/agentnexus/agentnexus/store"
)

// Runtime owns the shared observation bus and its durable/remote consumers.
// It deliberately has no dependency on an Agent implementation, allowing the
// CLI daemon and desktop app to use identical wiring.
type Runtime struct {
	Log         *slog.Logger
	Config      config.ObservabilityConfig
	Bus         *core.ObservationBus
	Store       *store.Store
	Recorder    *store.ObservationRecorder
	Exporters   *ExporterService
	Pipeline    *Pipeline
	Insights    *InsightEngine
	Ingest      *IngestService
	Transcript  *TranscriptTailer
	IngestToken string
}

// Daily reports read the last two days from detailed spans. Refresh one extra
// day so the boundary can roll over without a full-history scan at startup.
const observationDailyRefreshWindow = 3 * 24 * time.Hour

func observationDailyRefreshSince(now time.Time) time.Time {
	return now.UTC().Add(-observationDailyRefreshWindow).Truncate(24 * time.Hour)
}

func LocalOTLPEndpoint(serverAddr string) string {
	host, port, err := net.SplitHostPort(strings.TrimSpace(serverAddr))
	if err != nil {
		return ""
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/api/v1/observability/otlp"
}

func NewRuntime(log *slog.Logger, cfg config.ObservabilityConfig, st *store.Store, home string, knownSecrets []string) (*Runtime, error) {
	if st == nil {
		return nil, errors.New("observability runtime requires a store")
	}
	if log == nil {
		log = slog.Default()
	}
	knownSecrets = append(append([]string(nil), knownSecrets...), configuredObservationSecrets(cfg)...)
	token, err := LoadOrCreateIngestToken(home)
	if err != nil {
		return nil, err
	}
	knownSecrets = append(knownSecrets, token)
	recorder, err := store.NewObservationRecorder(st, store.ObservationRecorderOptions{
		CaptureContent:   cfg.CaptureContent == "full",
		MasterKeyEnv:     cfg.MasterKeyEnv,
		KnownSecrets:     knownSecrets,
		ContentRetention: time.Duration(cfg.ContentRetentionDays) * 24 * time.Hour,
		DetailRetention:  time.Duration(cfg.DetailRetentionDays) * 24 * time.Hour,
	})
	if err != nil {
		return nil, err
	}
	bus := core.NewObservationBus()
	exporters := NewExporterService(log, st, recorder, cfg.Exporters)
	pipeline := NewPipeline(recorder, exporters)
	bus.Subscribe("sqlite-recorder-and-export-outbox", pipeline.Observe)
	ingest := NewIngestService(log, bus, home, token)
	bus.Subscribe("native-session-trace-correlation", ingest.ObserveCorrelation)
	transcript := NewTranscriptTailer(log, st, bus, TranscriptTailerOptions{
		Home:             home,
		ContentBackfill:  time.Duration(cfg.ContentRetentionDays) * 24 * time.Hour,
		MetadataBackfill: time.Duration(cfg.BackfillDays) * 24 * time.Hour,
	})
	recorder.SetPayloadSourceResolver(transcript.ResolvePayloadSource)
	return &Runtime{
		Log: log, Config: cfg, Bus: bus, Store: st, Recorder: recorder,
		Exporters: exporters, Pipeline: pipeline, Insights: NewInsightEngine(st),
		Ingest:      ingest,
		IngestToken: token,
		Transcript:  transcript,
	}, nil
}

func configuredObservationSecrets(cfg config.ObservabilityConfig) []string {
	var secrets []string
	if cfg.MasterKeyEnv != "" {
		if value := strings.TrimSpace(os.Getenv(cfg.MasterKeyEnv)); value != "" {
			secrets = append(secrets, value)
		}
	}
	for _, exporter := range cfg.Exporters {
		for _, value := range exporter.Headers {
			if len(strings.TrimSpace(value)) >= 8 {
				secrets = append(secrets, value)
			}
		}
	}
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || len(strings.TrimSpace(value)) < 8 {
			continue
		}
		upper := strings.ToUpper(key)
		if strings.Contains(upper, "TOKEN") || strings.Contains(upper, "SECRET") || strings.Contains(upper, "PASSWORD") ||
			strings.Contains(upper, "API_KEY") || strings.Contains(upper, "APIKEY") || strings.Contains(upper, "AUTH") || strings.Contains(upper, "COOKIE") {
			secrets = append(secrets, value)
		}
	}
	return secrets
}

// Start activates local hook ingest, isolated exporter workers, retention and
// advisory insight materialization. Observation failures are logged and remain
// fail-open for agent execution.
func (r *Runtime) Start(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.Exporters.Start(ctx)
	if err := r.Ingest.Start(ctx); err != nil {
		return err
	}
	if r.Store != nil {
		if result, err := r.Store.ImportLegacyObservations(ctx); err != nil {
			r.Log.Warn("legacy observation import failed", "err", err)
		} else if imported := result.UsageImported + result.ProxyImported; imported > 0 {
			r.Log.Info("legacy observations imported", "events", imported)
		}
		if secured, err := r.Store.SecureLegacyProxyErrors(ctx, r.Recorder.Observe); err != nil {
			r.Log.Warn("legacy proxy error encryption failed", "err", err)
		} else if secured > 0 {
			r.Log.Info("legacy proxy errors encrypted", "errors", secured)
		}
	}
	go r.Transcript.Start(ctx)
	if r.Store != nil {
		if err := r.Store.MaterializeObservationDailyUsageSince(ctx, observationDailyRefreshSince(time.Now())); err != nil {
			r.Log.Warn("initial observation daily aggregation failed", "err", err)
		}
	}
	if _, err := r.Recorder.Cleanup(ctx, time.Now().UTC()); err != nil {
		r.Log.Warn("initial observation retention cleanup failed", "err", err)
	}
	if _, err := r.Insights.Run(ctx, time.Now().UTC().Add(-7*24*time.Hour)); err != nil {
		r.Log.Warn("initial observation insight materialization failed", "err", err)
	}
	go r.maintenance(ctx)
	return nil
}

func (r *Runtime) maintenance(ctx context.Context) {
	insightTicker := time.NewTicker(15 * time.Minute)
	cleanupTicker := time.NewTicker(24 * time.Hour)
	defer insightTicker.Stop()
	defer cleanupTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-insightTicker.C:
			if _, err := r.Insights.Run(ctx, now.UTC().Add(-7*24*time.Hour)); err != nil && ctx.Err() == nil {
				r.Log.Warn("observation insight materialization failed", "err", err)
			}
		case now := <-cleanupTicker.C:
			if r.Store != nil {
				if err := r.Store.MaterializeObservationDailyUsageSince(ctx, observationDailyRefreshSince(now)); err != nil && ctx.Err() == nil {
					r.Log.Warn("observation daily aggregation failed", "err", err)
				}
			}
			if _, err := r.Recorder.Cleanup(ctx, now.UTC()); err != nil && ctx.Err() == nil {
				r.Log.Warn("observation retention cleanup failed", "err", err)
			}
		}
	}
}
