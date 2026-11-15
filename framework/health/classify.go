package health

import (
	"regexp"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/framework"
)

// Error classification for real runs is deliberately narrower than the smoke
// test's: a smoke test asks for nothing but "ok", so any failure is the
// framework's, while a real run fails for many reasons that belong to the
// work (a tool's 403, a missing file, a cancelled turn, a provider's passing
// 429). Only messages that can only come from the framework itself count.
// Everything else is left alone.
var runErrorPatterns = []struct {
	kind framework.SmokeErrorKind
	re   *regexp.Regexp
}{
	{framework.SmokeNotInstalled, regexp.MustCompile(`(?i)executable file not found|: command not found|\bcli was not found|is not installed on this machine`)},
	{framework.SmokeUnauthenticated, regexp.MustCompile(`(?i)not (logged|signed) in|please run /login|run .{0,20}\blogin\b to|log ?in required|invalid[ _-]?(x-)?api[ _-]?key|authentication_error|(oauth|access|auth) token (has )?expired|credentials? (have |has )?expired|api error: 401\b|\b401 unauthori[sz]ed`)},
	{framework.SmokeModelNotFound, regexp.MustCompile(`(?i)model[ _]not[ _]found|unknown model|invalid model|no such model|unsupported model|model .{0,80}(does not exist|is not (available|supported))|cannot be routed|no account match`)},
	{framework.SmokeCLIOutdated, regexp.MustCompile(`(?i)unknown option ['"]?--|unrecognized option ['"]?--|requires? (a )?newer version|please (update|upgrade) (to|your|the)|version .{0,40}(too old|no longer supported)|minimum (required )?version`)},
}

// ClassifyRunError reports whether a real run's error text is a health
// failure of its framework, and which kind. A usage limit is one; a
// cancelled turn, a timeout, a network error, a generic rate limit and any
// failure of the work itself are not.
func ClassifyRunError(text string) (framework.SmokeErrorKind, bool) {
	text = strings.TrimSpace(text)
	if text == "" || isCancellation(text) {
		return "", false
	}
	if framework.IsUsageLimit(text) {
		return framework.SmokeUsageLimit, true
	}
	for _, pattern := range runErrorPatterns {
		if pattern.re.MatchString(text) {
			return pattern.kind, true
		}
	}
	return "", false
}

func isCancellation(text string) bool {
	lower := strings.ToLower(text)
	return lower == "context canceled" || lower == "context deadline exceeded" ||
		strings.HasSuffix(lower, ": context canceled") || strings.HasSuffix(lower, ": context deadline exceeded")
}

const reasonLimit = 600

// recordFromResult turns a check or test into a record.
func recordFromResult(result framework.SmokeResult, source Source, now time.Time) Record {
	rec := Record{Kind: result.Kind, Source: source, Model: result.Model, CheckedAt: result.CheckedAt}
	if rec.CheckedAt.IsZero() {
		rec.CheckedAt = now
	}
	if result.OK {
		rec.State = StateOK
		return rec
	}
	rec.ErrorKind = result.ErrorKind
	rec.Reason = reasonText(result.ErrorKind, result.ErrorDetail)
	rec.State = StateFailing
	if result.ErrorKind == framework.SmokeUsageLimit {
		rec.State = StateUsageLimit
		if at, ok := framework.UsageLimitReset(result.ErrorDetail+"\n"+result.Reply, now); ok {
			rec.ResetsAt = &at
		}
	}
	return rec
}

// recordFromRun turns a real run's framework-level failure into a record.
func recordFromRun(kind string, errorKind framework.SmokeErrorKind, text string, now time.Time) Record {
	rec := Record{
		Kind: kind, State: StateFailing, ErrorKind: errorKind, Source: SourceRun, CheckedAt: now,
		Reason: reasonText(errorKind, text),
	}
	if errorKind == framework.SmokeUsageLimit {
		rec.State = StateUsageLimit
		if at, ok := framework.UsageLimitReset(text, now); ok {
			rec.ResetsAt = &at
		}
	}
	return rec
}

var reasonPrefix = map[framework.SmokeErrorKind]string{
	framework.SmokeNotInstalled:    "CLI not installed",
	framework.SmokeUnauthenticated: "not signed in",
	framework.SmokeModelNotFound:   "model rejected",
	framework.SmokeUsageLimit:      "usage limit reached",
	framework.SmokeRateLimited:     "rate limited",
	framework.SmokeNetwork:         "network error",
	framework.SmokeTimeout:         "no reply in time",
	framework.SmokeUnexpectedReply: "unexpected reply",
	framework.SmokeCLIOutdated:     "CLI too old",
}

func reasonText(kind framework.SmokeErrorKind, detail string) string {
	detail = strings.Join(strings.Fields(framework.RedactSecrets(detail)), " ")
	if runes := []rune(detail); len(runes) > reasonLimit {
		detail = string(runes[:reasonLimit]) + "…"
	}
	prefix := reasonPrefix[kind]
	switch {
	case prefix == "":
		return detail
	case detail == "":
		return prefix
	default:
		return prefix + ": " + detail
	}
}
