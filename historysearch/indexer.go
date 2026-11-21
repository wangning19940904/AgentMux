package historysearch

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/wangning19940904/AgentMux/core"
)

const (
	// maxDocBytes caps the text kept for one document. A longer answer keeps
	// its beginning, where the point usually is.
	maxDocBytes = 64 << 10
	// maxDocTerms caps the terms indexed for one document.
	maxDocTerms = 8192
	// backfillBatch is how many recorded turns one backfill step decrypts.
	backfillBatch = 200
	// maintenanceInterval is how often memory is reconciled and expired
	// documents are deleted.
	maintenanceInterval = time.Hour
	// turnSource is the source of the turns the engine records itself.
	// Sessions AgentMux only watches (a person's own Claude Code, say)
	// belong to no agent and are not indexed.
	turnSource = "agentmux.internal"
)

// PayloadReader decrypts a recorded observation payload; the observation
// recorder is one.
type PayloadReader interface {
	ReadPayload(ctx context.Context, payloadID string) ([]byte, string, error)
}

// EnableOriginals starts indexing turns: their documents expire after
// retention, the observation content retention, so the index never keeps a
// turn longer than its recorded payload. payloads, when not nil, lets Start
// index the turns recorded before this version.
func (s *Service) EnableOriginals(retention time.Duration, payloads PayloadReader) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.originals = originalsState{enabled: true, retention: retention}
	s.payloads = payloads
}

// DisableOriginals records why turns are not indexed; a search says so.
func (s *Service) DisableOriginals(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.originals = originalsState{reason: reason}
	s.payloads = nil
}

// AddSecrets adds secrets known by value to the redaction of what is indexed
// and returned.
func (s *Service) AddSecrets(secrets []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secrets = append(s.secrets, secrets...)
}

func (s *Service) redactBytes(text []byte, extra []string) []byte {
	if s.redact == nil {
		return text
	}
	s.mu.RLock()
	secrets := append(append([]string(nil), s.secrets...), extra...)
	s.mu.RUnlock()
	return s.redact(text, secrets)
}

// Observe is an observation bus subscriber. It turns the start of an agent
// turn into a turn_input document and its end into a turn_answer document,
// and queues them for the writer. It never blocks the bus: when the queue is
// full the turn is dropped, and the next start's backfill picks it up.
func (s *Service) Observe(_ context.Context, envelope core.ObservationEnvelope) error {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	state := s.originals
	s.mu.RUnlock()
	if !state.enabled || envelope.Content == nil {
		return nil
	}
	doc, ok := s.turnDoc(envelope, envelope.Content.Data, envelope.Content.KnownSecrets, state.retention)
	if !ok {
		return nil
	}
	select {
	case s.queue <- []core.HistoryDoc{doc}:
	default:
		s.log.Warn("history search queue full; the turn is indexed at the next start", "trace_id", envelope.TraceID)
	}
	return nil
}

// turnDoc builds the document of one side of a turn, or reports that the
// envelope is not one.
func (s *Service) turnDoc(envelope core.ObservationEnvelope, raw []byte, secrets []string, retention time.Duration) (core.HistoryDoc, bool) {
	if envelope.Kind != "agent.turn" || envelope.Source != turnSource || strings.TrimSpace(envelope.AgentID) == "" {
		return core.HistoryDoc{}, false
	}
	var kind, suffix, text string
	switch envelope.Lifecycle {
	case core.ObservationLifecycleStart:
		kind, suffix = core.HistoryKindTurnInput, "input"
		text = core.ChannelPromptUserText(core.StripMemoryContext(string(raw)))
	case core.ObservationLifecycleEnd:
		// A failed turn's content is its error, not an answer.
		if envelope.Status == core.ObservationStatusError || envelope.Error != nil {
			return core.HistoryDoc{}, false
		}
		kind, suffix = core.HistoryKindTurnAnswer, "answer"
		text = string(raw)
	default:
		return core.HistoryDoc{}, false
	}
	text = truncateUTF8(string(s.redactBytes([]byte(strings.TrimSpace(text)), secrets)), maxDocBytes)
	attribute := func(key string) string {
		value, _ := envelope.Attributes[key].(string)
		return strings.TrimSpace(value)
	}
	userID := attribute("user_id")
	doc := core.HistoryDoc{
		ID: "turn:" + envelope.TraceID + ":" + suffix, Kind: kind,
		AgentID: envelope.AgentID, ChannelID: attribute("channel_id"), ChatID: attribute("chat_id"),
		ConversationKey: attribute("conversation_key"), UserID: userID, UserName: attribute("user_name"),
		TraceID: envelope.TraceID, Content: text, Terms: docTerms(text), OccurredAt: envelope.Time.UTC(),
	}
	if core.PrivateChatType(attribute("chat_type"), userID) {
		doc.PrivateTo = userID
	}
	if retention > 0 {
		doc.ExpiresAt = doc.OccurredAt.Add(retention)
	}
	return doc, true
}

