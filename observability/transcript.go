package observability

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/agentnexus/agentnexus/core"
	"github.com/agentnexus/agentnexus/store"
)

const (
	defaultTranscriptPollInterval     = 5 * time.Second
	defaultTranscriptContentBackfill  = 30 * 24 * time.Hour
	defaultTranscriptMetadataBackfill = 180 * 24 * time.Hour
	maxTranscriptLineBytes            = 64 << 20
)

// TranscriptTailerOptions controls local transcript discovery and retention.
// Home makes tests and embedders independent from the process user's HOME;
// ClaudeHome and CodexHome override the two runtime-specific roots.
type TranscriptTailerOptions struct {
	Home             string
	ClaudeHome       string
	CodexHome        string
	PollInterval     time.Duration
	ContentBackfill  time.Duration
	MetadataBackfill time.Duration
	Now              func() time.Time
}

type TranscriptScanResult struct {
	FilesDiscovered int `json:"files_discovered"`
	FilesRead       int `json:"files_read"`
	LinesRead       int `json:"lines_read"`
	EventsPublished int `json:"events_published"`
	OldLinesSkipped int `json:"old_lines_skipped"`
}

// TranscriptTailer incrementally backfills Claude Code and Codex JSONL files.
// Cursor state is durable; delivery is at-least-once and every emitted event
// carries a stable dedupe key so recorder replay cannot inflate trace usage.
type TranscriptTailer struct {
	log              *slog.Logger
	store            *store.Store
	bus              *core.ObservationBus
	claudeHome       string
	codexHome        string
	pollInterval     time.Duration
	contentBackfill  time.Duration
	metadataBackfill time.Duration
	now              func() time.Time
	scanMu           sync.Mutex
}

type transcriptFile struct {
	Runtime string
	Class   string
	Path    string
}

type transcriptCursorState struct {
	SessionID string            `json:"session_id,omitempty"`
	TurnID    string            `json:"turn_id,omitempty"`
	TraceID   string            `json:"trace_id,omitempty"`
	RootSpan  string            `json:"root_span,omitempty"`
	AgentID   string            `json:"agent_id,omitempty"`
	Cwd       string            `json:"cwd,omitempty"`
	Model     string            `json:"model,omitempty"`
	Previous  codexTokenNumbers `json:"previous_usage,omitempty"`
	HaveUsage bool              `json:"have_usage,omitempty"`
}

func NewTranscriptTailer(log *slog.Logger, st *store.Store, bus *core.ObservationBus, options TranscriptTailerOptions) *TranscriptTailer {
	if log == nil {
		log = slog.Default()
	}
	home := strings.TrimSpace(options.Home)
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	claudeHome := strings.TrimSpace(options.ClaudeHome)
	if claudeHome == "" {
		claudeHome = filepath.Join(home, ".claude")
	}
	codexHome := strings.TrimSpace(options.CodexHome)
	if codexHome == "" {
		if options.Home == "" {
			codexHome = strings.TrimSpace(os.Getenv("CODEX_HOME"))
		}
		if codexHome == "" {
			codexHome = filepath.Join(home, ".codex")
		}
	}
	if options.PollInterval <= 0 {
		options.PollInterval = defaultTranscriptPollInterval
	}
	if options.ContentBackfill <= 0 {
		options.ContentBackfill = defaultTranscriptContentBackfill
	}
	if options.MetadataBackfill <= 0 {
		options.MetadataBackfill = defaultTranscriptMetadataBackfill
	}
	if options.MetadataBackfill < options.ContentBackfill {
		options.MetadataBackfill = options.ContentBackfill
	}
	if options.Now == nil {
		options.Now = func() time.Time { return time.Now().UTC() }
	}
	return &TranscriptTailer{
		log: log, store: st, bus: bus, claudeHome: claudeHome, codexHome: codexHome,
		pollInterval: options.PollInterval, contentBackfill: options.ContentBackfill,
		metadataBackfill: options.MetadataBackfill, now: options.Now,
	}
}

// Start performs an immediate backfill and then follows newly appended lines.
// An individual malformed file never stops the rest of the scan.
func (t *TranscriptTailer) Start(ctx context.Context) {
	if t == nil || t.store == nil || t.bus == nil {
		return
	}
	if _, err := t.Scan(ctx); err != nil && ctx.Err() == nil {
		t.log.Warn("initial transcript backfill", "err", err)
	}
	ticker := time.NewTicker(t.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := t.Scan(ctx); err != nil && ctx.Err() == nil {
				t.log.Warn("tail transcripts", "err", err)
			}
		}
	}
}

