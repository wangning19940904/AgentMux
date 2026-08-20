package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// projectRuntime holds the live platform/agent instances for one project.
type projectRuntime struct {
	owner             *Engine
	name              string
	conversationScope string
	agent             Agent
	platforms         []Platform
	workDir           string
	workspace         WorkspaceInitOptions

	mu       sync.Mutex
	sessions map[string]AgentSession // chatID -> session
}

func (e *Engine) replyAll(ctx context.Context, pr *projectRuntime, msg *Message, text string) {
	for _, p := range pr.platforms {
		if p.Name() == msg.Platform {
			if err := p.Reply(ctx, msg, text); err != nil {
				e.log.Error("reply", "platform", p.Name(), "err", err)
			}
			return
		}
	}
}

func (e *Engine) handleProjectConversationCommand(ctx context.Context, pr *projectRuntime, msg *Message) bool {
	if !isConversationCommand(msg.Text) {
		return false
	}
	e.resetConversation(ctx, pr.scope(), msg.ChatID, msg.ChatType, ResolveConversationKey(msg), pr.workspace.AgentID, pr.dropSession)
	e.replyAll(ctx, pr, msg, conversationResetReply)
	return true
}

func (pr *projectRuntime) scope() string {
	if pr.conversationScope != "" {
		return pr.conversationScope
	}
	return "project:" + pr.name
}

// dropSession closes and removes the cached in-memory session for cacheKey.
// With durable conversations cacheKey is the conversation id; without them it
// is the platform chat id.
func (pr *projectRuntime) dropSession(ctx context.Context, cacheKey string) {
	pr.mu.Lock()
	s, ok := pr.sessions[cacheKey]
	if ok {
		delete(pr.sessions, cacheKey)
	}
	pr.mu.Unlock()
	if ok && s != nil {
		data := map[string]string{
			"project": pr.name, "agent_id": pr.workspace.AgentID, "runtime_id": pr.workspace.RuntimeID,
			"session_id": sessionObservationID(s), "conversation_id": cacheKey,
		}
		if pr.agent != nil {
			data["agent_name"] = pr.agent.Name()
		}
		pr.owner.emit(ctx, HookSessionEnded, data)
		_ = s.Close(ctx)
	}
}

const conversationResetReply = "Started a new conversation. Previous context has been cleared."

func isConversationCommand(text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "/new", "/clear", "/reset":
		return true
	default:
		return false
	}
}

func (e *Engine) resetConversation(ctx context.Context, scope, chatID, chatType, conversationKey, agentID string, dropSession func(context.Context, string)) {
	cacheKey := conversationKey
	if cacheKey == "" {
		cacheKey = "chat:" + chatID
	}
	if e.conversations != nil {
		conv, _, err := e.conversations.GetOrCreateConversation(ctx, Conversation{
			Scope:           scope,
			ConversationKey: conversationKey,
			ChatID:          chatID,
			ChatType:        chatType,
			AgentID:         agentID,
		})
		if err != nil {
			e.log.Warn("resolve conversation for command", "scope", scope, "chat_id", chatID, "err", err)
		} else if conv != nil {
			cacheKey = conv.ID
			if endErr := e.conversations.EndConversation(ctx, conv.ID); endErr != nil {
				e.log.Warn("end conversation", "conversation", conv.ID, "err", endErr)
			}
		}
	}
	if dropSession != nil {
		dropSession(ctx, cacheKey)
	}
}

func (pr *projectRuntime) session(ctx context.Context, chatID, chatType, conversationKey string) (AgentSession, *Conversation, bool, error) {
	if pr.agent == nil {
		return nil, nil, false, fmt.Errorf("project %q has no agent", pr.name)
	}
	conv, workDir, err := pr.owner.prepareConversation(ctx, pr.scope(), chatID, chatType, conversationKey, pr.workspace, pr.workDir)
	if err != nil {
		return nil, nil, false, err
	}
	cacheKey := conversationKey
	if cacheKey == "" {
		cacheKey = "chat:" + chatID
	}
	if conv != nil {
		cacheKey = conv.ID
	}

	pr.mu.Lock()
	defer pr.mu.Unlock()
	if s, ok := pr.sessions[cacheKey]; ok {
		return s, conv, false, nil
	}
	s, err := pr.owner.startAgentSession(ctx, pr.agent, workDir, conv)
	if err != nil {
		return nil, nil, false, err
	}
	pr.owner.persistConversationSessionHandle(ctx, conv, s)
	pr.sessions[cacheKey] = s
	return s, conv, true, nil
}

