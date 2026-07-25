package observability

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/hookrelay"
	"github.com/wangning19940904/AgentMux/store"
)

type hookTraceContext struct {
	traceID string
	rootID  string
	turnID  string
}

type sessionTraceContext struct {
	traceID      string
	parentSpanID string
	turnID       string
	updatedAt    time.Time
}

type IngestService struct {
	log        *slog.Logger
	bus        *core.ObservationBus
	socketPath string
	spoolDir   string
	keyPath    string
	token      string

	mu       sync.Mutex
	listener net.Listener
	traces   map[string]hookTraceContext
	aliases  map[string]string
	sessions map[string]sessionTraceContext
}

func NewIngestService(log *slog.Logger, bus *core.ObservationBus, home, token string) *IngestService {
	if log == nil {
		log = slog.Default()
	}
	opts := hookrelay.DefaultOptions(home)
	return &IngestService{
		log: log, bus: bus, socketPath: opts.SocketPath, spoolDir: opts.SpoolDir,
		keyPath: opts.KeyPath, token: token, traces: map[string]hookTraceContext{}, aliases: map[string]string{}, sessions: map[string]sessionTraceContext{},
	}
}

// ObserveCorrelation remembers the current AgentMux turn for a native
// session. Long-lived runtimes such as Codex app-server cannot change process
// OTel resource attributes for every Send, so their asynchronously exported
// spans are joined through the stable thread/session ID instead.
func (s *IngestService) ObserveCorrelation(_ context.Context, envelope core.ObservationEnvelope) error {
	if s == nil || envelope.Source != "agentmux.internal" || envelope.SessionID == "" || envelope.TraceID == "" {
		return nil
	}
	if envelope.Kind != "agent.turn" && envelope.Kind != "agent.run" && envelope.Kind != "subagent.run" {
		return nil
	}
	key := observationSessionKey(envelope.RuntimeID, envelope.SessionID)
	s.mu.Lock()
	if len(s.sessions) > 10000 {
		cutoff := time.Now().UTC().Add(-24 * time.Hour)
		for existing, value := range s.sessions {
			if value.updatedAt.Before(cutoff) {
				delete(s.sessions, existing)
			}
		}
	}
	current := s.sessions[key]
	if envelope.Kind == "agent.turn" || current.traceID == "" || current.traceID != envelope.TraceID {
		current = sessionTraceContext{traceID: envelope.TraceID, parentSpanID: envelope.SpanID, turnID: envelope.TurnID}
	}
	if envelope.Kind == "agent.run" || envelope.Kind == "subagent.run" {
		current.parentSpanID = envelope.SpanID
	}
	current.updatedAt = time.Now().UTC()
	s.sessions[key] = current
	s.mu.Unlock()
	return nil
}

func (s *IngestService) traceForSession(runtimeID, sessionID string) sessionTraceContext {
	if s == nil || sessionID == "" {
		return sessionTraceContext{}
	}
	s.mu.Lock()
	value := s.sessions[observationSessionKey(runtimeID, sessionID)]
	s.mu.Unlock()
	return value
}

func observationSessionKey(runtimeID, sessionID string) string {
	runtimeID = strings.ToLower(strings.TrimSpace(runtimeID))
	switch {
	case strings.Contains(runtimeID, "codex"):
		runtimeID = "codex"
	case strings.Contains(runtimeID, "claude"):
		runtimeID = "claude"
	}
	return runtimeID + ":" + strings.TrimSpace(sessionID)
}

func (s *IngestService) registerTraceAlias(nativeTraceID, parentTraceID string) {
	if nativeTraceID == "" || parentTraceID == "" || nativeTraceID == parentTraceID {
		return
	}
	s.mu.Lock()
	if len(s.aliases) > 10000 {
		s.aliases = map[string]string{}
	}
	s.aliases[nativeTraceID] = parentTraceID
	s.mu.Unlock()
}

func (s *IngestService) ResolveTraceID(traceID string) string {
	if s == nil || traceID == "" {
		return traceID
	}
	s.mu.Lock()
	resolved := s.aliases[traceID]
	s.mu.Unlock()
	if resolved != "" {
		return resolved
	}
	return traceID
}

func (s *IngestService) Start(ctx context.Context) error {
	if s == nil || s.bus == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.socketPath), 0o700); err != nil {
		return err
	}
	if info, err := os.Lstat(s.socketPath); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("observability socket path is occupied by a non-socket: %s", s.socketPath)
		}
		conn, dialErr := net.DialTimeout("unix", s.socketPath, 50*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			return fmt.Errorf("observability socket is already owned by another process: %s", s.socketPath)
		}
		if err := os.Remove(s.socketPath); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	listener, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return err
	}
	if err := os.Chmod(s.socketPath, 0o600); err != nil {
		_ = listener.Close()
		return err
	}
	s.mu.Lock()
	s.listener = listener
	s.mu.Unlock()
	go func() {
		<-ctx.Done()
		s.mu.Lock()
		if s.listener != nil {
			_ = s.listener.Close()
		}
		s.listener = nil
		s.mu.Unlock()
		_ = os.Remove(s.socketPath)
	}()
	go s.accept(ctx, listener)
	go s.consumeSpoolLoop(ctx)
	return nil
}

