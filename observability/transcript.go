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
	transcriptCursorCheckpointLines   = 256
	transcriptCursorCheckpointBytes   = 8 << 20
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
	trustedPaths     sync.Map
	// scanned fingerprints the last mtime/size seen per file so unchanged
	// files are skipped without opening them. Guarded by scanMu.
	scanned map[string]transcriptFileState
}

type transcriptFile struct {
	Runtime string
	Class   string
	Path    string
	ModTime time.Time
	Size    int64
}

// transcriptFileState is the last-scanned fingerprint used to skip unchanged
// files. Codex keeps hundreds of large rollout files (tens of GB total); without
// this short-circuit every poll re-opened and re-hashed all of them, pinning a
// CPU core and starving the shared SQLite connection.
type transcriptFileState struct {
	ModTime time.Time
	Size    int64
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
	if t.scanned == nil {
		t.scanned = make(map[string]transcriptFileState, len(files))
	}
	seen := make(map[string]struct{}, len(files))
	var errs []error
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return result, errors.Join(append(errs, err)...)
		}
		seen[file.Path] = struct{}{}
		// Skip files whose mtime and size are unchanged since the last scan.
		// The durable DB cursor already prevents re-emitting old lines, but
		// this avoids opening and re-hashing hundreds of large rollout files
		// on every poll when nothing has been appended.
		if prev, ok := t.scanned[file.Path]; ok && prev.Size == file.Size && prev.ModTime.Equal(file.ModTime) {
			continue
		}
		// A file last written before the metadata backfill window holds only
		// lines older than retention; scanFile would skip every one of them.
		// Skip opening it so the first cold start does not parse gigabytes of
		// ancient session history line by line. Fingerprint it so a later mtime
		// bump (an append) still gets picked up.
		if !file.ModTime.IsZero() && file.ModTime.Before(t.now().UTC().Add(-t.metadataBackfill)) {
			t.scanned[file.Path] = transcriptFileState{ModTime: file.ModTime, Size: file.Size}
			continue
		}
		partial, err := t.scanFile(ctx, file)
		result.FilesRead += partial.FilesRead
		result.LinesRead += partial.LinesRead
		result.EventsPublished += partial.EventsPublished
		result.OldLinesSkipped += partial.OldLinesSkipped
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", file.Path, err))
			continue
		}
		if !file.ModTime.IsZero() {
			t.scanned[file.Path] = transcriptFileState{ModTime: file.ModTime, Size: file.Size}
		}
	}
	// Drop fingerprints for files that disappeared so the map cannot grow
	// without bound as sessions are rotated or archived.
	for path := range t.scanned {
		if _, ok := seen[path]; !ok {
			delete(t.scanned, path)
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
			file := transcriptFile{Runtime: runtime, Class: itemClass, Path: filepath.Clean(absolute)}
			if info, infoErr := entry.Info(); infoErr == nil {
				file.ModTime = info.ModTime()
				file.Size = info.Size()
			}
			files = append(files, file)
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
		// Durable fast-path: a matching file identity that is already consumed
		// to EOF has no new lines. Return before seeking/scanning/publishing so
		// a restart (which clears the in-memory fingerprint) does not replay the
		// entire history back through the observation pipeline. Republishing old
		// lines is pure waste: each one re-runs the per-trace usage aggregation
		// against the multi-GB store, which pinned a CPU core indefinitely.
		if cursor.FileIdentity == identity && offset == info.Size() {
			return result, nil
		}
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
	lastCheckpointOffset := offset
	linesSinceCheckpoint := 0
	checkpoint := func(force bool) error {
		if !force && linesSinceCheckpoint < transcriptCursorCheckpointLines &&
			currentOffset-lastCheckpointOffset < transcriptCursorCheckpointBytes {
			return nil
		}
		encodedState, err := json.Marshal(state)
		if err != nil {
			return err
		}
		checkpointObservedAt := observedAt
		if checkpointObservedAt.IsZero() {
			checkpointObservedAt = info.ModTime().UTC()
		}
		if err := t.store.UpsertObservationIngestCursor(ctx, store.ObservationIngestCursor{
			Source: "transcript:" + item.Runtime, Resource: resource, Cursor: string(encodedState),
			MessageID: lastMessageID, FileIdentity: identity, ByteOffset: currentOffset,
			ObservedAt: checkpointObservedAt, UpdatedAt: t.now().UTC(),
		}); err != nil {
			return err
		}
		lastCheckpointOffset = currentOffset
		linesSinceCheckpoint = 0
		return nil
	}
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
		linesSinceCheckpoint++
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			if err := checkpoint(false); err != nil {
				return result, err
			}
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
		sourceDigest := sha256.Sum256(trimmed)
		for index := range envelopes {
			if envelopes[index].Content == nil {
				continue
			}
			envelopes[index].Content.Source = &core.ObservationContentSource{
				Storage: core.ObservationPayloadStorageTranscriptFile,
				Path:    item.Path, Offset: lineStart, Length: int64(len(line)), Identity: identity,
				SHA256: hex.EncodeToString(sourceDigest[:]), Runtime: item.Runtime, Class: item.Class,
			}
		}
		if !timestamp.IsZero() {
			observedAt = timestamp
		}
		if messageID != "" {
			lastMessageID = messageID
		}
		if timestamp.Before(t.now().UTC().Add(-t.metadataBackfill)) {
			result.OldLinesSkipped++
			if err := checkpoint(false); err != nil {
				return result, err
			}
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
		if err := checkpoint(false); err != nil {
			return result, err
		}
	}
	if err := checkpoint(true); err != nil {
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

// ResolvePayloadSource materializes the public content candidates derived from
// one trusted transcript JSONL record. The recorder performs secret redaction
// and selects the candidate matching the persisted content checksum.
func (t *TranscriptTailer) ResolvePayloadSource(ctx context.Context, ref core.ObservationPayloadRef) ([]core.ObservationContent, error) {
	if t == nil {
		return nil, errors.New("transcript tailer unavailable")
	}
	if ref.Storage != core.ObservationPayloadStorageTranscriptFile {
		return nil, fmt.Errorf("unsupported observation payload source %q", ref.Storage)
	}
	path, err := t.trustedTranscriptPath(ref.SourceRuntime, ref.SourcePath)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	line, err := readTranscriptRecord(path, ref.SourceOffset, ref.SourceLength)
	if err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace(line)
	digest := sha256.Sum256(trimmed)
	if !strings.EqualFold(hex.EncodeToString(digest[:]), ref.SourceSHA256) {
		return nil, errors.New("transcript source record checksum mismatch")
	}
	candidates := transcriptContentCandidates(ref.SourceRuntime, ref.SourceClass, trimmed)
	if len(candidates) == 0 {
		return nil, errors.New("transcript source record has no public content")
	}
	return candidates, nil
}

func (t *TranscriptTailer) trustedTranscriptPath(runtime, sourcePath string) (string, error) {
	if !filepath.IsAbs(sourcePath) {
		return "", errors.New("transcript source path must be absolute")
	}
	cacheKey := runtime + "\x00" + filepath.Clean(sourcePath)
	if cached, ok := t.trustedPaths.Load(cacheKey); ok {
		return cached.(string), nil
	}
	var roots []string
	switch runtime {
	case "claude":
		roots = []string{filepath.Join(t.claudeHome, "projects")}
	case "codex":
		roots = []string{filepath.Join(t.codexHome, "sessions"), filepath.Join(t.codexHome, "archived_sessions")}
	default:
		return "", fmt.Errorf("unsupported transcript runtime %q", runtime)
	}
	resolvedPath, err := filepath.EvalSymlinks(filepath.Clean(sourcePath))
	if err != nil {
		return "", fmt.Errorf("resolve transcript source path: %w", err)
	}
	for _, root := range roots {
		resolvedRoot, rootErr := filepath.EvalSymlinks(filepath.Clean(root))
		if rootErr != nil {
			continue
		}
		relative, relErr := filepath.Rel(resolvedRoot, resolvedPath)
		if relErr == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			t.trustedPaths.Store(cacheKey, resolvedPath)
			return resolvedPath, nil
		}
	}
	return "", errors.New("transcript source path is outside trusted roots")
}

// BuildPayloadSource reconstructs a source reference for a legacy transcript
// event whose path and byte offset were already persisted in envelope metadata.
func (t *TranscriptTailer) BuildPayloadSource(ctx context.Context, envelope core.ObservationEnvelope) (*core.ObservationContentSource, error) {
	if t == nil {
		return nil, errors.New("transcript tailer unavailable")
	}
	path := mapString(envelope.Attributes, "transcript_path")
	runtime := mapString(envelope.Attributes, "runtime")
	if runtime == "" {
		runtime = envelope.RuntimeID
	}
	class := mapString(envelope.Attributes, "transcript_class")
	trustedPath, err := t.trustedTranscriptPath(runtime, path)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	line, err := readTranscriptRecord(trustedPath, envelope.Sequence, 0)
	if err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace(line)
	digest := sha256.Sum256(trimmed)
	return &core.ObservationContentSource{
		Storage: core.ObservationPayloadStorageTranscriptFile,
		Path:    trustedPath, Offset: envelope.Sequence, Length: int64(len(line)),
		SHA256: hex.EncodeToString(digest[:]), Runtime: runtime, Class: class,
	}, nil
}

func readTranscriptRecord(path string, offset, length int64) ([]byte, error) {
	if offset < 0 || length < 0 || length > maxTranscriptLineBytes {
		return nil, errors.New("invalid transcript source byte range")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || offset > info.Size() || length > info.Size()-offset {
		return nil, errors.New("transcript source byte range is unavailable")
	}
	if length == 0 {
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			return nil, err
		}
		line, err := bufio.NewReaderSize(file, 256<<10).ReadBytes('\n')
		if len(line) > maxTranscriptLineBytes {
			return nil, fmt.Errorf("transcript line exceeds %d bytes", maxTranscriptLineBytes)
		}
		if err != nil {
			return nil, err
		}
		return line, nil
	}
	line := make([]byte, int(length))
	if _, err := file.ReadAt(line, offset); err != nil {
		return nil, err
	}
	return line, nil
}

func transcriptContentCandidates(runtime, class string, line []byte) []core.ObservationContent {
	var raw map[string]any
	if json.Unmarshal(line, &raw) != nil {
		return nil
	}
	var candidates []core.ObservationContent
	appendCandidate := func(value any) {
		if value == nil {
			return
		}
		if content := jsonObservationContent(value); content != nil {
			candidates = append(candidates, *content)
		}
	}
	if runtime == "claude" {
		if class == "workflow" {
			if result := raw["result"]; result != nil {
				appendCandidate(result)
			}
		}
		message := mapObject(raw, "message")
		typeName := mapString(raw, "type")
		role := firstNonBlank(mapString(message, "role"), typeName)
		if typeName == "user" && !claudeOnlyToolResults(message["content"]) {
			appendCandidate(claudePublicContent(message["content"], false))
		}
		if typeName == "assistant" || role == "assistant" {
			appendCandidate(claudePublicContent(message["content"], true))
		}
		for _, block := range mapArray(message["content"]) {
			switch mapString(block, "type") {
			case "tool_use":
				if input := block["input"]; input != nil {
					appendCandidate(input)
				}
			case "tool_result":
				if output := block["content"]; output != nil {
					appendCandidate(output)
				}
			}
		}
		return candidates
	}
	if runtime != "codex" {
		return nil
	}
	typeName := mapString(raw, "type")
	payload := mapObject(raw, "payload")
	payloadType := mapString(payload, "type")
	if typeName == "event_msg" && payloadType == "user_message" {
		appendCandidate(firstNonNil(payload["message"], payload["text"]))
	}
	if typeName == "event_msg" && (payloadType == "task_complete" || payloadType == "task_completed" || payloadType == "turn_completed") {
		appendCandidate(firstNonNil(payload["last_agent_message"], payload["message"]))
	}
	if typeName != "response_item" {
		return candidates
	}
	switch payloadType {
	case "function_call", "custom_tool_call", "computer_call":
		appendCandidate(firstNonNil(payload["arguments"], payload["input"], payload["action"]))
	case "function_call_output", "custom_tool_call_output", "computer_call_output":
		appendCandidate(firstNonNil(payload["output"], payload["result"]))
	case "message":
		role := mapString(payload, "role")
		if role == "assistant" || role == "user" {
			appendCandidate(codexPublicMessageContent(payload["content"]))
		}
	case "reasoning":
		appendCandidate(payload["summary"])
	}
	return candidates
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
