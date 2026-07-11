package core

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestObservationEnvelopeNormalizeAndValidate(t *testing.T) {
	envelope := ObservationEnvelope{Kind: "agent.turn", Usage: &ObservationUsage{InputTokens: 4, OutputTokens: 3, CacheReadTokens: 2, CacheWriteTokens: 1}}
	envelope.Normalize()
	if envelope.Version != ObservationEnvelopeVersion || envelope.EventID == "" || envelope.TraceID == "" || envelope.SpanID == "" {
		t.Fatalf("missing normalized fields: %+v", envelope)
	}
	if envelope.Time.Location() != time.UTC {
		t.Fatalf("time is not UTC: %v", envelope.Time)
	}
	if envelope.Usage.TotalTokens != 10 {
		t.Fatalf("total tokens = %d, want 10", envelope.Usage.TotalTokens)
	}
	if err := envelope.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestObservationBusMultiSubscriberUnsubscribeAndPanicIsolation(t *testing.T) {
	bus := NewObservationBus()
	var calls []string
	var mu sync.Mutex
	appendCall := func(name string) {
		mu.Lock()
		calls = append(calls, name)
		mu.Unlock()
	}
	bus.Subscribe("first", func(context.Context, ObservationEnvelope) error {
		appendCall("first")
		return errors.New("expected")
	})
	unsubscribe := bus.Subscribe("panic", func(context.Context, ObservationEnvelope) error {
		appendCall("panic")
		panic("boom")
	})
	bus.Subscribe("last", func(context.Context, ObservationEnvelope) error {
		appendCall("last")
		return nil
	})

	err := bus.Publish(context.Background(), ObservationEnvelope{Kind: "agent.turn"})
	if err == nil || !strings.Contains(err.Error(), "expected") || !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("publish error = %v", err)
	}
	if got := strings.Join(calls, ","); got != "first,panic,last" {
		t.Fatalf("delivery order = %q", got)
	}

	unsubscribe()
	unsubscribe()
	if bus.SubscriberCount() != 2 {
		t.Fatalf("subscriber count = %d, want 2", bus.SubscriberCount())
	}
}

func TestObservationBusConcurrentPublishAndSubscription(t *testing.T) {
	bus := NewObservationBus()
	var delivered atomic.Int64
	baseUnsubscribe := bus.Subscribe("counter", func(context.Context, ObservationEnvelope) error {
		delivered.Add(1)
		return nil
	})
	defer baseUnsubscribe()

	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = bus.Publish(context.Background(), ObservationEnvelope{Kind: "tool.call"})
		}()
		go func() {
			defer wg.Done()
			remove := bus.Subscribe("temporary", func(context.Context, ObservationEnvelope) error { return nil })
			remove()
		}()
	}
	wg.Wait()
	if delivered.Load() != 40 {
		t.Fatalf("delivered = %d, want 40", delivered.Load())
	}
}
