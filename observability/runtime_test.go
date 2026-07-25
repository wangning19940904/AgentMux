package observability

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/wangning19940904/AgentMux/config"
	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
)

func TestRuntimeAlwaysRedactsIngestAndConfiguredMasterKeys(t *testing.T) {
	home := t.TempDir()
	st, err := store.Open(filepath.Join(home, "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	master := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	t.Setenv("AGENTMUX_TEST_OBSERVATION_KEY", master)
	runtime, err := NewRuntime(nil, config.ObservabilityConfig{
		Enabled: true, CaptureContent: "full", MasterKeyEnv: "AGENTMUX_TEST_OBSERVATION_KEY",
		ContentRetentionDays: 30, DetailRetentionDays: 180, BackfillDays: 180,
	}, st, home, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Bus.Publish(context.Background(), core.ObservationEnvelope{
		EventID: "runtime-secret-event", TraceID: "runtime-secret-trace", SpanID: "runtime-secret-span", Kind: "agent.turn",
		Content: &core.ObservationContent{ContentType: "text/plain", Data: []byte("token=" + runtime.IngestToken + " master=" + master)},
	}); err != nil {
		t.Fatal(err)
	}
	events, err := st.ListObservationEvents(context.Background(), "runtime-secret-trace", 0, 10)
	if err != nil || len(events) != 1 || events[0].PayloadRef == nil {
		t.Fatalf("events = %+v, err=%v", events, err)
	}
	plaintext, _, err := runtime.Recorder.ReadPayload(context.Background(), events[0].PayloadRef.ID)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(plaintext, []byte(runtime.IngestToken)) || bytes.Contains(plaintext, []byte(master)) || !bytes.Contains(plaintext, []byte("[REDACTED]")) {
		t.Fatalf("runtime secrets were not redacted: %s", plaintext)
	}
}
