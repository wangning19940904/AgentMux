package usage

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/config"
	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
)

func codexReconciliationFixture() (core.UsageRecord, core.UsageRecord) {
	transcript := core.UsageRecord{
		Source: "codex", SessionID: "native-session", Model: "gpt-5",
		Timestamp:   time.Date(2026, 10, 14, 2, 0, 0, 0, time.UTC),
		InputTokens: 21, OutputTokens: 9, CacheReadTokens: 100, RuntimeID: "codex-app",
	}
	live := transcript
	live.RequestID, live.Provenance, live.RuntimeID = "request-1", "agentmux.internal", "codex"
	live.Timestamp = live.Timestamp.Add(40 * time.Second)
	return transcript, live
}

func TestReconcileCodexUsageKeepsLiveAndUnmatchedTranscriptTail(t *testing.T) {
	transcript, live := codexReconciliationFixture()
	tail := transcript
	tail.Timestamp = tail.Timestamp.Add(2 * time.Minute)
	tail.InputTokens++
	records := []core.UsageRecord{transcript, live, tail}
	before := append([]core.UsageRecord(nil), records...)
	got := reconcileCodexUsage(records)
	if !reflect.DeepEqual(got, []core.UsageRecord{live, tail}) {
		t.Fatalf("reconciled records = %+v", got)
	}
	if !reflect.DeepEqual(records, before) {
		t.Fatal("reconciliation mutated the original ledger records")
	}
}

func TestReconcileCodexUsagePreservesDistinctRequests(t *testing.T) {
	for name, mutate := range map[string]func(*core.UsageRecord){
		"host":                        func(r *core.UsageRecord) { r.Host = "remote" },
		"session":                     func(r *core.UsageRecord) { r.SessionID = "other-session" },
		"source":                      func(r *core.UsageRecord) { r.Source = "claude" },
		"model":                       func(r *core.UsageRecord) { r.Model = "other-model" },
		"input":                       func(r *core.UsageRecord) { r.InputTokens++ },
		"output":                      func(r *core.UsageRecord) { r.OutputTokens++ },
		"cache read":                  func(r *core.UsageRecord) { r.CacheReadTokens++ },
		"cache write":                 func(r *core.UsageRecord) { r.CacheWriteTokens++ },
		"same total split":            func(r *core.UsageRecord) { r.InputTokens++; r.CacheReadTokens-- },
		"outside window":              func(r *core.UsageRecord) { r.Timestamp = r.Timestamp.Add(21 * time.Second) },
		"different source provenance": func(r *core.UsageRecord) { r.Provenance = "proxy" },
		"no native session":           func(r *core.UsageRecord) { r.SessionID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			transcript, live := codexReconciliationFixture()
			mutate(&live)
			records := []core.UsageRecord{transcript, live}
			if got := reconcileCodexUsage(records); !reflect.DeepEqual(got, records) {
				t.Fatalf("distinct observations were dropped: %+v", got)
			}
		})
	}
}

func TestReconcileCodexUsageMatchesOneToOneAndPreservesRetries(t *testing.T) {
	transcript, live := codexReconciliationFixture()
	secondTranscript := transcript
	secondTranscript.Timestamp = transcript.Timestamp.Add(time.Second)
	if got := reconcileCodexUsage([]core.UsageRecord{transcript, secondTranscript, live}); !reflect.DeepEqual(got, []core.UsageRecord{secondTranscript, live}) {
		t.Fatalf("one live request must not suppress two transcript requests: %+v", got)
	}
	retry := live
	retry.RequestID = "request-1:attempt:2"
	if got := reconcileCodexUsage([]core.UsageRecord{transcript, live, retry}); !reflect.DeepEqual(got, []core.UsageRecord{live, retry}) {
		t.Fatalf("distinct live attempts must survive: %+v", got)
	}
	secondTranscript.Timestamp = transcript.Timestamp.Add(90 * time.Second)
	live.Timestamp = transcript.Timestamp
	retry.Timestamp = transcript.Timestamp.Add(50 * time.Second)
	transcript.Timestamp = transcript.Timestamp.Add(40 * time.Second)
	if got := reconcileCodexUsage([]core.UsageRecord{secondTranscript, retry, transcript, live}); !reflect.DeepEqual(got, []core.UsageRecord{retry, live}) {
		t.Fatalf("chronological one-to-one pairing should match both requests: %+v", got)
	}
}