func (t *TranscriptTailer) Scan(ctx context.Context) (TranscriptScanResult, error) {
	var result TranscriptScanResult
	if t == nil || t.store == nil || t.bus == nil {
		return result, nil
	}
	t.scanMu.Lock()
	defer t.scanMu.Unlock()
	files, err := t.discover()
	if err != nil {
		return result, err
	}
	result.FilesDiscovered = len(files)
	var errs []error
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return result, errors.Join(append(errs, err)...)
		}
		partial, err := t.scanFile(ctx, file)
		result.FilesRead += partial.FilesRead
		result.LinesRead += partial.LinesRead
		result.EventsPublished += partial.EventsPublished
		result.OldLinesSkipped += partial.OldLinesSkipped
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", file.Path, err))
		}
	}
	return result, errors.Join(errs...)
}

func (t *TranscriptTailer) discover() ([]transcriptFile, error) {
	var files []transcriptFile
	var errs []error
	walk := func(root, runtime, class string, accept func(string, os.DirEntry) bool) {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) || os.IsPermission(err) {
					return nil
				}
				return err
			}
			if entry.IsDir() || !accept(path, entry) {
				return nil
			}
			itemClass := class
			if runtime == "claude" {
				itemClass = claudeTranscriptClass(path)
			}
			absolute, absErr := filepath.Abs(path)
			if absErr != nil {
				return absErr
			}
			files = append(files, transcriptFile{Runtime: runtime, Class: itemClass, Path: filepath.Clean(absolute)})
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	walk(filepath.Join(t.claudeHome, "projects"), "claude", "main", func(path string, _ os.DirEntry) bool {
		return strings.HasSuffix(strings.ToLower(path), ".jsonl")
	})
	acceptCodex := func(_ string, entry os.DirEntry) bool {
		name := strings.ToLower(entry.Name())
		return strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, ".jsonl")
	}
	walk(filepath.Join(t.codexHome, "sessions"), "codex", "active", acceptCodex)
	walk(filepath.Join(t.codexHome, "archived_sessions"), "codex", "archived", acceptCodex)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, errors.Join(errs...)
}

func claudeTranscriptClass(path string) string {
	normalized := filepath.ToSlash(path)
	if strings.Contains(normalized, "/subagents/workflows/") {
		if strings.EqualFold(filepath.Base(path), "journal.jsonl") {
			return "workflow"
		}
		return "workflow_subagent"
	}
	if strings.Contains(normalized, "/subagents/") {
		return "subagent"
	}
	return "main"
}

func (t *TranscriptTailer) scanFile(ctx context.Context, item transcriptFile) (TranscriptScanResult, error) {
	var result TranscriptScanResult
	file, err := os.Open(item.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return result, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return result, err
	}
	result.FilesRead = 1
	identity, err := transcriptFileIdentity(file)
	if err != nil {
		return result, err
	}
	resource := item.Path
	cursor, err := t.store.GetObservationIngestCursor(ctx, "transcript:"+item.Runtime, resource)
	if err != nil {
		return result, err
	}
	state := transcriptCursorState{}
	offset := int64(0)
	lastMessageID := ""
	if cursor != nil {
		_ = json.Unmarshal([]byte(cursor.Cursor), &state)
		offset = cursor.ByteOffset
		lastMessageID = cursor.MessageID
	}
	reset := cursor != nil && (cursor.FileIdentity != identity || info.Size() < offset)
	if reset {
		resumeOffset, found, findErr := findTranscriptMessage(file, item.Runtime, lastMessageID)
		if findErr != nil {
			return result, findErr
		}
		if found {
			offset = resumeOffset
		} else {
			offset = 0
			state = transcriptCursorState{}
		}
	}
	if offset < 0 || offset > info.Size() {
		offset = 0
		state = transcriptCursorState{}
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return result, err
	}
	reader := bufio.NewReaderSize(file, 256<<10)
	currentOffset := offset
	observedAt := time.Time{}
	for {
		lineStart := currentOffset
		line, readErr := reader.ReadBytes('\n')
		if len(line) > maxTranscriptLineBytes {
			return result, fmt.Errorf("transcript line exceeds %d bytes", maxTranscriptLineBytes)
		}
		if readErr == io.EOF && len(line) > 0 {
			// Do not consume a partial JSONL record. The next scan will resume at
			// its first byte after the writer appends the terminating newline.
			break
		}
		if readErr != nil && readErr != io.EOF {
			return result, readErr
		}
		if len(line) == 0 {
			break
		}
		currentOffset += int64(len(line))
		result.LinesRead++
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		var envelopes []core.ObservationEnvelope
		var messageID string
		var timestamp time.Time
		if item.Runtime == "claude" {
			envelopes, messageID, timestamp = t.parseClaudeLine(item, trimmed, lineStart, info.ModTime(), &state)
		} else {
			envelopes, messageID, timestamp = t.parseCodexLine(item, trimmed, lineStart, info.ModTime(), &state)
		}
		if !timestamp.IsZero() {
			observedAt = timestamp
		}
		if messageID != "" {
			lastMessageID = messageID
		}
		if timestamp.Before(t.now().UTC().Add(-t.metadataBackfill)) {
			result.OldLinesSkipped++
			continue
		}
		for _, envelope := range envelopes {
			if timestamp.Before(t.now().UTC().Add(-t.contentBackfill)) {
				envelope.Content = nil
				envelope.Attributes = cloneMap(envelope.Attributes)
				envelope.Attributes["content_retention"] = "metadata_only"
			}
			if err := t.bus.Publish(ctx, envelope); err != nil {
				return result, err
			}
			result.EventsPublished++
		}
	}
	encodedState, err := json.Marshal(state)
	if err != nil {
		return result, err
	}
	if observedAt.IsZero() {
		observedAt = info.ModTime().UTC()
	}
	if err := t.store.UpsertObservationIngestCursor(ctx, store.ObservationIngestCursor{
		Source: "transcript:" + item.Runtime, Resource: resource, Cursor: string(encodedState),
		MessageID: lastMessageID, FileIdentity: identity, ByteOffset: currentOffset,
		ObservedAt: observedAt, UpdatedAt: t.now().UTC(),
	}); err != nil {
		return result, err
	}
	return result, nil
}

