package core

import (
	"context"
	"strconv"
	"strings"
	"time"
)

type observationTraceparentContextKey struct{}
type observationChildTelemetryContextKey struct{}

// ObservationChildTelemetry is private, per-process OTLP configuration for an
// AgentNexus-launched runtime. It is never written to a host's shared config.
type ObservationChildTelemetry struct {
	Endpoint       string
	Token          string
	CaptureContent bool
	TraceID        string
	ParentSpanID   string
	TurnID         string
	SessionID      string
	AgentID        string
}

// ObservationTraceparent returns the W3C trace context assigned by the common
// Send decorator. Adapters that launch a child process should forward it only
// to that process rather than mutating the user's global environment.
func ObservationTraceparent(ctx context.Context) string {
	value, _ := ctx.Value(observationTraceparentContextKey{}).(string)
	return value
}

func ObservationChildTelemetryFromContext(ctx context.Context) ObservationChildTelemetry {
	value, _ := ctx.Value(observationChildTelemetryContextKey{}).(ObservationChildTelemetry)
	return value
}

func WithObservationChildTelemetry(ctx context.Context, telemetry ObservationChildTelemetry) context.Context {
	return context.WithValue(ctx, observationChildTelemetryContextKey{}, telemetry)
}

// UsageRecordSink prices and persists one real-time, request-level usage
// delta. Returning the priced cost lets the trace and Usage materialization
// share one source of truth.
type UsageRecordSink func(context.Context, UsageRecord) (float64, error)

type observedModelSpan struct {
	spanID    string
	requestID string
	attempt   int
	started   time.Time
	closed    bool
	assigned  bool
	lastUsage ObservationUsage
}

type observedToolSpan struct {
	spanID    string
	callID    string
	name      string
	started   time.Time
	closed    bool
	inputSeen bool
}

type observedTurn struct {
	engine     *Engine
	ctx        context.Context
	data       map[string]string
	traceID    string
	turnID     string
	rootSpanID string
	runSpanID  string
	sequence   int64
	started    time.Time
	models     map[string]*observedModelSpan
	tools      map[string]*observedToolSpan
	defaultMod *observedModelSpan
	lastModel  *observedModelSpan
	usage      ObservationUsage
	answer     string
	thinking   string
	firstErr   error
	quality    string
}

// SetObservationBus attaches the shared multi-subscriber bus. Hook completion
// spans are wired at the same time so shell/HTTP hooks join their parent turn.
func (e *Engine) SetObservationBus(bus *ObservationBus) {
	e.observationMu.Lock()
	e.observations = bus
	e.observationMu.Unlock()
	if e.hooks != nil {
		e.hooks.SetObserver(e.observeHookRun)
	}
}

func (e *Engine) ObservationBus() *ObservationBus {
	e.observationMu.RLock()
	defer e.observationMu.RUnlock()
	return e.observations
}

func (e *Engine) SetUsageSink(sink UsageRecordSink) {
	e.observationMu.Lock()
	e.usageSink = sink
	e.observationMu.Unlock()
}

func (e *Engine) SetObservationChildTelemetry(telemetry ObservationChildTelemetry) {
	e.observationMu.Lock()
	e.childTelemetry = telemetry
	e.observationMu.Unlock()
}

func (e *Engine) observationChildTelemetry() ObservationChildTelemetry {
	e.observationMu.RLock()
	defer e.observationMu.RUnlock()
	return e.childTelemetry
}

func (e *Engine) observationBackends() (*ObservationBus, UsageRecordSink) {
	e.observationMu.RLock()
	defer e.observationMu.RUnlock()
	return e.observations, e.usageSink
}

func ensureObservationData(data map[string]string) {
	if data == nil {
		return
	}
	if data["trace_id"] == "" {
		data["trace_id"] = NewObservationTraceID()
	}
	if data["turn_id"] == "" {
		data["turn_id"] = "turn_" + NewObservationEventID()
	}
	if data["root_span_id"] == "" {
		data["root_span_id"] = NewObservationSpanID()
	}
	if data["agent_run_span_id"] == "" {
		data["agent_run_span_id"] = NewObservationSpanID()
	}
}

