package historysearch

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestTermsSplitChineseIntoPairsAndEnglishIntoWords(t *testing.T) {
	cases := []struct {
		text string
		want []string
	}{
		{"数据库迁移", []string{"数据", "据库", "库迁", "迁移"}},
		{"The Database Migration", []string{"database", "migration"}},
		{"部署k8s集群", []string{"部署", "k8s", "集群"}},
		{"迁移 to Postgres 18，下周一", []string{"迁移", "postgres", "18", "下周", "周一"}},
		{"好", []string{"好"}},
		{"  ,.!  ", nil},
		{"Ünïcode café", []string{"ünïcode", "café"}},
		{"迁移迁移 MIGRATION migration", []string{"迁移", "移迁", "migration"}},
	}
	for _, c := range cases {
		if got := Terms(c.text); !slices.Equal(got, c.want) {
			t.Errorf("Terms(%q) = %q, want %q", c.text, got, c.want)
		}
	}
	long := strings.Repeat("长", 3) + strings.Repeat("x", 200)
	for _, term := range Terms(long) {
		if len(term) > maxTermBytes {
			t.Errorf("term of %d bytes, want at most %d", len(term), maxTermBytes)
		}
	}
}

// A document found by more wordings ranks above one found by a single
// wording, even when that wording put it first.
func TestFuseRanksAgreementAboveOneFirstPlace(t *testing.T) {
	got := fuse([][]string{
		{"solo", "both", "x"},
		{"both", "y"},
		{"z", "both"},
	}, nil)
	var order []string
	for _, item := range got {
		order = append(order, item.id)
	}
	if order[0] != "both" || order[1] != "solo" {
		t.Fatalf("order = %v, want both first and solo second", order)
	}
	if len(order) != 5 {
		t.Fatalf("fused %d ids, want every id of every list (5): %v", len(order), order)
	}
	// Equal scores keep first-seen order.
	tie := fuse([][]string{{"a"}, {"b"}}, nil)
	if tie[0].id != "a" || tie[1].id != "b" {
		t.Fatalf("tie order = %v", tie)
	}
}

func TestTimeRankingNeverDropsADocument(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 11, d, 12, 0, 0, 0, time.UTC) }
	hint := TimeHint{From: day(10), To: day(12)}
	times := map[string]time.Time{"old": day(1), "inside": day(11), "edge": day(10), "new": day(19)}
	ranking := timeRanking(hint, []string{"edge", "inside", "new", "old"}, times)
	if !slices.Equal(ranking, []string{"inside", "edge"}) {
		t.Fatalf("time ranking = %v, want the window's documents, middle first", ranking)
	}
	// Fused with a wording that ranks "old" first, the time vote lifts
	// "inside" above it, and nothing disappears.
	fused := fuse([][]string{{"old", "new", "inside", "edge"}, ranking}, []float64{1, timeWeight})
	if fused[0].id != "inside" {
		t.Fatalf("fused order = %v, want inside first", fused)
	}
	if len(fused) != 4 {
		t.Fatalf("fused %d documents, want all 4", len(fused))
	}
	if (TimeHint{From: day(12), To: day(10)}).Valid() {
		t.Fatal("a window that ends before it starts is not a hint")
	}
}

func TestSnippetCutsAroundTheMatch(t *testing.T) {
	text := strings.Repeat("前文内容。", 60) + "我们决定把数据库迁移到 Postgres 18。" + strings.Repeat("后续讨论。", 60)
	got := snippet(text, []string{"数据库迁移"})
	if !strings.Contains(got, "数据库迁移") || !strings.HasPrefix(got, "…") || !strings.HasSuffix(got, "…") {
		t.Fatalf("snippet = %q", got)
	}
	if n := len([]rune(got)); n > 2*snippetRadius+2 {
		t.Fatalf("snippet has %d characters", n)
	}
	// A wording that is not in the text verbatim falls back to its terms.
	if got := snippet(text, []string{"postgres upgrade"}); !strings.Contains(got, "Postgres") {
		t.Fatalf("term fallback snippet = %q", got)
	}
	if got := snippet("short text", []string{"x"}); got != "short text" {
		t.Fatalf("short snippet = %q", got)
	}
}

func TestCleanWordingsDropsEmptyAndDuplicateWordings(t *testing.T) {
	got := cleanWordings([]string{" 迁移 ", "迁移", "MIGRATION", "migration", "the", "!!", ""})
	if !slices.Equal(got, []string{"迁移", "MIGRATION"}) {
		t.Fatalf("wordings = %q", got)
	}
	if minMatched(1) != 1 || minMatched(2) != 2 || minMatched(4) != 3 || minMatched(5) != 3 {
		t.Fatal("minMatched thresholds changed")
	}
}
