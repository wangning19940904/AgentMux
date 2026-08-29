package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

const (
	openAIAgentHeader   = "X-AgentMux-Agent-ID"
	openAIProjectHeader = "X-AgentMux-Project"
)

// openAIResponseRequest contains the Responses create fields that AgentMux
// consumes or reflects. Unknown fields remain forward-compatible because the
// standard OpenAI SDK may add optional request fields over time.
type openAIResponseRequest struct {
	Model              string                   `json:"model"`
	Input              json.RawMessage          `json:"input"`
	Instructions       *string                  `json:"instructions"`
	Stream             bool                     `json:"stream"`
	PreviousResponseID *string                  `json:"previous_response_id"`
	Conversation       json.RawMessage          `json:"conversation"`
	Metadata           map[string]any           `json:"metadata"`
	Store              *bool                    `json:"store"`
	Background         bool                     `json:"background"`
	MaxOutputTokens    *int64                   `json:"max_output_tokens"`
	Reasoning          json.RawMessage          `json:"reasoning"`
	Text               *openAIResponseTextInput `json:"text"`
	Tools              []json.RawMessage        `json:"tools"`
	ToolChoice         json.RawMessage          `json:"tool_choice"`
	ParallelToolCalls  *bool                    `json:"parallel_tool_calls"`
	MaxToolCalls       *int64                   `json:"max_tool_calls"`
	Temperature        *float64                 `json:"temperature"`
	TopP               *float64                 `json:"top_p"`
	Truncation         string                   `json:"truncation"`
	User               *string                  `json:"user"`
	parsedInput        openAIParsedInput
	parsedTools        []openAITool
	outputFormat       openAIOutputFormat
	finalOutput        *openAIFinalOutput
}

type openAIResponseTextInput struct {
	Format    *openAIOutputFormat `json:"format"`
	Verbosity string              `json:"verbosity"`
}

type openAIOutputFormat struct {
	Type        string         `json:"type"`
	Name        string         `json:"name,omitempty"`
	Description string         `json:"description,omitempty"`
	Schema      map[string]any `json:"schema,omitempty"`
	Strict      *bool          `json:"strict,omitempty"`
}

type openAIResponseIdentity struct {
	responseID           string
	itemID               string
	conversationID       string
	createdAt            int64
	previousResponseID   any
	responseConversation any
}

type openAIOutputText struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	Annotations []any  `json:"annotations"`
	Logprobs    []any  `json:"logprobs"`
}

type openAIOutputMessage struct {
	ID        string             `json:"id"`
	Type      string             `json:"type"`
	Status    string             `json:"status"`
	Role      string             `json:"role,omitempty"`
	Content   []openAIOutputText `json:"content,omitempty"`
	CallID    string             `json:"call_id,omitempty"`
	Name      string             `json:"name,omitempty"`
	Arguments string             `json:"arguments,omitempty"`
}

type openAIResponseObject struct {
	ID                   string                `json:"id"`
	Object               string                `json:"object"`
	CreatedAt            int64                 `json:"created_at"`
	Status               string                `json:"status"`
	Background           bool                  `json:"background"`
	CompletedAt          any                   `json:"completed_at"`
	Conversation         any                   `json:"conversation"`
	Error                any                   `json:"error"`
	IncompleteDetails    any                   `json:"incomplete_details"`
	Instructions         any                   `json:"instructions"`
	MaxOutputTokens      any                   `json:"max_output_tokens"`
	MaxToolCalls         any                   `json:"max_tool_calls"`
	Model                string                `json:"model"`
	Output               []openAIOutputMessage `json:"output"`
	ParallelToolCalls    bool                  `json:"parallel_tool_calls"`
	PreviousResponseID   any                   `json:"previous_response_id"`
	Prompt               any                   `json:"prompt"`
	PromptCacheKey       any                   `json:"prompt_cache_key"`
	PromptCacheRetention any                   `json:"prompt_cache_retention"`
	Reasoning            map[string]any        `json:"reasoning"`
	SafetyIdentifier     any                   `json:"safety_identifier"`
	ServiceTier          string                `json:"service_tier"`
	Store                bool                  `json:"store"`
	Temperature          float64               `json:"temperature"`
	Text                 map[string]any        `json:"text"`
	ToolChoice           any                   `json:"tool_choice"`
	Tools                []any                 `json:"tools"`
	TopLogprobs          int                   `json:"top_logprobs"`
	TopP                 float64               `json:"top_p"`
	Truncation           string                `json:"truncation"`
	Usage                any                   `json:"usage"`
	User                 any                   `json:"user"`
	Metadata             map[string]any        `json:"metadata"`
}

type openAIErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   any    `json:"param"`
	Code    any    `json:"code"`
}

type openAIErrorResponse struct {
	Error openAIErrorDetail `json:"error"`
}

// handleOpenAIResponse implements the OpenAI Responses create endpoint. The
// request's stream field selects a JSON response or typed SSE event stream.
func (s *Server) handleOpenAIResponse(w http.ResponseWriter, r *http.Request) {
	if s.invoker == nil {
		writeOpenAIError(w, http.StatusServiceUnavailable, "invocation runtime unavailable", "server_error", nil, "runtime_unavailable")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxOpenAIResponseRequestBytes)
	req, err := decodeOpenAIResponseRequest(r.Body)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", nil, "invalid_json")
		return
	}
	prompt, err := validateOpenAIResponseRequest(&req)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", openAIRequestErrorParam(err), "unsupported_request")
		return
	}
	if err := s.resolveOpenAIFileAttachments(&req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "input", "invalid_file")
		return
	}
	identity, err := newOpenAIResponseIdentity(req)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", openAIRequestErrorParam(err), "invalid_request")
		return
	}
	invocation, fallback, err := openAIInvocationRequest(r, req, identity.conversationID, prompt)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "model", "invalid_target")
		return
	}
	if !s.authorizeOpenAIInvocationTarget(w, r, invocation) {
		return
	}
	if req.Background {
		if req.Stream {
			s.handleOpenAIBackgroundResponseStream(w, r, req, identity, invocation, fallback, -1)
			return
		}
		s.handleOpenAIBackgroundResponse(w, r, req, identity, invocation, fallback)
		return
	}
	if req.Stream {
		s.handleOpenAIResponseStream(w, r, req, identity, invocation, fallback)
		return
	}
	result, err := invokeOpenAIResponse(r.Context(), s.invoker, invocation, fallback)
	if err != nil {
		writeOpenAIInvocationError(w, err)
		return
	}
	if err := prepareOpenAIFinalOutput(&req, &result); err != nil {
		writeOpenAIError(w, http.StatusBadGateway, err.Error(), "server_error", "text.format", "output_validation_failed")
		return
	}
	response := buildOpenAIResponse(req, identity, "completed", result.Answer, result.Usage, nil)
	s.storeOpenAIResponse(req, identity, response, nil)
	writeJSON(w, http.StatusOK, response)
}

func decodeOpenAIResponseRequest(body io.Reader) (openAIResponseRequest, error) {
	var req openAIResponseRequest
	decoder := json.NewDecoder(body)
	if err := decoder.Decode(&req); err != nil {
		return req, fmt.Errorf("invalid JSON request: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return req, errors.New("invalid JSON request: multiple JSON values")
		}
		return req, fmt.Errorf("invalid JSON request: %w", err)
	}
	return req, nil
}

func validateOpenAIResponseRequest(req *openAIResponseRequest) (string, error) {
	req.Model = strings.TrimSpace(req.Model)
	if req.Model == "" {
		return "", errors.New("model: field is required")
	}
	tools, err := parseOpenAITools(req.Tools)
	if err != nil {
		return "", fmt.Errorf("tools: %w", err)
	}
	req.parsedTools = tools
	format, err := parseOpenAIOutputFormat(req.Text)
	if err != nil {
		return "", fmt.Errorf("text.format: %w", err)
	}
	req.outputFormat = format
	parsed, err := parseOpenAIInput(req.Input)
	if err != nil {
		return "", fmt.Errorf("input: %w", err)
	}
	req.parsedInput = parsed
	prompt := parsed.Text
	if req.Instructions != nil && strings.TrimSpace(*req.Instructions) != "" {
		prompt = "Instructions:\n" + strings.TrimSpace(*req.Instructions) + "\n\nInput:\n" + prompt
	}
	prompt = appendOpenAIToolInstructions(prompt, *req)
	prompt = appendOpenAIFormatInstructions(prompt, format)
	return prompt, nil
}

