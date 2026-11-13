package bootstrap

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"
)

func TestDaemonListenerWaitsForExistingBackend(t *testing.T) {
	existing, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer existing.Close()
	addr := existing.Addr().String()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	type result struct {
		listener net.Listener
		err      error
	}
	results := make(chan result, 1)
	go func() {
		listener, err := waitForDaemonListener(ctx, log, addr, 5*time.Millisecond)
		results <- result{listener: listener, err: err}
	}()
	select {
	case result := <-results:
		if result.listener != nil {
			result.listener.Close()
		}
		t.Fatalf("standby returned while the existing backend owns the address: %v", result.err)
	case <-time.After(20 * time.Millisecond):
	}
	if err := existing.Close(); err != nil {
		t.Fatal(err)
	}
	outcome := <-results
	if outcome.err != nil {
		t.Fatal(outcome.err)
	}
	defer outcome.listener.Close()
	if outcome.listener.Addr().String() != addr {
		t.Fatalf("takeover address = %s, want %s", outcome.listener.Addr(), addr)
	}
}

func TestDaemonListenerStandbyCanBeCancelled(t *testing.T) {
	existing, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer existing.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	listener, err := waitForDaemonListener(ctx, log, existing.Addr().String(), time.Second)
	if listener != nil {
		listener.Close()
		t.Fatal("cancelled standby acquired a listener")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("standby error = %v", err)
	}
}

func TestDaemonListenerReturnsInvalidAddress(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	listener, err := waitForDaemonListener(ctx, log, "127.0.0.1:invalid-port", time.Millisecond)
	if listener != nil || err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("invalid address returned listener=%v, err=%v", listener, err)
	}
}
