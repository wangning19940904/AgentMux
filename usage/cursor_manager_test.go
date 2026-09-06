package usage

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestCursorSyncQueuesCloudRequestBehindLocalScan(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan bool, 3)
	release := make(chan struct{}, 3)
	manager := &CursorUsageManager{
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		state:  cursorUsageState{Connected: true},
		runCtx: ctx,
		syncFn: func(_ context.Context, includeCloud bool) error {
			started <- includeCloud
			<-release
			return nil
		},
	}

	manager.triggerSync(false)
	if cloud := receiveCursorSync(t, started); cloud {
		t.Fatal("first sync should be local-only")
	}
	manager.triggerSync(true)
	manager.triggerSync(true) // repeated cloud requests coalesce
	release <- struct{}{}
	if cloud := receiveCursorSync(t, started); !cloud {
		t.Fatal("queued follow-up should include cloud")
	}
	release <- struct{}{}
	waitCursorSyncIdle(t, manager)
	select {
	case extra := <-started:
		t.Fatalf("cloud requests did not coalesce: extra sync includeCloud=%t", extra)
	default:
	}
}

func TestCursorCloudSyncStartRecoversGapFromLastSuccess(t *testing.T) {
	now := time.Date(2026, 8, 31, 8, 0, 0, 0, time.UTC)
	lastSync := now.Add(-18 * time.Hour)
	state := cursorUsageState{BackfillComplete: true, LastSyncAt: lastSync.Format(time.RFC3339Nano)}
	if got, want := cursorCloudSyncStart(now, state), lastSync.Add(-cursorCloudOverlap); !got.Equal(want) {
		t.Fatalf("gap start = %v, want %v", got, want)
	}

	state.LastSyncAt = now.Add(-2 * cursorBackfillWindow).Format(time.RFC3339Nano)
	if got, want := cursorCloudSyncStart(now, state), now.Add(-cursorBackfillWindow); !got.Equal(want) {
		t.Fatalf("stale start = %v, want clamp %v", got, want)
	}

	state.LastSyncAt = ""
	if got, want := cursorCloudSyncStart(now, state), now.Add(-cursorCloudOverlap); !got.Equal(want) {
		t.Fatalf("missing checkpoint start = %v, want %v", got, want)
	}
}

func TestCursorCloudSyncStartHonorsInitialBackfill(t *testing.T) {
	now := time.Date(2026, 8, 31, 8, 0, 0, 0, time.UTC)
	requested := now.Add(-7 * 24 * time.Hour)
	state := cursorUsageState{BackfillFrom: requested.Format(time.RFC3339Nano)}
	if got := cursorCloudSyncStart(now, state); !got.Equal(requested) {
		t.Fatalf("initial backfill start = %v, want %v", got, requested)
	}
}

func receiveCursorSync(t *testing.T, started <-chan bool) bool {
	t.Helper()
	select {
	case includeCloud := <-started:
		return includeCloud
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Cursor sync")
		return false
	}
}

func waitCursorSyncIdle(t *testing.T, manager *CursorUsageManager) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		manager.mu.Lock()
		idle := !manager.syncing
		manager.mu.Unlock()
		if idle {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("Cursor sync did not become idle")
}