func transcriptFileIdentity(file *os.File) (string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	reader := bufio.NewReaderSize(file, 64<<10)
	line, err := reader.ReadBytes('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	digest := sha256.Sum256(bytes.TrimSpace(line))
	return hex.EncodeToString(digest[:]), nil
}

func findTranscriptMessage(file *os.File, runtime, wanted string) (int64, bool, error) {
	if wanted == "" {
		return 0, false, nil
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return 0, false, err
	}
	reader := bufio.NewReaderSize(file, 256<<10)
	var offset int64
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > maxTranscriptLineBytes {
			return 0, false, fmt.Errorf("transcript line exceeds %d bytes", maxTranscriptLineBytes)
		}
		if err == io.EOF && len(line) > 0 {
			return 0, false, nil
		}
		if err != nil && err != io.EOF {
			return 0, false, err
		}
		if len(line) == 0 {
			return 0, false, nil
		}
		offset += int64(len(line))
		if transcriptMessageID(runtime, bytes.TrimSpace(line)) == wanted {
			return offset, true, nil
		}
	}
}

func transcriptMessageID(runtime string, line []byte) string {
	var raw map[string]any
	if json.Unmarshal(line, &raw) != nil {
		return ""
	}
	if runtime == "claude" {
		if id := mapString(raw, "uuid", "key"); id != "" {
			return id
		}
		if message := mapObject(raw, "message"); message != nil {
			if id := mapString(message, "id"); id != "" {
				return id
			}
		}
		return stableHex(string(line), 16)
	}
	payload := mapObject(raw, "payload")
	return firstNonBlank(mapString(payload, "id", "turn_id", "call_id", "client_id"), mapString(raw, "id"), stableHex(string(line), 16))
}

