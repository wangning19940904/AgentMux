package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// historySearchSchema is migration 38: the keyword index of history search
// (docs/architecture/history-search.md §3). terms holds the document's
// search keys, tokenized in Go (historysearch.Terms): lower-cased words and
// pairs of Chinese characters. A GIN index over the array finds every
// document holding any of a query's keys; no extension is needed.
const historySearchSchema = `
CREATE TABLE IF NOT EXISTS history_search_docs (
	id TEXT PRIMARY KEY,
	kind TEXT NOT NULL,
	agent_id TEXT NOT NULL DEFAULT '',
	memory_scope TEXT NOT NULL DEFAULT '',
	private_to TEXT NOT NULL DEFAULT '',
	channel_id TEXT NOT NULL DEFAULT '',
	chat_id TEXT NOT NULL DEFAULT '',
	conversation_key TEXT NOT NULL DEFAULT '',
	user_id TEXT NOT NULL DEFAULT '',
	user_name TEXT NOT NULL DEFAULT '',
	trace_id TEXT NOT NULL DEFAULT '',
	content TEXT NOT NULL DEFAULT '',
	terms TEXT[] NOT NULL DEFAULT '{}',
	occurred_at TIMESTAMPTZ NOT NULL,
	expires_at TIMESTAMPTZ,
	source_version TEXT NOT NULL DEFAULT '',
	indexed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_history_search_terms ON history_search_docs USING gin (terms);
CREATE INDEX IF NOT EXISTS idx_history_search_agent ON history_search_docs(agent_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_history_search_expiry ON history_search_docs(expires_at) WHERE expires_at IS NOT NULL;
`

var errHistorySearchNeedsPostgres = errors.New("history search needs the PostgreSQL store")