func (e *Engine) publishObservation(ctx context.Context, envelope ObservationEnvelope) {
	bus, _ := e.observationBackends()
	if bus == nil {
		return
	}
	publishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := bus.Publish(publishCtx, envelope); err != nil {
		e.log.Warn("publish observation", "kind", envelope.Kind, "trace_id", envelope.TraceID, "err", err)
	}
}

func (e *Engine) observeLifecycle(ctx context.Context, event HookEvent, data map[string]string) {
	bus, _ := e.observationBackends()
	if bus == nil || data == nil {
		return
	}
	ensureObservationData(data)
	kind := "lifecycle." + string(event)
	name := string(event)
	switch event {
	case HookMessageReceived:
		kind = "channel.receive"
	case HookMessageSent:
		kind = "channel.reply"
	case HookSessionStarted, HookSessionEnded:
		kind = "agent.session"
	case HookPermission:
		kind = "permission"
	case HookCronTriggered, HookWebhookTriggered:
		kind = "agent.trigger"
	case HookError:
		kind = "agent.error"
	}
	status := ObservationStatusOK
	var observationErr *ObservationError
	var content *ObservationContent
	if event == HookError {
		status = ObservationStatusError
		observationErr = &ObservationError{Code: "runtime_error", Message: "Agent runtime error"}
		if raw := data["error"]; raw != "" {
			content = &ObservationContent{ContentType: "text/plain; charset=utf-8", Data: []byte(raw)}
		}
	}
	e.publishObservation(ctx, ObservationEnvelope{
		TraceID: data["trace_id"], SpanID: NewObservationSpanID(), ParentSpanID: data["root_span_id"],
		DedupeKey: lifecycleDedupeKey(event, data), Kind: kind, Name: name,
		Lifecycle: ObservationLifecycleEvent, AgentID: data["agent_id"], AgentName: data["agent_name"],
		RuntimeID: data["runtime_id"], ConversationID: data["conversation_id"], SessionID: data["session_id"],
		TurnID: data["turn_id"], Source: "agentnexus.internal", Provenance: []string{"engine", "lifecycle"},
		Quality: ObservationQualityComplete, Status: status, Error: observationErr,
		Attributes: safeObservationAttributes(data), Content: content,
	})
}

func lifecycleDedupeKey(event HookEvent, data map[string]string) string {
	return strings.Join([]string{"engine", data["trace_id"], string(event), data["message_id"], data["trigger_id"]}, ":")
}

func (e *Engine) observeHookRun(run HookRun) {
	data := run.Data
	if data == nil || data["trace_id"] == "" {
		return
	}
	status := ObservationStatusOK
	var observationErr *ObservationError
	var content *ObservationContent
	if run.Err != nil {
		status = ObservationStatusError
		observationErr = &ObservationError{Code: "hook_failed", Message: "Hook execution failed"}
		content = &ObservationContent{ContentType: "text/plain; charset=utf-8", Data: []byte(run.Err.Error())}
	}
	e.publishObservation(context.Background(), ObservationEnvelope{
		Time: run.StartedAt.Add(run.Duration), TraceID: data["trace_id"], SpanID: NewObservationSpanID(),
		ParentSpanID: data["root_span_id"], Kind: "hook.run", Name: string(run.Event), Lifecycle: ObservationLifecycleEnd,
		AgentID: data["agent_id"], AgentName: data["agent_name"], RuntimeID: data["runtime_id"],
		ConversationID: data["conversation_id"], SessionID: data["session_id"], TurnID: data["turn_id"],
		Source: "agentnexus.hook", Provenance: []string{"engine", "config_hook", run.Type}, Quality: ObservationQualityComplete,
		Status: status, Error: observationErr, Attributes: map[string]any{
			"hook_event": string(run.Event), "hook_type": run.Type, "duration_ms": run.Duration.Milliseconds(),
		}, Content: content,
	})
}

