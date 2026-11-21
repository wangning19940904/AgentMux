package server

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func TestRequestedTurnClassOnlyLowers(t *testing.T) {
	cases := map[string]struct {
		class core.TurnClass
		set   bool
	}{
		"":            {},
		"interactive": {},
		"urgent":      {},
		"ticket":      {core.TurnTicket, true},
		"Background":  {core.TurnBackground, true},
	}
	for header, want := range cases {
		r := httptest.NewRequest("POST", "/api/v1/invocations", nil)
		if header != "" {
			r.Header.Set(core.TurnClassHeader, header)
		}
		class, ok := core.TurnClassFrom(withRequestedTurnClass(r).Context())
		if ok != want.set || class != want.class {
			t.Fatalf("header %q: class %v, %v; want %v, %v", header, class, ok, want.class, want.set)
		}
	}
}

func TestClassifyUsageLimit(t *testing.T) {
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	if _, limited := classifyUsageLimit("exit status 1", now); limited {
		t.Fatal("an ordinary error is a usage limit")
	}
	resetsAt, limited := classifyUsageLimit("You've hit your usage limit. Try again in 2 hours.", now)
	if !limited || !resetsAt.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("usage limit = %v, %v", resetsAt, limited)
	}
}
