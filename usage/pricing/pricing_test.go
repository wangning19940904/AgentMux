package pricing

import (
	"encoding/json"
	"math"
	"testing"
)

func TestContextTierIncludesCachedInputAndUsesStrictBoundary(t *testing.T) {
	p := New(t.TempDir(), true)
	p.loaded = true
	if err := json.Unmarshal([]byte(`{"gpt-6-astra": {
		"input_cost_per_token": 0.00001,
		"output_cost_per_token": 0.00005,
		"cache_read_input_token_cost": 0.000001,
		"cache_creation_input_token_cost": 0.0000125,
		"input_cost_per_token_above_272k_tokens": 0.00002,
		"output_cost_per_token_above_272k_tokens": 0.000075,
		"cache_read_input_token_cost_above_272k_tokens": 0.000002,
		"cache_creation_input_token_cost_above_272k_tokens": 0.000025,
		"input_cost_per_token_above_272k_tokens_priority": 0.00004
	}}`), &p.table); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name                       string
		input, output, read, write int64
		want                       float64
	}{
		{"boundary", 2000, 1000, 270000, 0, .02 + .05 + .27},
		{"above", 2001, 1000, 270000, 0, .04002 + .075 + .54},
		{"cache write triggers tier", 2000, 1000, 270000, 1, .04 + .075 + .54 + .000025},
		{"output does not trigger tier", 2000, 300000, 1000, 0, .02 + 15 + .001},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := p.Cost("gpt-6-astra", tc.input, tc.output, tc.read, tc.write); math.Abs(got-tc.want) > 1e-9 {
				t.Fatalf("cost = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestContextTierSelectsHighestThresholdAndPreservesMissingRates(t *testing.T) {
	p := New(t.TempDir(), true)
	p.loaded = true
	if err := json.Unmarshal([]byte(`{"tiered": {
		"input_cost_per_token": 1, "output_cost_per_token": 3,
		"input_cost_per_token_above_200k_tokens": 2,
		"input_cost_per_token_above_1000k_tokens": 4,
		"output_cost_per_token_above_1000k_tokens": 0
	}}`), &p.table); err != nil {
		t.Fatal(err)
	}
	if got := p.Cost("tiered", 200001, 10, 0, 0); got != 400032 {
		t.Fatalf("first tier = %v", got)
	}
	if got := p.Cost("tiered", 1000001, 10, 0, 0); got != 4000004 {
		t.Fatalf("highest tier/explicit zero = %v", got)
	}
}

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

func TestLookupUsesMostSpecificModelMatch(t *testing.T) {
	p := New(t.TempDir(), true)
	p.loaded = true
	p.table = map[string]modelPrice{
		"gpt-5":         {Input: 1},
		"gpt-5.6-sol":   {Input: 2},
		"5.6-sol":       {Input: 3},
		"unrelated-key": {Input: 4},
	}

	got, ok := p.lookup("openai/gpt-5.6-sol")
	if !ok || got.Input != 2 {
		t.Fatalf("lookup = %+v, %t; want longest model key", got, ok)
	}
}
