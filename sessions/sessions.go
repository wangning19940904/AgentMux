package sessions

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Meta is the UI-facing shape for one local or app-server backed agent session.
type Meta struct {
	ProviderID      string    `json:"provider_id"`
	Surface         string    `json:"surface"` // cli, app-server
	SessionID       string    `json:"session_id"`
	NativeSessionID string    `json:"native_session_id,omitempty"`
	Title           string    `json:"title,omitempty"`
	Summary         string    `json:"summary,omitempty"`
	ProjectDir      string    `json:"project_dir,omitempty"`
	CreatedAt       time.Time `json:"created_at,omitempty"`
	LastActiveAt    time.Time `json:"last_active_at,omitempty"`
	SourcePath      string    `json:"source_path,omitempty"`
	ResumeCommand   string    `json:"resume_command,omitempty"`
	FileBacked      bool      `json:"file_backed"`
	MessageCount    int       `json:"message_count"`
	MessagesPartial bool      `json:"messages_partial,omitempty"`
	Available       bool      `json:"available"`
	StatusMessage   string    `json:"status_message,omitempty"`
	Origin          string    `json:"origin,omitempty"` // local, channel
	AgentID         string    `json:"agent_id,omitempty"`
	AgentName       string    `json:"agent_name,omitempty"`
	ChannelID       string    `json:"channel_id,omitempty"`
	ChannelName     string    `json:"channel_name,omitempty"`
	ChannelType     string    `json:"channel_type,omitempty"`
	ConversationID  string    `json:"conversation_id,omitempty"`
	ConversationKey string    `json:"conversation_key,omitempty"`
	ChatID          string    `json:"chat_id,omitempty"`
	ChatType        string    `json:"chat_type,omitempty"`
	CanChat         bool      `json:"can_chat,omitempty"`
	RunStatus       string    `json:"run_status,omitempty"`
	CanStop         bool      `json:"can_stop,omitempty"`
	ActiveTaskID    string    `json:"active_task_id,omitempty"`
}

// Message is a compact transcript row extracted from a session source.
type Message struct {
	Role       string    `json:"role"`
	Kind       string    `json:"kind,omitempty"`
	Content    string    `json:"content"`
	Timestamp  time.Time `json:"timestamp,omitempty"`
	ToolName   string    `json:"tool_name,omitempty"`
	ToolCallID string    `json:"tool_call_id,omitempty"`
	ToolInput  string    `json:"tool_input,omitempty"`
	ToolOutput string    `json:"tool_output,omitempty"`
	ToolStatus string    `json:"tool_status,omitempty"`
}

// ResumeRequest asks AgentMux to restore or open a session.
type ResumeRequest struct {
	ProviderID   string `json:"provider_id"`
	Surface      string `json:"surface"`
	SessionID    string `json:"session_id"`
	SourcePath   string `json:"source_path,omitempty"`
	ProjectDir   string `json:"project_dir,omitempty"`
	OpenTerminal bool   `json:"open_terminal,omitempty"`
}

// ResumeResult reports the exact command or app-server thread restored.
type ResumeResult struct {
	OK            bool   `json:"ok"`
	Command       string `json:"command,omitempty"`
	ThreadID      string `json:"thread_id,omitempty"`
	Opened        bool   `json:"opened,omitempty"`
	StatusMessage string `json:"status_message,omitempty"`
}

// Service coordinates the file-backed scanners and Codex app-server adapter.
type Service struct {
	app *CodexAppClient
}

// New builds the default session service.
func New() *Service {
	return &Service{app: &CodexAppClient{}}
}