// observeSend is the common AgentSession.Send decorator used by project,
// dynamic channel, cron, webhook and config-backed executions.
func (e *Engine) observeSend(ctx context.Context, sess AgentSession, text string, data map[string]string) (<-chan *Event, error) {
	bus, _ := e.observationBackends()
	if bus == nil {
		return sess.Send(ctx, text)
	}
	ensureObservationData(data)
	if data["session_id"] == "" {
		data["session_id"] = sessionObservationID(sess)
	}
	turn := &observedTurn{
		engine: e, ctx: ctx, data: cloneHookData(data), traceID: data["trace_id"], turnID: data["turn_id"],
		rootSpanID: data["root_span_id"], runSpanID: data["agent_run_span_id"], started: time.Now().UTC(),
		models: map[string]*observedModelSpan{}, tools: map[string]*observedToolSpan{}, quality: ObservationQualityInferred,
	}
	turn.start(text)
	traceparent := "00-" + turn.traceID + "-" + turn.runSpanID + "-01"
	sendCtx := context.WithValue(ctx, observationTraceparentContextKey{}, traceparent)
	telemetry := e.observationChildTelemetry()
	if telemetry.Endpoint != "" && telemetry.Token != "" {
		telemetry.TraceID = turn.traceID
		telemetry.ParentSpanID = turn.runSpanID
		telemetry.TurnID = turn.turnID
		telemetry.SessionID = turn.data["session_id"]
		telemetry.AgentID = turn.data["agent_id"]
		sendCtx = WithObservationChildTelemetry(sendCtx, telemetry)
	}
	events, err := sess.Send(sendCtx, text)
	if err != nil {
		turn.firstErr = err
		turn.finish(sess)
		return nil, err
	}
	out := make(chan *Event, 32)
	go func() {
		defer close(out)
		for event := range events {
			if event == nil {
				continue
			}
			turn.observeEvent(event)
			select {
			case out <- event:
			case <-ctx.Done():
				turn.firstErr = ctx.Err()
				turn.finish(sess)
				return
			}
		}
		turn.finish(sess)
	}()
	return out, nil
}

func (t *observedTurn) nextSequence() int64 {
	t.sequence++
	return t.sequence
}

func (t *observedTurn) base(spanID, parentID, kind, name, lifecycle, status string) ObservationEnvelope {
	return ObservationEnvelope{
		Sequence: t.nextSequence(), TraceID: t.traceID, SpanID: spanID, ParentSpanID: parentID,
		Kind: kind, Name: name, Lifecycle: lifecycle,
		AgentID: t.data["agent_id"], AgentName: t.data["agent_name"], RuntimeID: t.data["runtime_id"],
		ConversationID: t.data["conversation_id"], SessionID: t.data["session_id"], TurnID: t.turnID,
		Source: "agentnexus.internal", Provenance: []string{"engine", "agent_session"},
		Quality: t.quality, Status: status,
	}
}

func (t *observedTurn) start(prompt string) {
	root := t.base(t.rootSpanID, "", "agent.turn", "Agent turn", ObservationLifecycleStart, ObservationStatusRunning)
	root.DedupeKey = "turn:" + t.traceID + ":start"
	root.Attributes = safeObservationAttributes(t.data)
	root.Content = &ObservationContent{ContentType: "text/plain; charset=utf-8", Data: []byte(prompt)}
	t.engine.publishObservation(t.ctx, root)
	run := t.base(t.runSpanID, t.rootSpanID, "agent.run", "Agent run", ObservationLifecycleStart, ObservationStatusRunning)
	run.DedupeKey = "run:" + t.traceID + ":start"
	t.engine.publishObservation(t.ctx, run)
	// Every turn gets at least one model span. Native adapter events upgrade its
	// inferred quality and attach exact request/model identifiers.
	t.defaultMod = &observedModelSpan{spanID: NewObservationSpanID(), started: t.started}
	t.lastModel = t.defaultMod
	t.models["__default__"] = t.defaultMod
	model := t.base(t.defaultMod.spanID, t.runSpanID, "model.request", "Model request", ObservationLifecycleStart, ObservationStatusRunning)
	model.Quality = ObservationQualityInferred
	model.DedupeKey = "model:" + t.traceID + ":default:start"
	t.engine.publishObservation(t.ctx, model)
}

