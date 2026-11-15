package framework

import (
	"strconv"
	"testing"
	"time"
)

func TestUsageLimitReset(t *testing.T) {
	now := time.Date(2026, 11, 15, 8, 0, 0, 0, time.UTC) // 16:00 in Asia/Shanghai
	cases := []struct {
		text string
		want time.Time // zero: no trusted reset
	}{
		{"Claude AI usage limit reached|" + strconv.FormatInt(now.Add(3*time.Hour).Unix(), 10), now.Add(3 * time.Hour)},
		{"You've hit your session limit · resets 4:20pm (Asia/Shanghai)", time.Date(2026, 11, 15, 8, 20, 0, 0, time.UTC)},
		{"You've hit your session limit · resets 3pm (Asia/Shanghai)", time.Date(2026, 11, 16, 7, 0, 0, 0, time.UTC)},
		{"Error: You've hit your weekly limit · resets at 9am (UTC)", time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)},
		{`{"type":"usage_limit_reached","resets_in_seconds": 5400}`, now.Add(90 * time.Minute)},
		{"usage limit; resets_at: 2026-11-15T12:30:00Z", time.Date(2026, 11, 15, 12, 30, 0, 0, time.UTC)},
		{"You've hit your usage limit. Try again in 4 days 2 hours 13 minutes.", now.Add(98*time.Hour + 13*time.Minute)},
		{"You've hit your usage limit. Try again at 3:05 PM.", time.Time{}}, // no time zone
		{"Claude AI usage limit reached|1600000000", time.Time{}},           // in the past
		{"You've hit your usage limit. Try again in 30 days.", time.Time{}}, // too far off
		{"resets 4:20pm (Not/AZone)", time.Time{}},
	}
	for _, tc := range cases {
		got, ok := UsageLimitReset(tc.text, now)
		if ok != !tc.want.IsZero() || !got.Equal(tc.want) {
			t.Errorf("UsageLimitReset(%q) = %s, %v; want %s", tc.text, got, ok, tc.want)
		}
	}
}

func TestSmokeClassifiesUsageLimitBeforeRateLimit(t *testing.T) {
	if got := classifySmokeError("429 You've hit your usage limit"); got != SmokeUsageLimit {
		t.Fatalf("got %q", got)
	}
	if got := classifySmokeError("API Error: 429 rate_limit_error"); got != SmokeRateLimited {
		t.Fatalf("got %q", got)
	}
}