func (t *TranscriptTailer) parseClaudeLine(item transcriptFile, line []byte, offset int64, fallback time.Time, state *transcriptCursorState) ([]core.ObservationEnvelope, string, time.Time) {
	var raw map[string]any
	if json.Unmarshal(line, &raw) != nil {
		return nil, "", fallback.UTC()
	}
	timestamp := parseTranscriptTime(mapString(raw, "timestamp"), fallback)
	typeName := mapString(raw, "type")
	messageID := firstNonBlank(mapString(raw, "uuid", "key"), mapString(mapObject(raw, "message"), "id"))
	if messageID == "" {
		messageID = stableHex(string(line), 16)
	}
	if value := mapString(raw, "sessionId", "session_id"); value != "" {
		state.SessionID = value
	}
	if value := mapString(raw, "cwd"); value != "" {
		state.Cwd = value
	}
	if value := mapString(raw, "agentId", "agent_id"); value != "" {
		state.AgentID = value
	}
	if item.Class == "workflow" {
		return t.claudeWorkflowEvents(item, raw, messageID, timestamp, offset, state), messageID, timestamp
	}
	message := mapObject(raw, "message")
	role := firstNonBlank(mapString(message, "role"), typeName)
	if model := mapString(message, "model"); model != "" {
		state.Model = model
	}
	if typeName == "user" && !claudeOnlyToolResults(message["content"]) {
		turnID := firstNonBlank(mapString(raw, "promptId"), messageID)
		state.TurnID = turnID
		state.TraceID = stableHex("claude:trace:"+state.SessionID+":"+turnID+":"+state.AgentID, 16)
		state.RootSpan = stableHex("claude:root:"+state.TraceID, 8)
		kind, name := "agent.turn", "Claude turn"
		if item.Class != "main" {
			kind, name = "subagent.run", "Claude subagent"
		}
		envelope := t.baseTranscriptEnvelope(item, state, messageID, timestamp, offset, state.RootSpan, "", kind, name, core.ObservationLifecycleStart, core.ObservationStatusRunning)
		if content := claudePublicContent(message["content"], false); content != nil {
			envelope.Content = jsonObservationContent(content)
		}
		return []core.ObservationEnvelope{envelope}, messageID, timestamp
	}
	if state.TraceID == "" {
		state.TurnID = firstNonBlank(mapString(raw, "promptId"), "backfill-"+state.SessionID)
		state.TraceID = stableHex("claude:trace:"+state.SessionID+":"+state.TurnID+":"+state.AgentID, 16)
		state.RootSpan = stableHex("claude:root:"+state.TraceID, 8)
	}
	if typeName == "assistant" || role == "assistant" {
		return t.claudeAssistantEvents(item, raw, message, messageID, timestamp, offset, state), messageID, timestamp
	}
	if typeName == "user" {
		return t.claudeToolResultEvents(item, message, messageID, timestamp, offset, state), messageID, timestamp
	}
	return nil, messageID, timestamp
}

func (t *TranscriptTailer) claudeWorkflowEvents(item transcriptFile, raw map[string]any, messageID string, timestamp time.Time, offset int64, state *transcriptCursorState) []core.ObservationEnvelope {
	workflowID := filepath.Base(filepath.Dir(item.Path))
	agentID := mapString(raw, "agentId", "agent_id")
	state.AgentID = firstNonBlank(agentID, state.AgentID)
	state.SessionID = firstNonBlank(state.SessionID, workflowID)
	state.TurnID = workflowID
	state.TraceID = stableHex("claude:workflow:"+workflowID+":"+agentID, 16)
	state.RootSpan = stableHex("claude:workflow-root:"+state.TraceID, 8)
	lifecycle, status := core.ObservationLifecycleEvent, core.ObservationStatusUnset
	switch mapString(raw, "type") {
	case "started":
		lifecycle, status = core.ObservationLifecycleStart, core.ObservationStatusRunning
	case "result":
		lifecycle, status = core.ObservationLifecycleEnd, core.ObservationStatusOK
	case "failed", "error":
		lifecycle, status = core.ObservationLifecycleEnd, core.ObservationStatusError
	}
	envelope := t.baseTranscriptEnvelope(item, state, messageID, timestamp, offset, state.RootSpan, "", "subagent.run", "Claude workflow subagent", lifecycle, status)
	if lifecycle == core.ObservationLifecycleEnd {
		if result := raw["result"]; result != nil {
			envelope.Content = jsonObservationContent(result)
		}
	}
	return []core.ObservationEnvelope{envelope}
}

func (t *TranscriptTailer) claudeAssistantEvents(item transcriptFile, raw, message map[string]any, messageID string, timestamp time.Time, offset int64, state *transcriptCursorState) []core.ObservationEnvelope {
	requestID := firstNonBlank(mapString(raw, "requestId"), mapString(message, "id"), messageID)
	spanID := stableHex("claude:model:"+state.SessionID+":"+requestID, 8)
	stopReason := mapString(message, "stop_reason")
	lifecycle, status := core.ObservationLifecycleEvent, core.ObservationStatusRunning
	if stopReason != "" {
		lifecycle, status = core.ObservationLifecycleEnd, core.ObservationStatusOK
	}
	modelEvent := t.baseTranscriptEnvelope(item, state, messageID+":model", timestamp, offset, spanID, state.RootSpan, "model.request", "Claude model request", lifecycle, status)
	modelEvent.Model = &core.ObservationModel{Provider: "anthropic", Resolved: state.Model, RequestID: requestID, FinishReason: stopReason}
	modelEvent.Usage = claudeObservationUsage(mapObject(message, "usage"))
	if content := claudePublicContent(message["content"], true); content != nil {
		modelEvent.Content = jsonObservationContent(content)
	}
	events := []core.ObservationEnvelope{modelEvent}
	for index, block := range mapArray(message["content"]) {
		if mapString(block, "type") != "tool_use" {
			continue
		}
		callID := mapString(block, "id")
		name := mapString(block, "name")
		tool := t.baseTranscriptEnvelope(item, state, messageID+fmt.Sprintf(":tool:%d", index), timestamp, offset, stableHex("claude:tool:"+state.SessionID+":"+callID, 8), state.RootSpan, "tool.call", name, core.ObservationLifecycleStart, core.ObservationStatusRunning)
		input := block["input"]
		encoded, _ := json.Marshal(input)
		tool.Tool = &core.ObservationTool{Name: name, CallID: callID, InputBytes: int64(len(encoded))}
		if input != nil {
			tool.Content = jsonObservationContent(input)
		}
		events = append(events, tool)
	}
	return events
}

