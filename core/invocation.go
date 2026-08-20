package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// InvocationRequest is a transport-neutral request to run an Agent directly.
// Exactly one of AgentID and Project must be set. ConversationID is an opaque,
// caller-controlled key; omit it for a new conversation and reuse the returned
// value on later calls to continue the same native Agent session.
type InvocationRequest struct {
	AgentID        string            `json:"agent_id,omitempty"`
	Project        string            `json:"project,omitempty"`
	ConversationID string            `json:"conversation_id,omitempty"`
	Input          string            `json:"input"`
	Attachments    []AgentAttachment `json:"attachments,omitempty"`
	OutputSchema   map[string]any    `json:"output_schema,omitempty"`
}

// InvocationResult is the completed, synchronous result of an Agent turn.
type InvocationResult struct {
	ID             string     `json:"id"`
	AgentID        string     `json:"agent_id,omitempty"`
	Project        string     `json:"project,omitempty"`
	ConversationID string     `json:"conversation_id"`
	SessionID      string     `json:"session_id,omitempty"`
	Answer         string     `json:"answer"`
	DurationMS     int64      `json:"duration_ms"`
	Usage          *TurnUsage `json:"usage,omitempty"`
}

// InvocationStreamEvent is one transport-neutral update from a streaming
// invocation. Output and thinking text are snapshots: clients should replace
// the previously displayed value instead of blindly appending it.
type InvocationStreamEvent struct {
	Type           string            `json:"type"`
	InvocationID   string            `json:"invocation_id,omitempty"`
	ConversationID string            `json:"conversation_id,omitempty"`
	SessionID      string            `json:"session_id,omitempty"`
	EventID        string            `json:"event_id,omitempty"`
	TurnID         string            `json:"turn_id,omitempty"`
	ItemID         string            `json:"item_id,omitempty"`
	Text           string            `json:"text,omitempty"`
	Status         string            `json:"status,omitempty"`
	Final          bool              `json:"final,omitempty"`
	DurationMS     int64             `json:"duration_ms,omitempty"`
	ToolName       string            `json:"tool_name,omitempty"`
	ToolCallID     string            `json:"tool_call_id,omitempty"`
	ToolInput      string            `json:"tool_input,omitempty"`
	ToolResult     string            `json:"tool_result,omitempty"`
	Interaction    *AgentInteraction `json:"interaction,omitempty"`
	Usage          *TurnUsage        `json:"usage,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	Error          string            `json:"error,omitempty"`
	Result         *InvocationResult `json:"result,omitempty"`
}

type InvocationEventSink func(InvocationStreamEvent) error

// Invoker is the direct Agent execution surface used by the HTTP API. It is
// deliberately separate from Platform and Sender: an API caller is neither a
// chat channel nor an outbound delivery target.
type Invoker interface {
	Invoke(ctx context.Context, req InvocationRequest) (InvocationResult, error)
}

// StreamingInvoker is the optional streaming counterpart of Invoker. The
// server exposes it over SSE while keeping the synchronous endpoint stable.
type StreamingInvoker interface {
	InvokeStream(ctx context.Context, req InvocationRequest, sink InvocationEventSink) (InvocationResult, error)
}

var (
	ErrInvalidInvocation  = errors.New("invalid invocation")
	ErrInvocationNotFound = errors.New("invocation target not found")
	ErrInvocationDisabled = errors.New("invocation target is disabled")
	ErrInvocationBusy     = errors.New("conversation already has an active invocation")
	ErrInvocationRuntime  = errors.New("invocation runtime unavailable")
)

// Invoke implements Invoker for config.toml projects and console-managed
// Agent instances. Project runtimes are long-lived and reuse their in-memory
// sessions. Managed Agent runtimes are created per request and resume through
// the durable conversation's native session id.
func (c *ConnectService) Invoke(ctx context.Context, req InvocationRequest) (InvocationResult, error) {
	return c.invoke(ctx, req, nil)
}

// InvokeStream runs the same invocation while forwarding Agent lifecycle and
// output snapshots to sink. A nil sink behaves exactly like Invoke.
func (c *ConnectService) InvokeStream(ctx context.Context, req InvocationRequest, sink InvocationEventSink) (InvocationResult, error) {
	return c.invoke(ctx, req, sink)
}

func (c *ConnectService) invoke(ctx context.Context, req InvocationRequest, sink InvocationEventSink) (InvocationResult, error) {
	if c == nil || c.eng == nil {
		return InvocationResult{}, fmt.Errorf("%w: engine is unavailable", ErrInvocationRuntime)
	}
	req.AgentID = strings.TrimSpace(req.AgentID)
	req.Project = strings.TrimSpace(req.Project)
	if (req.AgentID == "") == (req.Project == "") {
		return InvocationResult{}, fmt.Errorf("%w: exactly one of agent_id and project is required", ErrInvalidInvocation)
	}
	if err := validateInvocationInput(&req); err != nil {
		return InvocationResult{}, err
	}
	if req.Project != "" {
		return c.eng.invokeProject(ctx, req, sink)
	}
	if result, found, err := c.eng.invokeProjectAgent(ctx, req, sink); found {
		return result, err
	}
	if c.store == nil {
		return InvocationResult{}, fmt.Errorf("%w: agent store is unavailable", ErrInvocationRuntime)
	}
	inst, err := c.store.GetAgentInstance(ctx, req.AgentID)
	if err != nil {
		return InvocationResult{}, fmt.Errorf("load agent %q: %w", req.AgentID, err)
	}
	if inst == nil {
		return InvocationResult{}, fmt.Errorf("%w: agent %q", ErrInvocationNotFound, req.AgentID)
	}
	if !inst.Enabled {
		return InvocationResult{}, fmt.Errorf("%w: agent %q", ErrInvocationDisabled, req.AgentID)
	}
	agent, workDir, workspace := c.resolveAgent(ctx, req.AgentID)
	if agent == nil {
		return InvocationResult{}, fmt.Errorf("%w: could not start agent %q", ErrInvocationRuntime, req.AgentID)
	}
	runtime := &projectRuntime{
		owner:             c.eng,
		name:              req.AgentID,
		conversationScope: "api-agent:" + req.AgentID,
		agent:             agent,
		workDir:           workDir,
		workspace:         workspace,
		sessions:          map[string]AgentSession{},
	}
	defer c.eng.closeInvocationRuntime(runtime)
	return c.eng.invokeRuntime(ctx, runtime, req, "agent:"+req.AgentID, sink)
}

func validateInvocationInput(req *InvocationRequest) error {
	if req == nil {
		return fmt.Errorf("%w: request is required", ErrInvalidInvocation)
	}
	req.Input = strings.TrimSpace(req.Input)
	if req.Input == "" {
		return fmt.Errorf("%w: input is required", ErrInvalidInvocation)
	}
	req.ConversationID = strings.TrimSpace(req.ConversationID)
	if len(req.ConversationID) > 200 {
		return fmt.Errorf("%w: conversation_id must be at most 200 characters", ErrInvalidInvocation)
	}
	if strings.IndexFunc(req.ConversationID, unicode.IsControl) >= 0 {
		return fmt.Errorf("%w: conversation_id must not contain control characters", ErrInvalidInvocation)
	}
	return nil
}

func (e *Engine) invokeProject(ctx context.Context, req InvocationRequest, sink InvocationEventSink) (InvocationResult, error) {
	e.mu.RLock()
	runtime := e.projects[req.Project]
	e.mu.RUnlock()
	if runtime == nil {
		return InvocationResult{}, fmt.Errorf("%w: project %q", ErrInvocationNotFound, req.Project)
	}
	return e.invokeRuntime(ctx, runtime, req, "project:"+req.Project, sink)
}

func (e *Engine) invokeProjectAgent(ctx context.Context, req InvocationRequest, sink InvocationEventSink) (InvocationResult, bool, error) {
	e.mu.RLock()
	project := e.projectByAgentID[req.AgentID]
	runtime := e.projects[project]
	e.mu.RUnlock()
	if runtime == nil {
		return InvocationResult{}, false, nil
	}
	req.Project = project
	result, err := e.invokeRuntime(ctx, runtime, req, "project:"+project, sink)
	return result, true, err
}

func (e *Engine) invokeRuntime(ctx context.Context, runtime *projectRuntime, req InvocationRequest, targetKey string, sink InvocationEventSink) (InvocationResult, error) {
	if runtime == nil || runtime.agent == nil {
		return InvocationResult{}, fmt.Errorf("%w: target has no agent", ErrInvocationRuntime)
	}
	if req.ConversationID == "" {
		req.ConversationID = "conv_" + randomHexID(12)
	}
	invocationID := "inv_" + randomHexID(12)
	conversationKey := "api:" + req.ConversationID
	if !e.beginInvocation(targetKey, conversationKey) {
		return InvocationResult{}, fmt.Errorf("%w: %q", ErrInvocationBusy, req.ConversationID)
	}
	defer e.finishInvocation(targetKey, conversationKey)
	turnCtx, cancelTurn := context.WithCancel(ctx)
	defer cancelTurn()

	startedAt := time.Now()
	result := InvocationResult{
		ID:             invocationID,
		AgentID:        req.AgentID,
		Project:        req.Project,
		ConversationID: req.ConversationID,
	}
	emitStream := func(event InvocationStreamEvent) error {
		if sink == nil {
			return nil
		}
		event.InvocationID = invocationID
		event.ConversationID = req.ConversationID
		if event.SessionID == "" {
			event.SessionID = result.SessionID
		}
		return sink(event)
	}
	if err := emitStream(InvocationStreamEvent{Type: "started"}); err != nil {
		result.DurationMS = time.Since(startedAt).Milliseconds()
		return result, err
	}

	msg := &Message{
		ID:              invocationID,
		ChatID:          req.ConversationID,
		ChatType:        "api",
		ConversationKey: conversationKey,
		UserID:          "agentmux-api",
		UserName:        "AgentMux API",
		Text:            req.Input,
		Timestamp:       startedAt.UTC(),
		Platform:        "api",
		Project:         req.Project,
		Origin:          OriginAPI,
	}
	data := eventData(msg)
	data["invocation_id"] = invocationID
	e.emit(turnCtx, HookMessageReceived, data)

	sess, conversation, created, err := runtime.session(turnCtx, msg.ChatID, msg.ChatType, conversationKey)
	if err != nil {
		e.emit(turnCtx, HookError, withError(data, err))
		result.DurationMS = time.Since(startedAt).Milliseconds()
		if emitErr := emitStream(InvocationStreamEvent{Type: "error", Error: err.Error(), DurationMS: result.DurationMS}); emitErr != nil {
			return result, emitErr
		}
		return result, err
	}
	result.SessionID = sessionObservationID(sess)
	data["agent_id"] = runtime.workspace.AgentID
	data["runtime_id"] = runtime.workspace.RuntimeID
	data["agent_name"] = runtime.agent.Name()
	data["session_id"] = result.SessionID
	if conversation != nil {
		data["conversation_id"] = conversation.ID
	}
	if created {
		e.emit(turnCtx, HookSessionStarted, data)
	}

	turnInput, cleanupInput, inputErr := prepareInvocationTurnInput(req, invocationID)
	if inputErr != nil {
		e.emit(turnCtx, HookError, withError(data, inputErr))
		result.DurationMS = time.Since(startedAt).Milliseconds()
		if emitErr := emitStream(InvocationStreamEvent{Type: "error", Error: inputErr.Error(), DurationMS: result.DurationMS}); emitErr != nil {
			return result, emitErr
		}
		return result, inputErr
	}
	defer cleanupInput()
	events, turnErr := e.observeSendInput(turnCtx, sess, turnInput, data)
	var answer string
	var sinkErr error
	streamedError := false
	if turnErr != nil {
		e.emit(turnCtx, HookError, withError(data, turnErr))
		streamedError = true
		if err := emitStream(InvocationStreamEvent{Type: "error", Error: turnErr.Error()}); err != nil {
			sinkErr = err
			cancelTurn()
		}
	} else {
		for event := range events {
			if event == nil {
				continue
			}
			e.updateRemoteTaskFromEvent(data, event)
			if event.Type == EventPermission {
				if !e.dispatchAgentInteraction(turnCtx, event, data) {
					e.declineAgentInteraction(turnCtx, sess, event)
				}
			}
			if (event.Type == EventFinal || event.Type == EventOutput) && event.Text != "" && event.Text != "NO_REPLY" {
				answer = event.Text
			}
			if event.Type == EventError {
				streamedError = true
				e.emit(turnCtx, HookError, withError(data, event.Err))
				if turnErr == nil {
					turnErr = fmt.Errorf("%s", errString(event.Err))
				}
			}
			if event.Usage != nil {
				usage := *event.Usage
				result.Usage = &usage
			}
			if sinkErr == nil {
				streamEvent := invocationStreamEventFromAgent(event)
				if err := emitStream(streamEvent); err != nil {
					sinkErr = err
					cancelTurn()
				}
			}
		}
	}
	if turnErr == nil && turnCtx.Err() != nil {
		turnErr = turnCtx.Err()
	}
	result.Answer = answer
	result.SessionID = sessionObservationID(sess)
	data["session_id"] = result.SessionID
	result.DurationMS = time.Since(startedAt).Milliseconds()
	e.persistConversationTurn(ctx, conversation, sess)
	e.emit(ctx, HookMessageSent, data)
	if sinkErr != nil {
		return result, sinkErr
	}
	if turnErr != nil {
		if !streamedError {
			if emitErr := emitStream(InvocationStreamEvent{Type: "error", Error: turnErr.Error(), DurationMS: result.DurationMS}); emitErr != nil {
				return result, emitErr
			}
		}
		return result, turnErr
	}
	completed := result
	if err := emitStream(InvocationStreamEvent{Type: "completed", Final: true, DurationMS: result.DurationMS, Result: &completed}); err != nil {
		return result, err
	}
	return result, nil
}

func prepareInvocationTurnInput(req InvocationRequest, invocationID string) (AgentTurnInput, func(), error) {
	input := AgentTurnInput{Text: req.Input, OutputSchema: req.OutputSchema}
	if len(req.Attachments) == 0 {
		return input, func() {}, nil
	}
	if len(req.Attachments) > 16 {
		return AgentTurnInput{}, func() {}, fmt.Errorf("%w: at most 16 attachments are allowed", ErrInvalidInvocation)
	}
	tempDir := ""
	cleanup := func() {
		if tempDir != "" {
			_ = os.RemoveAll(tempDir)
		}
	}
	input.Attachments = make([]AgentAttachment, 0, len(req.Attachments))
	for index, attachment := range req.Attachments {
		prepared := attachment
		prepared.Kind = strings.ToLower(strings.TrimSpace(prepared.Kind))
		if prepared.Kind != "image" && prepared.Kind != "file" {
			cleanup()
			return AgentTurnInput{}, func() {}, fmt.Errorf("%w: attachment %d has invalid kind %q", ErrInvalidInvocation, index, attachment.Kind)
		}
		if len(prepared.Data) > 25<<20 {
			cleanup()
			return AgentTurnInput{}, func() {}, fmt.Errorf("%w: attachment %d exceeds 25 MiB", ErrInvalidInvocation, index)
		}
		if len(prepared.Data) > 0 {
			if tempDir == "" {
				var err error
				tempDir, err = os.MkdirTemp("", "agentmux-"+safeInvocationFilename(invocationID)+"-")
				if err != nil {
					return AgentTurnInput{}, func() {}, fmt.Errorf("%w: create attachment directory: %v", ErrInvocationRuntime, err)
				}
			}
			name := safeInvocationFilename(prepared.Name)
			if name == "" {
				name = fmt.Sprintf("attachment-%d", index+1)
			}
			path := filepath.Join(tempDir, fmt.Sprintf("%02d-%s", index+1, name))
			if err := os.WriteFile(path, prepared.Data, 0o600); err != nil {
				cleanup()
				return AgentTurnInput{}, func() {}, fmt.Errorf("%w: write attachment: %v", ErrInvocationRuntime, err)
			}
			prepared.Path = path
			prepared.Data = nil
		}
		input.Attachments = append(input.Attachments, prepared)
	}
	return input, cleanup, nil
}

func safeInvocationFilename(value string) string {
	value = filepath.Base(strings.TrimSpace(value))
	var out strings.Builder
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9', char == '.', char == '-', char == '_':
			out.WriteRune(char)
		default:
			out.WriteByte('_')
		}
	}
	return strings.Trim(out.String(), "._-")
}

func invocationStreamEventFromAgent(event *Event) InvocationStreamEvent {
	text := event.Text
	if text == "NO_REPLY" {
		text = ""
	}
	stream := InvocationStreamEvent{
		Type: eventTypeName(event), Text: text, Status: event.Status,
		Final: event.Final || event.Type == EventFinal, DurationMS: event.DurationMs,
		EventID: event.EventID, TurnID: event.TurnID, ItemID: event.ItemID,
		ToolName: event.ToolName, ToolCallID: event.ToolCallID,
		ToolInput: event.ToolInput, ToolResult: event.ToolResult,
		Interaction: event.Interaction, Usage: event.Usage, Metadata: event.Metadata,
	}
	if event.Type == EventError {
		stream.Error = errString(event.Err)
	}
	return stream
}

func eventTypeName(event *Event) string {
	if event == nil || event.Type == "" {
		return "event"
	}
	return string(event.Type)
}

func (e *Engine) beginInvocation(targetKey, conversationKey string) bool {
	key := targetKey + "\x00" + conversationKey
	e.invocationMu.Lock()
	defer e.invocationMu.Unlock()
	if e.activeInvocations == nil {
		e.activeInvocations = map[string]struct{}{}
	}
	if _, exists := e.activeInvocations[key]; exists {
		return false
	}
	e.activeInvocations[key] = struct{}{}
	return true
}

func (e *Engine) finishInvocation(targetKey, conversationKey string) {
	key := targetKey + "\x00" + conversationKey
	e.invocationMu.Lock()
	delete(e.activeInvocations, key)
	e.invocationMu.Unlock()
}

func (e *Engine) closeInvocationRuntime(runtime *projectRuntime) {
	if runtime == nil {
		return
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runtime.mu.Lock()
	sessions := runtime.sessions
	runtime.sessions = map[string]AgentSession{}
	runtime.mu.Unlock()
	for cacheKey, session := range sessions {
		data := map[string]string{
			"agent_id": runtime.workspace.AgentID, "runtime_id": runtime.workspace.RuntimeID,
			"session_id": sessionObservationID(session), "conversation_id": cacheKey,
		}
		if runtime.agent != nil {
			data["agent_name"] = runtime.agent.Name()
		}
		e.emit(closeCtx, HookSessionEnded, data)
		if detachable, ok := session.(DetachableAgentSession); ok {
			_ = detachable.Detach(closeCtx)
		} else {
			_ = session.Close(closeCtx)
		}
	}
	if runtime.agent != nil {
		_ = runtime.agent.Stop(closeCtx)
	}
}