// PutHistoryDocs inserts or replaces documents of the history search index.
func (s *Store) PutHistoryDocs(ctx context.Context, docs []core.HistoryDoc) error {
	if s.dialect != DialectPostgres {
		return errHistorySearchNeedsPostgres
	}
	if len(docs) == 0 {
		return nil
	}
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, doc := range docs {
		terms := append(append([]string{}, doc.Terms...), historyScopeKeys(doc.Kind, doc.AgentID)...)
		var expires any
		if !doc.ExpiresAt.IsZero() {
			expires = doc.ExpiresAt.UTC()
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO history_search_docs
			(id,kind,agent_id,memory_scope,private_to,channel_id,chat_id,conversation_key,user_id,user_name,
			 trace_id,content,terms,occurred_at,expires_at,source_version,indexed_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?::text[],?,?,?,now())
			ON CONFLICT(id) DO UPDATE SET kind=excluded.kind,agent_id=excluded.agent_id,
			memory_scope=excluded.memory_scope,private_to=excluded.private_to,channel_id=excluded.channel_id,
			chat_id=excluded.chat_id,conversation_key=excluded.conversation_key,user_id=excluded.user_id,
			user_name=excluded.user_name,trace_id=excluded.trace_id,content=excluded.content,terms=excluded.terms,
			occurred_at=excluded.occurred_at,expires_at=excluded.expires_at,source_version=excluded.source_version,
			indexed_at=now()`,
			doc.ID, doc.Kind, doc.AgentID, doc.MemoryScope, doc.PrivateTo, doc.ChannelID, doc.ChatID,
			doc.ConversationKey, doc.UserID, doc.UserName, doc.TraceID, strings.ToValidUTF8(strings.ReplaceAll(doc.Content, "\x00", ""), ""),
			terms, doc.OccurredAt.UTC(), expires, doc.SourceVersion); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteHistoryDocs removes documents by id.
func (s *Store) DeleteHistoryDocs(ctx context.Context, ids []string) error {
	if s.dialect != DialectPostgres {
		return errHistorySearchNeedsPostgres
	}
	if len(ids) == 0 {
		return nil
	}
	_, err := s.writer.ExecContext(ctx, `DELETE FROM history_search_docs WHERE id = ANY(?::text[])`, ids)
	return err
}

// History scope keys sit in terms beside the words, so the GIN index
// narrows a search to one list and one agent before any row is read. Words
// never start with "@" (historysearch.Terms keeps letters and digits only).
const (
	historyListMemory    = "@list:memory"
	historyListOriginals = "@list:originals"
	historyAgentKey      = "@agent:"
)

// historyCandidates bounds the documents one wording ranks: the newest that
// qualify. A wording common enough to exceed it is answered from recent
// history, which is where a common word's best match usually is.
const historyCandidates = 1000

func historyList(kind string) string {
	if kind == core.HistoryKindMemory || kind == core.HistoryKindSummary {
		return historyListMemory
	}
	return historyListOriginals
}

func historyScopeKeys(kind, agentID string) []string {
	keys := []string{historyList(kind)}
	if agentID != "" {
		keys = append(keys, historyAgentKey+agentID)
	}
	return keys
}

// QueryHistoryDocs finds the documents of one wording that the scope may
// see: those holding at least MinMatched of its terms, or the whole phrase.
// Of the newest historyCandidates of them, the phrase matches rank first,
// then the documents holding more terms, then the newer ones. Kinds must all
// belong to one list (memory, or originals).
func (s *Store) QueryHistoryDocs(ctx context.Context, query core.HistoryDocQuery) ([]core.HistoryDocHit, error) {
	if s.dialect != DialectPostgres {
		return nil, errHistorySearchNeedsPostgres
	}
	if len(query.Terms) == 0 || len(query.Kinds) == 0 {
		return nil, nil
	}
	list := historyList(query.Kinds[0])
	for _, kind := range query.Kinds[1:] {
		if historyList(kind) != list {
			return nil, errors.New("history search kinds of one query must belong to one list")
		}
	}
	now := query.Now
	if now.IsZero() {
		now = time.Now()
	}
	limit := query.Limit
	if limit <= 0 {
		limit = 100
	}
	scope := query.Scope
	scopes := scope.MemoryScopes
	if scopes == nil {
		scopes = []string{}
	}
	required := []string{list}
	var scopeCondition string
	var scopeArgs []any
	switch {
	case scope.Admin && scope.AgentID == "":
		// The Console administrator without an agent filter sees everything.
		scopeCondition = "TRUE"
	case scope.Admin && list == historyListOriginals:
		required = append(required, historyAgentKey+scope.AgentID)
		scopeCondition = "d.agent_id = ?"
		scopeArgs = []any{scope.AgentID}
	case scope.Admin:
		scopeCondition = "(d.agent_id = ? OR (d.memory_scope <> '' AND d.memory_scope = ANY(?::text[])))"
		scopeArgs = []any{scope.AgentID, scopes}
	case scope.AgentID == "":
		// An agent's search names its agent; without one it sees nothing.
		return nil, nil
	case list == historyListOriginals:
		// An agent sees its own turns, except the private chats of anyone but
		// the person it works for now.
		required = append(required, historyAgentKey+scope.AgentID)
		scopeCondition = "d.agent_id = ? AND (d.private_to = '' OR d.private_to = ?)"
		scopeArgs = []any{scope.AgentID, scope.PersonID}
	default:
		// The memory scopes it reads, and its own summaries, private ones
		// only of the person it works for now.
		scopeCondition = `((d.memory_scope <> '' AND d.memory_scope = ANY(?::text[]))
			OR (d.agent_id = ? AND (d.private_to = '' OR d.private_to = ?)))`
		scopeArgs = []any{scopes, scope.AgentID, scope.PersonID}
	}
	args := []any{query.Terms, query.Phrase, query.Phrase, query.Terms, required, query.Kinds, now.UTC()}
	args = append(args, scopeArgs...)
	args = append(args, query.MinMatched, historyCandidates, limit)
	rows, err := s.db.QueryContext(ctx, `SELECT id,kind,agent_id,memory_scope,channel_id,chat_id,conversation_key,
		user_id,user_name,trace_id,content,occurred_at,matched,phrase FROM (
			SELECT d.id,d.kind,d.agent_id,d.memory_scope,d.channel_id,d.chat_id,d.conversation_key,
				d.user_id,d.user_name,d.trace_id,d.content,d.occurred_at,m.matched,p.phrase
			FROM history_search_docs d
			CROSS JOIN LATERAL (SELECT count(*) AS matched FROM unnest(d.terms) term WHERE term = ANY(?::text[])) m
			CROSS JOIN LATERAL (SELECT ?::text <> '' AND strpos(lower(d.content), ?::text) > 0 AS phrase) p
			WHERE d.terms && ?::text[] AND d.terms @> ?::text[] AND d.kind = ANY(?::text[])
				AND (d.expires_at IS NULL OR d.expires_at > ?)
				AND `+scopeCondition+`
				AND (m.matched >= ? OR p.phrase)
			ORDER BY d.occurred_at DESC
			LIMIT ?
		) c
		ORDER BY c.phrase DESC, c.matched DESC, c.occurred_at DESC, c.id
		LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.HistoryDocHit
	for rows.Next() {
		var hit core.HistoryDocHit
		if err := rows.Scan(&hit.ID, &hit.Kind, &hit.AgentID, &hit.MemoryScope, &hit.ChannelID, &hit.ChatID,
			&hit.ConversationKey, &hit.UserID, &hit.UserName, &hit.TraceID, &hit.Content, &hit.OccurredAt,
			&hit.Matched, &hit.Phrase); err != nil {
			return nil, err
		}
		hit.OccurredAt = hit.OccurredAt.UTC()
		out = append(out, hit)
	}
	return out, rows.Err()
}

// StaleHistoryMemory returns the memory entries whose document is missing or
// older than the entry, and the memory documents whose entry is gone.
func (s *Store) StaleHistoryMemory(ctx context.Context) ([]*core.MemoryEntry, []string, error) {
	if s.dialect != DialectPostgres {
		return nil, nil, errHistorySearchNeedsPostgres
	}
	rows, err := s.db.QueryContext(ctx, `SELECT m.id,m.scope,m.content,m.tags,m.meta,m.created_at,m.updated_at
		FROM memory_entries m LEFT JOIN history_search_docs d ON d.id = 'mem:' || m.id
		WHERE d.id IS NULL OR d.source_version <> COALESCE(m.updated_at,'') OR d.memory_scope <> m.scope`)
	if err != nil {
		return nil, nil, err
	}
	var stale []*core.MemoryEntry
	for rows.Next() {
		entry, err := scanMemory(rows)
		if err != nil {
			rows.Close()
			return nil, nil, err
		}
		stale = append(stale, entry)
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}
	orphanRows, err := s.db.QueryContext(ctx, `SELECT d.id FROM history_search_docs d
		WHERE d.kind = 'memory' AND NOT EXISTS (SELECT 1 FROM memory_entries m WHERE 'mem:' || m.id = d.id)`)
	if err != nil {
		return nil, nil, err
	}
	defer orphanRows.Close()
	var orphans []string
	for orphanRows.Next() {
		var id string
		if err := orphanRows.Scan(&id); err != nil {
			return nil, nil, err
		}
		orphans = append(orphans, id)
	}
	return stale, orphans, orphanRows.Err()
}

// HistoryTurnBackfill returns the engine's recorded turn envelopes since a
// time that carry a payload and have no history document yet, oldest first.
func (s *Store) HistoryTurnBackfill(ctx context.Context, since time.Time, limit int) ([]core.ObservationEnvelope, error) {
	if s.dialect != DialectPostgres {
		return nil, errHistorySearchNeedsPostgres
	}
	rows, err := s.db.QueryContext(ctx, `SELECT e.envelope_json FROM observation_events e
		WHERE e.source = 'agentmux.internal' AND e.kind = 'agent.turn' AND e.lifecycle IN ('start','end')
		AND e.payload_id <> '' AND e.timestamp >= ?
		AND NOT EXISTS (SELECT 1 FROM history_search_docs d
			WHERE d.id = 'turn:' || e.trace_id || ':' || CASE WHEN e.lifecycle = 'start' THEN 'input' ELSE 'answer' END)
		ORDER BY e.timestamp LIMIT ?`, observationTime(since), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.ObservationEnvelope
	for rows.Next() {
		var raw sql.NullString
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var envelope core.ObservationEnvelope
		if err := json.Unmarshal([]byte(raw.String), &envelope); err != nil {
			continue
		}
		out = append(out, envelope)
	}
	return out, rows.Err()
}

// CleanupHistoryDocs deletes the documents that expired by now, in batches.
func (s *Store) CleanupHistoryDocs(ctx context.Context, now time.Time) (int64, error) {
	if s.dialect != DialectPostgres {
		return 0, errHistorySearchNeedsPostgres
	}
	return s.cleanupObservationRowsInBatches(ctx, `DELETE FROM history_search_docs WHERE id IN (
		SELECT id FROM history_search_docs WHERE expires_at IS NOT NULL AND expires_at <= ? LIMIT ?)`,
		now.UTC())
}
