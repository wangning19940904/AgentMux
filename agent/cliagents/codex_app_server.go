package cliagents

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// Codex's app-server is the transport used by the Codex desktop app. It is
// materially richer than `codex exec --json`: it exposes token deltas,
// reasoning summaries, tool lifecycle events, and the signed-in account's
// own model catalog. One long-lived stdio process is shared by every session
// of a Codex Agent and multiplexes independent native threads and turns.

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
	defaultApprovalMode       string
	supportedReasoningEfforts []string
	supportedServiceTiers     []string
	supportedApprovalModes    []string
	env                       map[string]string
	clientMu                  sync.Mutex
	client                    *codexAppClient
	capabilityState           string
	capabilityError           string
}

func newCodexAgent(cfg map[string]any) *codexAgent {
	a := &codexAgent{capabilityState: "not_probed"}
	if value, ok := cfg["system_prompt"].(string); ok {
		a.systemPrompt = value
	}
	if settings := core.RuntimeSettingsSelectionFromConfig(cfg); settings != nil {
		defaults := settings.DefaultRuntimeSettings()
		capabilities := settings.RuntimeSettingsCapabilities()
		a.defaultModel = defaults.Model
		a.defaultReasoningEffort = defaults.ReasoningEffort
		a.defaultServiceTier = defaults.ServiceTier
		a.defaultApprovalMode = defaults.ApprovalMode
		for _, option := range capabilities.ReasoningEfforts {
			a.supportedReasoningEfforts = append(a.supportedReasoningEfforts, option.Value)
		}
		for _, option := range capabilities.ServiceTiers {
			a.supportedServiceTiers = append(a.supportedServiceTiers, option.Value)
		}
		for _, option := range capabilities.ApprovalModes {
			a.supportedApprovalModes = append(a.supportedApprovalModes, option.Value)
		}
	}
	if a.defaultApprovalMode == "" {
		a.defaultApprovalMode = core.ApprovalModeManual
	}
	if len(a.supportedApprovalModes) == 0 {
		a.supportedApprovalModes = core.ApprovalModeValuesForRuntime("codex")
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

func (a *codexAgent) ListSessions(ctx context.Context) ([]string, error) {
	a.clientMu.Lock()
	client := a.client
	a.clientMu.Unlock()
	if client == nil || client.isClosed() {
		return nil, nil
	}
	result, err := client.call(ctx, "thread/list", map[string]any{"limit": 100, "archived": false})
	if err != nil {
		return nil, err
	}
	rows, _ := result["data"].([]any)
	ids := make([]string, 0, len(rows))
	for _, raw := range rows {
		thread, _ := raw.(map[string]any)
		if id := firstString(thread, "id", "threadId"); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (a *codexAgent) ListNativeThreads(ctx context.Context, workDir string) ([]core.NativeThread, error) {
	client, err := a.appClient(ctx, workDir)
	if err != nil {
		return nil, err
	}
	result, err := client.call(ctx, "thread/list", map[string]any{
		"limit": 100, "archived": false, "cwd": workDir,
		"sortKey": "recency_at", "sortDirection": "desc",
	})
	if err != nil {
		return nil, err
	}
	rows, _ := result["data"].([]any)
	out := make([]core.NativeThread, 0, len(rows))
	for _, raw := range rows {
		thread, _ := raw.(map[string]any)
		id := firstString(thread, "id", "threadId")
		if id == "" {
			continue
		}
		threadWorkDir := firstString(thread, "cwd")
		if !sameCodexWorkDir(threadWorkDir, workDir) {
			continue
		}
		out = append(out, core.NativeThread{
			ID: id, Title: firstString(thread, "name", "title"),
			Preview: firstString(thread, "preview"), WorkDir: threadWorkDir,
		})
	}
	return out, nil
}

func sameCodexWorkDir(left, right string) bool {
	canonical := func(value string) string {
		value = strings.TrimSpace(value)
		if value == "" {
			return ""
		}
		if absolute, err := filepath.Abs(value); err == nil {
			value = absolute
		}
		value = filepath.Clean(value)
		if resolved, err := filepath.EvalSymlinks(value); err == nil {
			value = resolved
		}
		return value
	}
	return canonical(left) != "" && canonical(left) == canonical(right)
}

func (a *codexAgent) OpenNativeThread(ctx context.Context, threadID string) (bool, string, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return false, "", fmt.Errorf("Codex thread id is required")
	}
	for _, r := range threadID {
		if !(r == '-' || r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false, "", fmt.Errorf("invalid Codex thread id %q", threadID)
		}
	}
	fallback := "codex resume " + threadID
	if runtime.GOOS != "darwin" {
		return false, fallback, nil
	}
	if err := exec.CommandContext(ctx, "open", "codex://threads/"+threadID).Run(); err != nil {
		return false, fallback, fmt.Errorf("open Codex thread: %w", err)
	}
	return true, fallback, nil
}

func (a *codexAgent) CodexControlCapability() core.CodexControlCapability {
	a.clientMu.Lock()
	defer a.clientMu.Unlock()
	state := a.capabilityState
	if state == "" {
		state = "not_probed"
	}
	if a.client != nil && a.client.isClosed() {
		state = "disconnected"
	}
	return core.CodexControlCapability{
		State: state, Error: a.capabilityError, Experimental: true,
		Threads: true, Steer: true, Interrupt: true, Interactions: true,
		DeepLink: runtime.GOOS == "darwin",
	}
}

func (a *codexAgent) Stop(context.Context) error {
	a.clientMu.Lock()
	client := a.client
	a.client = nil
	a.clientMu.Unlock()
	if client != nil {
		return client.close()
	}
	return nil
}

func (a *codexAgent) appClient(ctx context.Context, workDir string) (*codexAppClient, error) {
	a.clientMu.Lock()
	defer a.clientMu.Unlock()
	if a.client != nil && !a.client.isClosed() {
		return a.client, nil
	}
	client, err := newCodexAppClient(ctx, a, workDir)
	if err != nil {
		a.client = nil
		a.capabilityState = "unavailable"
		a.capabilityError = err.Error()
		return nil, err
	}
	a.client = client
	a.capabilityState = "ready"
	a.capabilityError = ""
	return client, nil
}

type codexSession struct {
	agent   *codexAgent
	workDir string
	client  *codexAppClient
	inbox   chan map[string]any

	mu                  sync.Mutex
	turnMu              sync.Mutex
	closed              bool
	threadID            string
	activeTurnID        string
	activeTurn          bool
	freshThread         bool
	threadNamed         bool
	interactionPrefix   string
	pendingInteractions map[string]codexPendingInteraction
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
	defaultApprovalMode       string
	currentApprovalMode       string
	supportedModel            []string
	supportedReasoningEfforts []string
	supportedServiceTiers     []string
	supportedApprovalModes    []string
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
		defaultApprovalMode:       agent.defaultApprovalMode,
		supportedReasoningEfforts: append([]string(nil), agent.supportedReasoningEfforts...),
		supportedServiceTiers:     append([]string(nil), agent.supportedServiceTiers...),
		supportedApprovalModes:    append([]string(nil), agent.supportedApprovalModes...),
		modelReasoningEfforts:     map[string][]string{},
		pendingInteractions:       map[string]codexPendingInteraction{},
		interactionPrefix:         core.NewChannelControlID("app"),
		inbox:                     make(chan map[string]any, 256),
	}
	client, err := agent.appClient(ctx, workDir)
	if err != nil {
		return nil, err
	}
	s.client = client
	if err := s.startShared(ctx, resumeID); err != nil {
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
		ApprovalModes:    core.RuntimeOptions(s.supportedApprovalModes),
	}
}

func (s *codexSession) CurrentRuntimeSettings() core.RuntimeSettings {
	s.modelMu.Lock()
	defer s.modelMu.Unlock()
	settings := core.RuntimeSettings{Model: s.defaultModel, ReasoningEffort: s.defaultReasoningEffort, ServiceTier: s.defaultServiceTier, ApprovalMode: s.defaultApprovalMode}
	if s.currentModel != "" {
		settings.Model = s.currentModel
	}
	if s.currentReasoningEffort != "" {
		settings.ReasoningEffort = s.currentReasoningEffort
	}
	if s.currentServiceTier != "" {
		settings.ServiceTier = s.currentServiceTier
	}
	if s.currentApprovalMode != "" {
		settings.ApprovalMode = s.currentApprovalMode
	}
	return settings
}

func (s *codexSession) DefaultRuntimeSettings() core.RuntimeSettings {
	s.modelMu.Lock()
	defer s.modelMu.Unlock()
	return core.RuntimeSettings{Model: s.defaultModel, ReasoningEffort: s.defaultReasoningEffort, ServiceTier: s.defaultServiceTier, ApprovalMode: s.defaultApprovalMode}
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
	case core.RuntimeSettingApprovalMode:
		options = s.supportedApprovalModes
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
	} else if setting == core.RuntimeSettingServiceTier {
		s.currentServiceTier = value
	} else {
		s.currentApprovalMode = value
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
	case core.RuntimeSettingApprovalMode:
		s.currentApprovalMode = ""
	default:
		return fmt.Errorf("unknown runtime setting %q", setting)
	}
	return nil
}

func (s *codexSession) startShared(ctx context.Context, resumeID string) error {
	if err := s.loadNativeModels(ctx); err != nil {
		return s.withStderr(err)
	}
	var result map[string]any
	var err error
	if resumeID != "" {
		result, err = s.call(ctx, "thread/resume", map[string]any{"threadId": resumeID})
		if err != nil {
			return unavailableCodexThreadError(s.withStderr(err))
		}
	} else {
		s.freshThread = true
		params := map[string]any{"cwd": s.workDir}
		if prompt := strings.TrimSpace(s.agent.systemPrompt); prompt != "" {
			params["developerInstructions"] = prompt
		}
		if model := s.DefaultModel(); model != "" {
			params["model"] = model
		}
		result, err = s.call(ctx, "thread/start", params)
		if err != nil {
			return s.withStderr(err)
		}
	}
	threadID := firstString(nestedMap(result, "thread"), "id", "threadId")
	if threadID == "" {
		threadID = firstString(result, "id", "threadId")
	}
	if threadID == "" {
		return fmt.Errorf("codex app-server returned a thread without an id")
	}
	s.mu.Lock()
	s.threadID = threadID
	s.mu.Unlock()
	if s.DefaultModel() == "" {
		if model := firstString(result, "model"); model != "" {
			s.modelMu.Lock()
			s.defaultModel = model
			s.modelMu.Unlock()
		}
	}
	return s.client.register(threadID, s)
}

func codexAppServerArgs(ctx context.Context) []string {
	telemetry := core.ObservationChildTelemetryFromContext(ctx)
	args := []string{}
	if telemetry.Endpoint != "" && telemetry.Token != "" {
		endpoint := strings.TrimRight(telemetry.Endpoint, "/") + "/v1/traces"
		exporter := fmt.Sprintf(`{"otlp-http"={endpoint=%q,protocol="json",headers={Authorization=%q}}}`,
			endpoint, "Bearer "+telemetry.Token)
		args = append(args,
			"-c", "otel.environment=\"agentmux\"",
			"-c", "otel.exporter=\"none\"",
			"-c", "otel.metrics_exporter=\"none\"",
			"-c", "otel.trace_exporter="+exporter,
			"-c", fmt.Sprintf("otel.log_user_prompt=%t", telemetry.CaptureContent),
			"-c", `otel.span_attributes={"agentmux.runtime"="codex"}`,
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
	return s.SendInput(ctx, core.AgentTurnInput{Text: text})
}

func (s *codexSession) SendInput(ctx context.Context, input core.AgentTurnInput) (<-chan *core.Event, error) {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return nil, fmt.Errorf("codex session is closed")
	}
	out := make(chan *core.Event, 128)
	go s.runTurn(ctx, input, out)
	return out, nil
}

func (s *codexSession) runTurn(ctx context.Context, input core.AgentTurnInput, out chan<- *core.Event) {
	defer close(out)
	s.turnMu.Lock()
	defer s.turnMu.Unlock()

	s.mu.Lock()
	s.activeTurn = true
	s.activeTurnID = ""
	threadID := s.threadID
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.activeTurn = false
		s.activeTurnID = ""
		s.mu.Unlock()
	}()
	params := s.turnStartParamsInput(threadID, input)
	result, err := s.call(ctx, "turn/start", params)
	if err != nil {
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
	turn := nestedMap(result, "turn")
	if turnID := firstString(turn, "id"); turnID != "" {
		mapper.turnID = turnID
		s.setActiveTurnID(turnID)
	}
	if event := mapper.modelRequestEvent(); event != nil {
		out <- event
	}
	s.nameFreshThread(ctx, input.Text)
	for {
		var message map[string]any
		select {
		case message = <-s.inbox:
		case <-ctx.Done():
			if turnID := s.ActiveTurnID(); turnID != "" {
				interruptCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_, _ = s.client.call(interruptCtx, "turn/interrupt", map[string]any{
					"threadId": threadID, "turnId": turnID,
				})
				cancel()
			}
			out <- &core.Event{Type: core.EventError, Err: ctx.Err()}
			return
		case <-s.client.done:
			out <- &core.Event{Type: core.EventError, Err: s.withStderr(fmt.Errorf("codex app-server stopped"))}
			return
		}
		if method, _ := message["method"].(string); method != "" {
			if id, ok := rpcID(message); ok {
				if interaction, ok := s.captureServerInteraction(id, method, message); ok {
					out <- &core.Event{
						Type:        core.EventPermission,
						TurnID:      interaction.TurnID,
						ItemID:      interaction.ItemID,
						Interaction: interaction,
					}
				} else {
					// Unknown client-side capabilities are always declined so a
					// newer app-server cannot leave a channel turn blocked.
					_ = s.respondToServerRequest(id, method)
				}
				continue
			}
			params, _ := message["params"].(map[string]any)
			if turnID := codexTurnID(params); turnID != "" {
				s.setActiveTurnID(turnID)
			}
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

func (s *codexSession) nameFreshThread(ctx context.Context, text string) {
	s.mu.Lock()
	if !s.freshThread || s.threadNamed {
		s.mu.Unlock()
		return
	}
	s.threadNamed = true
	threadID := s.threadID
	s.mu.Unlock()
	title := strings.TrimSpace(strings.SplitN(text, "\n", 2)[0])
	title = strings.Join(strings.Fields(title), " ")
	runes := []rune(title)
	if len(runes) > 80 {
		title = string(runes[:80])
	}
	if title == "" {
		return
	}
	nameCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	_, _ = s.client.call(nameCtx, "thread/name/set", map[string]any{"threadId": threadID, "name": title})
	cancel()
}

func (s *codexSession) turnStartParams(threadID, text string) map[string]any {
	return s.turnStartParamsInput(threadID, core.AgentTurnInput{Text: text})
}

func (s *codexSession) turnStartParamsInput(threadID string, input core.AgentTurnInput) map[string]any {
	turnItems := []map[string]string{{"type": "text", "text": input.Text}}
	for _, attachment := range input.Attachments {
		if attachment.Kind != "image" {
			continue
		}
		if attachment.Path != "" {
			turnItems = append(turnItems, map[string]string{"type": "localImage", "path": attachment.Path})
		} else if attachment.URL != "" {
			turnItems = append(turnItems, map[string]string{"type": "image", "url": attachment.URL})
		}
	}
	params := map[string]any{
		"threadId": threadID,
		"input":    turnItems,
		// This asks Codex for its supported reasoning summary, never its hidden
		// chain-of-thought. The summary is safe to render as progress to users.
		"summary": "auto",
	}
	if len(input.OutputSchema) > 0 {
		params["outputSchema"] = input.OutputSchema
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
	if settings.ApprovalMode != "" {
		// These overrides are sticky for following turns. Send the ordinary
		// reviewer explicitly so switching away from auto review really resets it.
		params["approvalsReviewer"] = "user"
	}
	switch settings.ApprovalMode {
	case core.ApprovalModeManual:
		params["approvalPolicy"] = "on-request"
		params["sandbox"] = "readOnly"
	case core.ApprovalModeAutoEdit:
		params["approvalPolicy"] = "on-request"
		params["sandbox"] = "workspaceWrite"
	case core.ApprovalModeAuto:
		params["approvalPolicy"] = "on-request"
		params["sandbox"] = "workspaceWrite"
		params["approvalsReviewer"] = "auto_review"
	case core.ApprovalModePlan:
		params["approvalPolicy"] = "never"
		params["sandbox"] = "readOnly"
	case core.ApprovalModeYolo:
		params["approvalPolicy"] = "never"
		params["sandbox"] = "dangerFullAccess"
	}
	return params
}

func (s *codexSession) ActiveTurnID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeTurnID
}

func (s *codexSession) setActiveTurnID(turnID string) {
	if turnID == "" {
		return
	}
	s.mu.Lock()
	s.activeTurnID = turnID
	s.mu.Unlock()
}

func (s *codexSession) Steer(ctx context.Context, text string) error {
	turnID := s.ActiveTurnID()
	if turnID == "" {
		return fmt.Errorf("codex turn is not active")
	}
	return s.controlCall(ctx, "turn/steer", map[string]any{
		"threadId":       s.NativeSessionID(),
		"expectedTurnId": turnID,
		"input":          []map[string]string{{"type": "text", "text": text}},
	})
}

func (s *codexSession) Interrupt(ctx context.Context) error {
	turnID := s.ActiveTurnID()
	if turnID == "" {
		return fmt.Errorf("codex turn is not active")
	}
	return s.controlCall(ctx, "turn/interrupt", map[string]any{
		"threadId": s.NativeSessionID(),
		"turnId":   turnID,
	})
}

func (s *codexSession) controlCall(ctx context.Context, method string, params any) error {
	s.mu.Lock()
	if s.closed || !s.activeTurn {
		s.mu.Unlock()
		return fmt.Errorf("codex turn is not active")
	}
	s.mu.Unlock()
	_, err := s.client.call(ctx, method, params)
	return err
}

func (s *codexSession) RespondPermission(ctx context.Context, allow bool) error {
	s.mu.Lock()
	var id string
	for candidate := range s.pendingInteractions {
		id = candidate
		break
	}
	s.mu.Unlock()
	if id == "" {
		return fmt.Errorf("no pending Codex permission request")
	}
	decision := "decline"
	if allow {
		decision = "accept"
	}
	return s.ResolveInteraction(ctx, id, core.AgentInteractionResponse{Decision: decision})
}

func (s *codexSession) ResolveInteraction(_ context.Context, interactionID string, response core.AgentInteractionResponse) error {
	s.mu.Lock()
	pending, ok := s.pendingInteractions[interactionID]
	if ok {
		delete(s.pendingInteractions, interactionID)
	}
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("Codex interaction %q is not pending", interactionID)
	}
	result, err := codexInteractionResult(pending.method, pending.params, response)
	if err != nil {
		return err
	}
	return s.writeMessage(map[string]any{"jsonrpc": "2.0", "id": pending.rpcID, "result": result})
}

func (s *codexSession) Close(context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	threadID := s.threadID
	client := s.client
	s.mu.Unlock()
	if client != nil {
		client.unregister(threadID, s)
	}
	return nil
}

func (s *codexSession) call(ctx context.Context, method string, params any) (map[string]any, error) {
	if s.client == nil {
		return nil, fmt.Errorf("codex app-server is unavailable")
	}
	return s.client.call(ctx, method, params)
}

func (s *codexSession) respondToServerRequest(id int, method string) error {
	result := any(map[string]string{"decision": "decline"})
	switch method {
	case "item/permissions/requestApproval", "permissions/requestApproval":
		result = map[string]any{"permissions": map[string]any{}, "scope": "turn"}
	case "item/tool/requestUserInput":
		result = map[string]any{"answers": map[string]any{}}
	}
	return s.writeMessage(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

type codexPendingInteraction struct {
	rpcID  int
	method string
	params map[string]any
}

func (s *codexSession) captureServerInteraction(id int, method string, message map[string]any) (*core.AgentInteraction, bool) {
	params, _ := message["params"].(map[string]any)
	kind := core.AgentInteractionKind("")
	title := ""
	switch method {
	case "item/commandExecution/requestApproval":
		kind = core.AgentInteractionCommandApproval
		title = "命令执行审批"
	case "item/fileChange/requestApproval":
		kind = core.AgentInteractionFileChangeApproval
		title = "文件修改审批"
	case "item/permissions/requestApproval", "permissions/requestApproval":
		kind = core.AgentInteractionPermissionApproval
		title = "权限申请"
	case "item/tool/requestUserInput":
		kind = core.AgentInteractionUserInput
		title = "需要补充信息"
	default:
		return nil, false
	}
	threadID := firstString(params, "threadId")
	turnID := firstString(params, "turnId")
	itemID := firstString(params, "itemId")
	prefix := s.interactionPrefix
	if prefix == "" {
		prefix = "session"
	}
	interactionID := fmt.Sprintf("codex-%s-%s-%d", prefix, threadID, id)
	interaction := &core.AgentInteraction{
		ID:        interactionID,
		Kind:      kind,
		ThreadID:  threadID,
		TurnID:    turnID,
		ItemID:    itemID,
		Title:     title,
		Command:   firstString(params, "command"),
		Cwd:       firstString(params, "cwd"),
		Reason:    firstString(params, "reason"),
		RawParams: params,
	}
	interaction.HighRisk = codexHighRiskInteraction(interaction)
	if kind == core.AgentInteractionPermissionApproval {
		interaction.Description = codexValue(params["permissions"])
	}
	if kind == core.AgentInteractionUserInput {
		interaction.AutoResolutionMs = int64(numberValue(params["autoResolutionMs"]))
		interaction.Questions = codexInteractionQuestions(params["questions"])
	}
	s.mu.Lock()
	s.pendingInteractions[interactionID] = codexPendingInteraction{rpcID: id, method: method, params: params}
	s.mu.Unlock()
	return interaction, true
}

func codexInteractionQuestions(raw any) []core.InteractionQuestion {
	values, _ := raw.([]any)
	out := make([]core.InteractionQuestion, 0, len(values))
	for _, value := range values {
		question, _ := value.(map[string]any)
		item := core.InteractionQuestion{
			ID:       firstString(question, "id"),
			Header:   firstString(question, "header"),
			Question: firstString(question, "question"),
			Secret:   boolValue(question["isSecret"]),
			Other:    boolValue(question["isOther"]),
		}
		options, _ := question["options"].([]any)
		for _, rawOption := range options {
			option, _ := rawOption.(map[string]any)
			if label := firstString(option, "label"); label != "" {
				item.Options = append(item.Options, core.InteractionOption{
					Label: label, Description: firstString(option, "description"),
				})
			}
		}
		if item.ID != "" && item.Question != "" {
			out = append(out, item)
		}
	}
	return out
}

func codexInteractionResult(method string, params map[string]any, response core.AgentInteractionResponse) (any, error) {
	decision := strings.TrimSpace(response.Decision)
	switch method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		switch decision {
		case "accept", "acceptForSession", "decline", "cancel":
		default:
			return nil, fmt.Errorf("unsupported Codex approval decision %q", decision)
		}
		if decision == "acceptForSession" {
			interaction := &core.AgentInteraction{
				Kind:     core.AgentInteractionCommandApproval,
				Command:  firstString(params, "command"),
				Cwd:      firstString(params, "cwd"),
				HighRisk: false,
			}
			if method == "item/fileChange/requestApproval" {
				interaction.Kind = core.AgentInteractionFileChangeApproval
				interaction.Reason = firstString(params, "reason")
				interaction.RawParams = params
			}
			if codexHighRiskInteraction(interaction) {
				return nil, fmt.Errorf("high-risk actions cannot be approved for the session")
			}
		}
		return map[string]any{"decision": decision}, nil
	case "item/permissions/requestApproval", "permissions/requestApproval":
		if decision == "decline" || decision == "cancel" || decision == "" {
			return map[string]any{"permissions": map[string]any{}, "scope": "turn"}, nil
		}
		if decision != "accept" && decision != "acceptForSession" {
			return nil, fmt.Errorf("unsupported Codex permission decision %q", decision)
		}
		scope := "turn"
		if decision == "acceptForSession" {
			scope = "session"
		}
		return map[string]any{"permissions": params["permissions"], "scope": scope}, nil
	case "item/tool/requestUserInput":
		answers := map[string]any{}
		for _, question := range codexInteractionQuestions(params["questions"]) {
			values := response.Answers[question.ID]
			answers[question.ID] = map[string]any{"answers": values}
		}
		return map[string]any{"answers": answers}, nil
	default:
		return nil, fmt.Errorf("unsupported Codex interaction method %q", method)
	}
}

func codexHighRiskInteraction(interaction *core.AgentInteraction) bool {
	if interaction == nil {
		return false
	}
	if interaction.Kind == core.AgentInteractionPermissionApproval {
		return true
	}
	if interaction.Kind == core.AgentInteractionFileChangeApproval {
		reason := strings.ToLower(interaction.Reason)
		raw := strings.ToLower(codexValue(interaction.RawParams))
		return strings.Contains(reason, "outside") || strings.Contains(reason, "root") ||
			strings.Contains(reason, "grant") || strings.Contains(reason, "delete") ||
			strings.Contains(reason, "remove") || strings.Contains(raw, `"delete"`) ||
			strings.Contains(raw, `"deleted"`) || strings.Contains(raw, `"remove"`)
	}
	command := strings.ToLower(strings.TrimSpace(interaction.Command))
	for _, marker := range []string{
		"git commit", "git push", "gh pr create", "deploy", "publish", "release",
		"rm ", "rmdir ", "git clean", "git reset --hard", "sudo ", "curl ", "wget ", "ssh ", "scp ", "mail ", "send ",
	} {
		if strings.Contains(command, marker) {
			return true
		}
	}
	return false
}

func codexTurnID(params map[string]any) string {
	if id := firstString(params, "turnId"); id != "" {
		return id
	}
	return firstString(nestedMap(params, "turn"), "id", "turnId")
}

func (s *codexSession) writeMessage(message map[string]any) error {
	if s.client == nil {
		return fmt.Errorf("codex app-server is unavailable")
	}
	return s.client.writeMessage(message)
}

func (s *codexSession) withStderr(err error) error {
	if s.client == nil {
		return err
	}
	return s.client.withStderr(err)
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

func boolValue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	default:
		return false
	}
}

func numberValue(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case json.Number:
		n, _ := typed.Float64()
		return n
	default:
		return 0
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