func (t *observedTurn) observeEvent(event *Event) {
	quality := adapterObservationQuality(event)
	if quality == ObservationQualityComplete || (quality == ObservationQualityPartial && t.quality == ObservationQualityInferred) {
		t.quality = quality
	}
	switch event.Type {
	case EventModelRequest:
		t.observeModelRequest(event)
	case EventModelResponse:
		t.observeModelResponse(event)
	case EventToolUse:
		t.observeTool(event)
	case EventThinking:
		t.thinking = event.Text
		if event.Text != "" {
			envelope := t.base(NewObservationSpanID(), t.runSpanID, "agent.reasoning.summary", "Public reasoning summary", ObservationLifecycleEvent, ObservationStatusRunning)
			envelope.Quality = adapterObservationQuality(event)
			envelope.DedupeKey = adapterEventDedupe(t, event, "reasoning")
			envelope.Content = &ObservationContent{ContentType: "text/plain; charset=utf-8", Data: []byte(event.Text)}
			t.engine.publishObservation(t.ctx, envelope)
		}
	case EventCompaction:
		envelope := t.base(NewObservationSpanID(), t.runSpanID, "compaction", "Context compaction", observationLifecycle(event), observationStatus(event))
		envelope.DedupeKey = adapterEventDedupe(t, event, "compaction")
		envelope.Attributes = eventObservationAttributes(event)
		t.engine.publishObservation(t.ctx, envelope)
	case EventPermission:
		envelope := t.base(NewObservationSpanID(), t.runSpanID, "permission", "Permission request", ObservationLifecycleEvent, ObservationStatusRunning)
		envelope.DedupeKey = adapterEventDedupe(t, event, "permission")
		t.engine.publishObservation(t.ctx, envelope)
	case EventOutput, EventFinal:
		if event.Text != "" && event.Text != "NO_REPLY" {
			t.answer = event.Text
		}
	case EventError:
		if t.firstErr == nil {
			t.firstErr = event.Err
		}
	}
}

func (t *observedTurn) modelKey(event *Event) string {
	requestID := ""
	attempt := 1
	if event.Usage != nil {
		requestID = event.Usage.RequestID
		if event.Usage.Attempt > 0 {
			attempt = event.Usage.Attempt
		}
	}
	if requestID == "" {
		requestID = event.ItemID
	}
	if requestID == "" {
		return "__default__"
	}
	return requestID + ":" + strconv.Itoa(attempt)
}

func (t *observedTurn) ensureModel(event *Event) (*observedModelSpan, bool) {
	key := t.modelKey(event)
	if model := t.models[key]; model != nil {
		return model, false
	}
	if !t.defaultMod.assigned && !t.defaultMod.closed {
		t.defaultMod.assigned = true
		if event.Usage != nil {
			t.defaultMod.requestID = event.Usage.RequestID
			t.defaultMod.attempt = event.Usage.Attempt
		}
		t.models[key] = t.defaultMod
		return t.defaultMod, false
	}
	model := &observedModelSpan{spanID: NewObservationSpanID(), started: time.Now().UTC(), assigned: true}
	if event.Usage != nil {
		model.requestID = event.Usage.RequestID
		model.attempt = event.Usage.Attempt
	}
	t.models[key] = model
	t.lastModel = model
	return model, true
}

func (t *observedTurn) observeModelRequest(event *Event) {
	model, created := t.ensureModel(event)
	t.lastModel = model
	lifecycle := ObservationLifecycleEvent
	if created {
		lifecycle = ObservationLifecycleStart
	}
	envelope := t.base(model.spanID, t.runSpanID, "model.request", "Model request", lifecycle, observationStatus(event))
	envelope.Quality = adapterObservationQuality(event)
	envelope.DedupeKey = adapterEventDedupe(t, event, "model_request")
	envelope.Model = observationModel(event)
	envelope.Attributes = eventObservationAttributes(event)
	t.engine.publishObservation(t.ctx, envelope)
}

