package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRuntimeStopIsIdempotent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runtime := &Runtime{ctx: ctx, cancel: cancel}
	runtime.Stop()
	runtime.Stop()
	select {
	case <-ctx.Done():
	default:
		t.Fatal("runtime context was not cancelled")
	}
}

func TestRuntimeWaitJoinsComponentErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan runtimeResult, 2)
	errCh <- runtimeResult{name: "http", err: errors.New("listen failed")}
	errCh <- runtimeResult{name: "engine", err: nil}
	runtime := &Runtime{ctx: ctx, cancel: cancel, started: true, errCh: errCh}
	err := runtime.Wait()
	if err == nil || !strings.Contains(err.Error(), "http stopped: listen failed") {
		t.Fatalf("Wait error = %v", err)
	}
}

func TestRuntimeWaitRequiresStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := (&Runtime{ctx: ctx, cancel: cancel}).Wait()
	if err == nil || !strings.Contains(err.Error(), "not started") {
		t.Fatalf("Wait error = %v", err)
	}
}