// memoryDoc builds the document of a memory entry.
func (s *Service) memoryDoc(entry *core.MemoryEntry) core.HistoryDoc {
	text := truncateUTF8(string(s.redactBytes([]byte(strings.TrimSpace(entry.Content)), nil)), maxDocBytes)
	occurred := entry.UpdatedAt
	if occurred.IsZero() {
		occurred = entry.CreatedAt
	}
	return core.HistoryDoc{
		ID: core.HistoryMemoryDocID(entry.ID), Kind: core.HistoryKindMemory,
		MemoryScope: entry.Scope, Content: text, Terms: docTerms(text + " " + strings.Join(entry.Tags, " ")),
		OccurredAt: occurred.UTC(), SourceVersion: entry.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

// Put indexes documents another component writes, such as the summaries of
// a memory loop. Their content is redacted here like everything else.
func (s *Service) Put(ctx context.Context, docs []core.HistoryDoc) error {
	for i := range docs {
		docs[i].Content = truncateUTF8(string(s.redactBytes([]byte(docs[i].Content), nil)), maxDocBytes)
		docs[i].Terms = docTerms(docs[i].Content)
	}
	return s.index.PutHistoryDocs(ctx, docs)
}

func docTerms(text string) []string {
	terms := Terms(text)
	if len(terms) > maxDocTerms {
		terms = terms[:maxDocTerms]
	}
	return terms
}

func truncateUTF8(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

// Start runs the writer, then catches up: memory entries written while the
// daemon was down or by fleet sync, turns recorded before indexing existed,
// and expired documents. It returns at once; the work runs until ctx ends.
func (s *Service) Start(ctx context.Context) {
	if s == nil || s.index == nil {
		return
	}
	s.startOnce.Do(func() {
		go s.writer(ctx)
		go s.maintain(ctx)
	})
}

func (s *Service) writer(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case docs := <-s.queue:
			if err := s.index.PutHistoryDocs(ctx, docs); err != nil && ctx.Err() == nil {
				s.log.Warn("history search index write failed", "err", err)
			}
		}
	}
}

func (s *Service) maintain(ctx context.Context) {
	s.Maintain(ctx)
	ticker := time.NewTicker(maintenanceInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Maintain(ctx)
		}
	}
}

// Maintain reconciles memory, backfills turns and deletes expired documents
// once. Errors are logged: search keeps working on what is indexed.
func (s *Service) Maintain(ctx context.Context) {
	if err := s.ReconcileMemory(ctx); err != nil && ctx.Err() == nil {
		s.log.Warn("history search memory reconcile failed", "err", err)
	}
	if n, err := s.BackfillTurns(ctx); err != nil && ctx.Err() == nil {
		s.log.Warn("history search turn backfill failed", "indexed", n, "err", err)
	} else if n > 0 {
		s.log.Info("history search indexed recorded turns", "turns", n)
	}
	if _, err := s.index.CleanupHistoryDocs(ctx, s.now()); err != nil && ctx.Err() == nil {
		s.log.Warn("history search retention cleanup failed", "err", err)
	}
}

// ReconcileMemory makes the memory documents match memory_entries.
func (s *Service) ReconcileMemory(ctx context.Context) error {
	stale, orphans, err := s.index.StaleHistoryMemory(ctx)
	if err != nil {
		return err
	}
	if len(orphans) > 0 {
		if err := s.index.DeleteHistoryDocs(ctx, orphans); err != nil {
			return err
		}
	}
	docs := make([]core.HistoryDoc, 0, len(stale))
	for _, entry := range stale {
		docs = append(docs, s.memoryDoc(entry))
	}
	if len(docs) == 0 {
		return nil
	}
	return s.index.PutHistoryDocs(ctx, docs)
}

