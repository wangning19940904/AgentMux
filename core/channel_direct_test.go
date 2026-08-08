package core

import (
	"context"
	"testing"
)

func TestDirectChannelTurnIsSingleFlightPerConversation(t *testing.T) {
	rt := &channelRuntime{directTurns: map[string]*directChannelTurn{}}
	ctx, cancel := context.WithCancel(context.Background())
	turn, ok := rt.beginDirectTurn("chat-1", "user-1", cancel)
	if !ok || turn == nil {
		t.Fatal("first direct turn was not accepted")
	}
	_, second := rt.beginDirectTurn("chat-1", "user-1", func() {})
	if second {
		t.Fatal("overlapping direct turn was accepted")
	}
	if _, other := rt.beginDirectTurn("chat-2", "user-2", func() {}); !other {
		t.Fatal("independent conversation was blocked")
	}
	rt.finishDirectTurn("chat-1", turn)
	if _, next := rt.beginDirectTurn("chat-1", "user-1", func() {}); !next {
		t.Fatal("conversation remained blocked after the turn finished")
	}
	select {
	case <-ctx.Done():
		t.Fatal("finishing a turn must not cancel its context")
	default:
	}
}

func TestCancelDirectTurnForResetWaitsForCompletion(t *testing.T) {
	rt := &channelRuntime{directTurns: map[string]*directChannelTurn{}}
	ctx, cancel := context.WithCancel(context.Background())
	turn, ok := rt.beginDirectTurn("chat-1", "user-1", cancel)
	if !ok {
		t.Fatal("direct turn was not accepted")
	}
	finished := make(chan struct{})
	go func() {
		<-ctx.Done()
		rt.finishDirectTurn("chat-1", turn)
		close(finished)
	}()
	rt.cancelDirectTurnForReset(context.Background(), "chat-1")
	select {
	case <-finished:
	default:
		t.Fatal("conversation reset returned before the cancelled turn finished")
	}
}

func TestChannelTurnTimeoutPrefersGenericAndFallsBackToLegacy(t *testing.T) {
	generic := Channel{Config: map[string]string{
		ChannelConfigTurnTimeout:      "7",
		ChannelConfigCodexTurnTimeout: "9",
	}}
	if got := ChannelTurnTimeout(generic); got.Minutes() != 7 {
		t.Fatalf("generic timeout = %s", got)
	}
	legacy := Channel{Config: map[string]string{ChannelConfigCodexTurnTimeout: "9"}}
	if got := ChannelTurnTimeout(legacy); got.Minutes() != 9 {
		t.Fatalf("legacy timeout = %s", got)
	}
}