func (t *TranscriptTailer) claudeToolResultEvents(item transcriptFile, message map[string]any, messageID string, timestamp time.Time, offset int64, state *transcriptCursorState) []core.ObservationEnvelope {
	var events []core.ObservationEnvelope
	for index, block := range mapArray(message["content"]) {
		if mapString(block, "type") != "tool_result" {
			continue
		}
		callID := mapString(block, "tool_use_id")
		status := core.ObservationStatusOK
		if value, _ := block["is_error"].(bool); value {
			status = core.ObservationStatusError
		}
		content := block["content"]
		encoded, _ := json.Marshal(content)
		envelope := t.baseTranscriptEnvelope(item, state, messageID+fmt.Sprintf(":tool_result:%d", index), timestamp, offset, stableHex("claude:tool:"+state.SessionID+":"+callID, 8), state.RootSpan, "tool.call", "Claude tool", core.ObservationLifecycleEnd, status)
		envelope.Tool = &core.ObservationTool{CallID: callID, OutputBytes: int64(len(encoded))}
		if content != nil {
			envelope.Content = jsonObservationContent(content)
		}
		events = append(events, envelope)
	}
	return events
}

func (t *TranscriptTailer) parseCodexLine(item transcriptFile, line []byte, offset int64, fallback time.Time, state *transcriptCursorState) ([]core.ObservationEnvelope, string, time.Time) {
	var raw map[string]any
	if json.Unmarshal(line, &raw) != nil {
		return nil, "", fallback.UTC()
	}
	timestamp := parseTranscriptTime(mapString(raw, "timestamp"), fallback)
	typeName := mapString(raw, "type")
	payload := mapObject(raw, "payload")
	payloadType := mapString(payload, "type")
	messageID := firstNonBlank(mapString(payload, "id", "turn_id", "call_id", "client_id"), mapString(raw, "id"))
	if messageID == "" {
		messageID = stableHex(string(line), 16)
	}
	if typeName == "session_meta" {
		state.SessionID = firstNonBlank(mapString(payload, "id", "session_id"), state.SessionID, strings.TrimSuffix(filepath.Base(item.Path), ".jsonl"))
		state.Cwd = firstNonBlank(mapString(payload, "cwd"), state.Cwd)
		state.Model = firstNonBlank(mapString(payload, "model"), state.Model)
		return nil, messageID, timestamp
	}
	if model := firstNonBlank(mapString(payload, "model"), mapString(raw, "model")); model != "" {
		state.Model = model
	}
	if state.SessionID == "" {
		state.SessionID = strings.TrimSuffix(filepath.Base(item.Path), ".jsonl")
	}
	if (typeName == "event_msg" && payloadType == "task_started") || typeName == "turn_context" {
		turnID := firstNonBlank(mapString(payload, "turn_id"), messageID)
		return []core.ObservationEnvelope{t.startCodexTurn(item, state, turnID, messageID, timestamp, offset)}, messageID, timestamp
	}
	if state.TraceID == "" {
		turnID := firstNonBlank(mapString(payload, "turn_id"), "backfill-"+state.SessionID)
		t.startCodexTurn(item, state, turnID, messageID, timestamp, offset)
	}
	if tokenCount := mapObject(raw, "token_count"); tokenCount != nil {
		return t.codexTokenNumbersEvent(item, parseCodexTokens(tokenCount), codexTokenNumbers{}, messageID, messageID, timestamp, offset, state), messageID, timestamp
	}
	if typeName == "event_msg" && payloadType == "token_count" {
		return t.codexTokenEvent(item, payload, messageID, timestamp, offset, state), messageID, timestamp
	}
	if typeName == "event_msg" && payloadType == "user_message" {
		content := firstNonNil(payload["message"], payload["text"])
		if content == nil {
			return nil, messageID, timestamp
		}
		envelope := t.baseTranscriptEnvelope(item, state, messageID, timestamp, offset, stableHex("codex:input:"+state.SessionID+":"+messageID, 8), state.RootSpan, "agent.input", "Codex input", core.ObservationLifecycleEvent, core.ObservationStatusOK)
		envelope.Content = jsonObservationContent(content)
		return []core.ObservationEnvelope{envelope}, messageID, timestamp
	}
	if typeName == "event_msg" && (payloadType == "task_complete" || payloadType == "task_completed" || payloadType == "turn_completed") {
		status := core.ObservationStatusOK
		if mapString(payload, "status") == "failed" {
			status = core.ObservationStatusError
		}
		envelope := t.baseTranscriptEnvelope(item, state, messageID, timestamp, offset, state.RootSpan, "", "agent.turn", "Codex turn", core.ObservationLifecycleEnd, status)
		if response := firstNonNil(payload["last_agent_message"], payload["message"]); response != nil {
			envelope.Content = jsonObservationContent(response)
		}
		return []core.ObservationEnvelope{envelope}, messageID, timestamp
	}
	if typeName != "response_item" {
		return nil, messageID, timestamp
	}
	return t.codexResponseItemEvents(item, payload, messageID, timestamp, offset, state), messageID, timestamp
}