func (t *observedTurn) observeModelResponse(event *Event) {
	model, created := t.ensureModel(event)
	t.lastModel = model
	if created {
		start := t.base(model.spanID, t.runSpanID, "model.request", "Model request", ObservationLifecycleStart, ObservationStatusRunning)
		start.Quality = ObservationQualityInferred
		start.Model = observationModel(event)
		start.DedupeKey = adapterEventDedupe(t, event, "model_inferred_start")
		t.engine.publishObservation(t.ctx, start)
	}
	delta := t.addUsage(model, event.Usage)
	status := observationStatus(event)
	lifecycle := ObservationLifecycleEvent
	publishedUsage := delta
	if status == ObservationStatusOK || status == ObservationStatusError || status == ObservationStatusCancelled {
		lifecycle = ObservationLifecycleEnd
		model.closed = true
		// Persist one final request record, not every cumulative update. The
		// latter would either inflate usage or cause an early partial row to win
		// the request-id dedupe key.
		publishedUsage = model.lastUsage
		if cost := t.recordUsage(event, publishedUsage); cost != 0 {
			publishedUsage.CostUSD = cost
			t.usage.CostUSD += cost
		}
	}
	envelope := t.base(model.spanID, t.runSpanID, "model.request", "Model request", lifecycle, status)
	envelope.Quality = adapterObservationQuality(event)
	envelope.DedupeKey = adapterEventDedupe(t, event, "model_response")
	envelope.Model = observationModel(event)
	envelope.Usage = observationUsagePointer(publishedUsage)
	envelope.Attributes = eventObservationAttributes(event)
	if event.Err != nil {
		envelope.Error = &ObservationError{Code: "model_request_failed", Message: "Model request failed", Retryable: event.Metadata["will_retry"] == "true"}
		envelope.Content = &ObservationContent{ContentType: "text/plain; charset=utf-8", Data: []byte(event.Err.Error())}
	}
	t.engine.publishObservation(t.ctx, envelope)
}

func (t *observedTurn) addUsage(model *observedModelSpan, usage *TurnUsage) ObservationUsage {
	if usage == nil {
		return ObservationUsage{}
	}
	current := observationUsageFromTurn(usage)
	delta := current
	if usage.Cumulative {
		delta = subtractObservationUsage(current, model.lastUsage)
		model.lastUsage = current
	} else {
		model.lastUsage = addObservationUsage(model.lastUsage, current)
	}
	t.usage = addObservationUsage(t.usage, delta)
	return delta
}

func (t *observedTurn) recordUsage(event *Event, usage ObservationUsage) float64 {
	_, sink := t.engine.observationBackends()
	if sink == nil || usage.TotalTokens == 0 {
		return 0
	}
	model := ""
	requestID := ""
	if event.Usage != nil {
		model = firstNonEmpty(event.Usage.ResolvedModel, event.Usage.Model, event.Usage.RequestedModel)
		requestID = event.Usage.RequestID
		if requestID != "" && event.Usage.Attempt > 0 {
			requestID += ":attempt:" + strconv.Itoa(event.Usage.Attempt)
		}
	}
	record := UsageRecord{
		Source:    normalizeObservationUsageSource(t.data["runtime_id"], event.Metadata["runtime"], t.data["agent_name"]),
		SessionID: t.data["session_id"], Project: firstNonEmpty(t.data["project"], t.data["channel_id"]),
		Model: model, Timestamp: time.Now().UTC(), InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
		CacheReadTokens: usage.CacheReadTokens, CacheWriteTokens: usage.CacheWriteTokens,
		TraceID: t.traceID, TurnID: t.turnID, ConversationID: t.data["conversation_id"], RequestID: requestID,
		RuntimeID: t.data["runtime_id"],
	}
	usageCtx, cancel := context.WithTimeout(context.WithoutCancel(t.ctx), 2*time.Second)
	defer cancel()
	cost, err := sink(usageCtx, record)
	if err != nil {
		t.engine.log.Warn("record real-time usage", "trace_id", t.traceID, "err", err)
	}
	return cost
}

