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
func (e *Engine) ExecuteTrigger(ctx context.Context, tr Trigger, fallbackAgent Agent, fallbackWorkDir, input string, workspace ...WorkspaceInitOptions) (string, error) {
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
	opts := WorkspaceInitOptions{AgentID: tr.AgentID, WorkDir: fallbackWorkDir}
	if len(workspace) > 0 {
		opts = workspace[0]
		if opts.AgentID == "" {
			opts.AgentID = tr.AgentID
		}
		if opts.WorkDir == "" {
			opts.WorkDir = fallbackWorkDir
		}
	}
	if rt != nil && rt.agent != nil {
		agent, workDir = rt.agent, rt.workDir
		opts = rt.workspace
		if opts.WorkDir == "" {
			opts.WorkDir = workDir
		}
	}
	if agent == nil {
		return "", fmt.Errorf("trigger %q has no agent to run (bind an agent or a channel with an agent)", tr.Name)
	}
	data["runtime_id"] = opts.RuntimeID
	if agent != nil {
		data["agent_name"] = agent.Name()
	}

	// Reuse the channel's per-chat session unless the trigger asks for a
	// fresh one (cc-connect session_mode semantics).
	reuse := tr.SessionMode != SessionModeNewPerRun && rt != nil && rt.agent != nil && tr.ChatID != ""
	var sess AgentSession
	var conv *Conversation
	var created bool
	var err error
	if reuse {
		sess, conv, created, err = rt.session(ctx, &Message{
			ChatID:          tr.ChatID,
			ConversationKey: "chat:" + tr.ChatID,
			ChannelID:       tr.ChannelID,
			Origin:          OriginCron,
		})
	} else {
		workDir, err = e.initializeWorkspace(ctx, opts, workDir)
		if err != nil {
			return "", fmt.Errorf("start session: %w", err)
		}
		sess, err = e.startAgentSession(ctx, agent, workDir, nil)
		if err == nil {
			data["session_id"] = sessionObservationID(sess)
			e.emit(ctx, HookSessionStarted, data)
			defer func() {
				e.emit(context.Background(), HookSessionEnded, data)
				_ = sess.Close(context.Background())
			}()
		}
	}
	if err != nil {
		return "", fmt.Errorf("start session: %w", err)
	}
	data["session_id"] = sessionObservationID(sess)
	if conv != nil {
		data["conversation_id"] = conv.ID
	}
	if reuse && created {
		e.emit(ctx, HookSessionStarted, data)
	}

	result, err := e.streamTurn(ctx, sess, prompt, nil, data)
	if reuse {
		e.persistConversationTurn(ctx, conv, sess)
	}
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
