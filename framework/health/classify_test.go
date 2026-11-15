package health

import (
	"testing"

	"github.com/wangning19940904/AgentMux/framework"
)

func TestClassifyRunError(t *testing.T) {
	cases := []struct {
		text string
		want framework.SmokeErrorKind // "" means not a health failure
	}{
		{`exec: "claude": executable file not found in $PATH`, framework.SmokeNotInstalled},
		{"zsh:1: command not found: codex", framework.SmokeNotInstalled},
		{"Invalid API key · Please run /login", framework.SmokeUnauthenticated},
		{"Not logged in · Please run /login", framework.SmokeUnauthenticated},
		{`API Error: 401 {"type":"error","error":{"type":"authentication_error"}}`, framework.SmokeUnauthenticated},
		{"OAuth token has expired. Please obtain a new token.", framework.SmokeUnauthenticated},
		{"API Error: 400 Bare Claude model name cannot be routed", framework.SmokeModelNotFound},
		{"The model `gpt-9` does not exist or you do not have access to it", framework.SmokeModelNotFound},
		{"error: unknown option '--no-session-persistence'", framework.SmokeCLIOutdated},
		{"This version of Codex is too old; please upgrade to continue", framework.SmokeCLIOutdated},
		{"You've hit your usage limit. Upgrade to Pro or try again in 4 days 2 hours.", framework.SmokeUsageLimit},
		{"Claude AI usage limit reached|1763200000", framework.SmokeUsageLimit},
		{"You've hit your session limit · resets 4:20pm (Asia/Shanghai)", framework.SmokeUsageLimit},

		// Not the framework's fault, or not certainly.
		{"", ""},
		{"context canceled", ""},
		{"invoke agent: context deadline exceeded", ""},
		{"API Error: 429 rate_limit_error", ""},
		{"API Error: 529 overloaded_error", ""},
		{"stream error: connection reset by peer", ""},
		{"tool Bash failed: curl: (22) The requested URL returned error: 403", ""},
		{"go test ./... failed: 3 tests failed", ""},
		{"open /tmp/x/notes.md: no such file or directory", ""},
		{"insufficient quota for the request", ""},
	}
	for _, tc := range cases {
		got, ok := ClassifyRunError(tc.text)
		if ok != (tc.want != "") || got != tc.want {
			t.Errorf("ClassifyRunError(%q) = %q, %v; want %q", tc.text, got, ok, tc.want)
		}
	}
}

func TestReasonIsRedactedAndShort(t *testing.T) {
	reason := reasonText(framework.SmokeUnauthenticated, "invalid x-api-key: sk-abcdefghijklmnop\n\n  retry")
	if reason != "not signed in: invalid x-api-key: [redacted] retry" {
		t.Fatalf("reason = %q", reason)
	}
}
