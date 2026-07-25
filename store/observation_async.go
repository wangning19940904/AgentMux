package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

const (
	observationCriticalQueueCapacity = 8192
	observationNormalQueueCapacity   = 32768
	observationFlushInterval         = 100 * time.Millisecond
	observationCriticalFlushInterval = 20 * time.Millisecond
	observationAggregateInterval     = 500 * time.Millisecond
	observationCoalesceWindow        = 250 * time.Millisecond
	observationCoalesceBytes         = 32 << 10
)

type ObservationRecorderStats struct {
	QueueDepth       int64     `json:"queue_depth"`
	CriticalDepth    int64     `json:"critical_depth"`
	NormalDepth      int64     `json:"normal_depth"`
	Enqueued         uint64    `json:"enqueued"`
	Inserted         uint64    `json:"inserted"`
	Deduplicated     uint64    `json:"deduplicated"`
	Coalesced        uint64    `json:"coalesced"`
	Dropped          uint64    `json:"dropped"`
	CriticalOverflow uint64    `json:"critical_overflow"`
	Retries          uint64    `json:"retries"`
	Batches          uint64    `json:"batches"`
	LastFlushAt      time.Time `json:"last_flush_at,omitempty"`
	LastFlushError   string    `json:"last_flush_error,omitempty"`
}

type observationQueuedEvent struct {
	envelope   core.ObservationEnvelope
	critical   bool
	enqueuedAt time.Time
}

type observationAsyncWriter struct {
	recorder *ObservationRecorder

	mu            sync.Mutex
	critical      []*observationQueuedEvent
	normal        []*observationQueuedEvent
	coalesced     map[string]*observationQueuedEvent
	closed        bool
	lastFlushAt   time.Time
	lastFlushErr  string
	normalCounter uint64
	notify        chan struct{}
	stop          chan struct{}
	done          chan struct{}
	closeOnce     sync.Once

	enqueued         atomic.Uint64
	inserted         atomic.Uint64
	deduplicated     atomic.Uint64
	coalescedCount   atomic.Uint64
	dropped          atomic.Uint64
	criticalOverflow atomic.Uint64
	retries          atomic.Uint64
	batches          atomic.Uint64
}

