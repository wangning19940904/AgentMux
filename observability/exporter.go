// Package observability wires durable recording, OTLP export, insight
// materialization and native-hook ingestion around core.ObservationEnvelope.
package observability

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wangning19940904/AgentMux/config"
	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
)

// Pipeline is the single bus subscriber that first encrypts/persists content
// and only then enqueues the sanitized envelope for exporters.
type Pipeline struct {
	recorder  *store.ObservationRecorder
	exporters *ExporterService
}

func NewPipeline(recorder *store.ObservationRecorder, exporters *ExporterService) *Pipeline {
	return &Pipeline{recorder: recorder, exporters: exporters}
}

func (p *Pipeline) Observe(ctx context.Context, envelope core.ObservationEnvelope) error {
	if p == nil || p.recorder == nil {
		return nil
	}
	secured, recordErr := p.recorder.Record(ctx, envelope)
	var exportErr error
	if p.exporters != nil {
		exportErr = p.exporters.Enqueue(ctx, secured)
	}
	return errors.Join(recordErr, exportErr)
}

type ExporterService struct {
	log      *slog.Logger
	store    *store.Store
	recorder *store.ObservationRecorder
	configs  []config.ObservabilityExporterConfig
	client   *http.Client
	start    sync.Once
}

func NewExporterService(log *slog.Logger, st *store.Store, recorder *store.ObservationRecorder, exporters []config.ObservabilityExporterConfig) *ExporterService {
	if log == nil {
		log = slog.Default()
	}
	enabled := make([]config.ObservabilityExporterConfig, 0, len(exporters))
	for _, exporter := range exporters {
		if exporter.Enabled {
			enabled = append(enabled, exporter)
		}
	}
	return &ExporterService{
		log: log, store: st, recorder: recorder, configs: enabled,
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

func (s *ExporterService) Enqueue(ctx context.Context, envelope core.ObservationEnvelope) error {
	if s == nil || s.store == nil {
		return nil
	}
	var errs []error
	for _, exporter := range s.configs {
		if err := s.store.EnqueueObservationExport(ctx, exporter.Name, envelope, exporter.IncludeContent); err != nil {
			errs = append(errs, fmt.Errorf("enqueue %s: %w", exporter.Name, err))
			continue
		}
		if err := s.store.TrimObservationExportQueue(ctx, exporter.Name, exporter.QueueSize); err != nil {
			errs = append(errs, fmt.Errorf("trim %s queue: %w", exporter.Name, err))
		}
	}
	return errors.Join(errs...)
}

func (s *ExporterService) Start(ctx context.Context) {
	if s == nil {
		return
	}
	s.start.Do(func() {
		for _, exporter := range s.configs {
			exporter := exporter
			go s.run(ctx, exporter)
		}
	})
}

func (s *ExporterService) run(ctx context.Context, exporter config.ObservabilityExporterConfig) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if err := s.flush(ctx, exporter); err != nil && !errors.Is(err, context.Canceled) {
			s.log.Warn("OTLP export flush failed", "exporter", exporter.Name, "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *ExporterService) flush(ctx context.Context, exporter config.ObservabilityExporterConfig) error {
	limit := exporter.QueueSize
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	items, err := s.store.ListPendingObservationExports(ctx, exporter.Name, time.Now().UTC(), limit)
	if err != nil || len(items) == 0 {
		return err
	}
	var errs []error
	for _, item := range items {
		if err := s.exportOne(ctx, exporter, item); err != nil {
			attempt := item.Attempts + 1
			backoff := time.Duration(1<<min(attempt, 8)) * time.Second
			_ = s.store.RetryObservationExport(context.WithoutCancel(ctx), item.ID, err, time.Now().UTC().Add(backoff))
			errs = append(errs, err)
			continue
		}
		if err := s.store.CompleteObservationExport(context.WithoutCancel(ctx), item.ID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *ExporterService) exportOne(ctx context.Context, exporter config.ObservabilityExporterConfig, item store.ObservationExportItem) error {
	var content []byte
	var contentType string
	if exporter.IncludeContent && item.Envelope.PayloadRef != nil && s.recorder != nil {
		var err error
		content, contentType, err = s.recorder.ReadEnvelopePayload(ctx, item.Envelope)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read opted-in payload: %w", err)
		}
		// Content retention is intentionally shorter than exporter queue
		// retention. Once a payload expires, export the remaining metadata rather
		// than retrying the outbox item forever.
		if errors.Is(err, sql.ErrNoRows) {
			content, contentType = nil, ""
		}
	}
	body, err := json.Marshal(otlpTraceRequest(item.Envelope, content, contentType))
	if err != nil {
		return err
	}
	endpoint, err := otlpTracesEndpoint(exporter.Endpoint)
	if err != nil {
		return err
	}
	timeout := time.Duration(exporter.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	// OTLP/HTTP JSON uses the same data model as protobuf while avoiding a
	// heavyweight SDK in the local daemon. Collectors supporting OTLP/HTTP
	// accept this encoding at /v1/traces.
	req.Header.Set("Content-Type", "application/json")
	for key, value := range exporter.Headers {
		req.Header.Set(key, value)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("OTLP endpoint returned %s", resp.Status)
	}
	return nil
}

func otlpTracesEndpoint(endpoint string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid OTLP endpoint %q", endpoint)
	}
	if parsed.Path == "" || parsed.Path == "/" {
		parsed.Path = "/v1/traces"
	}
	return parsed.String(), nil
}

type otlpRequest struct {
	ResourceSpans []otlpResourceSpans `json:"resourceSpans"`
}

type otlpResourceSpans struct {
	Resource   otlpResource    `json:"resource"`
	ScopeSpans []otlpScopeSpan `json:"scopeSpans"`
}

type otlpResource struct {
	Attributes []otlpAttribute `json:"attributes"`
}

type otlpScopeSpan struct {
	Scope otlpScope  `json:"scope"`
	Spans []otlpSpan `json:"spans"`
}

type otlpScope struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type otlpSpan struct {
	TraceID           string          `json:"traceId"`
	SpanID            string          `json:"spanId"`
	ParentSpanID      string          `json:"parentSpanId,omitempty"`
	Name              string          `json:"name"`
	Kind              int             `json:"kind"`
	StartTimeUnixNano string          `json:"startTimeUnixNano"`
	EndTimeUnixNano   string          `json:"endTimeUnixNano"`
	Attributes        []otlpAttribute `json:"attributes,omitempty"`
	Status            otlpStatus      `json:"status"`
}

type otlpAttribute struct {
	Key   string       `json:"key"`
	Value otlpAnyValue `json:"value"`
}

type otlpAnyValue struct {
	StringValue string  `json:"stringValue,omitempty"`
	IntValue    string  `json:"intValue,omitempty"`
	DoubleValue float64 `json:"doubleValue,omitempty"`
	BoolValue   bool    `json:"boolValue,omitempty"`
}

type otlpStatus struct {
	Code    int    `json:"code"`
	Message string `json:"message,omitempty"`
}

func otlpTraceRequest(envelope core.ObservationEnvelope, content []byte, contentType string) otlpRequest {
	start := envelope.Time
	durationMs := int64(0)
	if envelope.Model != nil {
		durationMs = envelope.Model.DurationMillis
	}
	if envelope.Tool != nil && envelope.Tool.DurationMillis > durationMs {
		durationMs = envelope.Tool.DurationMillis
	}
	if envelope.Lifecycle == core.ObservationLifecycleEnd && durationMs > 0 {
		start = envelope.Time.Add(-time.Duration(durationMs) * time.Millisecond)
	}
	end := envelope.Time
	if envelope.Lifecycle == core.ObservationLifecycleStart {
		end = start.Add(time.Nanosecond)
	}
	attributes := otlpEnvelopeAttributes(envelope)
	if len(content) > 0 {
		attributes = append(attributes,
			otlpString("agentmux.content", string(content)),
			otlpString("agentmux.content_type", contentType),
		)
	}
	status := otlpStatus{Code: 1}
	if envelope.Status == core.ObservationStatusError {
		status.Code = 2
		if envelope.Error != nil {
			status.Message = envelope.Error.Message
		}
	}
	span := otlpSpan{
		TraceID: normalizeOTLPID(envelope.TraceID, 16), SpanID: normalizeOTLPID(envelope.SpanID, 8),
		ParentSpanID: normalizeOTLPID(envelope.ParentSpanID, 8), Name: firstOTLPName(envelope), Kind: 1,
		StartTimeUnixNano: strconv.FormatInt(start.UnixNano(), 10), EndTimeUnixNano: strconv.FormatInt(end.UnixNano(), 10),
		Attributes: attributes, Status: status,
	}
	return otlpRequest{ResourceSpans: []otlpResourceSpans{{
		Resource: otlpResource{Attributes: []otlpAttribute{
			otlpString("service.name", "agentmux"), otlpString("service.version", "0.1.0"),
		}},
		ScopeSpans: []otlpScopeSpan{{Scope: otlpScope{Name: "github.com/wangning19940904/AgentMux", Version: "1"}, Spans: []otlpSpan{span}}},
	}}}
}

func otlpEnvelopeAttributes(envelope core.ObservationEnvelope) []otlpAttribute {
	attrs := []otlpAttribute{
		otlpString("agentmux.kind", envelope.Kind), otlpString("agentmux.source", envelope.Source),
		otlpString("agentmux.quality", envelope.Quality), otlpString("agentmux.lifecycle", envelope.Lifecycle),
	}
	for key, value := range map[string]string{
		"gen_ai.agent.id": envelope.AgentID, "gen_ai.agent.name": envelope.AgentName,
		"gen_ai.conversation.id": envelope.ConversationID, "gen_ai.session.id": envelope.SessionID,
		"agentmux.turn.id": envelope.TurnID,
	} {
		if value != "" {
			attrs = append(attrs, otlpString(key, value))
		}
	}
	if envelope.Model != nil {
		attrs = append(attrs,
			otlpString("gen_ai.request.model", envelope.Model.Requested),
			otlpString("gen_ai.response.model", envelope.Model.Resolved),
			otlpString("gen_ai.response.id", envelope.Model.RequestID),
			otlpInt("gen_ai.request.attempt", int64(envelope.Model.Attempt)),
			otlpInt("gen_ai.server.time_to_first_token", envelope.Model.TTFTMillis),
		)
	}
	if envelope.Tool != nil {
		attrs = append(attrs, otlpString("gen_ai.tool.name", envelope.Tool.Name), otlpString("gen_ai.tool.call.id", envelope.Tool.CallID))
	}
	if envelope.Usage != nil {
		attrs = append(attrs,
			otlpInt("gen_ai.usage.input_tokens", envelope.Usage.InputTokens),
			otlpInt("gen_ai.usage.output_tokens", envelope.Usage.OutputTokens),
			otlpInt("gen_ai.usage.cache_read_tokens", envelope.Usage.CacheReadTokens),
			otlpInt("gen_ai.usage.cache_write_tokens", envelope.Usage.CacheWriteTokens),
		)
	}
	for key, value := range envelope.Attributes {
		attrs = append(attrs, otlpString("agentmux."+key, fmt.Sprint(value)))
	}
	return attrs
}

func otlpString(key, value string) otlpAttribute {
	return otlpAttribute{Key: key, Value: otlpAnyValue{StringValue: value}}
}

func otlpInt(key string, value int64) otlpAttribute {
	return otlpAttribute{Key: key, Value: otlpAnyValue{IntValue: strconv.FormatInt(value, 10)}}
}

func normalizeOTLPID(value string, bytesLen int) string {
	value = strings.TrimPrefix(strings.TrimSpace(value), "0x")
	decoded, err := hex.DecodeString(value)
	if err == nil && len(decoded) == bytesLen {
		return value
	}
	// Envelope IDs are normally valid hex. For legacy/backfill strings, derive
	// a stable zero-padded representation without leaking the original value.
	encoded := fmt.Sprintf("%x", []byte(value))
	need := bytesLen * 2
	if len(encoded) > need {
		encoded = encoded[len(encoded)-need:]
	}
	return strings.Repeat("0", need-len(encoded)) + encoded
}

func firstOTLPName(envelope core.ObservationEnvelope) string {
	if envelope.Name != "" {
		return envelope.Name
	}
	if envelope.Kind != "" {
		return envelope.Kind
	}
	return "agentmux.observation"
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