func newOpenAIResponseIdentity(req openAIResponseRequest) (openAIResponseIdentity, error) {
	var root string
	var previous any
	var conversation any
	if req.PreviousResponseID != nil && len(req.Conversation) > 0 && string(req.Conversation) != "null" {
		return openAIResponseIdentity{}, errors.New("conversation: cannot be used together with previous_response_id")
	}
	if req.PreviousResponseID != nil {
		value := strings.TrimSpace(*req.PreviousResponseID)
		parsed, ok := parseOpenAIResponseID(value)
		if !ok {
			return openAIResponseIdentity{}, errors.New("previous_response_id: response was not created by this AgentMux server")
		}
		root = parsed
		previous = value
	} else if len(req.Conversation) > 0 && string(req.Conversation) != "null" {
		conversationID, err := openAIConversationID(req.Conversation)
		if err != nil {
			return openAIResponseIdentity{}, fmt.Errorf("conversation: %w", err)
		}
		digest := sha256.Sum256([]byte(conversationID))
		root = hex.EncodeToString(digest[:12])
		conversation = map[string]any{"id": conversationID}
	} else {
		root = randHex(12)
	}
	return openAIResponseIdentity{
		responseID:           "resp_" + root + "_" + randHex(12),
		itemID:               "msg_" + randHex(16),
		conversationID:       "oai_" + root,
		createdAt:            time.Now().Unix(),
		previousResponseID:   previous,
		responseConversation: conversation,
	}, nil
}

func parseOpenAIResponseID(value string) (string, bool) {
	parts := strings.Split(value, "_")
	if len(parts) != 3 || parts[0] != "resp" || len(parts[1]) != 24 || len(parts[2]) != 24 {
		return "", false
	}
	if _, err := hex.DecodeString(parts[1] + parts[2]); err != nil {
		return "", false
	}
	return parts[1], true
}

func openAIConversationID(raw json.RawMessage) (string, error) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		var object struct {
			ID string `json:"id"`
		}
		if objectErr := json.Unmarshal(raw, &object); objectErr != nil {
			return "", errors.New("must be a conversation ID or an object containing id")
		}
		value = object.ID
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("id must not be empty")
	}
	return value, nil
}

func openAIInvocationRequest(r *http.Request, req openAIResponseRequest, conversationID, prompt string) (core.InvocationRequest, bool, error) {
	agentID := strings.TrimSpace(r.Header.Get(openAIAgentHeader))
	project := strings.TrimSpace(r.Header.Get(openAIProjectHeader))
	if agentID != "" && project != "" {
		return core.InvocationRequest{}, false, fmt.Errorf("only one of %s and %s may be set", openAIAgentHeader, openAIProjectHeader)
	}
	outputSchema := req.outputFormat.Schema
	if hasOpenAIFunctionTools(req) && openAIToolChoiceRequired(req.ToolChoice) {
		outputSchema = openAIFunctionEnvelopeSchema(req)
	}
	invocation := core.InvocationRequest{
		ConversationID: conversationID, Input: prompt,
		Attachments: req.parsedInput.Attachments, OutputSchema: outputSchema,
	}
	if agentID != "" {
		invocation.AgentID = agentID
		return invocation, false, nil
	}
	if project != "" {
		invocation.Project = project
		return invocation, false, nil
	}
	invocation.AgentID = req.Model
	return invocation, true, nil
}

func invokeOpenAIResponse(ctx context.Context, invoker core.Invoker, req core.InvocationRequest, fallback bool) (core.InvocationResult, error) {
	result, err := invoker.Invoke(ctx, req)
	if !fallback || !errors.Is(err, core.ErrInvocationNotFound) {
		return result, err
	}
	req.Project = req.AgentID
	req.AgentID = ""
	return invoker.Invoke(ctx, req)
}

func invokeOpenAIResponseStream(ctx context.Context, invoker core.StreamingInvoker, req core.InvocationRequest, fallback bool, sink core.InvocationEventSink) (core.InvocationResult, error) {
	emitted := false
	result, err := invoker.InvokeStream(ctx, req, func(event core.InvocationStreamEvent) error {
		emitted = true
		return sink(event)
	})
	if !fallback || emitted || !errors.Is(err, core.ErrInvocationNotFound) {
		return result, err
	}
	req.Project = req.AgentID
	req.AgentID = ""
	return invoker.InvokeStream(ctx, req, sink)
}

