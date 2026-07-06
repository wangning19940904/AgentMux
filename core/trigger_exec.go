package core

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// DefaultTriggerTimeout bounds a single cron/webhook trigger execution
// (matches cc-connect's default cron job timeout).
const DefaultTriggerTimeout = 30 * time.Minute

// ExecuteTrigger runs a cron or webhook trigger: it fires the matching
// lifecycle event, sends the trigger prompt (plus optional input, e.g. a
// webhook payload) to the bound agent and pushes the final answer to the
// bound channel chat. fallbackAgent/fallbackWorkDir are used when the trigger
// has no attached channel or the channel has no agent bound.
func (e *Engine) ExecuteTrigger(ctx context.Context, tr Trigger, fallbackAgent Agent, fallbackWorkDir, input string) (string, error) {
	data := map[string]string{
		"trigger_id": tr.ID,
		"trigger":    tr.Name,
		"kind":       tr.Kind,
		"channel_id": tr.ChannelID,
		"chat_id":    tr.ChatID,
		"agent_id":   tr.AgentID,
		"text":       tr.Prompt,
	}
	event := HookCronTriggered
	origin := OriginCron
	if tr.Kind == TriggerWebhook {
		event = HookWebhookTriggered
		origin = OriginWebhook
	}
	data["origin"] = origin
	e.emit(ctx, event, data)

	prompt := strings.TrimSpace(tr.Prompt)
	if input = strings.TrimSpace(input); input != "" {
		if prompt == "" {
			prompt = input
		} else {
			prompt = prompt + "\n\n" + input
		}
	}
	if prompt == "" {
		return "", fmt.Errorf("trigger %q has no prompt", tr.Name)
	}

	var rt *channelRuntime
	if tr.ChannelID != "" {
		rt = e.channelRuntime(tr.ChannelID)
		if rt == nil {
			return "", fmt.Errorf("channel %q is not attached (enable the channel first)", tr.ChannelID)
		}
	}

	agent, workDir := fallbackAgent, fallbackWorkDir
	if rt != nil && rt.agent != nil {
		agent, workDir = rt.agent, rt.workDir
	}
	if agent == nil {
		return "", fmt.Errorf("trigger %q has no agent to run (bind an agent or a channel with an agent)", tr.Name)
	}

	// Reuse the channel's per-chat session unless the trigger asks for a
	// fresh one (cc-connect session_mode semantics).
	reuse := tr.SessionMode != SessionModeNewPerRun && rt != nil && rt.agent != nil && tr.ChatID != ""
	var sess AgentSession
	var err error
	if reuse {
		sess, _, err = rt.session(ctx, tr.ChatID)
	} else {
		sess, err = agent.StartSession(ctx, workDir)
		if err == nil {
			defer func() { _ = sess.Close(context.Background()) }()
		}
	}
	if err != nil {
		return "", fmt.Errorf("start session: %w", err)
	}

	result, err := e.streamTurn(ctx, sess, prompt, nil, data)
	if err != nil {
		return result, err
	}
	if rt != nil && tr.ChatID != "" && result != "" {
		if sendErr := rt.platform.Send(ctx, tr.ChatID, result); sendErr != nil {
			return result, fmt.Errorf("deliver result to channel: %w", sendErr)
		}
	}
	return result, nil
}