func (t *TranscriptTailer) startCodexTurn(item transcriptFile, state *transcriptCursorState, turnID, messageID string, timestamp time.Time, offset int64) core.ObservationEnvelope {
	state.TurnID = turnID
	state.TraceID = stableHex("codex:trace:"+state.SessionID+":"+turnID, 16)
	state.RootSpan = stableHex("codex:root:"+state.TraceID, 8)
	return t.baseTranscriptEnvelope(item, state, messageID, timestamp, offset, state.RootSpan, "", "agent.turn", "Codex turn", core.ObservationLifecycleStart, core.ObservationStatusRunning)
}

func (t *TranscriptTailer) codexTokenEvent(item transcriptFile, payload map[string]any, messageID string, timestamp time.Time, offset int64, state *transcriptCursorState) []core.ObservationEnvelope {
	info := mapObject(payload, "info")
	current := parseCodexTokens(mapObject(info, "total_token_usage"))
	if current.zero() {
		current = parseCodexTokens(mapObject(payload, "token_count"))
	}
	if current.zero() {
		return nil
	}
	return t.codexTokenNumbersEvent(item, current, parseCodexTokens(mapObject(info, "last_token_usage")), mapString(payload, "request_id"), messageID, timestamp, offset, state)
}

func (t *TranscriptTailer) codexTokenNumbersEvent(item transcriptFile, current, last codexTokenNumbers, requestID, messageID string, timestamp time.Time, offset int64, state *transcriptCursorState) []core.ObservationEnvelope {
	if current.zero() {
		return nil
	}
	delta, reset := current.delta(state.Previous, state.HaveUsage)
	if reset && !last.zero() {
		delta = last
	}
	state.Previous, state.HaveUsage = current, true
	if delta.zero() {
		return nil
	}
	// token_count is emitted after each request and contains session-cumulative
	// totals. Give each non-zero delta its own request span; using one span per
	// turn would cause the recorder's per-span MAX aggregation to drop deltas.
	requestID = firstNonBlank(requestID, messageID)
	spanID := stableHex("codex:model:"+state.SessionID+":"+requestID, 8)
	// token_count is emitted after one model request and its delta is final for
	// that request. Close the synthetic span so realtime Usage materialization
	// includes transcript-only Codex sessions.
	envelope := t.baseTranscriptEnvelope(item, state, messageID, timestamp, offset, spanID, state.RootSpan, "model.request", "Codex model request", core.ObservationLifecycleEnd, core.ObservationStatusOK)
	envelope.Model = &core.ObservationModel{Provider: "openai", Resolved: state.Model, RequestID: requestID}
	envelope.Usage = delta.observationUsage()
	return []core.ObservationEnvelope{envelope}
}

