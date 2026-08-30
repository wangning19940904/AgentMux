package observability

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/hookrelay"
)

func TestCursorLowercaseStopHookRecordsExactUsage(t *testing.T) {
	bus := core.NewObservationBus()
	service := NewIngestService(nil, bus, t.TempDir(), "token")
	var captured core.UsageRecord
	service.SetUsageSink(func(_ context.Context, record core.UsageRecord) (float64, error) {
		captured = record
		return 0, nil
	})
	payload := json.RawMessage(`{
		"hook_event_name":"stop","conversation_id":"conversation-1","generation_id":"generation-1",
		"model_id":"claude-sonnet","workspace_roots":["/tmp/project"],
		"input_tokens":100,"output_tokens":20,"cache_read_tokens":30,"cache_write_tokens":4
	}`)
	wire, _ := json.Marshal(hookrelay.Message{Version: 1, Source: "cursor", ReceivedAt: time.Now().UTC(), Payload: payload})
	if err := service.ingestWire(context.Background(), wire); err != nil {
		t.Fatal(err)
	}
	if captured.Source != "cursor" || captured.SessionID != "conversation-1" || captured.RequestID != "generation-1" || captured.InputTokens != 100 || captured.CacheReadTokens != 30 {
		t.Fatalf("captured = %+v", captured)
	}
	if captured.TokenQuality != core.UsageTokenQualityExact || captured.Provenance != "cursor.hook" {
		t.Fatalf("quality = %+v", captured)
	}
}
