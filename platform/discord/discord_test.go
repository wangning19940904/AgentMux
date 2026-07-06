package discord

import (
	"strings"
	"testing"
)

func TestSplitMessage(t *testing.T) {
	if got := splitMessage("", 10); got != nil {
		t.Fatalf("empty = %v", got)
	}
	if got := splitMessage("short", 10); len(got) != 1 || got[0] != "short" {
		t.Fatalf("short = %v", got)
	}
	long := strings.Repeat("line one\n", 50) // 450 chars
	chunks := splitMessage(long, 100)
	if len(chunks) < 4 {
		t.Fatalf("chunks = %d", len(chunks))
	}
	for i, c := range chunks {
		if len([]rune(c)) > 100 {
			t.Fatalf("chunk %d too long: %d", i, len(c))
		}
	}
	if joined := strings.Join(chunks, "\n"); strings.ReplaceAll(joined, "\n", "") != strings.ReplaceAll(long, "\n", "") {
		t.Fatal("content lost in split")
	}
}

func TestStripBotMention(t *testing.T) {
	if got := stripBotMention("<@123> do the thing", "123"); got != "do the thing" {
		t.Fatalf("got %q", got)
	}
	if got := stripBotMention("<@!123>  hi", "123"); got != "hi" {
		t.Fatalf("got %q", got)
	}
}
