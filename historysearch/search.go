// Package historysearch finds what an agent was told and what it remembers:
// keyword search over memory entries and the inputs and answers of past
// turns, in Chinese and English, without embeddings
// (docs/architecture/history-search.md).
//
// A search takes several wordings of one question. Each wording is matched
// on its own; the ranked lists are merged with reciprocal rank fusion, so a
// document several wordings find rises above one only a single wording finds.
// Results come back as two lists: memory (entries, and later summaries) for
// an overview, and originals (turn inputs and answers) for exact details.
package historysearch

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// Limits of one request.
const (
	maxWordings      = 8
	maxWordingRunes  = 200
	defaultListLimit = 8
	maxListLimit     = 30
	// candidatesPerWording bounds what each wording brings to the fusion.
	candidatesPerWording = 100
	// parallelQueries bounds the queries one search runs at once.
	parallelQueries = 4
)

// The two lists of a result and the kinds each holds.
var (
	memoryKinds   = []string{core.HistoryKindMemory, core.HistoryKindSummary}
	originalKinds = []string{core.HistoryKindTurnInput, core.HistoryKindTurnAnswer}
)

// Request is one search.
type Request struct {
	// Queries are wordings of one question: synonyms, translations, the
	// words a document would use. Up to eight are used.
	Queries []string `json:"queries"`
	// Around is a time the question names. It only boosts.
	Around TimeHint `json:"around,omitzero"`
	// Limit is the length of each list (default 8, at most 30).
	Limit int `json:"limit,omitempty"`
	// Scope says what the search may see; the caller fills it, never the
	// request body.
	Scope core.HistoryScope `json:"-"`
}

// Match is one result.
type Match struct {
	ID              string    `json:"id"`
	Kind            string    `json:"kind"`
	AgentID         string    `json:"agent_id,omitempty"`
	MemoryScope     string    `json:"memory_scope,omitempty"`
	ChannelID       string    `json:"channel_id,omitempty"`
	ChatID          string    `json:"chat_id,omitempty"`
	ConversationKey string    `json:"conversation_key,omitempty"`
	UserName        string    `json:"user_name,omitempty"`
	Time            time.Time `json:"time"`
	Snippet         string    `json:"snippet"`
	Score           float64   `json:"score"`
	// MatchedQueries are the indexes of the wordings that found it.
	MatchedQueries []int `json:"matched_queries"`
	// InTimeRange says the document falls in the time the question named.
	InTimeRange bool `json:"in_time_range,omitempty"`
}

// Result holds the two ranked lists.
type Result struct {
	Memory    []Match `json:"memory"`
	Originals []Match `json:"originals"`
	// Notes say what the search could not cover.
	Notes []string `json:"notes,omitempty"`
}

// ErrNoQuery is a request without a usable wording.
var ErrNoQuery = errors.New("give at least one query with a word or Chinese character in it")

// Index is the store side of history search.
type Index interface {
	PutHistoryDocs(context.Context, []core.HistoryDoc) error
	DeleteHistoryDocs(context.Context, []string) error
	QueryHistoryDocs(context.Context, core.HistoryDocQuery) ([]core.HistoryDocHit, error)
	// StaleHistoryMemory returns the memory entries whose document is
	// missing or older than the entry, and the ids of memory documents whose
	// entry is gone.
	StaleHistoryMemory(context.Context) ([]*core.MemoryEntry, []string, error)
	// HistoryTurnBackfill returns recorded turn envelopes since a time that
	// carry an encrypted payload and have no document yet, oldest first.
	HistoryTurnBackfill(context.Context, time.Time, int) ([]core.ObservationEnvelope, error)
	// CleanupHistoryDocs deletes documents that expired by now.
	CleanupHistoryDocs(context.Context, time.Time) (int64, error)
}

// Redactor removes credentials from text, given secrets known by value.
type Redactor func(text []byte, knownSecrets []string) []byte

// Options configure a Service.
type Options struct {
	Index Index
	Log   *slog.Logger
	Now   func() time.Time
	// Redact runs on everything indexed and on every snippet returned.
	Redact  Redactor
	Secrets []string
}

// Service searches the index and keeps it current.
type Service struct {
	index  Index
	log    *slog.Logger
	now    func() time.Time
	redact Redactor

	mu        sync.RWMutex
	secrets   []string
	originals originalsState
	payloads  PayloadReader
	queue     chan []core.HistoryDoc
	startOnce sync.Once
}

// originalsState says whether turns are being indexed, and for how long.
type originalsState struct {
	enabled   bool
	reason    string
	retention time.Duration
}

