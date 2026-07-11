package cliagents

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/agentnexus/agentnexus/core"
)

// Codex's app-server is the transport used by the Codex desktop app. It is
// materially richer than `codex exec --json`: it exposes token deltas,
// reasoning summaries, tool lifecycle events, and the signed-in account's
// own model catalog. Keeping one app-server process per AgentSession also
// gives channel conversations a real native Codex thread.

func registerCodex() {
	core.RegisterAgent("codex", func(cfg map[string]any) (core.Agent, error) {
		return newCodexAgent(cfg), nil
	})
}

type codexAgent struct {
	systemPrompt              string
	defaultModel              string
	defaultReasoningEffort    string
	defaultServiceTier        string
	supportedReasoningEfforts []string
	supportedServiceTiers     []string
	env                       map[string]string
}

func newCodexAgent(cfg map[string]any) *codexAgent {
	a := &codexAgent{}
	if value, ok := cfg["system_prompt"].(string); ok {
		a.systemPrompt = value
	}
	if settings := core.RuntimeSettingsSelectionFromConfig(cfg); settings != nil {
		defaults := settings.DefaultRuntimeSettings()
		capabilities := settings.RuntimeSettingsCapabilities()
		a.defaultModel = defaults.Model
		a.defaultReasoningEffort = defaults.ReasoningEffort
		a.defaultServiceTier = defaults.ServiceTier
		for _, option := range capabilities.ReasoningEfforts {
			a.supportedReasoningEfforts = append(a.supportedReasoningEfforts, option.Value)
		}
		for _, option := range capabilities.ServiceTiers {
			a.supportedServiceTiers = append(a.supportedServiceTiers, option.Value)
		}
	}
	if env, ok := cfg["env"].(map[string]string); ok {
		a.env = env
	}
	return a
}

func (a *codexAgent) Name() string { return "codex" }

func (a *codexAgent) StartSession(ctx context.Context, workDir string) (core.AgentSession, error) {
	return newCodexSession(ctx, a, workDir, "")
}

func (a *codexAgent) StartSessionResume(ctx context.Context, workDir, resumeID string) (core.AgentSession, error) {
	return newCodexSession(ctx, a, workDir, resumeID)
}

func (a *codexAgent) ListSessions(context.Context) ([]string, error) { return nil, nil }
func (a *codexAgent) Stop(context.Context) error                     { return nil }

type codexSession struct {
	agent   *codexAgent
	workDir string

	mu       sync.Mutex
	turnMu   sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	reader   *bufio.Reader
	stderr   bytes.Buffer
	closed   bool
	nextID   int
	threadID string
	// usageTotal tracks the app-server's thread-cumulative counters across
	// turns. runTurn is serialized by turnMu, so it needs no separate lock.
	usageTotal codexTokenUsage

	modelMu                   sync.Mutex
	defaultModel              string
	currentModel              string
	defaultReasoningEffort    string
	currentReasoningEffort    string
	defaultServiceTier        string
	currentServiceTier        string
	supportedModel            []string
	supportedReasoningEfforts []string
	supportedServiceTiers     []string
	modelReasoningEfforts     map[string][]string
}

func newCodexSession(ctx context.Context, agent *codexAgent, workDir, resumeID string) (*codexSession, error) {
	if workDir == "" {
		workDir, _ = os.Getwd()
	}
	s := &codexSession{
		agent:                     agent,
		workDir:                   workDir,
		defaultModel:              agent.defaultModel,
		defaultReasoningEffort:    agent.defaultReasoningEffort,
		defaultServiceTier:        agent.defaultServiceTier,
		supportedReasoningEfforts: append([]string(nil), agent.supportedReasoningEfforts...),
		supportedServiceTiers:     append([]string(nil), agent.supportedServiceTiers...),
		modelReasoningEfforts:     map[string][]string{},
		nextID:                    1,
	}
	if err := s.start(ctx, resumeID); err != nil {
		_ = s.Close(context.Background())
		return nil, err
	}
	return s, nil
}

func (s *codexSession) ID() string { return s.NativeSessionID() }

func (s *codexSession) NativeSessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.threadID
}

func (s *codexSession) ModelSwitchingSupported() bool { return len(s.SupportedModels()) > 0 }

func (s *codexSession) CurrentModel() string {
	s.modelMu.Lock()
	defer s.modelMu.Unlock()
	if s.currentModel != "" {
		return s.currentModel
	}
	return s.defaultModel
}

func (s *codexSession) DefaultModel() string {
	s.modelMu.Lock()
	defer s.modelMu.Unlock()
	return s.defaultModel
}

func (s *codexSession) SupportedModels() []string {
	s.modelMu.Lock()
	defer s.modelMu.Unlock()
	return append([]string(nil), s.supportedModel...)
}

func (s *codexSession) SetModel(model string) error {
	model = strings.TrimSpace(model)
	if model == "" {
		return fmt.Errorf("model is required")
	}
	s.modelMu.Lock()
	defer s.modelMu.Unlock()
	for _, supported := range s.supportedModel {
		if model == supported {
			s.currentModel = model
			return nil
		}
	}
	return fmt.Errorf("model %q is not available to the signed-in Codex account", model)
}

func (s *codexSession) ResetModel() error {
	s.modelMu.Lock()
	s.currentModel = ""
	s.modelMu.Unlock()
	return nil
}