func (t *observedTurn) observeTool(event *Event) {
	callID := firstNonEmpty(event.ToolCallID, event.ItemID, event.ToolUse)
	if callID == "" {
		callID = "tool_" + NewObservationEventID()
	}
	tool := t.tools[callID]
	starts := event.ToolName != "" && tool == nil
	if starts {
		tool = &observedToolSpan{spanID: NewObservationSpanID(), callID: callID, name: event.ToolName, started: time.Now().UTC()}
		t.tools[callID] = tool
	}
	lateInput := event.ToolName != "" && tool != nil && !tool.inputSeen
	recordedInput := starts || lateInput
	if recordedInput {
		tool.inputSeen = true
		if tool.name == "" || tool.name == "unknown tool" {
			tool.name = event.ToolName
		}
		lifecycle, status := ObservationLifecycleStart, ObservationStatusRunning
		if !starts {
			lifecycle = ObservationLifecycleEvent
			if tool.closed {
				status = ObservationStatusOK
			}
		}
		envelope := t.base(tool.spanID, t.runSpanID, "tool.call", tool.name, lifecycle, status)
		envelope.Quality = adapterObservationQuality(event)
		envelope.DedupeKey = adapterEventDedupe(t, event, "tool_start")
		envelope.Tool = &ObservationTool{Name: tool.name, CallID: callID, InputBytes: int64(len(event.ToolInputRaw))}
		envelope.Attributes = eventObservationAttributes(event)
		input := firstNonEmpty(event.ToolInputRaw, event.ToolInput)
		if input != "" {
			envelope.Content = &ObservationContent{ContentType: "application/json", Data: []byte(input)}
		}
		t.engine.publishObservation(t.ctx, envelope)
	}
	completed := event.Status == "completed" || event.Status == "failed" || event.Err != nil || event.ToolResult != "" || event.ToolResultRaw != ""
	if !completed {
		if recordedInput {
			return
		}
		if tool != nil && !starts {
			envelope := t.base(tool.spanID, t.runSpanID, "tool.call", tool.name, ObservationLifecycleEvent, ObservationStatusRunning)
			envelope.DedupeKey = adapterEventDedupe(t, event, "tool_update")
			envelope.Tool = &ObservationTool{Name: tool.name, CallID: callID}
			envelope.Attributes = eventObservationAttributes(event)
			t.engine.publishObservation(t.ctx, envelope)
		}
		return
	}
	if tool == nil {
		name := event.ToolName
		if name == "" {
			name = "unknown tool"
		}
		tool = &observedToolSpan{spanID: NewObservationSpanID(), callID: callID, name: name, started: time.Now().UTC(), closed: false}
		t.tools[callID] = tool
		start := t.base(tool.spanID, t.runSpanID, "tool.call", name, ObservationLifecycleStart, ObservationStatusRunning)
		start.Quality = ObservationQualityInferred
		start.Tool = &ObservationTool{Name: name, CallID: callID}
		t.engine.publishObservation(t.ctx, start)
	}
	tool.closed = true
	status := ObservationStatusOK
	if event.Err != nil || event.Status == "failed" {
		status = ObservationStatusError
	}
	envelope := t.base(tool.spanID, t.runSpanID, "tool.call", tool.name, ObservationLifecycleEnd, status)
	envelope.Quality = adapterObservationQuality(event)
	envelope.DedupeKey = adapterEventDedupe(t, event, "tool_end")
	duration := event.DurationMs
	if duration == 0 {
		duration = time.Since(tool.started).Milliseconds()
	}
	result := firstNonEmpty(event.ToolResultRaw, event.ToolResult)
	envelope.Tool = &ObservationTool{Name: tool.name, CallID: callID, DurationMillis: duration, OutputBytes: int64(len(result))}
	envelope.Attributes = eventObservationAttributes(event)
	if event.Err != nil {
		envelope.Error = &ObservationError{Code: "tool_failed", Message: "Tool call failed"}
	}
	if result != "" {
		envelope.Content = &ObservationContent{ContentType: "application/json", Data: []byte(result)}
	}
	t.engine.publishObservation(t.ctx, envelope)
}