func (r *ObservationRecorder) enableAsync() {
	if r == nil || r.store == nil || !r.store.IsPostgres() || r.async != nil {
		return
	}
	writer := &observationAsyncWriter{
		recorder:  r,
		coalesced: map[string]*observationQueuedEvent{},
		notify:    make(chan struct{}, 1),
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
	r.async = writer
	go writer.run()
}

func (r *ObservationRecorder) Async() bool {
	return r != nil && r.async != nil
}

func (r *ObservationRecorder) SetAfterRecord(handler func(context.Context, core.ObservationEnvelope) error) {
	if r != nil {
		r.afterRecord = handler
	}
}

// BindContext flushes and stops the asynchronous writer when the runtime ends.
func (r *ObservationRecorder) BindContext(ctx context.Context) {
	if r == nil || r.async == nil || ctx == nil {
		return
	}
	go func() {
		<-ctx.Done()
		flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = r.Close(flushCtx)
	}()
}

func (r *ObservationRecorder) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	defer r.clearCachedObservationDataKeys()
	if r.async == nil {
		return nil
	}
	r.async.closeOnce.Do(func() { close(r.async.stop) })
	select {
	case <-r.async.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *ObservationRecorder) clearCachedObservationDataKeys() {
	r.keyMu.Lock()
	defer r.keyMu.Unlock()
	for keyID, key := range r.dataKeys {
		clearObservationBytes(key)
		delete(r.dataKeys, keyID)
	}
}

func (r *ObservationRecorder) Stats() ObservationRecorderStats {
	if r == nil || r.async == nil {
		return ObservationRecorderStats{}
	}
	return r.async.stats()
}

func cloneObservationEnvelopeForQueue(envelope core.ObservationEnvelope) core.ObservationEnvelope {
	envelope.Provenance = append([]string(nil), envelope.Provenance...)
	envelope.Attributes = cloneObservationAttributes(envelope.Attributes)
	if envelope.Content != nil {
		content := *envelope.Content
		content.Data = append([]byte(nil), content.Data...)
		content.KnownSecrets = append([]string(nil), content.KnownSecrets...)
		if content.Source != nil {
			source := *content.Source
			content.Source = &source
		}
		envelope.Content = &content
	}
	if envelope.Error != nil {
		value := *envelope.Error
		envelope.Error = &value
	}
	if envelope.Model != nil {
		value := *envelope.Model
		envelope.Model = &value
	}
	if envelope.Tool != nil {
		value := *envelope.Tool
		envelope.Tool = &value
	}
	if envelope.Usage != nil {
		value := *envelope.Usage
		envelope.Usage = &value
	}
	return envelope
}

func (w *observationAsyncWriter) enqueue(envelope core.ObservationEnvelope) {
	if w == nil {
		return
	}
	now := time.Now()
	item := &observationQueuedEvent{envelope: envelope, critical: criticalObservation(envelope), enqueuedAt: now}
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		w.dropped.Add(1)
		return
	}
	if item.critical {
		if len(w.critical) >= observationCriticalQueueCapacity {
			w.mu.Unlock()
			w.criticalOverflow.Add(1)
			w.dropped.Add(1)
			return
		}
		w.critical = append(w.critical, item)
	} else {
		if w.tryCoalesceLocked(item) {
			w.mu.Unlock()
			w.coalescedCount.Add(1)
			return
		}
		depth := len(w.normal)
		w.normalCounter++
		switch {
		case depth >= observationNormalQueueCapacity:
			w.mu.Unlock()
			w.dropped.Add(1)
			return
		case depth >= observationNormalQueueCapacity*95/100 && w.normalCounter%20 != 0:
			w.mu.Unlock()
			w.dropped.Add(1)
			return
		case depth >= observationNormalQueueCapacity*80/100 && w.normalCounter%5 != 0:
			w.mu.Unlock()
			w.dropped.Add(1)
			return
		}
		w.normal = append(w.normal, item)
		if key := coalesceObservationKey(envelope); key != "" {
			w.coalesced[key] = item
		}
	}
	w.mu.Unlock()
	w.enqueued.Add(1)
	select {
	case w.notify <- struct{}{}:
	default:
	}
}

func (w *observationAsyncWriter) tryCoalesceLocked(item *observationQueuedEvent) bool {
	key := coalesceObservationKey(item.envelope)
	if key == "" {
		return false
	}
	existing := w.coalesced[key]
	if existing == nil || item.enqueuedAt.Sub(existing.enqueuedAt) > observationCoalesceWindow {
		return false
	}
	size := 0
	if existing.envelope.Content != nil {
		size += len(existing.envelope.Content.Data)
	}
	if item.envelope.Content != nil {
		size += len(item.envelope.Content.Data)
	}
	if size > observationCoalesceBytes {
		return false
	}
	attributes := cloneObservationAttributes(existing.envelope.Attributes)
	count, _ := attributes["coalesced_event_count"].(int)
	if count == 0 {
		count = 1
	}
	attributes["coalesced_event_count"] = count + 1
	attributes["coalesced_last_event_id"] = item.envelope.EventID
	attributes["coalesced_last_event_time"] = item.envelope.Time.UTC().Format(time.RFC3339Nano)
	existing.envelope.Attributes = attributes
	if existing.envelope.Content != nil && item.envelope.Content != nil {
		existing.envelope.Content.Data = append(existing.envelope.Content.Data, item.envelope.Content.Data...)
	}
	return true
}

