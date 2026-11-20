package usage

import (
	"reflect"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func claudeLineRecord(at time.Time) core.UsageRecord {
	return core.UsageRecord{
		Source: "claude", SessionID: "s1", Model: "opus", Project: "repo", Timestamp: at,
		InputTokens: 3, OutputTokens: 40, CacheReadTokens: 9000, CacheWriteTokens: 20,
	}
}

func TestReconcileClaudeUsageKeepsOneRowPerReply(t *testing.T) {
	at := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	first := claudeLineRecord(at)
	// The same reply's other content blocks, as the older parser stored them.
	second := claudeLineRecord(at.Add(2 * time.Second))
	third := claudeLineRecord(at.Add(70 * time.Second))
	records := []core.UsageRecord{first, second, third}
	before := append([]core.UsageRecord(nil), records...)
	if got := reconcileClaudeUsage(records); !reflect.DeepEqual(got, []core.UsageRecord{first}) {
		t.Fatalf("three lines of one reply = %+v, want one row", got)
	}
	if !reflect.DeepEqual(records, before) {
		t.Fatal("reconciliation changed the ledger records")
	}
}

func TestReconcileClaudeUsagePreservesDistinctReplies(t *testing.T) {
	at := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	for name, mutate := range map[string]func(*core.UsageRecord){
		"session":       func(r *core.UsageRecord) { r.SessionID = "s2" },
		"host":          func(r *core.UsageRecord) { r.Host = "remote" },
		"model":         func(r *core.UsageRecord) { r.Model = "haiku" },
		"input":         func(r *core.UsageRecord) { r.InputTokens++ },
		"output":        func(r *core.UsageRecord) { r.OutputTokens++ },
		"cache read":    func(r *core.UsageRecord) { r.CacheReadTokens++ },
		"cache write":   func(r *core.UsageRecord) { r.CacheWriteTokens++ },
		"other source":  func(r *core.UsageRecord) { r.Source = "codex" },
		"much later":    func(r *core.UsageRecord) { r.Timestamp = r.Timestamp.Add(claudeReplyGap + time.Second) },
		"no session id": func(r *core.UsageRecord) { r.SessionID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			other := claudeLineRecord(at.Add(time.Second))
			mutate(&other)
			records := []core.UsageRecord{claudeLineRecord(at), other}
			if got := reconcileClaudeUsage(records); !reflect.DeepEqual(got, records) {
				t.Fatalf("distinct replies were merged: %+v", got)
			}
		})
	}
}

func TestReconcileClaudeUsagePrefersRowsThatCarryTheReplyID(t *testing.T) {
	at := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	legacy := claudeLineRecord(at)
	legacyAgain := claudeLineRecord(at.Add(time.Second))
	identified := claudeLineRecord(at.Add(2 * time.Second))
	identified.RequestID = "msg_1"
	// The same transcript read again by the parser that keeps the reply ID.
	if got := reconcileClaudeUsage([]core.UsageRecord{legacy, legacyAgain, identified}); !reflect.DeepEqual(got, []core.UsageRecord{identified}) {
		t.Fatalf("legacy rows of an identified reply = %+v", got)
	}
	// Two IDs are two replies, however alike their usage.
	other := identified
	other.RequestID = "msg_2"
	other.Timestamp = at.Add(3 * time.Second)
	if got := reconcileClaudeUsage([]core.UsageRecord{identified, other}); !reflect.DeepEqual(got, []core.UsageRecord{identified, other}) {
		t.Fatalf("identified replies were merged: %+v", got)
	}
}

func TestReconcileClaudeUsageAttributesSubagentRowsToTheSessionsProject(t *testing.T) {
	at := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	parent := claudeLineRecord(at)
	subagent := claudeLineRecord(at.Add(time.Second))
	subagent.Project, subagent.Model = claudeSubagentProject, "haiku"
	orphan := subagent
	orphan.SessionID = "session-without-a-parent-row"
	got := reconcileClaudeUsage([]core.UsageRecord{parent, subagent, orphan})
	if len(got) != 3 || got[1].Project != "repo" || got[2].Project != claudeSubagentProject {
		t.Fatalf("projects = %q, %q, %q", got[0].Project, got[1].Project, got[2].Project)
	}
}