// BackfillTurns indexes recorded turns that have no document yet, within
// the content retention, by decrypting their payloads. It returns how many
// turn sides it indexed.
func (s *Service) BackfillTurns(ctx context.Context) (int, error) {
	s.mu.RLock()
	state, payloads := s.originals, s.payloads
	s.mu.RUnlock()
	if !state.enabled || payloads == nil {
		return 0, nil
	}
	since := s.now().Add(-state.retention)
	indexed := 0
	for ctx.Err() == nil {
		envelopes, err := s.index.HistoryTurnBackfill(ctx, since, backfillBatch)
		if err != nil {
			return indexed, err
		}
		if len(envelopes) == 0 {
			return indexed, nil
		}
		docs := make([]core.HistoryDoc, 0, len(envelopes))
		for _, envelope := range envelopes {
			var raw []byte
			if envelope.PayloadRef != nil {
				raw, _, err = payloads.ReadPayload(ctx, envelope.PayloadRef.ID)
			}
			if envelope.PayloadRef == nil || err != nil {
				// An unreadable payload still gets an empty document, so it is
				// not decrypted again at every start.
				raw = nil
			}
			doc, ok := s.turnDoc(envelope, raw, nil, state.retention)
			if !ok {
				kind := core.HistoryKindTurnAnswer
				if envelope.Lifecycle == core.ObservationLifecycleStart {
					kind = core.HistoryKindTurnInput
				}
				doc = core.HistoryDoc{
					ID: backfillDocID(envelope), Kind: kind, AgentID: envelope.AgentID,
					TraceID: envelope.TraceID, OccurredAt: envelope.Time.UTC(), ExpiresAt: envelope.Time.UTC().Add(state.retention),
				}
			}
			docs = append(docs, doc)
		}
		if err := s.index.PutHistoryDocs(ctx, docs); err != nil {
			return indexed, err
		}
		indexed += len(docs)
		if len(envelopes) < backfillBatch {
			return indexed, nil
		}
	}
	return indexed, ctx.Err()
}

// backfillDocID is the document id the store looks for to tell a recorded
// turn side was indexed.
func backfillDocID(envelope core.ObservationEnvelope) string {
	suffix := "answer"
	if envelope.Lifecycle == core.ObservationLifecycleStart {
		suffix = "input"
	}
	return fmt.Sprintf("turn:%s:%s", envelope.TraceID, suffix)
}

// IndexedMemory is a memory store whose writes reach the index at once.
// Writes that bypass it are picked up by ReconcileMemory.
type IndexedMemory struct {
	core.MemoryStore
	search *Service
}

// WrapMemory returns store with its writes indexed by s.
func (s *Service) WrapMemory(store core.MemoryStore) *IndexedMemory {
	return &IndexedMemory{MemoryStore: store, search: s}
}

// Put writes the entry, then indexes it.
func (m *IndexedMemory) Put(ctx context.Context, entry *core.MemoryEntry) (string, error) {
	id, err := m.MemoryStore.Put(ctx, entry)
	if err != nil || m.search == nil || m.search.index == nil {
		return id, err
	}
	if indexErr := m.search.index.PutHistoryDocs(ctx, []core.HistoryDoc{m.search.memoryDoc(entry)}); indexErr != nil {
		m.search.log.Warn("history search memory index failed; reconciled later", "id", id, "err", indexErr)
	}
	return id, nil
}

// Delete removes the entry, then its document.
func (m *IndexedMemory) Delete(ctx context.Context, id string) error {
	if err := m.MemoryStore.Delete(ctx, id); err != nil {
		return err
	}
	if m.search != nil && m.search.index != nil {
		if err := m.search.index.DeleteHistoryDocs(ctx, []string{core.HistoryMemoryDocID(id)}); err != nil {
			m.search.log.Warn("history search memory delete failed; reconciled later", "id", id, "err", err)
		}
	}
	return nil
}