func (s *codexSession) RuntimeSettingsCapabilities() core.RuntimeSettingsCapabilities {
	s.modelMu.Lock()
	defer s.modelMu.Unlock()
	efforts := s.supportedReasoningEfforts
	if model := s.currentModel; model != "" && len(s.modelReasoningEfforts[model]) > 0 {
		efforts = s.modelReasoningEfforts[model]
	} else if model := s.defaultModel; model != "" && len(s.modelReasoningEfforts[model]) > 0 {
		efforts = s.modelReasoningEfforts[model]
	}
	serviceTiers := append([]string(nil), s.supportedServiceTiers...)
	if len(serviceTiers) > 0 && !containsCodexOption(serviceTiers, "default") {
		serviceTiers = append([]string{"default"}, serviceTiers...)
	}
	return core.RuntimeSettingsCapabilities{
		Models:           core.RuntimeOptions(s.supportedModel),
		ReasoningEfforts: core.RuntimeOptions(efforts),
		ServiceTiers:     core.RuntimeOptions(serviceTiers),
	}
}

func (s *codexSession) CurrentRuntimeSettings() core.RuntimeSettings {
	s.modelMu.Lock()
	defer s.modelMu.Unlock()
	settings := core.RuntimeSettings{Model: s.defaultModel, ReasoningEffort: s.defaultReasoningEffort, ServiceTier: s.defaultServiceTier}
	if s.currentModel != "" {
		settings.Model = s.currentModel
	}
	if s.currentReasoningEffort != "" {
		settings.ReasoningEffort = s.currentReasoningEffort
	}
	if s.currentServiceTier != "" {
		settings.ServiceTier = s.currentServiceTier
	}
	return settings
}

func (s *codexSession) DefaultRuntimeSettings() core.RuntimeSettings {
	s.modelMu.Lock()
	defer s.modelMu.Unlock()
	return core.RuntimeSettings{Model: s.defaultModel, ReasoningEffort: s.defaultReasoningEffort, ServiceTier: s.defaultServiceTier}
}

func (s *codexSession) SetRuntimeSetting(setting core.RuntimeSetting, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("%s is required", setting)
	}
	if setting == core.RuntimeSettingModel {
		return s.SetModel(value)
	}
	s.modelMu.Lock()
	defer s.modelMu.Unlock()
	var options []string
	switch setting {
	case core.RuntimeSettingReasoningEffort:
		options = s.supportedReasoningEfforts
		model := s.currentModel
		if model == "" {
			model = s.defaultModel
		}
		if modelOptions := s.modelReasoningEfforts[model]; len(modelOptions) > 0 {
			options = modelOptions
		}
	case core.RuntimeSettingServiceTier:
		if value == "default" {
			s.currentServiceTier = ""
			return nil
		}
		options = s.supportedServiceTiers
	default:
		return fmt.Errorf("unknown runtime setting %q", setting)
	}
	if !containsCodexOption(options, value) {
		if len(options) == 0 {
			return fmt.Errorf("%s is not available to the signed-in Codex account", setting)
		}
		return fmt.Errorf("%q is not supported for %s", value, setting)
	}
	if setting == core.RuntimeSettingReasoningEffort {
		s.currentReasoningEffort = value
	} else {
		s.currentServiceTier = value
	}
	return nil
}

func (s *codexSession) ResetRuntimeSetting(setting core.RuntimeSetting) error {
	if setting == core.RuntimeSettingModel {
		return s.ResetModel()
	}
	s.modelMu.Lock()
	defer s.modelMu.Unlock()
	switch setting {
	case core.RuntimeSettingReasoningEffort:
		s.currentReasoningEffort = ""
	case core.RuntimeSettingServiceTier:
		s.currentServiceTier = ""
	default:
		return fmt.Errorf("unknown runtime setting %q", setting)
	}
	return nil
}

func (s *codexSession) start(ctx context.Context, resumeID string) error {
	name := "codex"
	if path, err := exec.LookPath(name); err == nil {
		name = path
	}
	cmd := exec.Command(name, codexAppServerArgs(ctx)...)
	cmd.Dir = s.workDir
	cmd.Env = codexBuildEnv(s.agent.env)
	cmd.Stderr = &s.stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	s.cmd = cmd
	s.stdin = stdin
	s.reader = bufio.NewReader(stdout)

	if _, err := s.call(ctx, "initialize", map[string]any{
		"clientInfo": map[string]string{"name": "AgentNexus", "version": "0.1.0"},
	}); err != nil {
		return s.withStderr(err)
	}
	if err := s.notify("initialized", map[string]any{}); err != nil {
		return err
	}
	if err := s.loadNativeModels(ctx); err != nil {
		return s.withStderr(err)
	}

	if resumeID != "" {
		result, err := s.call(ctx, "thread/resume", map[string]any{"threadId": resumeID})
		if err != nil {
			return unavailableCodexThreadError(s.withStderr(err))
		}
		s.threadID = firstString(nestedMap(result, "thread"), "id", "threadId")
		if s.threadID == "" {
			s.threadID = firstString(result, "id", "threadId")
		}
		if s.threadID == "" {
			return fmt.Errorf("codex app-server resumed a thread without an id")
		}
		return nil
	}

	params := map[string]any{"cwd": s.workDir}
	if prompt := strings.TrimSpace(s.agent.systemPrompt); prompt != "" {
		params["developerInstructions"] = prompt
	}
	if model := s.DefaultModel(); model != "" {
		params["model"] = model
	}
	result, err := s.call(ctx, "thread/start", params)
	if err != nil {
		return s.withStderr(err)
	}
	s.threadID = firstString(nestedMap(result, "thread"), "id", "threadId")
	if s.threadID == "" {
		return fmt.Errorf("codex app-server started a thread without an id")
	}
	// The app-server resolves the actual runtime default after it reads the
	// local Codex config and signed-in account. Prefer that over a catalog
	// generic default so /model matches the user's Codex CLI state exactly.
	if s.DefaultModel() == "" {
		if model := firstString(result, "model"); model != "" {
			s.modelMu.Lock()
			s.defaultModel = model
			s.modelMu.Unlock()
		}
	}
	return nil
}

