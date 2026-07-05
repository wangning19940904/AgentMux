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
	"time"
)

// Meta is the UI-facing shape for one local or app-server backed agent session.
type Meta struct {
	ProviderID      string    `json:"provider_id"`
	Surface         string    `json:"surface"` // cli, app-server
	SessionID       string    `json:"session_id"`
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
}

// Message is a compact transcript row extracted from a session source.
type Message struct {
	Role      string    `json:"role"`
	Kind      string    `json:"kind,omitempty"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp,omitempty"`
}

// ResumeRequest asks AgentNexus to restore or open a session.
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
			meta, _, err := parseClaudeFile(path)
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
		content := extractContent(firstNonNil(msgMap["content"], raw["content"]))
		if content == "" {
			continue
		}
		msg := Message{Role: normalizeRole(role), Content: content, Timestamp: ts}
		if msg.Role == "tool" {
			msg.Kind = "tool"
		}
		messages = append(messages, msg)
		if meta.Title == "" && msg.Role == "user" {
			meta.Title = truncate(content, 80)
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
			meta, _, err := parseCodexFileSummary(path)
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
		role := normalizeRole(stringField(item, "role", "type"))
		content := extractContent(firstNonNil(item["content"], item["text"], item["arguments"], item["output"]))
		if content == "" {
			continue
		}
		kind := stringField(item, "type")
		msg := Message{Role: role, Kind: kind, Content: content, Timestamp: ts}
		messages = append(messages, msg)
		if meta.Title == "" && msg.Role == "user" {
			meta.Title = truncate(content, 80)
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
	raw, err := c.call(ctx, "thread/list", map[string]any{})
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
		})
	}
	return out, nil
}

func (c *CodexAppClient) Messages(ctx context.Context, threadID string) ([]Message, error) {
	raw, err := c.call(ctx, "thread/read", map[string]any{"threadId": threadID})
	if err != nil {
		return nil, err
	}
	items := resultArray(raw, "items", "messages", "turns", "data")
	if len(items) == 0 {
		var obj map[string]any
		_ = json.Unmarshal(raw, &obj)
		items = resultArrayFromMap(mapField(obj, "thread"), "turns", "items", "messages")
	}
	messages := make([]Message, 0, len(items))
	for _, item := range items {
		content := extractContent(firstNonNil(item["content"], item["message"], item["text"]))
		if content == "" {
			content = extractContent(firstNonNil(item["input"], item["output"], item["events"]))
		}
		if content == "" {
			continue
		}
		messages = append(messages, Message{
			Role:      normalizeRole(stringField(item, "role", "type")),
			Kind:      stringField(item, "type"),
			Content:   content,
			Timestamp: parseTimestamp(stringField(item, "timestamp", "created_at", "createdAt")),
		})
	}
	return messages, nil
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
	if err := writeRPC(stdin, nextID, "initialize", map[string]any{"clientInfo": map[string]any{"name": "AgentNexus", "version": "0.1.0"}}); err != nil {
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