func (t *observedTurn) finish(sess AgentSession) {
	for _, tool := range t.tools {
		if tool.closed {
			continue
		}
		tool.closed = true
		envelope := t.base(tool.spanID, t.runSpanID, "tool.call", tool.name, ObservationLifecycleEnd, ObservationStatusCancelled)
		envelope.Tool = &ObservationTool{Name: tool.name, CallID: tool.callID, DurationMillis: time.Since(tool.started).Milliseconds()}
		t.engine.publishObservation(t.ctx, envelope)
	}
	status := ObservationStatusOK
	if t.firstErr != nil {
		status = ObservationStatusError
	} else if t.ctx.Err() != nil {
		status = ObservationStatusCancelled
	}
	seen := map[string]bool{}
	for _, model := range t.models {
		if model == nil || seen[model.spanID] || model.closed {
			continue
		}
		seen[model.spanID] = true
		model.closed = true
		envelope := t.base(model.spanID, t.runSpanID, "model.request", "Model request", ObservationLifecycleEnd, status)
		envelope.Quality = ObservationQualityInferred
		envelope.Model = &ObservationModel{RequestID: model.requestID, Attempt: model.attempt, DurationMillis: time.Since(model.started).Milliseconds()}
		t.engine.publishObservation(t.ctx, envelope)
	}
	if nativeID := sessionObservationID(sess); nativeID != "" {
		t.data["session_id"] = nativeID
	}
	run := t.base(t.runSpanID, t.rootSpanID, "agent.run", "Agent run", ObservationLifecycleEnd, status)
	run.Usage = observationUsagePointer(t.usage)
	run.DedupeKey = "run:" + t.traceID + ":end"
	t.engine.publishObservation(t.ctx, run)
	root := t.base(t.rootSpanID, "", "agent.turn", "Agent turn", ObservationLifecycleEnd, status)
	root.Usage = observationUsagePointer(t.usage)
	root.DedupeKey = "turn:" + t.traceID + ":end"
	root.Attributes = safeObservationAttributes(t.data)
	root.Attributes["duration_ms"] = time.Since(t.started).Milliseconds()
	if t.firstErr != nil {
		root.Error = &ObservationError{Code: "agent_turn_failed", Message: "Agent turn failed"}
		root.Content = &ObservationContent{ContentType: "text/plain; charset=utf-8", Data: []byte(t.firstErr.Error())}
	} else if t.answer != "" {
		root.Content = &ObservationContent{ContentType: "text/plain; charset=utf-8", Data: []byte(t.answer)}
	}
	t.engine.publishObservation(t.ctx, root)
}

func observationUsageFromTurn(usage *TurnUsage) ObservationUsage {
	if usage == nil {
		return ObservationUsage{}
	}
	total := usage.TotalTokens
	if total == 0 {
		total = usage.InputTokens + usage.OutputTokens + usage.CacheReadTokens + usage.CacheWriteTokens
	}
	return ObservationUsage{
		InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens,
		CacheReadTokens: usage.CacheReadTokens, CacheWriteTokens: usage.CacheWriteTokens,
		ReasoningTokens: usage.ReasoningTokens, TotalTokens: total, Cumulative: usage.Cumulative,
	}
}

func observationUsagePointer(usage ObservationUsage) *ObservationUsage {
	if usage.InputTokens == 0 && usage.OutputTokens == 0 && usage.CacheReadTokens == 0 && usage.CacheWriteTokens == 0 && usage.TotalTokens == 0 {
		return nil
	}
	usage.Cumulative = true
	return &usage
}

func addObservationUsage(left, right ObservationUsage) ObservationUsage {
	return ObservationUsage{
		InputTokens: left.InputTokens + right.InputTokens, OutputTokens: left.OutputTokens + right.OutputTokens,
		CacheReadTokens: left.CacheReadTokens + right.CacheReadTokens, CacheWriteTokens: left.CacheWriteTokens + right.CacheWriteTokens,
		ReasoningTokens: left.ReasoningTokens + right.ReasoningTokens, ToolTokens: left.ToolTokens + right.ToolTokens,
		TotalTokens: left.TotalTokens + right.TotalTokens, CostUSD: left.CostUSD + right.CostUSD, Cumulative: true,
	}
}

func subtractObservationUsage(current, previous ObservationUsage) ObservationUsage {
	return ObservationUsage{
		InputTokens:      nonNegative(current.InputTokens - previous.InputTokens),
		OutputTokens:     nonNegative(current.OutputTokens - previous.OutputTokens),
		CacheReadTokens:  nonNegative(current.CacheReadTokens - previous.CacheReadTokens),
		CacheWriteTokens: nonNegative(current.CacheWriteTokens - previous.CacheWriteTokens),
		ReasoningTokens:  nonNegative(current.ReasoningTokens - previous.ReasoningTokens),
		ToolTokens:       nonNegative(current.ToolTokens - previous.ToolTokens),
		TotalTokens:      nonNegative(current.TotalTokens - previous.TotalTokens),
		CostUSD:          current.CostUSD - previous.CostUSD,
	}
}

