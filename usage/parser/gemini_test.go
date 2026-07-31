package parser

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGeminiCachedTokensAreNotCountedAsUncachedInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	line := `{"timestamp":"2026-07-26T08:00:00Z","model":"gemini-test","sessionId":"s1","usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":20,"cachedContentTokenCount":70}}`
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	records := (&geminiCollector{}).parseFile(path, time.Time{})
	if len(records) != 1 {
		t.Fatalf("records = %+v", records)
	}
	if records[0].InputTokens != 30 || records[0].CacheReadTokens != 70 || records[0].OutputTokens != 20 {
		t.Fatalf("usage = %+v", records[0])
	}
}