// prepareConversation resolves (or creates) the durable conversation for
// (scope, chatID) and prepares the agent's configured working directory. Every
// conversation for an agent runs directly in that directory while conversation
// context remains separate through each persisted native session id.
// When no conversation store is attached it returns a nil conversation and the
// initialized fallback work dir, preserving the legacy chatID-keyed behavior.
func (e *Engine) prepareConversation(ctx context.Context, scope, chatID, chatType, conversationKey string, ws WorkspaceInitOptions, fallbackWorkDir string) (*Conversation, string, error) {
	if conversationKey == "" {
		conversationKey = "chat:" + chatID
	}
	ws.ConversationScope = scope
	ws.ConversationKey = conversationKey
	workDir, err := e.initializeWorkspace(ctx, ws, fallbackWorkDir)
	if err != nil {
		return nil, "", err
	}
	if e.conversations == nil {
		return nil, workDir, nil
	}

	conv, _, err := e.conversations.GetOrCreateConversation(ctx, Conversation{
		Scope:           scope,
		ConversationKey: conversationKey,
		ChatID:          chatID,
		ChatType:        chatType,
		AgentID:         ws.AgentID,
		WorkDir:         workDir,
	})
	if err != nil {
		return nil, "", fmt.Errorf("resolve conversation: %w", err)
	}

	// Older AgentMux versions stored each conversation under
	// <work_dir>/.agentmux/conversations/.../cwd. Migrate those records to the
	// configured work directory. Clear the native resume handle at the same time
	// because runtimes such as Codex can retain the old cwd in a resumed thread.
	if conv.WorkDir != workDir {
		if uerr := e.conversations.UpdateConversationSession(ctx, conv.ID, "", workDir); uerr != nil {
			e.log.Warn("persist conversation workdir", "conversation", conv.ID, "err", uerr)
		}
		conv.WorkDir = workDir
		conv.NativeSessionID = ""
	}
	return conv, workDir, nil
}

// startAgentSession starts a new agent session, resuming the conversation's
// native session id when both the agent and a stored id are available.
func (e *Engine) startAgentSession(ctx context.Context, agent Agent, workDir string, conv *Conversation) (AgentSession, error) {
	if telemetry := e.observationChildTelemetry(); telemetry.Endpoint != "" && telemetry.Token != "" {
		ctx = WithObservationChildTelemetry(ctx, telemetry)
	}
	if conv != nil && conv.NativeSessionID != "" {
		if ra, ok := agent.(ResumableAgent); ok {
			sess, err := ra.StartSessionResume(ctx, workDir, conv.NativeSessionID)
			if err == nil {
				return sess, nil
			}
			if !errors.Is(err, ErrNativeSessionUnavailable) {
				return nil, err
			}

			// Codex app-server occasionally prunes old native threads. Forget
			// only that known-stale handle, then let the same chat continue in a
			// new native session. Persisting the cleared id prevents every later
			// command (including /model) from failing before it can render.
			e.log.Warn("native session is unavailable; starting a fresh session",
				"conversation", conv.ID,
				"native_session_id", conv.NativeSessionID,
				"err", err)
			if e.conversations != nil {
				if clearErr := e.conversations.UpdateConversationSession(ctx, conv.ID, "", conv.WorkDir); clearErr != nil {
					e.log.Warn("clear unavailable native session", "conversation", conv.ID, "err", clearErr)
				}
			}
			conv.NativeSessionID = ""
		}
	}
	return agent.StartSession(ctx, workDir)
}

// persistConversationTurn records a completed turn: it bumps the conversation
// activity counter and persists any newly discovered native session id so
// later turns and restarts can resume.
func (e *Engine) persistConversationTurn(ctx context.Context, conv *Conversation, sess AgentSession) {
	if e.conversations == nil || conv == nil {
		return
	}
	if err := e.conversations.TouchConversation(ctx, conv.ID); err != nil {
		e.log.Warn("touch conversation", "conversation", conv.ID, "err", err)
	}
	e.persistConversationSessionHandle(ctx, conv, sess)
}

// persistConversationSessionHandle records a resume handle as soon as a
// session exposes it. Persistent terminal runtimes know their tmux identity
// before the first turn, so a daemon crash during that turn must not orphan the
// backing process merely because the ordinary completed-turn persistence did
// not run yet.
func (e *Engine) persistConversationSessionHandle(ctx context.Context, conv *Conversation, sess AgentSession) {
	if e.conversations == nil || conv == nil || sess == nil {
		return
	}
	ns, ok := sess.(NativeSessioned)
	if !ok {
		return
	}
	if id := ns.NativeSessionID(); id != "" && id != conv.NativeSessionID {
		if err := e.conversations.UpdateConversationSession(ctx, conv.ID, id, conv.WorkDir); err != nil {
			e.log.Warn("persist native session id", "conversation", conv.ID, "err", err)
			return
		}
		conv.NativeSessionID = id
	}
}