func buildOpenAIResponse(req openAIResponseRequest, identity openAIResponseIdentity, status, answer string, usage *core.TurnUsage, responseErr any) openAIResponseObject {
	createdAt := identity.createdAt
	var completedAt any
	var responseUsage any
	output := []openAIOutputMessage{}
	if status == "completed" {
		completedAt = time.Now().Unix()
		responseUsage = openAIUsage(usage)
		if req.finalOutput != nil && len(req.finalOutput.FunctionCalls) > 0 {
			for _, call := range req.finalOutput.FunctionCalls {
				output = append(output, openAIOutputMessage{
					ID: call.ID, Type: "function_call", Status: "completed",
					CallID: call.CallID, Name: call.Name, Arguments: call.Arguments,
				})
			}
		} else {
			if req.finalOutput != nil {
				answer = req.finalOutput.Text
			}
			output = []openAIOutputMessage{{
				ID: identity.itemID, Type: "message", Status: "completed", Role: "assistant",
				Content: []openAIOutputText{{Type: "output_text", Text: answer, Annotations: []any{}, Logprobs: []any{}}},
			}}
		}
	} else if status == "failed" || status == "cancelled" || status == "incomplete" {
		completedAt = time.Now().Unix()
		if usage != nil {
			responseUsage = openAIUsage(usage)
		}
	}
	store := true
	if req.Store != nil {
		store = *req.Store
	}
	temperature := 1.0
	if req.Temperature != nil {
		temperature = *req.Temperature
	}
	topP := 1.0
	if req.TopP != nil {
		topP = *req.TopP
	}
	truncation := "disabled"
	if req.Truncation != "" {
		truncation = req.Truncation
	}
	var instructions any
	if req.Instructions != nil {
		instructions = *req.Instructions
	}
	var maxOutputTokens any
	if req.MaxOutputTokens != nil {
		maxOutputTokens = *req.MaxOutputTokens
	}
	var user any
	if req.User != nil {
		user = *req.User
	}
	metadata := req.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	reasoning := map[string]any{"effort": nil, "summary": nil}
	if len(req.Reasoning) > 0 && string(req.Reasoning) != "null" {
		var configured map[string]any
		if json.Unmarshal(req.Reasoning, &configured) == nil {
			for _, key := range []string{"effort", "summary"} {
				if value, exists := configured[key]; exists {
					reasoning[key] = value
				}
			}
		}
	}
	parallelToolCalls := true
	if req.ParallelToolCalls != nil {
		parallelToolCalls = *req.ParallelToolCalls
	}
	var maxToolCalls any
	if req.MaxToolCalls != nil {
		maxToolCalls = *req.MaxToolCalls
	}
	tools := make([]any, 0, len(req.parsedTools))
	for _, tool := range req.parsedTools {
		tools = append(tools, tool.Raw)
	}
	toolChoice := any("auto")
	if len(req.ToolChoice) > 0 && string(req.ToolChoice) != "null" {
		var configured any
		if json.Unmarshal(req.ToolChoice, &configured) == nil {
			toolChoice = configured
		}
	}
	format := map[string]any{"type": "text"}
	if req.outputFormat.Type != "" {
		encoded, _ := json.Marshal(req.outputFormat)
		_ = json.Unmarshal(encoded, &format)
	}
	verbosity := "medium"
	if req.Text != nil && strings.TrimSpace(req.Text.Verbosity) != "" {
		verbosity = strings.TrimSpace(req.Text.Verbosity)
	}
	return openAIResponseObject{
		ID: identity.responseID, Object: "response", CreatedAt: createdAt, Status: status,
		Background: req.Background, CompletedAt: completedAt, Conversation: identity.responseConversation,
		Error: responseErr, IncompleteDetails: nil, Instructions: instructions, MaxOutputTokens: maxOutputTokens,
		MaxToolCalls: maxToolCalls, Model: req.Model, Output: output, ParallelToolCalls: parallelToolCalls,
		PreviousResponseID: identity.previousResponseID, Prompt: nil, PromptCacheKey: nil, PromptCacheRetention: nil,
		Reasoning: reasoning, SafetyIdentifier: nil, ServiceTier: "default",
		Store: store, Temperature: temperature,
		Text:       map[string]any{"format": format, "verbosity": verbosity},
		ToolChoice: toolChoice, Tools: tools, TopLogprobs: 0, TopP: topP, Truncation: truncation,
		Usage: responseUsage, User: user, Metadata: metadata,
	}
}