// New builds a Service. Turns are not indexed until EnableOriginals.
func New(options Options) *Service {
	if options.Log == nil {
		options.Log = slog.Default()
	}
	if options.Now == nil {
		options.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{
		index: options.Index, log: options.Log, now: options.Now, redact: options.Redact,
		secrets:   append([]string(nil), options.Secrets...),
		originals: originalsState{reason: "observability is off, so past turns are not indexed"},
		queue:     make(chan []core.HistoryDoc, 256),
	}
}

// Search runs every wording against both lists and fuses the rankings.
func (s *Service) Search(ctx context.Context, request Request) (Result, error) {
	if s == nil || s.index == nil {
		return Result{}, errors.New("history search is not available")
	}
	wordings := cleanWordings(request.Queries)
	if len(wordings) == 0 {
		return Result{}, ErrNoQuery
	}
	limit := request.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	limit = min(limit, maxListLimit)
	now := s.now()
	// Every wording of both lists is one query; they run side by side, a few
	// at a time, so a search costs about its slowest query.
	slots := make(chan struct{}, parallelQueries)
	var memory, originals []Match
	var memoryErr, originalsErr error
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		memory, memoryErr = s.searchList(ctx, wordings, memoryKinds, request, limit, now, slots)
	}()
	go func() {
		defer wait.Done()
		originals, originalsErr = s.searchList(ctx, wordings, originalKinds, request, limit, now, slots)
	}()
	wait.Wait()
	if err := errors.Join(memoryErr, originalsErr); err != nil {
		return Result{}, err
	}
	result := Result{Memory: memory, Originals: originals}
	s.mu.RLock()
	if !s.originals.enabled {
		result.Notes = append(result.Notes, s.originals.reason)
	} else {
		result.Notes = append(result.Notes, "originals cover turns since "+now.Add(-s.originals.retention).Format("2006-01-02")+"; older turns have expired")
	}
	s.mu.RUnlock()
	return result, nil
}

func (s *Service) searchList(ctx context.Context, wordings []string, kinds []string, request Request, limit int, now time.Time, slots chan struct{}) ([]Match, error) {
	found := make([][]core.HistoryDocHit, len(wordings))
	failed := make([]error, len(wordings))
	var wait sync.WaitGroup
	for i, wording := range wordings {
		terms := Terms(wording)
		wait.Add(1)
		go func() {
			defer wait.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			found[i], failed[i] = s.index.QueryHistoryDocs(ctx, core.HistoryDocQuery{
				Terms: terms, Phrase: phraseOf(wording), MinMatched: minMatched(len(terms)),
				Kinds: kinds, Scope: request.Scope, Limit: candidatesPerWording, Now: now,
			})
		}()
	}
	wait.Wait()
	if err := errors.Join(failed...); err != nil {
		return nil, err
	}
	docs := map[string]core.HistoryDocHit{}
	matchedBy := map[string][]int{}
	var lists [][]string
	var weights []float64
	for i, hits := range found {
		ranked := make([]string, 0, len(hits))
		for _, hit := range hits {
			if _, seen := docs[hit.ID]; !seen {
				docs[hit.ID] = hit
			}
			matchedBy[hit.ID] = append(matchedBy[hit.ID], i)
			ranked = append(ranked, hit.ID)
		}
		lists = append(lists, ranked)
		weights = append(weights, 1)
	}
	if request.Around.Valid() {
		ids := make([]string, 0, len(docs))
		times := make(map[string]time.Time, len(docs))
		for id, doc := range docs {
			ids = append(ids, id)
			times[id] = doc.OccurredAt
		}
		slices.Sort(ids)
		lists = append(lists, timeRanking(request.Around, ids, times))
		weights = append(weights, timeWeight)
	}
	fused := fuse(lists, weights)
	if len(fused) > limit {
		fused = fused[:limit]
	}
	out := make([]Match, 0, len(fused))
	for _, item := range fused {
		doc := docs[item.id]
		out = append(out, Match{
			ID: doc.ID, Kind: doc.Kind, AgentID: doc.AgentID, MemoryScope: doc.MemoryScope,
			ChannelID: doc.ChannelID, ChatID: doc.ChatID, ConversationKey: doc.ConversationKey,
			UserName: doc.UserName, Time: doc.OccurredAt,
			Snippet:        s.redactOutput(snippet(doc.Content, wordings)),
			Score:          math.Round(item.score*1e6) / 1e6,
			MatchedQueries: matchedBy[item.id],
			InTimeRange:    request.Around.Contains(doc.OccurredAt),
		})
	}
	return out, nil
}

// redactOutput redacts a snippet once more with the secrets known now: the
// index holds text redacted when it was written, and a secret configured
// since then must not come back either.
func (s *Service) redactOutput(text string) string {
	return string(s.redactBytes([]byte(text), nil))
}

func cleanWordings(queries []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, query := range queries {
		query = collapseSpace(query)
		if runes := []rune(query); len(runes) > maxWordingRunes {
			query = string(runes[:maxWordingRunes])
		}
		key := strings.ToLower(query)
		if query == "" || seen[key] || len(Terms(query)) == 0 {
			continue
		}
		seen[key] = true
		out = append(out, query)
		if len(out) == maxWordings {
			break
		}
	}
	return out
}

// phraseOf is a wording as the store compares it with content.
func phraseOf(wording string) string { return strings.ToLower(collapseSpace(wording)) }

// minMatched is how many of a wording's n terms a document must hold. One or
// two terms must all be there; longer wordings need three in five, so a
// natural question still finds a text that words it differently.
func minMatched(n int) int {
	if n <= 2 {
		return n
	}
	return int(math.Ceil(float64(n) * 0.6))
}
