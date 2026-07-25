package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ObservationEnvelopeVersion is the stable wire/storage version for
// AgentMux observability events. Additive fields may be introduced without
// changing it; incompatible changes require a new version.
const ObservationEnvelopeVersion = "v1"

// Observation quality values describe how complete and authoritative an event
// is. They let materializers prefer native runtime data over inferred or
// backfilled data without discarding useful partial coverage.
const (
	ObservationQualityComplete = "complete"
	ObservationQualityPartial  = "partial"
	ObservationQualityInferred = "inferred"
	ObservationQualityLegacy   = "legacy"
)

// Observation lifecycle values describe whether an envelope opens, updates,
// or closes its span.
const (
	ObservationLifecycleStart = "start"
	ObservationLifecycleEvent = "event"
	ObservationLifecycleEnd   = "end"
)

// Observation status values are intentionally small and exporter-neutral.
const (
	ObservationStatusUnset     = "unset"
	ObservationStatusRunning   = "running"
	ObservationStatusOK        = "ok"
	ObservationStatusError     = "error"
	ObservationStatusCancelled = "cancelled"
)

// ObservationEnvelope is the canonical event shared by the in-process bus,
// SQLite recorder, native hook ingest, transcript backfill, and exporters.
// Content is process-local only and is never serialized. Recorders either
// redact, compress, and encrypt it or replace transcript-backed content with a
// verified local-file PayloadRef.
type ObservationEnvelope struct {
	Version string `json:"version"`

	EventID  string    `json:"event_id"`
	Sequence int64     `json:"sequence,omitempty"`
	Time     time.Time `json:"time"`

	TraceID      string `json:"trace_id"`
	SpanID       string `json:"span_id"`
	ParentSpanID string `json:"parent_span_id,omitempty"`
	DedupeKey    string `json:"dedupe_key,omitempty"`

	Kind      string `json:"kind"`
	Name      string `json:"name,omitempty"`
	Lifecycle string `json:"lifecycle,omitempty"`

	AgentID        string `json:"agent_id,omitempty"`
	AgentName      string `json:"agent_name,omitempty"`
	RuntimeID      string `json:"runtime_id,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
	SessionID      string `json:"session_id,omitempty"`
	TurnID         string `json:"turn_id,omitempty"`

	Source     string            `json:"source,omitempty"`
	Provenance []string          `json:"provenance,omitempty"`
	Quality    string            `json:"quality,omitempty"`
	Status     string            `json:"status,omitempty"`
	Error      *ObservationError `json:"error,omitempty"`

	Model *ObservationModel `json:"model,omitempty"`
	Tool  *ObservationTool  `json:"tool,omitempty"`
	Usage *ObservationUsage `json:"usage,omitempty"`

	PayloadRef *ObservationPayloadRef `json:"payload_ref,omitempty"`
	Attributes map[string]any         `json:"attributes,omitempty"`

	// Content may contain a prompt, public response, tool input/result, or
	// error detail. It is deliberately excluded from JSON and SQLite envelope
	// columns so plaintext cannot be persisted accidentally.
	Content *ObservationContent `json:"-"`
}

type ObservationError struct {
	Code      string `json:"code,omitempty"`
	Message   string `json:"message,omitempty"`
	Retryable bool   `json:"retryable,omitempty"`
}

type ObservationModel struct {
	Provider        string `json:"provider,omitempty"`
	Requested       string `json:"requested,omitempty"`
	Resolved        string `json:"resolved,omitempty"`
	Protocol        string `json:"protocol,omitempty"`
	RequestID       string `json:"request_id,omitempty"`
	Attempt         int    `json:"attempt,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	ServiceTier     string `json:"service_tier,omitempty"`
	FinishReason    string `json:"finish_reason,omitempty"`
	TTFTMillis      int64  `json:"ttft_ms,omitempty"`
	DurationMillis  int64  `json:"duration_ms,omitempty"`
}

