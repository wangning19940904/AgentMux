package historysearch

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// TestPostgresSearchLatency loads a busy machine's month of turns and times
// searches. It is slow, so it runs only with AGENTMUX_HISTORY_LATENCY set to
// the number of documents to load (for example 200000).
func TestPostgresSearchLatency(t *testing.T) {
	total, _ := strconv.Atoi(os.Getenv("AGENTMUX_HISTORY_LATENCY"))
	if total <= 0 {
		t.Skip("set AGENTMUX_HISTORY_LATENCY to the number of documents to load")
	}
	st, db := openPostgres(t)
	service := newTestService(t, st)
	ctx := context.Background()
	random := rand.New(rand.NewSource(1))
	// A Zipf-distributed vocabulary of 4000 Chinese words (pairs of common
	// Han characters) and 2000 English-like words, as chat text has; the
	// queried words are added at fixed rates below.
	var vocabulary []string
	for range 4000 {
		vocabulary = append(vocabulary, string([]rune{rune(0x4E00 + random.Intn(3000)), rune(0x4E00 + random.Intn(3000))}))
	}
	for i := range 2000 {
		vocabulary = append(vocabulary, fmt.Sprintf("w%sx%d", strings.Repeat("e", i%5), i))
	}
	random.Shuffle(len(vocabulary), func(i, j int) { vocabulary[i], vocabulary[j] = vocabulary[j], vocabulary[i] })
	zipf := rand.NewZipf(random, 1.07, 2, uint64(len(vocabulary)-1))
	planted := []struct {
		word string
		rate float64
	}{
		{"我们", 0.40}, // a stress case: a word in four documents of ten
		{"迁移", 0.03}, {"数据库", 0.02}, {"回滚", 0.01}, {"预案", 0.005},
		{"database", 0.02}, {"migration", 0.02}, {"rollback", 0.01}, {"postgres", 0.01},
		{"kubernetes", 0.001}, {"招聘", 0.001},
	}
	sentence := func() string {
		var words []string
		for range 8 + random.Intn(60) {
			words = append(words, vocabulary[zipf.Uint64()])
		}
		for _, p := range planted {
			if random.Float64() < p.rate {
				at := random.Intn(len(words) + 1)
				words = append(words[:at], append([]string{p.word}, words[at:]...)...)
			}
		}
		return strings.Join(words, " ")
	}
	agents, people := 20, 60
	start := time.Now()
	var loaded int
	if err := db.QueryRow(`SELECT count(*) FROM history_search_docs WHERE kind <> 'memory'`).Scan(&loaded); err != nil {
		t.Fatal(err)
	}
	if loaded >= total {
		total = 0
	}
	batch := make([]core.HistoryDoc, 0, 1000)
	for i := range total {
		agent := fmt.Sprintf("agent-%d", i%agents)
		person := fmt.Sprintf("ou_%d", random.Intn(people))
		text := sentence()
		doc := core.HistoryDoc{
			ID: fmt.Sprintf("turn:bench%d:answer", i), Kind: core.HistoryKindTurnAnswer, AgentID: agent,
			UserID: person, Content: text, Terms: docTerms(text),
			OccurredAt: testNow.Add(-time.Duration(random.Int63n(int64(30 * 24 * time.Hour)))),
		}
		if i%2 == 1 {
			doc.ID, doc.Kind = fmt.Sprintf("turn:bench%d:input", i), core.HistoryKindTurnInput
		}
		if random.Intn(3) == 0 {
			doc.PrivateTo = person
		}
		doc.ExpiresAt = doc.OccurredAt.Add(30 * 24 * time.Hour)
		batch = append(batch, doc)
		if len(batch) == cap(batch) || i == total-1 {
			if err := st.PutHistoryDocs(ctx, batch); err != nil {
				t.Fatal(err)
			}
			batch = batch[:0]
		}
	}
	for i := range min(total, 2000) {
		if err := st.PutHistoryDocs(ctx, []core.HistoryDoc{service.memoryDoc(&core.MemoryEntry{
			ID: fmt.Sprintf("m%d", i), Scope: fmt.Sprintf("agent:agent-%d", i%agents), Content: sentence(), UpdatedAt: testNow,
		})}); err != nil {
			t.Fatal(err)
		}
	}
	if total == 0 {
		t.Logf("reusing %d turn documents in schema %s", loaded, os.Getenv("AGENTMUX_HISTORY_KEEP_SCHEMA"))
	} else {
		t.Logf("loaded %d turn documents and 2000 memory entries in %s", total, time.Since(start).Round(time.Second))
	}
	if _, err := db.Exec(`ANALYZE history_search_docs`); err != nil {
		t.Fatal(err)
	}
	var tableSize string
	if err := db.QueryRow(`SELECT pg_size_pretty(pg_total_relation_size('history_search_docs'))`).Scan(&tableSize); err != nil {
		t.Fatal(err)
	}
	t.Logf("history_search_docs with indexes: %s", tableSize)

	cases := []struct {
		name    string
		scope   core.HistoryScope
		queries []string
		around  TimeHint
	}{
		{"agent, word in 40% of documents", agentScope("agent-3", "ou_7"), []string{"我们"}, TimeHint{}},
		{"agent, 2-char Chinese word", agentScope("agent-3", "ou_7"), []string{"迁移"}, TimeHint{}},
		{"agent, Chinese phrase", agentScope("agent-3", "ou_7"), []string{"数据库迁移回滚预案"}, TimeHint{}},
		{"agent, English words", agentScope("agent-3", "ou_7"), []string{"database migration rollback"}, TimeHint{}},
		{"agent, 3 wordings + time", agentScope("agent-3", "ou_7"), []string{"数据库迁移", "database migration", "postgres upgrade"},
			TimeHint{From: testNow.Add(-10 * 24 * time.Hour), To: testNow.Add(-8 * 24 * time.Hour)}},
		{"agent, rare word", agentScope("agent-3", "ou_7"), []string{"kubernetes"}, TimeHint{}},
		{"console, all agents, common word", core.HistoryScope{Admin: true}, []string{"我们"}, TimeHint{}},
		{"console, all agents, 3 wordings", core.HistoryScope{Admin: true}, []string{"数据库迁移", "database migration", "postgres upgrade"}, TimeHint{}},
	}
	for _, c := range cases {
		var durations []time.Duration
		var found int
		for range 7 {
			began := time.Now()
			result, err := service.Search(ctx, Request{Queries: c.queries, Around: c.around, Scope: c.scope})
			if err != nil {
				t.Fatal(err)
			}
			durations = append(durations, time.Since(began))
			found = len(result.Originals) + len(result.Memory)
		}
		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
		t.Logf("%-38s median %6s  max %6s  (%d results)", c.name, durations[len(durations)/2].Round(time.Millisecond), durations[len(durations)-1].Round(time.Millisecond), found)
	}
}
