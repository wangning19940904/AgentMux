package framework

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// A subscription usage limit is not a rate limit: the account is refused
// until a reset, often hours away, and every request before then is refused
// too. Only the frameworks' own wording counts. A gateway's generic 429,
// "rate limit" or "quota" stays a rate limit.
var usageLimitPattern = regexp.MustCompile(`(?i)you'?ve hit your (session |usage |weekly |daily |5-hour )?limit|usage limit (reached|exceeded)|usage_limit_(reached|exceeded)|usagelimitexceeded|claude ai usage limit|out of extra usage`)

// IsUsageLimit reports whether text is a framework's own report that its
// subscription reached its usage limit.
func IsUsageLimit(text string) bool {
	return usageLimitPattern.MatchString(text)
}

// maxUsageLimitReset bounds a reset read from text: one further off is taken
// as misread rather than trusted to hold a framework for days.
const maxUsageLimitReset = 8 * 24 * time.Hour

var (
	resetUnixPattern     = regexp.MustCompile(`(?i)usage limit reached\|(\d{10})\b`)
	resetSecondsPattern  = regexp.MustCompile(`(?i)resets?_in_seconds["'\s]*[:=]\s*(\d+)`)
	resetRFC3339Pattern  = regexp.MustCompile(`(?i)resets?(?:_at)?["'\s]*(?:at|:|=)?\s*["']?(\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}(?::\d{2})?(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2}))`)
	resetClockPattern    = regexp.MustCompile(`(?i)resets?\s+(?:at\s+)?(\d{1,2})(?::(\d{2}))?\s*(am|pm)?\s*\(([A-Za-z_]+(?:/[A-Za-z0-9_+\-]+)*)\)`)
	resetDurationPattern = regexp.MustCompile(`(?i)(?:try again|resets?)\s+in\s+((?:\d+\s*(?:days?|d|hours?|hrs?|h|minutes?|mins?|m|seconds?|secs?|s)\b[\s,]*(?:and\s+)?)+)`)
	resetDurationPart    = regexp.MustCompile(`(?i)(\d+)\s*(days?|d|hours?|hrs?|h|minutes?|mins?|m|seconds?|secs?|s)\b`)
)

// UsageLimitReset reads when a usage limit resets from a framework's own
// message, relative to now. It recognises a Unix timestamp after "usage limit
// reached|", "resets_in_seconds", an RFC 3339 time after "resets", a clock
// time with its time zone ("resets 4:20pm (Asia/Shanghai)") and a duration
// ("try again in 2 hours 5 minutes"). A clock time without a time zone, a
// time already past, or one more than eight days away is not trusted.
func UsageLimitReset(text string, now time.Time) (time.Time, bool) {
	if m := resetUnixPattern.FindStringSubmatch(text); m != nil {
		if sec, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			return plausibleReset(time.Unix(sec, 0), now)
		}
	}
	if m := resetSecondsPattern.FindStringSubmatch(text); m != nil {
		if sec, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			return plausibleReset(now.Add(time.Duration(sec)*time.Second), now)
		}
	}
	if m := resetRFC3339Pattern.FindStringSubmatch(text); m != nil {
		value := strings.Replace(m[1], " ", "T", 1)
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04Z07:00", "2006-01-02T15:04:05Z0700", "2006-01-02T15:04Z0700"} {
			if at, err := time.Parse(layout, value); err == nil {
				return plausibleReset(at, now)
			}
		}
	}
	if m := resetClockPattern.FindStringSubmatch(text); m != nil {
		if at, ok := nextClockTime(m[1], m[2], m[3], m[4], now); ok {
			return plausibleReset(at, now)
		}
	}
	if m := resetDurationPattern.FindStringSubmatch(text); m != nil {
		var total time.Duration
		for _, part := range resetDurationPart.FindAllStringSubmatch(m[1], -1) {
			n, err := strconv.Atoi(part[1])
			if err != nil {
				continue
			}
			switch unit := strings.ToLower(part[2]); {
			case strings.HasPrefix(unit, "d"):
				total += time.Duration(n) * 24 * time.Hour
			case strings.HasPrefix(unit, "h"):
				total += time.Duration(n) * time.Hour
			case strings.HasPrefix(unit, "m"):
				total += time.Duration(n) * time.Minute
			default:
				total += time.Duration(n) * time.Second
			}
		}
		if total > 0 {
			return plausibleReset(now.Add(total), now)
		}
	}
	return time.Time{}, false
}

func nextClockTime(hour, minute, meridiem, zone string, now time.Time) (time.Time, bool) {
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return time.Time{}, false
	}
	h, err := strconv.Atoi(hour)
	if err != nil {
		return time.Time{}, false
	}
	m := 0
	if minute != "" {
		if m, err = strconv.Atoi(minute); err != nil || m > 59 {
			return time.Time{}, false
		}
	}
	switch strings.ToLower(meridiem) {
	case "am":
		if h < 1 || h > 12 {
			return time.Time{}, false
		}
		if h == 12 {
			h = 0
		}
	case "pm":
		if h < 1 || h > 12 {
			return time.Time{}, false
		}
		if h != 12 {
			h += 12
		}
	default:
		if h > 23 {
			return time.Time{}, false
		}
	}
	local := now.In(loc)
	at := time.Date(local.Year(), local.Month(), local.Day(), h, m, 0, 0, loc)
	if !at.After(now) {
		at = at.AddDate(0, 0, 1)
	}
	return at, true
}

func plausibleReset(at, now time.Time) (time.Time, bool) {
	if !at.After(now) || at.Sub(now) > maxUsageLimitReset {
		return time.Time{}, false
	}
	return at.UTC(), true
}