func codexAppServerArgs(ctx context.Context) []string {
	telemetry := core.ObservationChildTelemetryFromContext(ctx)
	args := []string{}
	if telemetry.Endpoint != "" && telemetry.Token != "" {
		endpoint := strings.TrimRight(telemetry.Endpoint, "/") + "/v1/traces"
		exporter := fmt.Sprintf(`{"otlp-http"={endpoint=%q,protocol="json",headers={Authorization=%q}}}`,
			endpoint, "Bearer "+telemetry.Token)
		args = append(args,
			"-c", "otel.environment=\"agentnexus\"",
			"-c", "otel.exporter=\"none\"",
			"-c", "otel.metrics_exporter=\"none\"",
			"-c", "otel.trace_exporter="+exporter,
			"-c", fmt.Sprintf("otel.log_user_prompt=%t", telemetry.CaptureContent),
			"-c", `otel.span_attributes={"agentnexus.runtime"="codex"}`,
		)
	}
	return append(args, "app-server", "--listen", "stdio://")
}

// unavailableCodexThreadError identifies the one app-server resume failure
// that is safe to recover from: its backing rollout no longer exists. Keep
// every other RPC error intact so callers never mistake an auth or transport
// failure for a fresh conversation.
func unavailableCodexThreadError(err error) error {
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "no rollout found for thread id") {
		return err
	}
	return fmt.Errorf("%w: %w", core.ErrNativeSessionUnavailable, err)
}

func (s *codexSession) loadNativeModels(ctx context.Context) error {
	result, err := s.call(ctx, "model/list", map[string]any{"limit": 100, "includeHidden": false})
	if err != nil {
		return err
	}
	s.modelMu.Lock()
	s.supportedModel = codexModelsFromResult(result)
	if len(s.supportedReasoningEfforts) == 0 {
		s.supportedReasoningEfforts = codexReasoningEffortsFromResult(result)
	}
	if len(s.supportedServiceTiers) == 0 {
		s.supportedServiceTiers = codexServiceTiersFromResult(result)
	}
	s.modelReasoningEfforts = codexModelReasoningEfforts(result)
	s.modelMu.Unlock()
	return nil
}

func codexModelsFromResult(result map[string]any) []string {
	entries, _ := result["data"].([]any)
	seen := make(map[string]bool, len(entries))
	models := make([]string, 0, len(entries))
	for _, raw := range entries {
		entry, _ := raw.(map[string]any)
		model := firstString(entry, "model", "id")
		if model == "" || seen[model] {
			continue
		}
		seen[model] = true
		models = append(models, model)
	}
	return models
}

func codexReasoningEffortsFromResult(result map[string]any) []string {
	values := codexStringSlice(firstNonNil(result["supportedReasoningEfforts"], result["reasoningEfforts"]))
	entries, _ := result["data"].([]any)
	for _, raw := range entries {
		entry, _ := raw.(map[string]any)
		values = append(values, codexStringSlice(firstNonNil(entry["supportedReasoningEfforts"], entry["reasoningEfforts"]))...)
	}
	return uniqueCodexStrings(values)
}

func codexServiceTiersFromResult(result map[string]any) []string {
	values := codexStringSlice(firstNonNil(result["supportedServiceTiers"], result["serviceTiers"]))
	entries, _ := result["data"].([]any)
	for _, raw := range entries {
		entry, _ := raw.(map[string]any)
		values = append(values, codexStringSlice(firstNonNil(entry["supportedServiceTiers"], entry["serviceTiers"]))...)
	}
	return uniqueCodexStrings(values)
}

func codexModelReasoningEfforts(result map[string]any) map[string][]string {
	out := map[string][]string{}
	entries, _ := result["data"].([]any)
	for _, raw := range entries {
		entry, _ := raw.(map[string]any)
		model := firstString(entry, "model", "id")
		if model == "" {
			continue
		}
		if values := uniqueCodexStrings(codexStringSlice(firstNonNil(entry["supportedReasoningEfforts"], entry["reasoningEfforts"]))); len(values) > 0 {
			out[model] = values
		}
	}
	return out
}

func codexStringSlice(raw any) []string {
	values, _ := raw.([]any)
	out := make([]string, 0, len(values))
	for _, value := range values {
		switch typed := value.(type) {
		case string:
			if strings.TrimSpace(typed) != "" {
				out = append(out, strings.TrimSpace(typed))
			}
		case map[string]any:
			if text := firstString(typed, "reasoningEffort", "id", "value", "name"); text != "" {
				out = append(out, text)
			}
		}
	}
	return out
}

func uniqueCodexStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func containsCodexOption(options []string, value string) bool {
	for _, option := range options {
		if option == value {
			return true
		}
	}
	return false
}