func openAIUsage(usage *core.TurnUsage) map[string]any {
	var input, output, cached, cacheWrite, reasoning int64
	if usage != nil {
		input = usage.InputTokens
		output = usage.OutputTokens
		cached = usage.CacheReadTokens
		cacheWrite = usage.CacheWriteTokens
		reasoning = usage.ReasoningTokens
	}
	return map[string]any{
		"input_tokens": input,
		"input_tokens_details": map[string]any{
			"cached_tokens":      cached,
			"cache_write_tokens": cacheWrite,
		},
		"output_tokens":         output,
		"output_tokens_details": map[string]any{"reasoning_tokens": reasoning},
		"total_tokens":          input + output,
	}
}

type openAIStreamOutcome struct {
	result core.InvocationResult
	err    error
}

func (s *Server) handleOpenAIResponseStream(w http.ResponseWriter, r *http.Request, req openAIResponseRequest, identity openAIResponseIdentity, invocation core.InvocationRequest, fallback bool) {
	streamer, ok := s.invoker.(core.StreamingInvoker)
	if !ok {
		writeOpenAIError(w, http.StatusServiceUnavailable, "streaming invocation runtime unavailable", "server_error", nil, "streaming_unavailable")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeOpenAIError(w, http.StatusInternalServerError, "streaming is unsupported by this HTTP server", "server_error", nil, "streaming_unavailable")
		return
	}
	streamCtx, cancel := context.WithCancel(r.Context())
	defer cancel()
	events := make(chan core.InvocationStreamEvent)
	done := make(chan openAIStreamOutcome, 1)
	go func() {
		result, err := invokeOpenAIResponseStream(streamCtx, streamer, invocation, fallback, func(event core.InvocationStreamEvent) error {
			select {
			case events <- event:
				return nil
			case <-streamCtx.Done():
				return streamCtx.Err()
			}
		})
		done <- openAIStreamOutcome{result: result, err: err}
	}()

	state := &openAIResponseStreamState{server: s, w: w, flusher: flusher, req: req, identity: identity}
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case event := <-events:
			if err := state.consume(event); err != nil {
				cancel()
				return
			}
			if state.terminal {
				return
			}
		case outcome := <-done:
			if outcome.err != nil {
				if !state.started {
					writeOpenAIInvocationError(w, outcome.err)
					return
				}
				if !errors.Is(outcome.err, context.Canceled) {
					_ = state.fail(outcome.err)
				}
				return
			}
			if err := state.complete(outcome.result); err != nil {
				cancel()
			}
			return
		case <-keepalive.C:
			if state.started {
				if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
					cancel()
					return
				}
				flusher.Flush()
			}
		case <-streamCtx.Done():
			return
		}
	}
}

type openAIResponseStreamState struct {
	server         *Server
	w              http.ResponseWriter
	flusher        http.Flusher
	record         func(openAIRecordedEvent)
	req            openAIResponseRequest
	identity       openAIResponseIdentity
	sequence       int
	started        bool
	terminal       bool
	messageStarted bool
	observedText   string
	emittedText    string
	usage          *core.TurnUsage
}

func (s *openAIResponseStreamState) consume(event core.InvocationStreamEvent) error {
	if !s.started {
		if err := s.start(); err != nil {
			return err
		}
	}
	if event.Usage != nil {
		usage := *event.Usage
		s.usage = &usage
	}
	switch event.Type {
	case "output", "final":
		return s.snapshot(event.Text)
	case "completed":
		if event.Result != nil {
			return s.complete(*event.Result)
		}
		return nil
	case "error":
		return s.fail(errors.New(event.Error))
	default:
		return nil
	}
}

