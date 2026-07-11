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
	cmd := exec.Command(name, "app-server", "--listen", "stdio://")
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
			return s.withStderr(err)
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

	// Emit immediately, before the first server response, so the Feishu reply
	// is visibly live even for turns that spend time setting up tools.
	out <- &core.Event{Type: core.EventThinking, Text: "正在思考…"}

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

	mapper := &codexEventMapper{}
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
	answer   string
	thinking string
}

func (m *codexEventMapper) mapNotification(method string, params map[string]any) ([]*core.Event, bool, error) {
	switch method {
	case "item/agentMessage/delta":
		delta, _ := params["delta"].(string)
		if delta == "" {
			return nil, false, nil
		}
		m.answer += delta
		return []*core.Event{{Type: core.EventOutput, Text: m.answer}}, false, nil
	case "item/reasoning/summaryTextDelta", "item/plan/delta":
		delta, _ := params["delta"].(string)
		if delta == "" {
			return nil, false, nil
		}
		m.thinking += delta
		return []*core.Event{{Type: core.EventThinking, Text: m.thinking}}, false, nil
	case "item/reasoning/summaryPartAdded":
		if m.thinking == "" {
			return []*core.Event{{Type: core.EventThinking, Text: "正在思考…"}}, false, nil
		}
		return nil, false, nil
	case "item/started":
		item := nestedMap(params, "item")
		if firstString(item, "type") == "reasoning" && m.thinking == "" {
			return []*core.Event{{Type: core.EventThinking, Text: "正在思考…"}}, false, nil
		}
		if event := codexToolStart(item); event != nil {
			return []*core.Event{event}, false, nil
		}
	case "item/completed":
		item := nestedMap(params, "item")
		if firstString(item, "type") == "agentMessage" {
			if text := firstString(item, "text"); text != "" {
				m.answer = text
				return []*core.Event{{Type: core.EventFinal, Text: text, Final: true}}, false, nil
			}
			return nil, false, nil
		}
		if result, ok := codexToolResult(item); ok {
			return []*core.Event{{Type: core.EventToolUse, ToolResult: result}}, false, nil
		}
	case "turn/completed":
		turn := nestedMap(params, "turn")
		if firstString(turn, "status") == "failed" {
			return nil, true, fmt.Errorf("%s", codexValue(turn["error"]))
		}
		if m.answer != "" {
			return []*core.Event{{Type: core.EventFinal, Text: m.answer, Final: true}}, true, nil
		}
		return nil, true, nil
	}
	// Raw reasoning deltas are intentionally not rendered. Codex's supported
	// reasoning summary above is designed for user-visible progress; raw model
	// reasoning is internal and may be incomplete or misleading.
	return nil, false, nil
}

func codexToolStart(item map[string]any) *core.Event {
	typ := firstString(item, "type")
	var name, input string
	switch typ {
	case "commandExecution":
		name, input = "执行命令", codexValue(item["command"])
	case "mcpToolCall":
		name = strings.Trim(strings.Join([]string{firstString(item, "server"), firstString(item, "tool")}, ":"), ":")
		if name == "" {
			name = "MCP 工具"
		}
		input = codexValue(firstNonNil(item["arguments"], item["input"]))
	case "webSearch":
		name, input = "网页搜索", codexValue(firstNonNil(item["query"], item["queries"]))
	case "fileChange":
		name, input = "修改文件", codexValue(firstNonNil(item["changes"], item["patch"]))
	case "dynamicToolCall":
		name, input = firstString(item, "tool"), codexValue(firstNonNil(item["arguments"], item["input"]))
		if name == "" {
			name = "动态工具"
		}
	default:
		return nil
	}
	return &core.Event{Type: core.EventToolUse, ToolName: name, ToolInput: truncateCodex(input, 600)}
}

func codexToolResult(item map[string]any) (string, bool) {
	typ := firstString(item, "type")
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
		return truncateCodex(strings.Join(parts, " · "), 800), true
	case "mcpToolCall", "webSearch", "fileChange", "dynamicToolCall":
		result := codexValue(firstNonNil(item["result"], item["output"], item["status"]))
		if result == "" {
			result = "完成"
		}
		return truncateCodex(result, 800), true
	default:
		return "", false
	}
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