type ObservationTool struct {
	Name           string `json:"name,omitempty"`
	CallID         string `json:"call_id,omitempty"`
	Category       string `json:"category,omitempty"`
	DurationMillis int64  `json:"duration_ms,omitempty"`
	InputBytes     int64  `json:"input_bytes,omitempty"`
	OutputBytes    int64  `json:"output_bytes,omitempty"`
}

type ObservationUsage struct {
	InputTokens      int64   `json:"input_tokens,omitempty"`
	OutputTokens     int64   `json:"output_tokens,omitempty"`
	CacheReadTokens  int64   `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64   `json:"cache_write_tokens,omitempty"`
	ReasoningTokens  int64   `json:"reasoning_tokens,omitempty"`
	ToolTokens       int64   `json:"tool_tokens,omitempty"`
	TotalTokens      int64   `json:"total_tokens,omitempty"`
	CostUSD          float64 `json:"cost_usd,omitempty"`
	Cumulative       bool    `json:"cumulative,omitempty"`
}

type ObservationPayloadRef struct {
	ID             string    `json:"id"`
	Storage        string    `json:"storage,omitempty"`
	ContentType    string    `json:"content_type,omitempty"`
	KeyID          string    `json:"key_id,omitempty"`
	OriginalBytes  int64     `json:"original_bytes,omitempty"`
	StoredBytes    int64     `json:"stored_bytes,omitempty"`
	Redacted       bool      `json:"redacted"`
	ExpiresAt      time.Time `json:"expires_at,omitempty"`
	SourcePath     string    `json:"source_path,omitempty"`
	SourceOffset   int64     `json:"source_offset,omitempty"`
	SourceLength   int64     `json:"source_length,omitempty"`
	SourceIdentity string    `json:"source_identity,omitempty"`
	SourceSHA256   string    `json:"source_sha256,omitempty"`
	SourceRuntime  string    `json:"source_runtime,omitempty"`
	SourceClass    string    `json:"source_class,omitempty"`
	// SourceContentSHA256 identifies the exact public-content candidate inside
	// the source JSONL record before redaction. New file references use it so
	// ingest remains cheap; redaction is applied only when content is expanded.
	SourceContentSHA256 string `json:"source_content_sha256,omitempty"`
	// ContentSHA256 is the post-redaction checksum retained for references
	// migrated from legacy encrypted SQLite payloads.
	ContentSHA256 string `json:"content_sha256,omitempty"`
}

const ObservationPayloadStorageTranscriptFile = "transcript_file"

// ObservationContentSource points at the stable JSONL record from which an
// ephemeral content value was derived. It is process-local until the recorder
// verifies and promotes it into an ObservationPayloadRef.
type ObservationContentSource struct {
	Storage  string
	Path     string
	Offset   int64
	Length   int64
	Identity string
	SHA256   string
	Runtime  string
	Class    string
}

// ObservationContent is ephemeral input for a secure recorder. KnownSecrets
// augments the recorder's global secret list for this payload only.
type ObservationContent struct {
	ContentType  string
	Data         []byte
	KnownSecrets []string
	Source       *ObservationContentSource
}

// Normalize fills safe defaults and IDs. It never changes an already supplied
// correlation ID, making replay and backfill idempotent.
func (e *ObservationEnvelope) Normalize() {
	if e.Version == "" {
		e.Version = ObservationEnvelopeVersion
	}
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	} else {
		e.Time = e.Time.UTC()
	}
	if e.TraceID == "" {
		e.TraceID = NewObservationTraceID()
	}
	if e.SpanID == "" {
		e.SpanID = NewObservationSpanID()
	}
	if e.EventID == "" {
		e.EventID = NewObservationEventID()
	}
	if e.Lifecycle == "" {
		e.Lifecycle = ObservationLifecycleEvent
	}
	if e.Quality == "" {
		e.Quality = ObservationQualityComplete
	}
	if e.Status == "" {
		e.Status = ObservationStatusUnset
	}
	if e.Usage != nil && e.Usage.TotalTokens == 0 {
		e.Usage.TotalTokens = e.Usage.InputTokens + e.Usage.OutputTokens + e.Usage.CacheReadTokens + e.Usage.CacheWriteTokens
	}
}

func (e ObservationEnvelope) Validate() error {
	if e.Version != ObservationEnvelopeVersion {
		return fmt.Errorf("unsupported observation envelope version %q", e.Version)
	}
	if strings.TrimSpace(e.EventID) == "" || strings.TrimSpace(e.TraceID) == "" || strings.TrimSpace(e.SpanID) == "" {
		return errors.New("observation event_id, trace_id, and span_id are required")
	}
	if strings.TrimSpace(e.Kind) == "" {
		return errors.New("observation kind is required")
	}
	return nil
}

func NewObservationTraceID() string { return randomHexID(16) }
func NewObservationSpanID() string  { return randomHexID(8) }
func NewObservationEventID() string { return "obs_" + randomHexID(16) }

func randomHexID(size int) string {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure is exceptionally rare. A time-based value keeps
		// the call usable while retaining process-level uniqueness.
		return hex.EncodeToString([]byte(fmt.Sprintf("%x", time.Now().UnixNano())))
	}
	return hex.EncodeToString(b)
}

// ObservationHandler is a bus subscriber. Handlers should treat envelopes as
// immutable and return quickly; durable or remote work should use an outbox.
type ObservationHandler func(context.Context, ObservationEnvelope) error

type observationSubscriber struct {
	id      uint64
	name    string
	handler ObservationHandler
}

// ObservationBus is a concurrency-safe, ordered, multi-subscriber event bus.
// A subscriber panic or error never prevents later subscribers from running.
type ObservationBus struct {
	mu          sync.RWMutex
	nextID      uint64
	subscribers map[uint64]observationSubscriber
}

func NewObservationBus() *ObservationBus {
	return &ObservationBus{subscribers: make(map[uint64]observationSubscriber)}
}

// Subscribe registers a named handler and returns an idempotent unsubscribe
// function. The name is included in delivery errors for diagnostics.
func (b *ObservationBus) Subscribe(name string, handler ObservationHandler) func() {
	if b == nil || handler == nil {
		return func() {}
	}
	b.mu.Lock()
	b.nextID++
	id := b.nextID
	b.subscribers[id] = observationSubscriber{id: id, name: name, handler: handler}
	b.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subscribers, id)
			b.mu.Unlock()
		})
	}
}

// Publish delivers an envelope to a stable subscriber snapshot in
// subscription order. Errors are joined after every subscriber has run.
func (b *ObservationBus) Publish(ctx context.Context, envelope ObservationEnvelope) error {
	if b == nil {
		return nil
	}
	envelope.Normalize()
	if err := envelope.Validate(); err != nil {
		return err
	}
	b.mu.RLock()
	subs := make([]observationSubscriber, 0, len(b.subscribers))
	for _, sub := range b.subscribers {
		subs = append(subs, sub)
	}
	b.mu.RUnlock()
	// Map iteration is deliberately normalized so event ordering is stable.
	for i := 1; i < len(subs); i++ {
		for j := i; j > 0 && subs[j-1].id > subs[j].id; j-- {
			subs[j-1], subs[j] = subs[j], subs[j-1]
		}
	}
	var errs []error
	for _, sub := range subs {
		if err := callObservationHandler(ctx, sub, envelope); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Observe aliases Publish so a bus can itself be subscribed to another bus.
func (b *ObservationBus) Observe(ctx context.Context, envelope ObservationEnvelope) error {
	return b.Publish(ctx, envelope)
}

func (b *ObservationBus) SubscriberCount() int {
	if b == nil {
		return 0
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subscribers)
}

func callObservationHandler(ctx context.Context, sub observationSubscriber, envelope ObservationEnvelope) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("observation subscriber %q panicked: %v", sub.name, recovered)
		}
	}()
	if err := sub.handler(ctx, envelope); err != nil {
		return fmt.Errorf("observation subscriber %q: %w", sub.name, err)
	}
	return nil
}
