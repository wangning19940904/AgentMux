package pricing

import "testing"

func TestFallbackPricing(t *testing.T) {
	p := New(t.TempDir(), true) // offline: forces fallback table

	// Opus: 1M input @ $15/M = $15.
	if got := p.Cost("claude-opus-4-8", 1_000_000, 0, 0, 0); got < 14.9 || got > 15.1 {
		t.Fatalf("opus input cost = %v, want ~15", got)
	}
	// Sonnet output: 1M @ $15/M = $15.
	if got := p.Cost("claude-sonnet-4", 0, 1_000_000, 0, 0); got < 14.9 || got > 15.1 {
		t.Fatalf("sonnet output cost = %v, want ~15", got)
	}
	// Unknown model uses conservative default (non-zero).
	if got := p.Cost("totally-unknown", 1_000_000, 0, 0, 0); got <= 0 {
		t.Fatalf("unknown model cost = %v, want > 0", got)
	}
}