func (s *IngestService) accept(ctx context.Context, listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() == nil {
				s.log.Warn("observation socket accept", "err", err)
			}
			return
		}
		go func() {
			defer conn.Close()
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			scanner := bufio.NewScanner(io.LimitReader(conn, 64<<20))
			scanner.Buffer(make([]byte, 0, 64<<10), 64<<20)
			for scanner.Scan() {
				if err := s.ingestWire(ctx, scanner.Bytes()); err != nil {
					s.log.Warn("ingest native hook", "err", err)
				}
			}
		}()
	}
}

func (s *IngestService) consumeSpoolLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		_ = s.ConsumeSpool(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *IngestService) ConsumeSpool(ctx context.Context) error {
	entries, err := os.ReadDir(s.spoolDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".amuxspool") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	var errs []error
	for _, name := range names {
		path := filepath.Join(s.spoolDir, name)
		processing := path + ".processing"
		if err := os.Rename(path, processing); err != nil {
			continue
		}
		wire, err := hookrelay.DecryptSpool(processing, s.keyPath)
		if err == nil {
			err = s.ingestWire(ctx, wire)
		}
		if err != nil {
			_ = os.Rename(processing, path)
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		_ = os.Remove(processing)
	}
	return errors.Join(errs...)
}

func (s *IngestService) ingestWire(ctx context.Context, wire []byte) error {
	wire = []byte(strings.TrimSpace(string(wire)))
	if len(wire) == 0 {
		return nil
	}
	var envelopeWire struct {
		core.ObservationEnvelope
		Content     json.RawMessage `json:"content,omitempty"`
		ContentType string          `json:"content_type,omitempty"`
	}
	if json.Unmarshal(wire, &envelopeWire) == nil && envelopeWire.Version == core.ObservationEnvelopeVersion && envelopeWire.Kind != "" {
		envelope := envelopeWire.ObservationEnvelope
		if len(envelopeWire.Content) > 0 && string(envelopeWire.Content) != "null" {
			contentType := envelopeWire.ContentType
			if contentType == "" {
				contentType = "application/json"
			}
			envelope.Content = &core.ObservationContent{ContentType: contentType, Data: envelopeWire.Content}
		}
		return s.bus.Publish(ctx, envelope)
	}
	var message hookrelay.Message
	if err := json.Unmarshal(wire, &message); err != nil {
		return err
	}
	if message.Version != 1 {
		return fmt.Errorf("unsupported hook relay version %d", message.Version)
	}
	envelope, err := s.hookEnvelope(message)
	if err != nil {
		return err
	}
	return s.bus.Publish(ctx, envelope)
}

func (s *IngestService) hookEnvelope(message hookrelay.Message) (core.ObservationEnvelope, error) {
	var payload map[string]any
	if err := json.Unmarshal(message.Payload, &payload); err != nil {
		return core.ObservationEnvelope{}, err
	}
	event := firstMapString(payload, "hook_event_name", "event", "hookEventName", "type")
	sessionID := firstMapString(payload, "session_id", "sessionId", "thread_id", "threadId")
	if sessionID == "" {
		sessionID = "unknown"
	}
	correlation := s.traceForSession(message.Source, sessionID)
	joinedTurn := correlation.traceID != ""
	key := message.Source + ":" + sessionID
	s.mu.Lock()
	trace := s.traces[key]
	if trace.traceID == "" || event == "UserPromptSubmit" {
		if joinedTurn {
			trace = hookTraceContext{traceID: correlation.traceID, rootID: correlation.parentSpanID, turnID: correlation.turnID}
		} else {
			trace = hookTraceContext{traceID: core.NewObservationTraceID(), rootID: core.NewObservationSpanID(), turnID: "turn_" + core.NewObservationEventID()}
		}
		s.traces[key] = trace
	}
	if event == "SessionEnd" {
		delete(s.traces, key)
	}
	s.mu.Unlock()
	name, kind, lifecycle, status := event, "hook.event", core.ObservationLifecycleEvent, core.ObservationStatusOK
	spanID := core.NewObservationSpanID()
	parentID := trace.rootID
	toolName := firstMapString(payload, "tool_name", "toolName")
	callID := firstMapString(payload, "tool_use_id", "toolUseId", "tool_call_id", "toolCallId")
	var tool *core.ObservationTool
	var contentValue any
	switch event {
	case "UserPromptSubmit":
		if joinedTurn {
			kind, name, lifecycle, status = "hook.run", "UserPromptSubmit", core.ObservationLifecycleEvent, core.ObservationStatusOK
		} else {
			kind, name, lifecycle, status = "agent.turn", "Agent turn", core.ObservationLifecycleStart, core.ObservationStatusRunning
			spanID, parentID = trace.rootID, ""
		}
		contentValue = firstMapValue(payload, "prompt", "user_prompt", "input")
	case "PreToolUse":
		kind, name, lifecycle, status = "tool.call", toolName, core.ObservationLifecycleStart, core.ObservationStatusRunning
		spanID = stableHookSpanID(trace.traceID, callID)
		tool = &core.ObservationTool{Name: toolName, CallID: callID}
		contentValue = firstMapValue(payload, "tool_input", "toolInput")
	case "PostToolUse", "PostToolUseFailure":
		kind, name, lifecycle = "tool.call", toolName, core.ObservationLifecycleEnd
		spanID = stableHookSpanID(trace.traceID, callID)
		tool = &core.ObservationTool{Name: toolName, CallID: callID}
		contentValue = firstMapValue(payload, "tool_response", "tool_result", "toolResponse")
		if event == "PostToolUseFailure" {
			status = core.ObservationStatusError
		}
	case "PermissionRequest":
		kind, name, status = "permission", "Permission request", core.ObservationStatusRunning
	case "PreCompact":
		kind, name, lifecycle, status = "compaction", "Context compaction", core.ObservationLifecycleStart, core.ObservationStatusRunning
	case "Stop":
		if joinedTurn {
			kind, name, lifecycle = "hook.run", "Stop", core.ObservationLifecycleEvent
		} else {
			kind, name, lifecycle = "agent.turn", "Agent turn", core.ObservationLifecycleEnd
			spanID, parentID = trace.rootID, ""
		}
		contentValue = firstMapValue(payload, "response", "result", "last_assistant_message")
	case "SubagentStart":
		kind, name, lifecycle, status = "subagent.run", "Subagent run", core.ObservationLifecycleStart, core.ObservationStatusRunning
	case "SubagentStop":
		kind, name, lifecycle = "subagent.run", "Subagent run", core.ObservationLifecycleEnd
	case "SessionStart":
		kind, name, lifecycle, status = "agent.session", "Session", core.ObservationLifecycleStart, core.ObservationStatusRunning
	case "SessionEnd":
		kind, name, lifecycle = "agent.session", "Session", core.ObservationLifecycleEnd
	}
	content := hookContent(contentValue)
	digest := sha256.Sum256(message.Payload)
	return core.ObservationEnvelope{
		Time: message.ReceivedAt, TraceID: trace.traceID, SpanID: spanID, ParentSpanID: parentID,
		DedupeKey: "hook:" + message.Source + ":" + hex.EncodeToString(digest[:]), Kind: kind, Name: name, Lifecycle: lifecycle,
		RuntimeID: message.Source, SessionID: sessionID, TurnID: trace.turnID, Source: "hook." + message.Source,
		Provenance: []string{"native_plugin", "hook"}, Quality: core.ObservationQualityPartial, Status: status,
		Tool: tool, Attributes: safeHookAttributes(payload), Content: content,
	}, nil
}

func (s *IngestService) HandleHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	wire, err := io.ReadAll(io.LimitReader(r.Body, (64<<20)+1))
	if err != nil || len(wire) > 64<<20 {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	if err := s.ingestWire(r.Context(), wire); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func LoadOrCreateIngestToken(home string) (string, error) {
	path := filepath.Join(home, ".agentmux", "observability", "ingest.token")
	if raw, err := os.ReadFile(path); err == nil {
		if token := strings.TrimSpace(string(raw)); token != "" {
			return token, nil
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	token := hex.EncodeToString(random)
	if err := store.AtomicWrite(path, []byte(token+"\n"), 0o600); err != nil {
		return "", err
	}
	return token, nil
}

func stableHookSpanID(traceID, callID string) string {
	if callID == "" {
		return core.NewObservationSpanID()
	}
	digest := sha256.Sum256([]byte(traceID + "\x00" + callID))
	return hex.EncodeToString(digest[:8])
}

func hookContent(value any) *core.ObservationContent {
	if value == nil {
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil || string(raw) == "null" || string(raw) == `""` {
		return nil
	}
	return &core.ObservationContent{ContentType: "application/json", Data: raw}
}

func safeHookAttributes(payload map[string]any) map[string]any {
	attributes := map[string]any{}
	for _, key := range []string{"cwd", "permission_mode", "transcript_path", "agent_id", "agent_type", "source"} {
		if value, ok := payload[key]; ok {
			attributes[key] = value
		}
	}
	return attributes
}

func firstMapString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstMapValue(values map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := values[key]; ok && value != nil {
			return value
		}
	}
	return nil
}