func (s *openAIResponseStreamState) start() error {
	if s.started {
		return nil
	}
	s.started = true
	if s.w != nil {
		s.w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		s.w.Header().Set("Cache-Control", "no-cache, no-transform")
		s.w.Header().Set("Connection", "keep-alive")
		s.w.Header().Set("X-Accel-Buffering", "no")
		s.w.WriteHeader(http.StatusOK)
	}
	response := buildOpenAIResponse(s.req, s.identity, "in_progress", "", nil, nil)
	if err := s.write("response.created", map[string]any{"response": response}); err != nil {
		return err
	}
	if err := s.write("response.in_progress", map[string]any{"response": response}); err != nil {
		return err
	}
	if !s.shouldBufferOutput() {
		return s.beginMessage()
	}
	return nil
}

func (s *openAIResponseStreamState) beginMessage() error {
	if s.messageStarted {
		return nil
	}
	s.messageStarted = true
	item := openAIOutputMessage{ID: s.identity.itemID, Type: "message", Status: "in_progress", Role: "assistant", Content: []openAIOutputText{}}
	if err := s.write("response.output_item.added", map[string]any{"output_index": 0, "item": item}); err != nil {
		return err
	}
	part := openAIOutputText{Type: "output_text", Text: "", Annotations: []any{}, Logprobs: []any{}}
	return s.write("response.content_part.added", map[string]any{
		"item_id": s.identity.itemID, "output_index": 0, "content_index": 0, "part": part,
	})
}

func (s *openAIResponseStreamState) snapshot(value string) error {
	if value == "" || value == s.observedText {
		return nil
	}
	s.observedText = value
	if s.shouldBufferOutput() {
		return nil
	}
	if err := s.beginMessage(); err != nil {
		return err
	}
	return s.emitTextSnapshot(value)
}

func (s *openAIResponseStreamState) emitTextSnapshot(value string) error {
	if value == "" || value == s.emittedText {
		return nil
	}
	if !strings.HasPrefix(value, s.emittedText) {
		// AgentMux events are snapshots. A non-monotonic correction cannot be
		// represented as an OpenAI delta, so retain the already emitted prefix;
		// the completed Response still contains the authoritative final text.
		return nil
	}
	delta := strings.TrimPrefix(value, s.emittedText)
	s.emittedText = value
	return s.write("response.output_text.delta", map[string]any{
		"item_id": s.identity.itemID, "output_index": 0, "content_index": 0,
		"delta": delta, "logprobs": []any{},
	})
}

func (s *openAIResponseStreamState) complete(result core.InvocationResult) error {
	if s.terminal {
		return nil
	}
	if !s.started {
		if err := s.start(); err != nil {
			return err
		}
	}
	if result.Usage != nil {
		s.usage = result.Usage
	}
	if result.Answer == "" {
		result.Answer = s.observedText
	}
	if err := prepareOpenAIFinalOutput(&s.req, &result); err != nil {
		return s.fail(err)
	}
	if s.req.finalOutput != nil && len(s.req.finalOutput.FunctionCalls) > 0 {
		return s.completeFunctionCalls(result)
	}
	if err := s.beginMessage(); err != nil {
		return err
	}
	finalText := result.Answer
	if s.req.finalOutput != nil {
		finalText = s.req.finalOutput.Text
	}
	if err := s.emitTextSnapshot(finalText); err != nil {
		return err
	}
	if err := s.write("response.output_text.done", map[string]any{
		"item_id": s.identity.itemID, "output_index": 0, "content_index": 0,
		"text": finalText, "logprobs": []any{},
	}); err != nil {
		return err
	}
	part := openAIOutputText{Type: "output_text", Text: finalText, Annotations: []any{}, Logprobs: []any{}}
	if err := s.write("response.content_part.done", map[string]any{
		"item_id": s.identity.itemID, "output_index": 0, "content_index": 0, "part": part,
	}); err != nil {
		return err
	}
	item := openAIOutputMessage{
		ID: s.identity.itemID, Type: "message", Status: "completed", Role: "assistant",
		Content: []openAIOutputText{part},
	}
	if err := s.write("response.output_item.done", map[string]any{"output_index": 0, "item": item}); err != nil {
		return err
	}
	response := buildOpenAIResponse(s.req, s.identity, "completed", finalText, s.usage, nil)
	if err := s.write("response.completed", map[string]any{"response": response}); err != nil {
		return err
	}
	if s.server != nil {
		s.server.storeOpenAIResponse(s.req, s.identity, response, nil)
	}
	s.terminal = true
	return nil
}

