package core

import (
	"context"
	"errors"
)

// A turn an Agent keeps running without this AgentMux process, as a detached
// Codex app-server does, survives an AgentMux restart. The caller that
// started it records its AgentTurnRef and, after the restart, rejoins it
// rather than starting the work over.

// AgentTurnRef names a running turn of a native session.
type AgentTurnRef struct {
	SessionID string `json:"session_id"`
	TurnID    string `json:"turn_id"`
}

// TurnRejoiningAgent is an optional Agent capability: following a turn that
// an earlier AgentMux process started with AgentConfigKeepTurns. The events
// are those still to come; the session is closed like any other. It returns
// ErrInvocationTurnGone when the turn can no longer be followed.
type TurnRejoiningAgent interface {
	Agent
	RejoinTurn(ctx context.Context, workDir string, turn AgentTurnRef) (AgentSession, <-chan *Event, error)
}

// ErrInvocationTurnGone means a turn to rejoin is no longer running: it was
// stopped, its process ended, or the Agent cannot keep turns.
var ErrInvocationTurnGone = errors.New("the turn is no longer running")

// EventMetadataKeptTurn marks the events of a turn that outlives this process
// (value "true"); with the event's session and turn ids it can be rejoined.
const EventMetadataKeptTurn = "kept_turn"

type turnDetachKey struct{}

// WithTurnDetach returns ctx carrying owner, the context of the process that
// runs the turn's caller. A kept turn whose ctx ends because owner ended is
// left running, to be rejoined by the next process; one whose ctx ends while
// owner lives was stopped and is interrupted.
func WithTurnDetach(ctx, owner context.Context) context.Context {
	if owner == nil {
		return ctx
	}
	return context.WithValue(ctx, turnDetachKey{}, owner)
}

// TurnOwnerEnded reports whether the process that owns ctx's turn is
// shutting down, so a kept turn should be left running.
func TurnOwnerEnded(ctx context.Context) bool {
	owner, _ := ctx.Value(turnDetachKey{}).(context.Context)
	return owner != nil && owner.Err() != nil
}
