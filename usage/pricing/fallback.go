package pricing

import "strings"

// fallback returns hardcoded per-token prices (USD) for common models when the
// LiteLLM table is unavailable or lacks the model. Values are per single token.
func fallback(model string) modelPrice {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "opus"):
		return modelPrice{Input: 15.0 / 1e6, Output: 75.0 / 1e6,
			CacheRead: 1.5 / 1e6, CacheWrite: 18.75 / 1e6}
	case strings.Contains(m, "sonnet"):
		return modelPrice{Input: 3.0 / 1e6, Output: 15.0 / 1e6,
			CacheRead: 0.3 / 1e6, CacheWrite: 3.75 / 1e6}
	case strings.Contains(m, "haiku"):
		return modelPrice{Input: 0.8 / 1e6, Output: 4.0 / 1e6,
			CacheRead: 0.08 / 1e6, CacheWrite: 1.0 / 1e6}
	case strings.Contains(m, "gpt-5"), strings.Contains(m, "gpt5"):
		return modelPrice{Input: 1.25 / 1e6, Output: 10.0 / 1e6,
			CacheRead: 0.125 / 1e6}
	case strings.Contains(m, "gemini"):
		return modelPrice{Input: 1.25 / 1e6, Output: 5.0 / 1e6,
			CacheRead: 0.3125 / 1e6}
	default:
		// Conservative default to avoid wild over/under estimates.
		return modelPrice{Input: 3.0 / 1e6, Output: 15.0 / 1e6}
	}
}