func coalesceObservationKey(envelope core.ObservationEnvelope) string {
	kind := strings.ToLower(envelope.Kind)
	if !strings.Contains(kind, "transcript") && !strings.Contains(kind, "progress") && !strings.Contains(kind, "delta") {
		return ""
	}
	return envelope.TraceID + "\x00" + envelope.SpanID + "\x00" + envelope.Kind
}

func criticalObservation(envelope core.ObservationEnvelope) bool {
	if envelope.Lifecycle == core.ObservationLifecycleEnd || observationTerminalStatus(envelope.Status) ||
		envelope.Error != nil || envelope.Usage != nil {
		return true
	}
	kind := strings.ToLower(envelope.Kind)
	return strings.Contains(kind, "model.response") || strings.Contains(kind, "tool.result") ||
		strings.Contains(kind, "error") || strings.Contains(kind, "cancel")
}

func (w *observationAsyncWriter) run() {
	defer close(w.done)
	flushTicker := time.NewTicker(observationFlushInterval)
	criticalTicker := time.NewTicker(observationCriticalFlushInterval)
	aggregateTicker := time.NewTicker(observationAggregateInterval)
	defer flushTicker.Stop()
	defer criticalTicker.Stop()
	defer aggregateTicker.Stop()
	backoff := 100 * time.Millisecond
	for {
		select {
		case <-w.stop:
			w.mu.Lock()
			w.closed = true
			w.mu.Unlock()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			for {
				batch := w.takeBatch()
				if len(batch) == 0 {
					break
				}
				if err := w.flush(ctx, batch); err != nil {
					break
				}
			}
			_, _ = w.recorder.store.MaterializeDirtyObservationTraces(ctx, 500)
			cancel()
			return
		case <-w.notify:
			if w.depth() < observationWriteBatchSize {
				continue
			}
		case <-criticalTicker.C:
			if !w.hasCriticalTerminal() {
				continue
			}
		case <-flushTicker.C:
		case <-aggregateTicker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_, _ = w.recorder.store.MaterializeDirtyObservationTraces(ctx, 500)
			cancel()
			continue
		}
		batch := w.takeBatch()
		if len(batch) == 0 {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := w.flush(ctx, batch)
		cancel()
		if err != nil {
			w.retries.Add(1)
			w.requeue(batch)
			select {
			case <-w.stop:
			case <-time.After(backoff):
			}
			backoff = min(5*time.Second, backoff*2)
		} else {
			backoff = 100 * time.Millisecond
		}
	}
}

func (w *observationAsyncWriter) takeBatch() []*observationQueuedEvent {
	w.mu.Lock()
	defer w.mu.Unlock()
	maximum := observationWriteBatchSize
	batch := make([]*observationQueuedEvent, 0, maximum)
	for len(batch) < maximum && len(w.critical) > 0 {
		item := w.critical[0]
		w.critical = w.critical[1:]
		batch = append(batch, item)
	}
	for len(batch) < maximum && len(w.normal) > 0 {
		item := w.normal[0]
		w.normal = w.normal[1:]
		if key := coalesceObservationKey(item.envelope); key != "" && w.coalesced[key] == item {
			delete(w.coalesced, key)
		}
		batch = append(batch, item)
	}
	return batch
}

func (w *observationAsyncWriter) flush(ctx context.Context, batch []*observationQueuedEvent) error {
	envelopes := make([]core.ObservationEnvelope, 0, len(batch))
	originals := make(map[string]core.ObservationEnvelope, len(batch))
	payloadErrors := make([]error, 0)
	seen := make(map[string]bool, len(batch)*2)
	for _, item := range batch {
		eventKey := "event:" + item.envelope.EventID
		dedupeKey := ""
		if item.envelope.DedupeKey != "" {
			dedupeKey = "dedupe:" + item.envelope.DedupeKey
		}
		if seen[eventKey] || (dedupeKey != "" && seen[dedupeKey]) {
			w.deduplicated.Add(1)
			continue
		}
		seen[eventKey] = true
		if dedupeKey != "" {
			seen[dedupeKey] = true
		}
		original := item.envelope
		if suppressHiddenReasoningContent(original) {
			original.Content = nil
			original.Attributes = cloneObservationAttributes(original.Attributes)
			original.Attributes["content_capture"] = "suppressed_hidden_reasoning"
		}
		metadata := original
		metadata.Content = nil
		envelope, err := w.recorder.secureObservationEnvelope(ctx, metadata)
		if err != nil {
			payloadErrors = append(payloadErrors, err)
		}
		envelopes = append(envelopes, envelope)
		originals[envelope.EventID] = original
	}
	inserted, err := w.recorder.store.recordObservationBatch(ctx, envelopes)
	if err != nil {
		w.setLastFlush(err)
		return errors.Join(append(payloadErrors, err)...)
	}
	for _, envelope := range envelopes {
		if !inserted[envelope.EventID] {
			w.deduplicated.Add(1)
			continue
		}
		secured := envelope
		if original := originals[envelope.EventID]; original.Content != nil {
			var secureErr error
			secured, secureErr = w.recorder.secureObservationEnvelope(ctx, original)
			if secureErr != nil {
				payloadErrors = append(payloadErrors, secureErr)
			}
			if err := w.recorder.store.attachObservationPayload(ctx, secured); err != nil {
				payloadErrors = append(payloadErrors, err)
				if secured.PayloadRef != nil {
					if cleanupErr := w.recorder.store.deleteObservationPayloadIfUnreferenced(ctx, secured.PayloadRef.ID); cleanupErr != nil {
						payloadErrors = append(payloadErrors, cleanupErr)
					}
					secured.PayloadRef = nil
				}
			}
		}
		w.inserted.Add(1)
		if w.recorder.afterRecord != nil {
			if err := w.recorder.afterRecord(ctx, secured); err != nil {
				payloadErrors = append(payloadErrors, err)
			}
		}
	}
	w.batches.Add(1)
	w.setLastFlush(errors.Join(payloadErrors...))
	return nil
}

func (w *observationAsyncWriter) requeue(batch []*observationQueuedEvent) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for index := len(batch) - 1; index >= 0; index-- {
		item := batch[index]
		if item.critical {
			if len(w.critical) < observationCriticalQueueCapacity {
				w.critical = append([]*observationQueuedEvent{item}, w.critical...)
			} else {
				w.criticalOverflow.Add(1)
				w.dropped.Add(1)
			}
		} else if len(w.normal) < observationNormalQueueCapacity {
			w.normal = append([]*observationQueuedEvent{item}, w.normal...)
		} else {
			w.dropped.Add(1)
		}
	}
}

