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

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
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
// CPU core and starving the persistence pool.
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