func (s *codexSession) Send(ctx context.Context, text string) (<-chan *core.Event, error) {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return nil, fmt.Errorf("codex session is closed")
	}
	out := make(chan *core.Event, 128)
	go s.runTurn(ctx, text, out)
	return out, nil
}

func (s *codexSession) runTurn(ctx context.Context, text string, out chan<- *core.Event) {
	defer close(out)
	s.turnMu.Lock()
	defer s.turnMu.Unlock()

	s.mu.Lock()
	requestID := s.nextID
	s.nextID++
	threadID := s.threadID
	s.mu.Unlock()
	params := s.turnStartParams(threadID, text)
	if err := s.request(requestID, "turn/start", params); err != nil {
		out <- &core.Event{Type: core.EventError, Err: s.withStderr(err)}
		return
	}

	requestedModel := firstString(params, "model")
	mapper := &codexEventMapper{
		threadID:       threadID,
		requestedModel: requestedModel,
		resolvedModel:  requestedModel,
		lastUsage:      s.usageTotal,
		itemStarted:    map[string]int64{},
	}
	defer func() { s.usageTotal = mapper.lastUsage }()
	for {
		message, err := s.readMessage()
		if err != nil {
			out <- &core.Event{Type: core.EventError, Err: s.withStderr(err)}
			return
		}
		if id, ok := rpcID(message); ok && id == requestID {
			if rpcErr := rpcError(message); rpcErr != nil {
				out <- &core.Event{Type: core.EventError, Err: rpcErr}
				return
			}
			result, _ := message["result"].(map[string]any)
			turn := nestedMap(result, "turn")
			if turnID := firstString(turn, "id"); turnID != "" {
				mapper.turnID = turnID
			}
			if event := mapper.modelRequestEvent(); event != nil {
				out <- event
			}
			continue // turn/start acknowledgement; stream notifications follow.
		}
		if method, _ := message["method"].(string); method != "" {
			if id, ok := rpcID(message); ok {
				// Channels are non-interactive. Returning decline keeps the Codex
				// turn progressing instead of leaving it permanently blocked.
				_ = s.respondToServerRequest(id, method)
				continue
			}
			params, _ := message["params"].(map[string]any)
			events, done, turnErr := mapper.mapNotification(method, params)
			for _, event := range events {
				out <- event
			}
			if turnErr != nil {
				out <- &core.Event{Type: core.EventError, Err: turnErr}
			}
			if done {
				return
			}
		}
	}
}

func (s *codexSession) turnStartParams(threadID, text string) map[string]any {
	params := map[string]any{
		"threadId": threadID,
		"input":    []map[string]string{{"type": "text", "text": text}},
		// This asks Codex for its supported reasoning summary, never its hidden
		// chain-of-thought. The summary is safe to render as progress to users.
		"summary": "auto",
	}
	settings := s.CurrentRuntimeSettings()
	if model := settings.Model; model != "" {
		params["model"] = model
	}
	if effort := settings.ReasoningEffort; effort != "" {
		params["effort"] = effort
	}
	if tier := settings.ServiceTier; tier != "" {
		params["serviceTier"] = tier
	}
	return params
}

func (s *codexSession) RespondPermission(context.Context, bool) error { return nil }

func (s *codexSession) Close(context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	stdin, cmd := s.stdin, s.cmd
	s.mu.Unlock()
	if stdin != nil {
		_ = stdin.Close()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}
	return nil
}

func (s *codexSession) call(_ context.Context, method string, params any) (map[string]any, error) {
	s.mu.Lock()
	id := s.nextID
	s.nextID++
	s.mu.Unlock()
	if err := s.request(id, method, params); err != nil {
		return nil, err
	}
	for {
		message, err := s.readMessage()
		if err != nil {
			return nil, err
		}
		gotID, ok := rpcID(message)
		if !ok || gotID != id {
			continue
		}
		if rpcErr := rpcError(message); rpcErr != nil {
			return nil, rpcErr
		}
		result, _ := message["result"].(map[string]any)
		return result, nil
	}
}