func nonNegative(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func observationModel(event *Event) *ObservationModel {
	if event == nil || event.Usage == nil {
		return nil
	}
	usage := event.Usage
	return &ObservationModel{
		Provider:  firstNonEmpty(event.Metadata["provider"], event.Metadata["gen_ai.system"]),
		Requested: firstNonEmpty(usage.RequestedModel, usage.Model), Resolved: firstNonEmpty(usage.ResolvedModel, usage.Model),
		RequestID: usage.RequestID, Attempt: usage.Attempt, TTFTMillis: usage.TTFTMs,
		DurationMillis: firstPositive(usage.DurationMs, event.DurationMs),
		Protocol:       event.Metadata["protocol"], ReasoningEffort: event.Metadata["reasoning_effort"],
		ServiceTier: event.Metadata["service_tier"], FinishReason: event.Metadata["finish_reason"],
	}
}

func observationStatus(event *Event) string {
	if event == nil {
		return ObservationStatusUnset
	}
	if event.Err != nil || event.Status == "failed" || event.Status == "error" {
		return ObservationStatusError
	}
	switch event.Status {
	case "completed", "ok", "success":
		return ObservationStatusOK
	case "cancelled", "canceled", "aborted":
		return ObservationStatusCancelled
	case "in_progress", "running", "retrying", "rerouted", "":
		return ObservationStatusRunning
	default:
		return event.Status
	}
}

func observationLifecycle(event *Event) string {
	status := observationStatus(event)
	if status == ObservationStatusOK || status == ObservationStatusError || status == ObservationStatusCancelled {
		return ObservationLifecycleEnd
	}
	if event != nil && event.Metadata["lifecycle"] == "started" {
		return ObservationLifecycleStart
	}
	return ObservationLifecycleEvent
}

func adapterObservationQuality(event *Event) string {
	if event != nil && event.Metadata["coverage"] == "partial" {
		return ObservationQualityPartial
	}
	return ObservationQualityComplete
}

func adapterEventDedupe(turn *observedTurn, event *Event, suffix string) string {
	id := ""
	if event != nil {
		id = firstNonEmpty(event.EventID, event.ItemID, event.ToolCallID)
	}
	if id == "" {
		return ""
	}
	return strings.Join([]string{"adapter", turn.data["runtime_id"], turn.data["session_id"], id, suffix}, ":")
}

func eventObservationAttributes(event *Event) map[string]any {
	attributes := map[string]any{}
	if event == nil {
		return attributes
	}
	for key, value := range event.Metadata {
		if isSensitiveObservationKey(key) {
			continue
		}
		attributes[key] = value
	}
	if event.ItemID != "" {
		attributes["item_id"] = event.ItemID
	}
	if event.EventID != "" {
		attributes["native_event_id"] = event.EventID
	}
	if event.TurnID != "" {
		attributes["native_turn_id"] = event.TurnID
	}
	if event.DurationMs > 0 {
		attributes["duration_ms"] = event.DurationMs
	}
	return attributes
}

func safeObservationAttributes(data map[string]string) map[string]any {
	attributes := make(map[string]any, len(data))
	for key, value := range data {
		if value == "" || key == "text" || key == "error" || isSensitiveObservationKey(key) {
			continue
		}
		attributes[key] = value
	}
	return attributes
}

func isSensitiveObservationKey(key string) bool {
	key = strings.ToLower(key)
	return strings.Contains(key, "authorization") || strings.Contains(key, "cookie") || strings.Contains(key, "api_key") ||
		strings.Contains(key, "apikey") || strings.Contains(key, "secret") || strings.Contains(key, "password") || strings.HasSuffix(key, "token")
}

func normalizeObservationUsageSource(values ...string) string {
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		switch {
		case strings.Contains(value, "claude"):
			return "claude"
		case strings.Contains(value, "codex"):
			return "codex"
		case value != "":
			return value
		}
	}
	return "agentnexus"
}

func sessionObservationID(session AgentSession) string {
	if native, ok := session.(NativeSessioned); ok {
		if id := native.NativeSessionID(); id != "" {
			return id
		}
	}
	if session == nil {
		return ""
	}
	return session.ID()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func firstPositive(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}