func (t *TranscriptTailer) codexResponseItemEvents(item transcriptFile, payload map[string]any, messageID string, timestamp time.Time, offset int64, state *transcriptCursorState) []core.ObservationEnvelope {
	switch mapString(payload, "type") {
	case "function_call", "custom_tool_call", "computer_call":
		callID := firstNonBlank(mapString(payload, "call_id"), mapString(payload, "id"), messageID)
		name := firstNonBlank(mapString(payload, "name"), mapString(payload, "action"), "Codex tool")
		input := firstNonNil(payload["arguments"], payload["input"], payload["action"])
		encoded, _ := json.Marshal(input)
		envelope := t.baseTranscriptEnvelope(item, state, messageID, timestamp, offset, stableHex("codex:tool:"+state.SessionID+":"+callID, 8), state.RootSpan, "tool.call", name, core.ObservationLifecycleStart, core.ObservationStatusRunning)
		envelope.Tool = &core.ObservationTool{Name: name, CallID: callID, InputBytes: int64(len(encoded))}
		if input != nil {
			envelope.Content = jsonObservationContent(input)
		}
		return []core.ObservationEnvelope{envelope}
	case "function_call_output", "custom_tool_call_output", "computer_call_output":
		callID := firstNonBlank(mapString(payload, "call_id"), mapString(payload, "id"), messageID)
		output := firstNonNil(payload["output"], payload["result"])
		encoded, _ := json.Marshal(output)
		status := core.ObservationStatusOK
		if mapString(payload, "status") == "failed" {
			status = core.ObservationStatusError
		}
		envelope := t.baseTranscriptEnvelope(item, state, messageID, timestamp, offset, stableHex("codex:tool:"+state.SessionID+":"+callID, 8), state.RootSpan, "tool.call", "Codex tool", core.ObservationLifecycleEnd, status)
		envelope.Tool = &core.ObservationTool{CallID: callID, OutputBytes: int64(len(encoded))}
		if output != nil {
			envelope.Content = jsonObservationContent(output)
		}
		return []core.ObservationEnvelope{envelope}
	case "message":
		role := mapString(payload, "role")
		if role != "assistant" && role != "user" {
			return nil
		}
		content := codexPublicMessageContent(payload["content"])
		if content == nil {
			return nil
		}
		kind, name := "model.response", "Codex response"
		if role == "user" {
			kind, name = "agent.input", "Codex input"
		}
		envelope := t.baseTranscriptEnvelope(item, state, messageID, timestamp, offset, stableHex("codex:item:"+state.SessionID+":"+messageID, 8), state.RootSpan, kind, name, core.ObservationLifecycleEvent, core.ObservationStatusOK)
		envelope.Content = jsonObservationContent(content)
		return []core.ObservationEnvelope{envelope}
	case "reasoning":
		// Hidden chain-of-thought and encrypted_content are intentionally
		// ignored. Only the runtime's explicitly public summary is retained.
		summary := payload["summary"]
		if summary == nil {
			return nil
		}
		envelope := t.baseTranscriptEnvelope(item, state, messageID, timestamp, offset, stableHex("codex:reasoning-summary:"+state.SessionID+":"+messageID, 8), state.RootSpan, "model.reasoning_summary", "Codex reasoning summary", core.ObservationLifecycleEvent, core.ObservationStatusOK)
		envelope.Content = jsonObservationContent(summary)
		return []core.ObservationEnvelope{envelope}
	}
	return nil
}

func (t *TranscriptTailer) baseTranscriptEnvelope(item transcriptFile, state *transcriptCursorState, nativeID string, timestamp time.Time, sequence int64, spanID, parentID, kind, name, lifecycle, status string) core.ObservationEnvelope {
	if nativeID == "" {
		nativeID = fmt.Sprintf("offset:%d", sequence)
	}
	dedupe := strings.Join([]string{"transcript", item.Runtime, state.SessionID, nativeID, kind, lifecycle}, ":")
	attributes := map[string]any{
		"backfill": true, "runtime": item.Runtime, "transcript_class": item.Class,
		"transcript_path": item.Path, "native_event_id": nativeID,
	}
	if state.Cwd != "" {
		attributes["cwd"] = state.Cwd
	}
	return core.ObservationEnvelope{
		Version: core.ObservationEnvelopeVersion, EventID: "obs_" + stableHex("event:"+dedupe, 16),
		DedupeKey: dedupe, Time: timestamp.UTC(), Sequence: sequence,
		TraceID: state.TraceID, SpanID: spanID, ParentSpanID: parentID,
		Kind: kind, Name: name, Lifecycle: lifecycle, Status: status,
		AgentID: state.AgentID, AgentName: transcriptAgentName(item.Runtime), RuntimeID: item.Runtime,
		SessionID: state.SessionID, TurnID: state.TurnID,
		Source: "transcript", Provenance: []string{item.Runtime + "_transcript", "backfill"},
		Quality: core.ObservationQualityPartial, Attributes: attributes,
	}
}

func transcriptAgentName(runtime string) string {
	if runtime == "claude" {
		return "Claude Code"
	}
	return "Codex"
}

type codexTokenNumbers struct {
	Input     int64 `json:"input_tokens"`
	Output    int64 `json:"output_tokens"`
	Cached    int64 `json:"cached_input_tokens"`
	Reasoning int64 `json:"reasoning_output_tokens"`
	Total     int64 `json:"total_tokens"`
}

func parseCodexTokens(raw map[string]any) codexTokenNumbers {
	return codexTokenNumbers{
		Input: mapInt64(raw, "input_tokens"), Output: mapInt64(raw, "output_tokens"),
		Cached: mapInt64(raw, "cached_input_tokens"), Reasoning: mapInt64(raw, "reasoning_output_tokens"),
		Total: mapInt64(raw, "total_tokens"),
	}
}