func (s *codexSession) request(id int, method string, params any) error {
	return s.writeMessage(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
}

func (s *codexSession) notify(method string, params any) error {
	return s.writeMessage(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (s *codexSession) respondToServerRequest(id int, method string) error {
	params := any(map[string]string{"decision": "decline"})
	if method == "permissions/requestApproval" {
		params = map[string]string{"permissions": "none"}
	}
	return s.writeMessage(map[string]any{"jsonrpc": "2.0", "id": id, "result": params})
}

func (s *codexSession) writeMessage(message map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.stdin == nil {
		return fmt.Errorf("codex app-server is closed")
	}
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	_, err = s.stdin.Write(append(data, '\n'))
	return err
}

func (s *codexSession) readMessage() (map[string]any, error) {
	line, err := s.reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	var message map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &message); err != nil {
		return nil, err
	}
	return message, nil
}

func (s *codexSession) withStderr(err error) error {
	detail := strings.TrimSpace(s.stderr.String())
	if detail == "" {
		return err
	}
	if len(detail) > 16*1024 {
		detail = detail[len(detail)-16*1024:]
	}
	return fmt.Errorf("%s (%w)", detail, err)
}

func codexBuildEnv(extra map[string]string) []string {
	env := os.Environ()
	for key, value := range extra {
		env = append(env, key+"="+value)
	}
	return env
}

type codexEventMapper struct {
	answer         string
	thinking       string
	threadID       string
	turnID         string
	requestedModel string
	resolvedModel  string
	requestEmitted bool
	usageEvents    int
	retryAttempt   int
	lastUsage      codexTokenUsage
	itemStarted    map[string]int64
}

func (m *codexEventMapper) mapNotification(method string, params map[string]any) ([]*core.Event, bool, error) {
	m.updateContext(params)
	switch method {
	case "turn/started":
		turn := nestedMap(params, "turn")
		if turnID := firstString(turn, "id"); turnID != "" {
			m.turnID = turnID
		}
		if event := m.modelRequestEvent(); event != nil {
			return []*core.Event{event}, false, nil
		}
	case "item/agentMessage/delta":
		delta, _ := params["delta"].(string)
		if delta == "" {
			return nil, false, nil
		}
		m.answer += delta
		return []*core.Event{{
			Type: core.EventOutput, TurnID: m.turnID, ItemID: firstString(params, "itemId"), Text: m.answer,
			Metadata: m.metadata("delta"),
		}}, false, nil
	case "item/reasoning/summaryTextDelta", "item/plan/delta":
		delta, _ := params["delta"].(string)
		if delta == "" {
			return nil, false, nil
		}
		m.thinking += delta
		return []*core.Event{{
			Type: core.EventThinking, TurnID: m.turnID, ItemID: firstString(params, "itemId"), Text: m.thinking,
			Metadata: m.metadata("public_summary"),
		}}, false, nil
	case "item/started":
		item := nestedMap(params, "item")
		itemID := firstString(item, "id")
		startedAt := codexInt64(params["startedAtMs"])
		if itemID != "" && startedAt != 0 {
			if m.itemStarted == nil {
				m.itemStarted = map[string]int64{}
			}
			m.itemStarted[itemID] = startedAt
		}
		if firstString(item, "type") == "contextCompaction" {
			status := firstString(item, "status")
			if status == "" {
				status = "in_progress"
			}
			return []*core.Event{{
				Type: core.EventCompaction, EventID: lifecycleEventID(itemID, "started"), TurnID: m.turnID,
				ItemID: itemID, Status: status, Metadata: m.metadata("started"),
			}}, false, nil
		}
		if event := codexToolStart(item); event != nil {
			m.decorateItemEvent(event, itemID, "started", 0)
			return []*core.Event{event}, false, nil
		}
	case "item/completed":
		item := nestedMap(params, "item")
		itemID := firstString(item, "id")
		duration := codexInt64(item["durationMs"])
		if duration == 0 && itemID != "" {
			if started := m.itemStarted[itemID]; started != 0 {
				if elapsed := codexInt64(params["completedAtMs"]) - started; elapsed > 0 {
					duration = elapsed
				}
			}
		}
		delete(m.itemStarted, itemID)
		if firstString(item, "type") == "agentMessage" {
			if text := firstString(item, "text"); text != "" {
				m.answer = text
				return []*core.Event{{
					Type: core.EventOutput, EventID: lifecycleEventID(itemID, "completed"), TurnID: m.turnID,
					ItemID: itemID, Text: text, Status: "completed", DurationMs: duration, Metadata: m.metadata("completed"),
				}}, false, nil
			}
			return nil, false, nil
		}
		if firstString(item, "type") == "contextCompaction" {
			return []*core.Event{{
				Type: core.EventCompaction, EventID: lifecycleEventID(itemID, "completed"), TurnID: m.turnID,
				ItemID: itemID, Status: "completed", DurationMs: duration, Metadata: m.metadata("completed"),
			}}, false, nil
		}
		if event := codexToolResult(item); event != nil {
			m.decorateItemEvent(event, itemID, "completed", duration)
			return []*core.Event{event}, false, nil
		}
	case "item/commandExecution/outputDelta", "item/fileChange/outputDelta":
		if delta, _ := params["delta"].(string); delta != "" {
			return []*core.Event{m.toolUpdateEvent(firstString(params, "itemId"), "output_delta", "", delta)}, false, nil
		}
	case "item/mcpToolCall/progress":
		if message, _ := params["message"].(string); message != "" {
			return []*core.Event{m.toolUpdateEvent(firstString(params, "itemId"), "progress", "", message)}, false, nil
		}
	case "item/fileChange/patchUpdated":
		if changes := codexValue(params["changes"]); changes != "" {
			return []*core.Event{m.toolUpdateEvent(firstString(params, "itemId"), "input_update", changes, "")}, false, nil
		}
	case "item/commandExecution/terminalInteraction":
		if stdin, _ := params["stdin"].(string); stdin != "" {
			event := m.toolUpdateEvent(firstString(params, "itemId"), "terminal_input", stdin, "")
			event.Metadata["process_id"] = firstString(params, "processId")
			return []*core.Event{event}, false, nil
		}
	case "thread/tokenUsage/updated":
		if event := m.tokenUsageEvent(params); event != nil {
			return []*core.Event{event}, false, nil
		}
	case "thread/compacted":
		return []*core.Event{{
			Type: core.EventCompaction, EventID: lifecycleEventID(m.turnID, "legacy_compaction"), TurnID: m.turnID,
			Status: "completed", Metadata: m.metadata("completed"),
		}}, false, nil
	case "model/rerouted":
		fromModel := firstString(params, "fromModel")
		toModel := firstString(params, "toModel")
		if m.requestedModel == "" {
			m.requestedModel = fromModel
		}
		if toModel != "" {
			m.resolvedModel = toModel
		}
		metadata := m.metadata("rerouted")
		metadata["from_model"] = fromModel
		metadata["to_model"] = toModel
		metadata["reroute_reason"] = firstString(params, "reason")
		requestID := m.currentModelRequestID()
		attempt := m.currentAttempt()
		return []*core.Event{{
			Type: core.EventModelRequest, EventID: lifecycleEventID(requestID, fmt.Sprintf("attempt:%d:rerouted:%s", attempt, toModel)), TurnID: m.turnID,
			Status: "rerouted", Usage: &core.TurnUsage{Model: toModel, RequestID: requestID, RequestedModel: m.requestedModel, ResolvedModel: toModel, Attempt: attempt},
			Metadata: metadata,
		}}, false, nil
	case "error":
		err := codexNotificationError(params)
		willRetry := codexBool(params["willRetry"])
		attempt := m.currentAttempt()
		requestID := m.currentModelRequestID()
		metadata := m.metadata("attempt_failed")
		metadata["will_retry"] = fmt.Sprintf("%t", willRetry)
		events := []*core.Event{{
			Type: core.EventModelResponse, EventID: lifecycleEventID(requestID, fmt.Sprintf("attempt:%d:failed", attempt)), TurnID: m.turnID,
			Status: "failed", Err: err, Usage: &core.TurnUsage{
				Model: m.resolvedModel, RequestID: requestID, RequestedModel: m.requestedModel, ResolvedModel: m.resolvedModel, Attempt: attempt,
			}, Metadata: metadata,
		}}
		if willRetry {
			m.retryAttempt = attempt + 1
			events = append(events, &core.Event{
				Type: core.EventModelRequest, EventID: lifecycleEventID(requestID, fmt.Sprintf("attempt:%d:start", m.retryAttempt)), TurnID: m.turnID,
				Status: "retrying", Usage: &core.TurnUsage{
					Model: m.resolvedModel, RequestID: requestID, RequestedModel: m.requestedModel, ResolvedModel: m.resolvedModel, Attempt: m.retryAttempt,
				}, Metadata: m.metadata("retry_started"),
			})
			return events, false, nil
		}
		return events, false, err
	case "turn/completed":
		turn := nestedMap(params, "turn")
		if turnID := firstString(turn, "id"); turnID != "" {
			m.turnID = turnID
		}
		status := firstString(turn, "status")
		duration := codexInt64(turn["durationMs"])
		if status == "failed" {
			err := codexTurnError(turn)
			return []*core.Event{{
				Type: core.EventModelResponse, EventID: lifecycleEventID(m.turnID, "failed"), TurnID: m.turnID,
				Status: status, DurationMs: duration, Err: err, Metadata: m.metadata("completed"),
			}}, true, err
		}
		if m.answer != "" {
			return []*core.Event{{
				Type: core.EventFinal, EventID: lifecycleEventID(m.turnID, "final"), TurnID: m.turnID,
				Text: m.answer, Final: true, Status: status, DurationMs: duration, Metadata: m.metadata("completed"),
			}}, true, nil
		}
		return []*core.Event{{
			Type: core.EventModelResponse, EventID: lifecycleEventID(m.turnID, "completed"), TurnID: m.turnID,
			Status: status, DurationMs: duration, Metadata: m.metadata("completed"),
		}}, true, nil
	}
	// Raw reasoning deltas are intentionally not rendered. Codex's supported
	// reasoning summary above is designed for user-visible progress; raw model
	// reasoning is internal and may be incomplete or misleading.
	return nil, false, nil
}

func (m *codexEventMapper) updateContext(params map[string]any) {
	if threadID := firstString(params, "threadId"); threadID != "" {
		m.threadID = threadID
	}
	if turnID := firstString(params, "turnId"); turnID != "" {
		m.turnID = turnID
	}
}

func (m *codexEventMapper) modelRequestEvent() *core.Event {
	if m.requestEmitted || m.turnID == "" {
		return nil
	}
	m.requestEmitted = true
	m.retryAttempt = 1
	requestID := m.currentModelRequestID()
	return &core.Event{
		Type: core.EventModelRequest, EventID: lifecycleEventID(requestID, "attempt:1:start"), TurnID: m.turnID, Status: "in_progress",
		Usage: &core.TurnUsage{
			Model: m.resolvedModel, RequestID: requestID, RequestedModel: m.requestedModel, ResolvedModel: m.resolvedModel, Attempt: 1,
		},
		Metadata: m.metadata("started"),
	}
}

func (m *codexEventMapper) metadata(lifecycle string) map[string]string {
	metadata := map[string]string{
		"runtime":   "codex",
		"transport": "app-server",
		"coverage":  "native",
		"lifecycle": lifecycle,
	}
	if m.threadID != "" {
		metadata["thread_id"] = m.threadID
	}
	return metadata
}

func (m *codexEventMapper) decorateItemEvent(event *core.Event, itemID, lifecycle string, duration int64) {
	event.EventID = lifecycleEventID(itemID, lifecycle)
	event.TurnID = m.turnID
	event.ItemID = itemID
	event.ToolCallID = itemID
	event.DurationMs = duration
	event.Metadata = m.metadata(lifecycle)
}

func (m *codexEventMapper) toolUpdateEvent(itemID, lifecycle, input, result string) *core.Event {
	return &core.Event{
		Type: core.EventToolUse, TurnID: m.turnID, ItemID: itemID, ToolCallID: itemID,
		ToolInputRaw: input, ToolResultRaw: result, Status: "in_progress", Metadata: m.metadata(lifecycle),
	}
}

func (m *codexEventMapper) tokenUsageEvent(params map[string]any) *core.Event {
	tokenUsage := nestedMap(params, "tokenUsage")
	totalMap := nestedMap(tokenUsage, "total")
	lastMap := nestedMap(tokenUsage, "last")
	current := codexTokenUsageFromMap(totalMap)
	last := codexTokenUsageFromMap(lastMap)
	if current.isZero() && !last.isZero() {
		current = m.lastUsage.add(last)
	}
	delta := current.deltaFrom(m.lastUsage)
	// A resumed thread starts with no in-memory baseline. Its first cumulative
	// total includes historic turns, while `last` is only the current request.
	if m.lastUsage.isZero() && !last.isZero() {
		delta = last
	}
	m.lastUsage = current
	if delta.isZero() {
		return nil
	}
	m.usageEvents++
	resolved := m.resolvedModel
	if resolved == "" {
		resolved = m.requestedModel
	}
	requestID := m.modelRequestID(m.usageEvents)
	attempt := m.currentAttempt()
	m.retryAttempt = 1
	return &core.Event{
		Type: core.EventModelResponse, EventID: lifecycleEventID(requestID, fmt.Sprintf("usage:%d", m.usageEvents)), TurnID: requestID,
		Status: "completed",
		Usage: &core.TurnUsage{
			Model: resolved, RequestID: requestID, RequestedModel: m.requestedModel, ResolvedModel: resolved,
			InputTokens: codexUncachedInput(delta.InputTokens, delta.CachedInputTokens), OutputTokens: delta.OutputTokens, CacheReadTokens: delta.CachedInputTokens,
			ReasoningTokens: delta.ReasoningOutputTokens, TotalTokens: delta.total(), Cumulative: false, Attempt: attempt,
		},
		Metadata: m.metadata("usage_delta"),
	}
}

func (m *codexEventMapper) currentAttempt() int {
	if m.retryAttempt < 1 {
		return 1
	}
	return m.retryAttempt
}

func (m *codexEventMapper) currentModelRequestID() string {
	return m.modelRequestID(m.usageEvents + 1)
}

func (m *codexEventMapper) modelRequestID(index int) string {
	if m.turnID == "" {
		return ""
	}
	return fmt.Sprintf("%s:model:%d", m.turnID, index)
}

func codexToolStart(item map[string]any) *core.Event {
	name, input, ok := codexToolDescriptor(item)
	if !ok {
		return nil
	}
	status := firstString(item, "status")
	if status == "" {
		status = "in_progress"
	}
	return &core.Event{
		Type: core.EventToolUse, ToolName: name, ToolInput: truncateCodex(input, 600), ToolInputRaw: input, Status: status,
	}
}

func codexToolResult(item map[string]any) *core.Event {
	typ := firstString(item, "type")
	_, _, ok := codexToolDescriptor(item)
	if !ok {
		return nil
	}
	var result string
	switch typ {
	case "commandExecution":
		parts := make([]string, 0, 2)
		if code, ok := item["exitCode"]; ok && code != nil {
			parts = append(parts, "exit "+codexValue(code))
		}
		if output := codexValue(firstNonNil(item["aggregatedOutput"], item["output"])); output != "" {
			parts = append(parts, output)
		}
		if len(parts) == 0 {
			parts = append(parts, firstString(item, "status"))
		}
		result = strings.Join(parts, " · ")
	case "mcpToolCall", "webSearch", "fileChange", "dynamicToolCall", "collabAgentToolCall", "subAgentActivity", "imageView", "imageGeneration", "sleep":
		result = codexValue(firstNonNil(item["result"], item["output"], item["aggregatedOutput"], item["contentItems"], item["agentsStates"], item["status"]))
		if result == "" {
			result = "完成"
		}
	}
	status := firstString(item, "status")
	if status == "" {
		status = "completed"
	}
	return &core.Event{
		Type: core.EventToolUse, ToolResult: truncateCodex(result, 800),
		ToolResultRaw: codexValue(item), Status: status, Err: codexItemError(item),
	}
}

func codexToolDescriptor(item map[string]any) (name, input string, ok bool) {
	switch firstString(item, "type") {
	case "commandExecution":
		return "执行命令", codexValue(item["command"]), true
	case "mcpToolCall":
		name = strings.Trim(strings.Join([]string{firstString(item, "server"), firstString(item, "tool")}, ":"), ":")
		if name == "" {
			name = "MCP 工具"
		}
		return name, codexValue(firstNonNil(item["arguments"], item["input"])), true
	case "webSearch":
		return "网页搜索", codexValue(firstNonNil(item["query"], item["queries"])), true
	case "fileChange":
		return "修改文件", codexValue(firstNonNil(item["changes"], item["patch"])), true
	case "dynamicToolCall":
		name = firstString(item, "tool")
		if name == "" {
			name = "动态工具"
		}
		return name, codexValue(firstNonNil(item["arguments"], item["input"])), true
	case "collabAgentToolCall":
		name = firstString(item, "tool")
		if name == "" {
			name = "协作代理"
		}
		return name, codexValue(firstNonNil(item["prompt"], item["receiverThreadIds"])), true
	case "subAgentActivity":
		return "子代理活动", codexValue(map[string]any{"agentPath": item["agentPath"], "agentThreadId": item["agentThreadId"], "kind": item["kind"]}), true
	case "imageView":
		return "查看图片", codexValue(item["path"]), true
	case "imageGeneration":
		return "生成图片", codexValue(firstNonNil(item["revisedPrompt"], item["prompt"])), true
	case "sleep":
		return "等待", codexValue(item["durationMs"]), true
	default:
		return "", "", false
	}
}

func codexItemError(item map[string]any) error {
	status := strings.ToLower(firstString(item, "status"))
	if status != "failed" && status != "declined" {
		return nil
	}
	detail := firstString(nestedMap(item, "error"), "message")
	if detail == "" {
		detail = codexValue(firstNonNil(item["error"], item["result"], item["status"]))
	}
	return fmt.Errorf("codex tool %s: %s", status, detail)
}

func codexTurnError(turn map[string]any) error {
	detail := firstString(nestedMap(turn, "error"), "message")
	if detail == "" {
		detail = codexValue(turn["error"])
	}
	if detail == "" {
		detail = "turn failed"
	}
	return fmt.Errorf("%s", detail)
}

func codexNotificationError(params map[string]any) error {
	detail := firstString(nestedMap(params, "error"), "message")
	if detail == "" {
		detail = codexValue(params["error"])
	}
	if detail == "" {
		detail = "model request failed"
	}
	return fmt.Errorf("%s", detail)
}

func lifecycleEventID(id, lifecycle string) string {
	if id == "" {
		return ""
	}
	return id + ":" + lifecycle
}

type codexTokenUsage struct {
	InputTokens           int64
	CachedInputTokens     int64
	OutputTokens          int64
	ReasoningOutputTokens int64
	TotalTokens           int64
}

func codexTokenUsageFromMap(value map[string]any) codexTokenUsage {
	return codexTokenUsage{
		InputTokens: codexInt64(value["inputTokens"]), CachedInputTokens: codexInt64(value["cachedInputTokens"]),
		OutputTokens: codexInt64(value["outputTokens"]), ReasoningOutputTokens: codexInt64(value["reasoningOutputTokens"]),
		TotalTokens: codexInt64(value["totalTokens"]),
	}
}

func (u codexTokenUsage) isZero() bool {
	return u.InputTokens == 0 && u.CachedInputTokens == 0 && u.OutputTokens == 0 && u.ReasoningOutputTokens == 0 && u.TotalTokens == 0
}

func (u codexTokenUsage) total() int64 {
	if u.TotalTokens != 0 {
		return u.TotalTokens
	}
	return u.InputTokens + u.OutputTokens
}

func (u codexTokenUsage) add(other codexTokenUsage) codexTokenUsage {
	return codexTokenUsage{
		InputTokens: u.InputTokens + other.InputTokens, CachedInputTokens: u.CachedInputTokens + other.CachedInputTokens,
		OutputTokens: u.OutputTokens + other.OutputTokens, ReasoningOutputTokens: u.ReasoningOutputTokens + other.ReasoningOutputTokens,
		TotalTokens: u.TotalTokens + other.TotalTokens,
	}
}

func (u codexTokenUsage) deltaFrom(previous codexTokenUsage) codexTokenUsage {
	if u.InputTokens < previous.InputTokens || u.CachedInputTokens < previous.CachedInputTokens ||
		u.OutputTokens < previous.OutputTokens || u.ReasoningOutputTokens < previous.ReasoningOutputTokens || u.TotalTokens < previous.TotalTokens {
		return u
	}
	return codexTokenUsage{
		InputTokens: u.InputTokens - previous.InputTokens, CachedInputTokens: u.CachedInputTokens - previous.CachedInputTokens,
		OutputTokens: u.OutputTokens - previous.OutputTokens, ReasoningOutputTokens: u.ReasoningOutputTokens - previous.ReasoningOutputTokens,
		TotalTokens: u.TotalTokens - previous.TotalTokens,
	}
}

func codexInt64(value any) int64 {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	case json.Number:
		out, _ := typed.Int64()
		return out
	default:
		return 0
	}
}

func codexBool(value any) bool {
	out, _ := value.(bool)
	return out
}

func codexUncachedInput(input, cached int64) int64 {
	if input <= cached {
		return 0
	}
	return input - cached
}

func rpcID(message map[string]any) (int, bool) {
	value, ok := message["id"]
	if !ok {
		return 0, false
	}
	switch v := value.(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	default:
		return 0, false
	}
}

func rpcError(message map[string]any) error {
	if raw, ok := message["error"]; ok && raw != nil {
		return fmt.Errorf("codex app-server RPC error: %s", codexValue(raw))
	}
	return nil
}

func nestedMap(value map[string]any, key string) map[string]any {
	nested, _ := value[key].(map[string]any)
	return nested
}

func firstString(value map[string]any, keys ...string) string {
	for _, key := range keys {
		if out := strings.TrimSpace(codexValue(value[key])); out != "" {
			return out
		}
	}
	return ""
}

func firstNonNil(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func codexValue(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(data))
	}
}

func truncateCodex(value string, max int) string {
	value = strings.TrimSpace(strings.Join(strings.Fields(value), " "))
	if len(value) <= max {
		return value
	}
	if max <= 3 {
		return value[:max]
	}
	return value[:max-3] + "..."
}
