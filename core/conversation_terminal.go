package core

import (
	"context"
	"fmt"
	"strings"
)

// managedTerminalSession resolves an in-memory terminal session or safely
// reattaches its persisted native handle after a daemon restart.
func (e *Engine) managedTerminalSession(ctx context.Context, channelID string, conversation Conversation) (TerminalAgentSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	channelID = strings.TrimSpace(channelID)
	if channelID == "" || strings.TrimSpace(conversation.ID) == "" {
		return nil, fmt.Errorf("channel and conversation are required")
	}
	rt := e.channelRuntime(channelID)
	if rt == nil {
		return nil, fmt.Errorf("channel %q is not running", channelID)
	}

	rt.mu.Lock()
	defer rt.mu.Unlock()
	if existing := rt.sessions[conversation.ID]; existing != nil {
		terminal, ok := existing.(TerminalAgentSession)
		if !ok {
			return nil, nil
		}
		return terminal, nil
	}
	if strings.TrimSpace(conversation.NativeSessionID) == "" || rt.agent == nil {
		return nil, nil
	}
	resumable, ok := rt.agent.(ResumableAgent)
	if !ok {
		return nil, nil
	}
	resumed, err := resumable.StartSessionResume(ctx, conversation.WorkDir, conversation.NativeSessionID)
	if err != nil {
		return nil, err
	}
	terminal, ok := resumed.(TerminalAgentSession)
	if !ok {
		_ = resumed.Close(ctx)
		return nil, nil
	}
	rt.applyRuntimeDefaults(resumed)
	rt.sessions[conversation.ID] = resumed
	return terminal, nil
}

func (e *Engine) TerminalSessionInfo(ctx context.Context, channelID string, conversation Conversation) (TerminalSessionInfo, error) {
	session, err := e.managedTerminalSession(ctx, channelID, conversation)
	if err != nil {
		return TerminalSessionInfo{}, err
	}
	if session == nil {
		return TerminalSessionInfo{}, nil
	}
	return session.TerminalInfo(), nil
}

func (e *Engine) TerminalSnapshot(ctx context.Context, channelID string, conversation Conversation) (string, error) {
	session, err := e.managedTerminalSession(ctx, channelID, conversation)
	if err != nil {
		return "", err
	}
	if session == nil {
		return "", fmt.Errorf("conversation has no managed terminal session")
	}
	return session.TerminalSnapshot(ctx)
}

func (e *Engine) WriteTerminal(ctx context.Context, channelID string, conversation Conversation, text string, submit bool) error {
	session, err := e.managedTerminalSession(ctx, channelID, conversation)
	if err != nil {
		return err
	}
	if session == nil {
		return fmt.Errorf("conversation has no managed terminal session")
	}
	return session.WriteTerminal(ctx, text, submit)
}

func (e *Engine) ResizeTerminal(ctx context.Context, channelID string, conversation Conversation, columns, rows int) error {
	session, err := e.managedTerminalSession(ctx, channelID, conversation)
	if err != nil {
		return err
	}
	if session == nil {
		return fmt.Errorf("conversation has no managed terminal session")
	}
	return session.ResizeTerminal(ctx, columns, rows)
}