func TestReportReconcilesCodexWithoutChangingStoredLedger(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenLegacySQLite(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	eng := NewEngine(&config.Config{Usage: config.UsageConfig{CacheDir: t.TempDir(), Offline: true}}, st, nil)
	transcript, live := codexReconciliationFixture()
	if err := st.UpsertUsage(ctx, []core.UsageRecord{transcript, live}); err != nil {
		t.Fatal(err)
	}
	report, err := eng.Report(ctx, "daily", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Totals.InputTokens+report.Totals.OutputTokens+report.Totals.CacheReadTokens+report.Totals.CacheWriteTokens != 130 || report.Totals.Records != 1 || report.Totals.Sessions != 1 {
		t.Fatalf("report counts the same request twice: %+v", report.Totals)
	}
	if len(report.ByRuntime) != 1 || report.ByRuntime[0].Runtime != "codex-agentmux" {
		t.Fatalf("internal calls were labelled as CLI: %+v", report.ByRuntime)
	}
	stored, err := st.QueryUsage(ctx, time.Time{})
	if err != nil || len(stored) != 2 {
		t.Fatalf("stored ledger changed: records=%+v err=%v", stored, err)
	}
}

func TestCodexClientsAndRetriesStayDistinct(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenLegacySQLite(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	eng := NewEngine(&config.Config{Usage: config.UsageConfig{CacheDir: t.TempDir(), Offline: true}}, st, nil)
	transcript, live := codexReconciliationFixture()
	// The same native thread also has a direct Desktop turn, not managed by AgentMux.
	desktop := transcript
	desktop.Timestamp = desktop.Timestamp.Add(5 * time.Minute)
	cli := transcript
	cli.SessionID, cli.RequestID, cli.RuntimeID, cli.Provenance = "worker", "codex-exec:run:0", "codex", "codex.exec.receipt"
	retry := cli
	retry.RequestID = "codex-exec:retry:0"
	if err := st.UpsertUsage(ctx, []core.UsageRecord{transcript, live, desktop, cli, cli, retry}); err != nil {
		t.Fatal(err)
	}
	report, err := eng.Report(ctx, "daily", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, runtime := range report.ByRuntime {
		counts[runtime.Runtime] = runtime.Records
	}
	if report.Totals.Records != 4 || report.Totals.Sessions != 2 || !reflect.DeepEqual(counts, map[string]int{"codex": 2, "codex-app": 1, "codex-agentmux": 1}) {
		t.Fatalf("classification/replay/retry mismatch: %+v %+v", report.Totals, report.ByRuntime)
	}
	live.RequestID = "new-internal"
	live.Provenance = ""
	if _, err := eng.Record(ctx, live); err != nil {
		t.Fatal(err)
	}
	stored, err := st.QueryUsage(ctx, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range stored {
		if row.RequestID == live.RequestID && row.RuntimeID != "codex-agentmux" {
			t.Fatalf("new internal row not classified: %+v", row)
		}
	}
}

func TestReportReconciliationMatchesAcrossDateBoundary(t *testing.T) {
	for _, source := range []string{"codex", "cursor"} {
		t.Run(source, func(t *testing.T) {
			ctx := context.Background()
			st, err := store.OpenLegacySQLite(filepath.Join(t.TempDir(), "usage.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			eng := NewEngine(&config.Config{Usage: config.UsageConfig{CacheDir: t.TempDir(), Offline: true}}, st, nil)
			secondary, primary := codexReconciliationFixture()
			if source == "cursor" {
				primary, secondary = cursorReconciliationFixture()
			}
			boundary := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
			secondary.Timestamp = boundary.Add(-10 * time.Second)
			primary.Timestamp = boundary.Add(10 * time.Second)
			earlier, later := primary, primary
			earlier.Timestamp, earlier.RequestID, earlier.OutputTokens = boundary.Add(-30*time.Second), "earlier", 1
			later.Timestamp, later.RequestID, later.OutputTokens = boundary.Add(30*time.Second), "later", 2
			if err := st.UpsertUsage(ctx, []core.UsageRecord{secondary, primary, earlier, later}); err != nil {
				t.Fatal(err)
			}
			previous, err := eng.ReportRange(ctx, "daily", boundary.AddDate(0, 0, -1), boundary)
			if err != nil {
				t.Fatal(err)
			}
			current, err := eng.ReportRange(ctx, "daily", boundary, boundary.AddDate(0, 0, 1))
			if err != nil {
				t.Fatal(err)
			}
			if previous.Totals.Records != 1 || previous.Totals.OutputTokens != earlier.OutputTokens ||
				current.Totals.Records != 2 || current.Totals.OutputTokens != primary.OutputTokens+later.OutputTokens {
				t.Fatalf("boundary matching/filtering failed: previous=%+v current=%+v", previous.Totals, current.Totals)
			}
		})
	}
}