// List returns sessions across the requested provider/surface.
func (s *Service) List(ctx context.Context, providerID, surface string) ([]Meta, error) {
	providerID = strings.TrimSpace(providerID)
	surface = strings.TrimSpace(surface)
	var out []Meta
	var appErr error
	if matches(providerID, "claudecode", "claude") && matches(surface, "cli", "") {
		items, err := (&claudeScanner{}).List(ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	if matches(providerID, "codex", "") && matches(surface, "cli", "") {
		items, err := (&codexScanner{}).List(ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	if matches(providerID, "codex", "") && matches(surface, "app-server", "desktop", "") {
		items, err := s.app.List(ctx)
		if err != nil {
			appErr = err
			if surface == "app-server" || surface == "desktop" {
				return nil, err
			}
		}
		out = append(out, items...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].LastActiveAt.After(out[j].LastActiveAt)
	})
	for i := range out {
		if out[i].RunStatus == "" {
			out[i].RunStatus = "idle"
		}
	}
	if len(out) == 0 && appErr != nil && providerID == "codex" && surface == "" {
		return out, nil
	}
	return out, nil
}

// Messages returns the parsed transcript for a session.
func (s *Service) Messages(ctx context.Context, req ResumeRequest) ([]Message, error) {
	if req.Surface == "app-server" || req.Surface == "desktop" {
		return s.app.Messages(ctx, req.SessionID)
	}
	if req.SourcePath == "" {
		meta, err := s.find(ctx, req)
		if err != nil {
			return nil, err
		}
		req.SourcePath = meta.SourcePath
		req.ProviderID = meta.ProviderID
	}
	switch req.ProviderID {
	case "claudecode", "claude":
		_, messages, err := parseClaudeFile(req.SourcePath)
		return messages, err
	case "codex", "":
		_, messages, err := parseCodexFile(req.SourcePath)
		return messages, err
	default:
		return nil, fmt.Errorf("unsupported session provider %q", req.ProviderID)
	}
}

// Resume restores a Codex app-server thread or returns/opens the native CLI resume command.
func (s *Service) Resume(ctx context.Context, req ResumeRequest) (ResumeResult, error) {
	if req.Surface == "app-server" || req.Surface == "desktop" {
		threadID, err := s.app.Resume(ctx, req.SessionID)
		if err != nil {
			return ResumeResult{}, err
		}
		return ResumeResult{OK: true, ThreadID: threadID, StatusMessage: "thread resumed via codex app-server"}, nil
	}
	meta, err := s.find(ctx, req)
	if err != nil {
		return ResumeResult{}, err
	}
	command := meta.ResumeCommand
	if command == "" {
		return ResumeResult{}, errors.New("session has no resume command")
	}
	res := ResumeResult{OK: true, Command: command}
	if req.OpenTerminal {
		opened, err := openTerminal(command, firstNonEmpty(req.ProjectDir, meta.ProjectDir))
		if err != nil {
			return res, err
		}
		res.Opened = opened
	}
	return res, nil
}

// OpenCodexThread opens the exact native Codex thread on macOS. Other
// platforms receive a truthful CLI fallback and never report a successful
// desktop launch.
func (s *Service) OpenCodexThread(ctx context.Context, threadID string) (ResumeResult, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return ResumeResult{}, errors.New("Codex thread id is required")
	}
	for _, r := range threadID {
		if !(r == '-' || r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return ResumeResult{}, fmt.Errorf("invalid Codex thread id %q", threadID)
		}
	}
	command := "codex resume " + shellToken(threadID)
	result := ResumeResult{
		OK: true, ThreadID: threadID, Command: command,
		StatusMessage: "Codex desktop deep link is unavailable; use the resume command",
	}
	if runtime.GOOS != "darwin" {
		return result, nil
	}
	deepLink := "codex://threads/" + threadID
	if err := exec.CommandContext(ctx, "open", deepLink).Run(); err != nil {
		return result, fmt.Errorf("open Codex thread: %w", err)
	}
	result.Opened = true
	result.StatusMessage = "opened in Codex App"
	return result, nil
}

// Delete removes a file-backed session after verifying it lives below a known scanner root.
func (s *Service) Delete(ctx context.Context, req ResumeRequest) error {
	if req.SourcePath == "" {
		meta, err := s.find(ctx, req)
		if err != nil {
			return err
		}
		req.SourcePath = meta.SourcePath
		req.ProviderID = meta.ProviderID
	}
	if req.SourcePath == "" {
		return errors.New("session source path is required")
	}
	allowed := append(claudeRoots(), codexRoots()...)
	if !pathInRoots(req.SourcePath, allowed) {
		return fmt.Errorf("refusing to delete path outside session roots: %s", req.SourcePath)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return os.Remove(req.SourcePath)
	}
}

func (s *Service) find(ctx context.Context, req ResumeRequest) (Meta, error) {
	items, err := s.List(ctx, req.ProviderID, req.Surface)
	if err != nil {
		return Meta{}, err
	}
	for _, item := range items {
		if req.SourcePath != "" && item.SourcePath == req.SourcePath {
			return item, nil
		}
		if req.SessionID != "" && item.SessionID == req.SessionID {
			return item, nil
		}
	}
	return Meta{}, fmt.Errorf("session %q not found", req.SessionID)
}

func matches(value string, allowed ...string) bool {
	if value == "" {
		return true
	}
	for _, item := range allowed {
		if item == value {
			return true
		}
	}
	return false
}

type claudeScanner struct{}

// sessionMetaCache memoizes per-file session summaries keyed by path. A file
// whose size and mtime are unchanged since the last scan reuses the cached
// Meta, so periodic console refreshes stat files instead of re-parsing every
// transcript line.
var sessionMetaCache sync.Map // path -> sessionMetaCacheEntry

type sessionMetaCacheEntry struct {
	size    int64
	modTime time.Time
	meta    Meta
}

// cachedSessionMeta returns a cached Meta for path when the file is unchanged,
// otherwise it runs parse and stores the result.
func cachedSessionMeta(path string, d os.DirEntry, parse func(string) (Meta, []Message, error)) (Meta, error) {
	info, err := d.Info()
	if err == nil {
		if value, ok := sessionMetaCache.Load(path); ok {
			entry := value.(sessionMetaCacheEntry)
			if entry.size == info.Size() && entry.modTime.Equal(info.ModTime()) {
				return entry.meta, nil
			}
		}
	}
	meta, _, err := parse(path)
	if err != nil {
		return Meta{}, err
	}
	if info != nil {
		sessionMetaCache.Store(path, sessionMetaCacheEntry{size: info.Size(), modTime: info.ModTime(), meta: meta})
	}
	return meta, nil
}

func (s *claudeScanner) List(ctx context.Context) ([]Meta, error) {
	var out []Meta
	for _, root := range claudeRoots() {
		projectsDir := filepath.Join(root, "projects")
		err := filepath.WalkDir(projectsDir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d == nil {
				return nil
			}
			if d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") || strings.HasPrefix(d.Name(), "agent-") {
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			meta, err := cachedSessionMeta(path, d, parseClaudeFile)
			if err == nil && meta.SessionID != "" {
				out = append(out, meta)
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	return out, nil
}

func claudeRoots() []string {
	if v := os.Getenv("CLAUDE_CONFIG_DIR"); v != "" {
		return []string{v}
	}
	home, _ := os.UserHomeDir()
	return []string{filepath.Join(home, ".claude")}
}

func parseClaudeFile(path string) (Meta, []Message, error) {
	f, err := os.Open(path)
	if err != nil {
		return Meta{}, nil, err
	}
	defer f.Close()
	meta := Meta{
		ProviderID:    "claudecode",
		Surface:       "cli",
		SessionID:     strings.TrimSuffix(filepath.Base(path), ".jsonl"),
		ProjectDir:    filepath.Base(filepath.Dir(path)),
		SourcePath:    path,
		FileBacked:    true,
		Available:     true,
		ResumeCommand: "claude --resume " + shellToken(strings.TrimSuffix(filepath.Base(path), ".jsonl")),
	}
	var messages []Message
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 32*1024*1024)
	for sc.Scan() {
		var raw map[string]any
		if err := json.Unmarshal(sc.Bytes(), &raw); err != nil {
			continue
		}
		if id := stringField(raw, "sessionId", "session_id", "uuid"); id != "" {
			meta.SessionID = id
			meta.ResumeCommand = "claude --resume " + shellToken(id)
		}
		if cwd := stringField(raw, "cwd", "projectDir", "project_dir"); cwd != "" {
			meta.ProjectDir = cwd
		}
		ts := parseTimestamp(stringField(raw, "timestamp", "createdAt", "updatedAt"))
		mergeTimes(&meta, ts)
		if title := claudeTitle(raw); title != "" {
			meta.Title = title
		}
		if summary := stringField(raw, "summary", "text"); summary != "" && strings.EqualFold(stringField(raw, "type"), "summary") {
			meta.Summary = truncate(summary, 220)
		}
		role := stringField(raw, "type", "role")
		msgMap := mapField(raw, "message")
		if msgRole := stringField(msgMap, "role"); msgRole != "" {
			role = msgRole
		}
		parsed := parseClaudeContent(firstNonNil(msgMap["content"], raw["content"]), role, ts)
		for _, msg := range parsed {
			appendTranscriptMessage(&messages, msg)
			if meta.Title == "" && msg.Role == "user" && msg.Content != "" {
				meta.Title = truncate(msg.Content, 80)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return Meta{}, nil, err
	}
	if meta.Title == "" {
		meta.Title = meta.SessionID
	}
	meta.MessageCount = len(messages)
	return meta, messages, nil
}

func claudeTitle(raw map[string]any) string {
	for _, key := range []string{"custom-title", "custom_title", "title", "name"} {
		if v := stringField(raw, key); v != "" {
			return truncate(v, 80)
		}
	}
	if stringField(raw, "type") == "system" && stringField(raw, "subtype") == "custom-title" {
		return truncate(stringField(raw, "text"), 80)
	}
	return ""
}

type codexScanner struct{}

func (s *codexScanner) List(ctx context.Context) ([]Meta, error) {
	var out []Meta
	for _, root := range codexRoots() {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d == nil {
				return nil
			}
			if d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			meta, err := cachedSessionMeta(path, d, parseCodexFileSummary)
			if err == nil && meta.SessionID != "" && meta.Available {
				out = append(out, meta)
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	return out, nil
}

func codexRoots() []string {
	base := os.Getenv("CODEX_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".codex")
	}
	return []string{filepath.Join(base, "sessions"), filepath.Join(base, "archived_sessions")}
}

func parseCodexFile(path string) (Meta, []Message, error) {
	return parseCodexFileWithLimit(path, 0)
}

func parseCodexFileSummary(path string) (Meta, []Message, error) {
	return parseCodexFileWithLimit(path, 220)
}

func parseCodexFileWithLimit(path string, maxLines int) (Meta, []Message, error) {
	f, err := os.Open(path)
	if err != nil {
		return Meta{}, nil, err
	}
	defer f.Close()
	info, _ := f.Stat()
	sessionID := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	sessionID = strings.TrimPrefix(sessionID, "rollout-")
	meta := Meta{
		ProviderID:    "codex",
		Surface:       "cli",
		SessionID:     sessionID,
		SourcePath:    path,
		FileBacked:    true,
		Available:     true,
		ResumeCommand: "codex resume " + shellToken(sessionID),
	}
	if info != nil {
		meta.LastActiveAt = info.ModTime()
	}
	var messages []Message
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 32*1024*1024)
	lines := 0
	truncated := false
	for sc.Scan() {
		lines++
		if maxLines > 0 && lines > maxLines {
			truncated = true
			break
		}
		var raw map[string]any
		if err := json.Unmarshal(sc.Bytes(), &raw); err != nil {
			continue
		}
		if stringField(raw, "source") == "subagent" {
			meta.Available = false
		}
		ts := parseTimestamp(stringField(raw, "timestamp", "created_at", "createdAt"))
		mergeTimes(&meta, ts)
		if stringField(raw, "type") == "session_meta" {
			payload := mapField(raw, "payload")
			if id := stringField(payload, "id", "session_id", "sessionId"); id != "" {
				meta.SessionID = id
				meta.ResumeCommand = "codex resume " + shellToken(id)
			}
			if cwd := stringField(payload, "cwd", "project_dir", "projectDir"); cwd != "" {
				meta.ProjectDir = cwd
			}
			mergeTimes(&meta, parseTimestamp(stringField(payload, "timestamp", "created_at", "createdAt")))
			continue
		}
		item := codexItem(raw)
		if len(item) == 0 {
			continue
		}
		for _, msg := range parseCodexItem(item, ts) {
			appendTranscriptMessage(&messages, msg)
			if meta.Title == "" && msg.Role == "user" && msg.Content != "" {
				meta.Title = truncate(msg.Content, 80)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return Meta{}, nil, err
	}
	if meta.Title == "" {
		meta.Title = meta.SessionID
	}
	meta.MessageCount = len(messages)
	meta.MessagesPartial = truncated
	return meta, messages, nil
}

func codexItem(raw map[string]any) map[string]any {
	if item := mapField(raw, "item"); len(item) > 0 {
		return item
	}
	payload := mapField(raw, "payload")
	if item := mapField(payload, "item"); len(item) > 0 {
		return item
	}
	if msg := mapField(payload, "message"); len(msg) > 0 {
		return msg
	}
	if stringField(raw, "type") == "response_item" {
		return payload
	}
	if stringField(raw, "role") != "" {
		return raw
	}
	return nil
}

func openTerminal(command, dir string) (bool, error) {
	if runtime.GOOS != "darwin" {
		return false, nil
	}
	script := command
	if dir != "" {
		script = "cd " + shellToken(dir) + " && " + command
	}
	return true, exec.Command("osascript", "-e", `tell application "Terminal" to do script `+strconv.Quote(script)).Start()
}

func pathInRoots(path string, roots []string) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	for _, root := range roots {
		absRoot, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(absRoot, absPath)
		if err == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." {
			return true
		}
	}
	return false
}

func mergeTimes(meta *Meta, ts time.Time) {
	if ts.IsZero() {
		return
	}
	if meta.CreatedAt.IsZero() || ts.Before(meta.CreatedAt) {
		meta.CreatedAt = ts
	}
	if meta.LastActiveAt.IsZero() || ts.After(meta.LastActiveAt) {
		meta.LastActiveAt = ts
	}
}

func normalizeRole(role string) string {
	role = strings.ToLower(strings.TrimSpace(role))
	switch role {
	case "user", "assistant", "system":
		return role
	case "tool", "tool_use", "tool_result", "function_call", "function_call_output":
		return "tool"
	default:
		if role == "" {
			return "assistant"
		}
		return role
	}
}

func parseTimestamp(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	if n, err := strconv.ParseInt(value, 10, 64); err == nil {
		if n > 1_000_000_000_000 {
			return time.UnixMilli(n).UTC()
		}
		return time.Unix(n, 0).UTC()
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if ts, err := time.Parse(layout, value); err == nil {
			return ts
		}
	}
	return time.Time{}
}

func stringField(m map[string]any, keys ...string) string {
	if m == nil {
		return ""
	}
	for _, key := range keys {
		if v, ok := m[key]; ok {
			if s := anyToString(v); s != "" {
				return s
			}
		}
	}
	return ""
}

func mapField(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	if child, ok := m[key].(map[string]any); ok {
		return child
	}
	return nil
}

func firstNonNil(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func extractContent(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			if s := extractContent(item); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	case map[string]any:
		for _, key := range []string{"text", "content", "input", "output", "arguments", "name"} {
			if s := extractContent(v[key]); s != "" {
				return s
			}
		}
		b, _ := json.Marshal(v)
		return string(b)
	case nil:
		return ""
	default:
		return anyToString(v)
	}
}

func parseClaudeContent(value any, fallbackRole string, ts time.Time) []Message {
	role := normalizeRole(fallbackRole)
	switch content := value.(type) {
	case []any:
		messages := make([]Message, 0, len(content))
		var textParts []string
		flushText := func() {
			if text := strings.TrimSpace(strings.Join(textParts, "\n")); text != "" {
				messages = append(messages, Message{Role: role, Content: text, Timestamp: ts})
			}
			textParts = nil
		}
		for _, block := range content {
			item, ok := block.(map[string]any)
			if !ok {
				if text := extractContent(block); text != "" {
					textParts = append(textParts, text)
				}
				continue
			}
			kind := strings.ToLower(stringField(item, "type"))
			switch kind {
			case "tool_use", "tool_call", "function_call":
				flushText()
				messages = append(messages, Message{
					Role:       "tool",
					Kind:       kind,
					Timestamp:  ts,
					ToolName:   firstNonEmpty(stringField(item, "name", "tool"), kind),
					ToolCallID: stringField(item, "id", "call_id", "tool_use_id"),
					ToolInput:  formatToolValue(firstNonNil(item["input"], item["arguments"])),
					ToolStatus: firstNonEmpty(stringField(item, "status"), "called"),
				})
			case "tool_result", "tool_output", "function_call_output":
				flushText()
				status := stringField(item, "status")
				if failed, _ := item["is_error"].(bool); failed {
					status = "failed"
				}
				messages = append(messages, Message{
					Role:       "tool",
					Kind:       kind,
					Timestamp:  ts,
					ToolName:   stringField(item, "name", "tool"),
					ToolCallID: stringField(item, "tool_use_id", "call_id", "id"),
					ToolOutput: formatToolValue(firstNonNil(item["content"], item["output"], item["result"])),
					ToolStatus: firstNonEmpty(status, "completed"),
				})
			default:
				if text := extractContent(firstNonNil(item["text"], item["content"])); text != "" {
					textParts = append(textParts, text)
				}
			}
		}
		flushText()
		return messages
	case map[string]any:
		return parseClaudeContent([]any{content}, fallbackRole, ts)
	default:
		if text := extractContent(value); text != "" {
			return []Message{{Role: role, Content: text, Timestamp: ts}}
		}
	}
	return nil
}

func parseCodexItem(item map[string]any, ts time.Time) []Message {
	kind := strings.ToLower(stringField(item, "type"))
	if kind == "message" || stringField(item, "role") != "" {
		content := extractContent(firstNonNil(item["content"], item["text"]))
		if content == "" {
			return nil
		}
		return []Message{{
			Role: normalizeRole(stringField(item, "role", "type")), Kind: kind, Content: content, Timestamp: ts,
		}}
	}
	if !isToolKind(kind) {
		content := extractContent(firstNonNil(item["content"], item["text"]))
		if content == "" {
			return nil
		}
		return []Message{{Role: normalizeRole(kind), Kind: kind, Content: content, Timestamp: ts}}
	}
	msg := Message{
		Role:       "tool",
		Kind:       kind,
		Timestamp:  ts,
		ToolName:   firstNonEmpty(stringField(item, "name", "tool"), toolNameForKind(kind)),
		ToolCallID: stringField(item, "call_id", "tool_use_id", "id"),
		ToolStatus: stringField(item, "status"),
	}
	if isToolOutputKind(kind) {
		msg.ToolOutput = formatToolValue(firstNonNil(item["output"], item["result"], item["content"], item["error"]))
		if msg.ToolStatus == "" {
			msg.ToolStatus = "completed"
		}
	} else {
		msg.ToolInput = formatToolValue(firstNonNil(item["arguments"], item["input"], item["command"], item["action"], item["query"], item["changes"]))
		if msg.ToolStatus == "" {
			msg.ToolStatus = "called"
		}
	}
	return []Message{msg}
}

func isToolKind(kind string) bool {
	return strings.Contains(kind, "tool") || strings.Contains(kind, "function_call") ||
		strings.Contains(kind, "functioncall") || strings.Contains(kind, "web_search") ||
		strings.Contains(kind, "websearch") || strings.Contains(kind, "computer_call")
}

func isToolOutputKind(kind string) bool {
	return strings.Contains(kind, "output") || strings.Contains(kind, "result")
}

func toolNameForKind(kind string) string {
	name := strings.TrimSuffix(kind, "_output")
	name = strings.TrimSuffix(name, "output")
	name = strings.TrimSuffix(name, "_call")
	name = strings.TrimSuffix(name, "call")
	return strings.Trim(name, "_")
}

func appendTranscriptMessage(messages *[]Message, msg Message) {
	if msg.Role == "tool" && msg.ToolCallID != "" {
		for i := len(*messages) - 1; i >= 0; i-- {
			existing := &(*messages)[i]
			if existing.Role != "tool" || existing.ToolCallID != msg.ToolCallID {
				continue
			}
			if existing.ToolName == "" {
				existing.ToolName = msg.ToolName
			}
			if existing.ToolInput == "" {
				existing.ToolInput = msg.ToolInput
			}
			if msg.ToolOutput != "" {
				existing.ToolOutput = msg.ToolOutput
			}
			if msg.ToolStatus != "" {
				existing.ToolStatus = msg.ToolStatus
			}
			return
		}
	}
	*messages = append(*messages, msg)
}

func formatToolValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		text = strings.TrimSpace(text)
		if text == "" {
			return ""
		}
		var parsed any
		if json.Unmarshal([]byte(text), &parsed) == nil {
			if pretty, err := json.MarshalIndent(parsed, "", "  "); err == nil {
				return string(pretty)
			}
		}
		return text
	}
	pretty, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return extractContent(value)
	}
	return string(pretty)
}

func anyToString(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	case json.Number:
		return v.String()
	case float64:
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	default:
		return ""
	}
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}

func shellToken(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// CodexAppClient speaks the official codex app-server JSON-RPC protocol over stdio.
type CodexAppClient struct {
	Command string
}

func (c *CodexAppClient) List(ctx context.Context) ([]Meta, error) {
	raw, err := c.call(ctx, "thread/list", map[string]any{
		"limit": 100, "archived": false, "sortKey": "recency_at", "sortDirection": "desc",
	})
	if err != nil {
		return nil, err
	}
	threads := resultArray(raw, "threads", "items")
	if len(threads) == 0 {
		threads = resultArray(raw, "data")
	}
	out := make([]Meta, 0, len(threads))
	for _, thread := range threads {
		id := stringField(thread, "id", "thread_id", "threadId")
		if id == "" {
			continue
		}
		title := firstNonEmpty(stringField(thread, "title"), stringField(thread, "name"), stringField(thread, "preview"), id)
		created := parseTimestamp(stringField(thread, "created_at", "createdAt"))
		updated := parseTimestamp(stringField(thread, "updated_at", "updatedAt", "last_active_at", "lastActiveAt"))
		if updated.IsZero() {
			updated = created
		}
		out = append(out, Meta{
			ProviderID:    "codex",
			Surface:       "app-server",
			SessionID:     id,
			Title:         title,
			Summary:       stringField(thread, "summary"),
			ProjectDir:    stringField(thread, "cwd", "project_dir", "projectDir"),
			CreatedAt:     created,
			LastActiveAt:  updated,
			FileBacked:    false,
			Available:     true,
			MessageCount:  int(firstNumber(numberField(thread, "turn_count", "message_count"), float64(len(resultArrayFromMap(thread, "turns"))))),
			StatusMessage: "codex app-server",
			RunStatus:     appServerThreadStatus(thread),
		})
	}
	return out, nil
}

func appServerThreadStatus(thread map[string]any) string {
	status := firstNonEmpty(stringField(thread, "run_status"), stringField(thread, "state"), stringField(mapField(thread, "status"), "type"))
	status = strings.ToLower(strings.NewReplacer("_", "", "-", "", " ", "").Replace(status))
	switch status {
	case "active", "running", "inprogress", "loaded":
		return "running"
	case "queued", "pending":
		return "queued"
	case "waiting", "waitinginput", "blocked":
		return "waiting_input"
	case "failed", "error":
		return "failed"
	case "cancelled", "canceled":
		return "cancelled"
	case "interrupted", "stopped":
		return "interrupted"
	default:
		// A fresh app-server reports persisted threads as notLoaded. That means
		// this AgentMux process does not own a live turn, so idle is the only
		// safe status and the UI must not offer a stop action.
		return "idle"
	}
}

func (c *CodexAppClient) Messages(ctx context.Context, threadID string) ([]Message, error) {
	raw, err := c.call(ctx, "thread/read", map[string]any{"threadId": threadID, "includeTurns": true})
	if err != nil {
		return nil, err
	}
	items := appServerTranscriptItems(raw)
	messages := make([]Message, 0, len(items))
	for _, entry := range items {
		for _, msg := range parseAppServerItem(entry.item, entry.timestamp) {
			appendTranscriptMessage(&messages, msg)
		}
	}
	return messages, nil
}

type appServerTranscriptItem struct {
	item      map[string]any
	timestamp time.Time
}

func appServerTranscriptItems(raw json.RawMessage) []appServerTranscriptItem {
	var obj map[string]any
	if json.Unmarshal(raw, &obj) != nil {
		return nil
	}
	thread := mapField(obj, "thread")
	if len(thread) == 0 {
		thread = obj
	}
	turns := resultArrayFromMap(thread, "turns")
	if len(turns) > 0 {
		var entries []appServerTranscriptItem
		for _, turn := range turns {
			ts := parseTimestamp(stringField(turn, "timestamp", "created_at", "createdAt", "started_at", "startedAt", "completed_at", "completedAt"))
			turnItems := resultArrayFromMap(turn, "items", "messages")
			if len(turnItems) == 0 {
				entries = append(entries, appServerTranscriptItem{item: turn, timestamp: ts})
				continue
			}
			for _, item := range turnItems {
				entries = append(entries, appServerTranscriptItem{item: item, timestamp: ts})
			}
		}
		return entries
	}
	items := resultArrayFromMap(thread, "items", "messages", "data")
	if len(items) == 0 {
		items = resultArray(raw, "items", "messages", "data")
	}
	entries := make([]appServerTranscriptItem, 0, len(items))
	for _, item := range items {
		entries = append(entries, appServerTranscriptItem{
			item:      item,
			timestamp: parseTimestamp(stringField(item, "timestamp", "created_at", "createdAt")),
		})
	}
	return entries
}

func parseAppServerItem(item map[string]any, ts time.Time) []Message {
	kind := stringField(item, "type")
	switch kind {
	case "userMessage":
		if content := extractContent(item["content"]); content != "" {
			return []Message{{Role: "user", Kind: kind, Content: content, Timestamp: ts}}
		}
		return nil
	case "agentMessage":
		if content := extractContent(item["text"]); content != "" {
			return []Message{{Role: "assistant", Kind: kind, Content: content, Timestamp: ts}}
		}
		return nil
	case "commandExecution":
		input := map[string]any{"command": item["command"], "cwd": item["cwd"]}
		output := formatToolValue(item["aggregatedOutput"])
		if exitCode := item["exitCode"]; exitCode != nil {
			if output != "" {
				output += "\n\n"
			}
			output += "exit code: " + anyToString(exitCode)
		}
		return []Message{{Role: "tool", Kind: kind, Timestamp: ts, ToolName: "exec_command", ToolCallID: stringField(item, "id"), ToolInput: formatToolValue(input), ToolOutput: output, ToolStatus: stringField(item, "status")}}
	case "fileChange":
		return []Message{{Role: "tool", Kind: kind, Timestamp: ts, ToolName: "apply_patch", ToolCallID: stringField(item, "id"), ToolInput: formatToolValue(item["changes"]), ToolStatus: stringField(item, "status")}}
	case "mcpToolCall":
		name := strings.Trim(strings.Join([]string{stringField(item, "server"), stringField(item, "tool")}, "/"), "/")
		return []Message{{Role: "tool", Kind: kind, Timestamp: ts, ToolName: name, ToolCallID: stringField(item, "id"), ToolInput: formatToolValue(item["arguments"]), ToolOutput: formatToolValue(firstNonNil(item["result"], item["error"])), ToolStatus: stringField(item, "status")}}
	case "dynamicToolCall":
		name := strings.Trim(strings.Join([]string{stringField(item, "namespace"), stringField(item, "tool")}, "/"), "/")
		return []Message{{Role: "tool", Kind: kind, Timestamp: ts, ToolName: name, ToolCallID: stringField(item, "id"), ToolInput: formatToolValue(item["arguments"]), ToolOutput: formatToolValue(item["contentItems"]), ToolStatus: stringField(item, "status")}}
	case "collabAgentToolCall":
		input := map[string]any{"prompt": item["prompt"], "model": item["model"], "reasoningEffort": item["reasoningEffort"]}
		output := map[string]any{"receiverThreadIds": item["receiverThreadIds"], "agentsStates": item["agentsStates"]}
		return []Message{{Role: "tool", Kind: kind, Timestamp: ts, ToolName: stringField(item, "tool"), ToolCallID: stringField(item, "id"), ToolInput: formatToolValue(input), ToolOutput: formatToolValue(output), ToolStatus: stringField(item, "status")}}
	}
	if stringField(item, "role") != "" {
		content := extractContent(firstNonNil(item["content"], item["message"], item["text"]))
		if content != "" {
			return []Message{{Role: normalizeRole(stringField(item, "role")), Kind: kind, Content: content, Timestamp: ts}}
		}
	}
	lowerKind := strings.ToLower(kind)
	if isToolKind(lowerKind) || strings.Contains(lowerKind, "execution") || strings.Contains(lowerKind, "search") {
		name := firstNonEmpty(stringField(item, "name", "tool"), toolNameForKind(lowerKind))
		return []Message{{
			Role: "tool", Kind: kind, Timestamp: ts, ToolName: name, ToolCallID: stringField(item, "id", "callId", "call_id"),
			ToolInput:  formatToolValue(firstNonNil(item["arguments"], item["input"], item["command"], item["query"], item["action"])),
			ToolOutput: formatToolValue(firstNonNil(item["result"], item["output"], item["aggregatedOutput"], item["error"])),
			ToolStatus: stringField(item, "status"),
		}}
	}
	return nil
}

func (c *CodexAppClient) Resume(ctx context.Context, threadID string) (string, error) {
	raw, err := c.call(ctx, "thread/resume", map[string]any{"threadId": threadID})
	if err != nil {
		return "", err
	}
	var result map[string]any
	_ = json.Unmarshal(raw, &result)
	if id := stringField(result, "id", "thread_id", "threadId"); id != "" {
		return id, nil
	}
	return threadID, nil
}

func (c *CodexAppClient) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	name := c.Command
	if name == "" {
		name = "codex"
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, "app-server", "--listen", "stdio://")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()
	reader := bufio.NewReader(stdout)
	nextID := 1
	if err := writeRPC(stdin, nextID, "initialize", map[string]any{"clientInfo": map[string]any{"name": "AgentMux", "version": "0.1.0"}}); err != nil {
		return nil, err
	}
	if _, err := readRPCResult(reader, nextID); err != nil {
		return nil, withStderr(err, stderr.String())
	}
	if err := writeRPCNotification(stdin, "initialized", map[string]any{}); err != nil {
		return nil, err
	}
	nextID++
	if err := writeRPC(stdin, nextID, method, params); err != nil {
		return nil, err
	}
	raw, err := readRPCResult(reader, nextID)
	if err != nil {
		return nil, withStderr(err, stderr.String())
	}
	return raw, nil
}

func writeRPC(w io.Writer, id int, method string, params any) error {
	return writeRPCMessage(w, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
}

func writeRPCNotification(w io.Writer, method string, params any) error {
	return writeRPCMessage(w, map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func writeRPCMessage(w io.Writer, msg map[string]any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = w.Write(data)
	return err
}

func readRPCResult(r *bufio.Reader, id int) (json.RawMessage, error) {
	for {
		msg, err := readRPCMessage(r)
		if err != nil {
			return nil, err
		}
		msgID, ok := msg["id"].(float64)
		if !ok || int(msgID) != id {
			continue
		}
		if rpcErr, ok := msg["error"]; ok {
			b, _ := json.Marshal(rpcErr)
			return nil, fmt.Errorf("json-rpc error: %s", b)
		}
		result, _ := json.Marshal(msg["result"])
		return result, nil
	}
}

func readRPCMessage(r *bufio.Reader) (map[string]any, error) {
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var msg map[string]any
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			return nil, err
		}
		return msg, nil
	}
}

func withStderr(err error, stderr string) error {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, stderr)
}

func resultArray(raw json.RawMessage, keys ...string) []map[string]any {
	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	for _, key := range keys {
		if out := resultArrayFromMap(obj, key); len(out) > 0 {
			return out
		}
	}
	return nil
}

func resultArrayFromMap(obj map[string]any, keys ...string) []map[string]any {
	if obj == nil {
		return nil
	}
	for _, key := range keys {
		if rawArr, ok := obj[key].([]any); ok {
			out := make([]map[string]any, 0, len(rawArr))
			for _, item := range rawArr {
				if m, ok := item.(map[string]any); ok {
					out = append(out, m)
				}
			}
			return out
		}
	}
	return nil
}

func numberField(m map[string]any, keys ...string) float64 {
	for _, key := range keys {
		if n, ok := m[key].(float64); ok {
			return n
		}
	}
	return 0
}

func firstNumber(values ...float64) float64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}
