package core

import (
	"context"
	"strings"
)

// BrainHook lets the agent layer take part in a channel turn. The channel
// engine still runs the turn, so streaming, the conversation queue, steering,
// approval cards and / commands work as for any other turn; the hook only adds
// what the agent's brain needs to start workers and carries out what it asked.
type BrainHook interface {
	// BrainBrief returns what the brain of agentID is told before msg, or ""
	// when the agent cannot start workers. An empty brief leaves the turn
	// exactly as it would be without a hook.
	BrainBrief(ctx context.Context, agentID string, msg *Message) string
	// BrainFinished receives the whole final answer of a briefed turn that
	// completed, including any action blocks hidden from the chat.
	BrainFinished(ctx context.Context, agentID string, msg *Message, answer string)
}

// SetBrainHook is called by bootstrap before channels start.
func (e *Engine) SetBrainHook(hook BrainHook) { e.brainHook = hook }

// BrainActionOpen starts the block a brain ends its answer with to have
// AgentMux act. It and everything after it are hidden from the chat.
const BrainActionOpen = "<agentmux-action>"

// brainTurn records the raw final answer of a briefed turn while the channel
// renderers show only its visible part.
type brainTurn struct{ answer string }

type brainTurnKey struct{}

func withBrainTurn(ctx context.Context) (context.Context, *brainTurn) {
	turn := &brainTurn{}
	return context.WithValue(ctx, brainTurnKey{}, turn), turn
}

func brainTurnFrom(ctx context.Context) *brainTurn {
	turn, _ := ctx.Value(brainTurnKey{}).(*brainTurn)
	return turn
}

// brainVisibleText is what the chat sees of a brain's answer: everything
// before its first action block. While the answer streams, a trailing partial
// opening tag is held back too, so the block never flashes in the chat.
func brainVisibleText(text string, streaming bool) string {
	if index := strings.Index(text, BrainActionOpen[:len(BrainActionOpen)-1]); index >= 0 {
		return strings.TrimRight(text[:index], " \t\r\n")
	}
	if streaming {
		for size := len(BrainActionOpen) - 1; size >= 2; size-- {
			if strings.HasSuffix(text, BrainActionOpen[:size]) {
				return strings.TrimRight(text[:len(text)-size], " \t\r\n")
			}
		}
	}
	return text
}

// observeBrainText records an answer event of a briefed turn and returns the
// text to render. Outside a briefed turn it returns text unchanged.
func observeBrainText(ctx context.Context, text string) string {
	turn := brainTurnFrom(ctx)
	if turn == nil {
		return text
	}
	turn.answer = text
	return brainVisibleText(text, true)
}
