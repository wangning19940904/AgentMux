package cliagents

import (
	"context"
	"errors"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
)

func TestCodexInterruptedTurnDoesNotReportPartialAnswerAsSuccess(t *testing.T) {
	for _, answer := range []string{"", "unfinished answer"} {
		mapper := &codexEventMapper{threadID: "thread-1", turnID: "turn-1", answer: answer}
		events, done, err := mapper.mapNotification("turn/completed", map[string]any{
			"turn": map[string]any{"id": "turn-1", "status": "interrupted"},
		})
		if !done || !errors.Is(err, context.Canceled) || len(events) != 1 || events[0].Type != core.EventModelResponse || events[0].Status != "interrupted" || events[0].Final {
			t.Fatalf("interrupted turn: events=%+v done=%t err=%v", events, done, err)
		}
	}
}
