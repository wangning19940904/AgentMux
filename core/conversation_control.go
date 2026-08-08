package core

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	ConversationStatusIdle     = "idle"
	ConversationStatusOffline  = "offline"
	ConversationStatusStopping = "stopping"
)

// ConversationRuntimeState reports the current in-memory turn for one
// conversation without treating native sessions owned by other applications
// as controllable work.
func (e *Engine) ConversationRuntimeState(ctx context.Context, channelID, conversationKey string) (ConversationRuntimeState, error) {
	if err := ctx.Err(); err != nil {
		return ConversationRuntimeState{}, err
	}
	channelID = strings.TrimSpace(channelID)
	conversationKey = normalizedConversationRuntimeKey(conversationKey)
	if channelID == "" {
		return ConversationRuntimeState{}, fmt.Errorf("channel is required")
	}
	rt := e.channelRuntime(channelID)
	if rt == nil {
		return ConversationRuntimeState{Status: ConversationStatusOffline}, nil
	}

	rt.controlMu.Lock()
	defer rt.controlMu.Unlock()
	state := rt.controlTasks[conversationKey]
	if state != nil && state.active != nil {
		status := string(state.active.task.Status)
		if status == "" {
			status = string(ChannelTaskRunning)
		}
		if state.active.stopRequested {
			status = ConversationStatusStopping
		}
		return ConversationRuntimeState{Status: status, CanStop: !state.active.stopRequested, TaskID: state.active.task.ID}, nil
	}
	if turn := rt.directTurns[conversationKey]; turn != nil {
		status := string(ChannelTaskRunning)
		if turn.stopRequested {
			status = ConversationStatusStopping
		}
		return ConversationRuntimeState{Status: status, CanStop: !turn.stopRequested}, nil
	}
	if state != nil && len(state.queue) > 0 {
		return ConversationRuntimeState{Status: string(ChannelTaskQueued), TaskID: state.queue[0].task.ID}, nil
	}
	return ConversationRuntimeState{Status: ConversationStatusIdle}, nil
}

// StopConversation interrupts only the live turn owned by this AgentMux
// runtime. expectedTaskID keeps a delayed UI action from stopping a newer
// durable task; direct turns do not have durable task IDs.
func (e *Engine) StopConversation(ctx context.Context, channelID, conversationKey, expectedTaskID string) (ConversationRuntimeState, error) {
	if err := ctx.Err(); err != nil {
		return ConversationRuntimeState{}, err
	}
	channelID = strings.TrimSpace(channelID)
	conversationKey = normalizedConversationRuntimeKey(conversationKey)
	expectedTaskID = strings.TrimSpace(expectedTaskID)
	rt := e.channelRuntime(channelID)
	if rt == nil {
		return ConversationRuntimeState{}, fmt.Errorf("channel %q is not running", channelID)
	}

	rt.controlMu.Lock()
	state := rt.controlTasks[conversationKey]
	if state != nil && state.active != nil {
		active := state.active
		if expectedTaskID != "" && active.task.ID != expectedTaskID {
			rt.controlMu.Unlock()
			return ConversationRuntimeState{}, fmt.Errorf("task %q is no longer active", expectedTaskID)
		}
		active.stopRequested = true
		cancelTask := active.cancel
		session := active.session
		taskID := active.task.ID
		rt.controlMu.Unlock()

		if interactive, ok := session.(InteractiveAgentSession); ok {
			interruptCtx, cancelInterrupt := context.WithTimeout(ctx, 15*time.Second)
			if err := interactive.Interrupt(interruptCtx); err != nil && e.log != nil {
				e.log.Warn("interrupt console conversation", "task", taskID, "err", err)
			}
			cancelInterrupt()
		}
		if cancelTask != nil {
			cancelTask()
		}
		return ConversationRuntimeState{Status: ConversationStatusStopping, TaskID: taskID}, nil
	}
	if turn := rt.directTurns[conversationKey]; turn != nil {
		turn.stopRequested = true
		cancelTurn := turn.cancel
		rt.controlMu.Unlock()
		if cancelTurn != nil {
			cancelTurn()
		}
		return ConversationRuntimeState{Status: ConversationStatusStopping}, nil
	}
	rt.controlMu.Unlock()
	return ConversationRuntimeState{}, fmt.Errorf("conversation is not running")
}

func normalizedConversationRuntimeKey(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "default"
	}
	return value
}