func (w *observationAsyncWriter) depth() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.critical) + len(w.normal)
}

func (w *observationAsyncWriter) hasCriticalTerminal() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.critical) > 0
}

func (w *observationAsyncWriter) setLastFlush(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lastFlushAt = time.Now().UTC()
	if err == nil {
		w.lastFlushErr = ""
	} else {
		w.lastFlushErr = err.Error()
	}
}

func (w *observationAsyncWriter) stats() ObservationRecorderStats {
	w.mu.Lock()
	criticalDepth, normalDepth := len(w.critical), len(w.normal)
	lastFlushAt, lastFlushErr := w.lastFlushAt, w.lastFlushErr
	w.mu.Unlock()
	return ObservationRecorderStats{
		QueueDepth: int64(criticalDepth + normalDepth), CriticalDepth: int64(criticalDepth), NormalDepth: int64(normalDepth),
		Enqueued: w.enqueued.Load(), Inserted: w.inserted.Load(), Deduplicated: w.deduplicated.Load(),
		Coalesced: w.coalescedCount.Load(), Dropped: w.dropped.Load(), CriticalOverflow: w.criticalOverflow.Load(),
		Retries: w.retries.Load(), Batches: w.batches.Load(), LastFlushAt: lastFlushAt, LastFlushError: lastFlushErr,
	}
}
