package core

import (
	"context"
	"fmt"
	"strings"
)

// managedTerminalSession resolves an in-memory terminal session or safely
// reattaches its persisted native handle after a daemon restart.
func (e *Engine) managedTerminalSession(ctx context.Context, channelID string, conversation Conversation) (TerminalAgentSession, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	channelID = strings.TrimSpace(channelID)
	if channelID == "" || strings.TrimSpace(conversation.ID) == "" {
		return nil, nil, fmt.Errorf("channel and conversation are required")
	}
	rt := e.channelRuntime(channelID)
	if rt == nil {
		return nil, nil, fmt.Errorf("channel %q is not running", channelID)
	}

	rt.mu.Lock()
	defer rt.mu.Unlock()
	if existing := rt.sessions[conversation.ID]; existing != nil {
		terminal, ok := existing.session.(TerminalAgentSession)
		if !ok {
			return nil, nil, nil
		}
		existing.active++
		return terminal, rt.bindingRelease(existing), nil
	}
	generation := rt.ensureGenerationLocked()
	for {
		var wait <-chan struct{}
		for binding := range rt.retiredSessions {
			if binding.active > 0 && binding.generation.workDir == generation.workDir {
				wait = binding.done
				break
			}
		}
		if wait == nil {
			break
		}
		rt.mu.Unlock()
		select {
		case <-ctx.Done():
			rt.mu.Lock()
			return nil, nil, ctx.Err()
		case <-wait:
		}
		rt.mu.Lock()
		generation = rt.ensureGenerationLocked()
	}
	if strings.TrimSpace(conversation.NativeSessionID) == "" || generation.agent == nil {
		return nil, nil, nil
	}
	resumable, ok := generation.agent.(ResumableAgent)
	if !ok {
		return nil, nil, nil
	}
	resumed, err := resumable.StartSessionResume(ctx, conversation.WorkDir, conversation.NativeSessionID)
	if err != nil {
		return nil, nil, err
	}
	terminal, ok := resumed.(TerminalAgentSession)
	if !ok {
		_ = resumed.Close(ctx)
		return nil, nil, nil
	}
	rt.applyRuntimeDefaultsFrom(resumed, generation.defaultSettings)
	binding := &channelSessionBinding{
		cacheKey: conversation.ID, session: resumed, generation: generation, active: 1, done: make(chan struct{}),
	}
	generation.sessions++
	rt.sessions[conversation.ID] = binding
	return terminal, rt.bindingRelease(binding), nil
}

func (e *Engine) TerminalSessionInfo(ctx context.Context, channelID string, conversation Conversation) (TerminalSessionInfo, error) {
	session, releaseSession, err := e.managedTerminalSession(ctx, channelID, conversation)
	if err != nil {
		return TerminalSessionInfo{}, err
	}
	if session == nil {
		return TerminalSessionInfo{}, nil
	}
	defer releaseSession()
	return session.TerminalInfo(), nil
}

func (e *Engine) TerminalSnapshot(ctx context.Context, channelID string, conversation Conversation) (string, error) {
	session, releaseSession, err := e.managedTerminalSession(ctx, channelID, conversation)
	if err != nil {
		return "", err
	}
	if session == nil {
		return "", fmt.Errorf("conversation has no managed terminal session")
	}
	defer releaseSession()
	return session.TerminalSnapshot(ctx)
}

func (e *Engine) WriteTerminal(ctx context.Context, channelID string, conversation Conversation, text string, submit bool) error {
	session, releaseSession, err := e.managedTerminalSession(ctx, channelID, conversation)
	if err != nil {
		return err
	}
	if session == nil {
		return fmt.Errorf("conversation has no managed terminal session")
	}
	defer releaseSession()
	return session.WriteTerminal(ctx, text, submit)
}

func (e *Engine) ResizeTerminal(ctx context.Context, channelID string, conversation Conversation, columns, rows int) error {
	session, releaseSession, err := e.managedTerminalSession(ctx, channelID, conversation)
	if err != nil {
		return err
	}
	if session == nil {
		return fmt.Errorf("conversation has no managed terminal session")
	}
	defer releaseSession()
	return session.ResizeTerminal(ctx, columns, rows)
}