func (u codexTokenNumbers) zero() bool {
	return u.Input == 0 && u.Output == 0 && u.Cached == 0 && u.Reasoning == 0 && u.Total == 0
}

func (u codexTokenNumbers) delta(previous codexTokenNumbers, havePrevious bool) (codexTokenNumbers, bool) {
	if !havePrevious {
		return u, false
	}
	if u.Input < previous.Input || u.Output < previous.Output || u.Cached < previous.Cached || u.Reasoning < previous.Reasoning || u.Total < previous.Total {
		return u, true
	}
	return codexTokenNumbers{Input: u.Input - previous.Input, Output: u.Output - previous.Output, Cached: u.Cached - previous.Cached, Reasoning: u.Reasoning - previous.Reasoning, Total: u.Total - previous.Total}, false
}

func (u codexTokenNumbers) observationUsage() *core.ObservationUsage {
	input := u.Input - u.Cached
	if input < 0 {
		input = 0
	}
	total := u.Total
	if total == 0 {
		total = input + u.Cached + u.Output
	}
	return &core.ObservationUsage{InputTokens: input, OutputTokens: u.Output, CacheReadTokens: u.Cached, ReasoningTokens: u.Reasoning, TotalTokens: total}
}

func claudeObservationUsage(raw map[string]any) *core.ObservationUsage {
	if raw == nil {
		return nil
	}
	usage := &core.ObservationUsage{
		InputTokens: mapInt64(raw, "input_tokens"), OutputTokens: mapInt64(raw, "output_tokens"),
		CacheReadTokens: mapInt64(raw, "cache_read_input_tokens"), CacheWriteTokens: mapInt64(raw, "cache_creation_input_tokens"),
	}
	usage.TotalTokens = usage.InputTokens + usage.OutputTokens + usage.CacheReadTokens + usage.CacheWriteTokens
	if usage.TotalTokens == 0 {
		return nil
	}
	return usage
}

func claudeOnlyToolResults(value any) bool {
	blocks := mapArray(value)
	if len(blocks) == 0 {
		return false
	}
	for _, block := range blocks {
		if mapString(block, "type") != "tool_result" {
			return false
		}
	}
	return true
}

func claudePublicContent(value any, assistant bool) any {
	if text, ok := value.(string); ok {
		return text
	}
	var result []any
	for _, block := range mapArray(value) {
		typeName := mapString(block, "type")
		if typeName == "thinking" || typeName == "redacted_thinking" {
			continue
		}
		if assistant && typeName == "tool_use" {
			continue
		}
		if typeName == "text" {
			result = append(result, mapString(block, "text"))
		} else if !assistant && typeName != "tool_result" {
			result = append(result, block)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func codexPublicMessageContent(value any) any {
	var result []any
	for _, block := range mapArray(value) {
		typeName := mapString(block, "type")
		if typeName == "input_text" || typeName == "output_text" || typeName == "text" {
			result = append(result, mapString(block, "text"))
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func jsonObservationContent(value any) *core.ObservationContent {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) == 0 {
		return nil
	}
	return &core.ObservationContent{ContentType: "application/json", Data: encoded}
}

func parseTranscriptTime(value string, fallback time.Time) time.Time {
	if value != "" {
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
			if parsed, err := time.Parse(layout, value); err == nil {
				return parsed.UTC()
			}
		}
	}
	return fallback.UTC()
}

func stableHex(value string, bytes int) string {
	digest := sha256.Sum256([]byte(value))
	if bytes <= 0 || bytes > len(digest) {
		bytes = len(digest)
	}
	return hex.EncodeToString(digest[:bytes])
}

func mapObject(raw map[string]any, key string) map[string]any {
	if raw == nil {
		return nil
	}
	value, _ := raw[key].(map[string]any)
	return value
}

func mapString(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := raw[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func mapArray(value any) []map[string]any {
	items, _ := value.([]any)
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if mapped, ok := item.(map[string]any); ok {
			result = append(result, mapped)
		}
	}
	return result
}

func mapInt64(raw map[string]any, key string) int64 {
	if raw == nil {
		return 0
	}
	switch value := raw[key].(type) {
	case float64:
		return int64(value)
	case json.Number:
		parsed, _ := value.Int64()
		return parsed
	case int64:
		return value
	case int:
		return int64(value)
	}
	return 0
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
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

func cloneMap(input map[string]any) map[string]any {
	result := make(map[string]any, len(input)+1)
	for key, value := range input {
		result[key] = value
	}
	return result
}
