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
	"os"
	"path/filepath"
	"strings"

	"github.com/wangning19940904/AgentMux/core"
)

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