func (s *openAIResponseStreamState) completeFunctionCalls(result core.InvocationResult) error {
	for index, call := range s.req.finalOutput.FunctionCalls {
		added := openAIOutputMessage{
			ID: call.ID, Type: "function_call", Status: "in_progress",
			CallID: call.CallID, Name: call.Name,
		}
		if err := s.write("response.output_item.added", map[string]any{"output_index": index, "item": added}); err != nil {
			return err
		}
		if call.Arguments != "" {
			if err := s.write("response.function_call_arguments.delta", map[string]any{
				"item_id": call.ID, "output_index": index, "delta": call.Arguments,
			}); err != nil {
				return err
			}
		}
		if err := s.write("response.function_call_arguments.done", map[string]any{
			"item_id": call.ID, "output_index": index, "arguments": call.Arguments,
		}); err != nil {
			return err
		}
		done := added
		done.Status = "completed"
		done.Arguments = call.Arguments
		if err := s.write("response.output_item.done", map[string]any{"output_index": index, "item": done}); err != nil {
			return err
		}
	}
	response := buildOpenAIResponse(s.req, s.identity, "completed", "", s.usage, nil)
	if err := s.write("response.completed", map[string]any{"response": response}); err != nil {
		return err
	}
	if s.server != nil {
		s.server.storeOpenAIResponse(s.req, s.identity, response, nil)
	}
	s.terminal = true
	return nil
}

func (s *openAIResponseStreamState) shouldBufferOutput() bool {
	return hasOpenAIFunctionTools(s.req) || (s.req.outputFormat.Type != "" && s.req.outputFormat.Type != "text")
}

func (s *openAIResponseStreamState) fail(err error) error {
	if s.terminal {
		return nil
	}
	if !s.started {
		if startErr := s.start(); startErr != nil {
			return startErr
		}
	}
	detail := openAIErrorDetail{Message: err.Error(), Type: "server_error", Param: nil, Code: "agent_error"}
	if writeErr := s.write("error", map[string]any{
		"code": detail.Code, "message": detail.Message, "param": detail.Param,
	}); writeErr != nil {
		return writeErr
	}
	response := buildOpenAIResponse(s.req, s.identity, "failed", "", s.usage, detail)
	if writeErr := s.write("response.failed", map[string]any{"response": response}); writeErr != nil {
		return writeErr
	}
	if s.server != nil {
		s.server.storeOpenAIResponse(s.req, s.identity, response, nil)
	}
	s.terminal = true
	return nil
}

func (s *openAIResponseStreamState) write(eventType string, fields map[string]any) error {
	payload := make(map[string]any, len(fields)+2)
	payload["type"] = eventType
	payload["sequence_number"] = s.sequence
	for key, value := range fields {
		payload[key] = value
	}
	s.sequence++
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if s.record != nil {
		s.record(openAIRecordedEvent{sequence: s.sequence - 1, eventType: eventType, payload: append([]byte(nil), encoded...)})
	}
	if s.w != nil {
		if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", eventType, encoded); err != nil {
			return err
		}
		if s.flusher != nil {
			s.flusher.Flush()
		}
	}
	return nil
}

func writeOpenAIInvocationError(w http.ResponseWriter, err error) {
	status := invocationErrorStatus(err)
	errorType := "invalid_request_error"
	var param any
	var code any = "invocation_failed"
	if status >= 500 {
		errorType = "server_error"
	}
	if errors.Is(err, core.ErrInvocationNotFound) {
		param = "model"
		code = "model_not_found"
	}
	if errors.Is(err, core.ErrInvocationBusy) {
		code = "response_in_progress"
	}
	writeOpenAIError(w, status, err.Error(), errorType, param, code)
}

func writeOpenAIError(w http.ResponseWriter, status int, message, errorType string, param, code any) {
	writeJSON(w, status, openAIErrorResponse{Error: openAIErrorDetail{
		Message: message, Type: errorType, Param: param, Code: code,
	}})
}

func openAIRequestErrorParam(err error) any {
	message := err.Error()
	if index := strings.IndexByte(message, ':'); index > 0 {
		return message[:index]
	}
	return nil
}
