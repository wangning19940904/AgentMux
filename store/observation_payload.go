package store

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/agentnexus/agentnexus/core"
)

const (
	defaultObservationContentRetention = 30 * 24 * time.Hour
	defaultObservationDetailRetention  = 180 * 24 * time.Hour
	observationMasterKeySize           = 32
	observationKeychainService         = "AgentNexus Observability Master Key"
	observationPayloadChunkBytes       = 1 << 20
)

var errObservationSecureKeyUnavailable = errors.New("secure observation master key unavailable")

type ObservationRecorderOptions struct {
	CaptureContent   bool
	MasterKey        []byte
	MasterKeyEnv     string
	KeychainAccount  string
	KnownSecrets     []string
	ContentRetention time.Duration
	DetailRetention  time.Duration
	Now              func() time.Time
}

// ObservationRecorder is a bus subscriber backed by SQLite. If secure key
// material is unavailable, it remains operational in metadata-only mode.
type ObservationRecorder struct {
	store            *Store
	masterKey        []byte
	knownSecrets     []string
	captureContent   bool
	metadataReason   string
	contentRetention time.Duration
	detailRetention  time.Duration
	now              func() time.Time
}

func NewObservationRecorder(store *Store, options ObservationRecorderOptions) (*ObservationRecorder, error) {
	if store == nil {
		return nil, errors.New("observation recorder requires a store")
	}
	if options.ContentRetention <= 0 {
		options.ContentRetention = defaultObservationContentRetention
	}
	if options.DetailRetention <= 0 {
		options.DetailRetention = defaultObservationDetailRetention
	}
	if options.Now == nil {
		options.Now = func() time.Time { return time.Now().UTC() }
	}
	recorder := &ObservationRecorder{
		store:            store,
		knownSecrets:     append([]string(nil), options.KnownSecrets...),
		captureContent:   options.CaptureContent,
		contentRetention: options.ContentRetention,
		detailRetention:  options.DetailRetention,
		now:              options.Now,
	}
	if !options.CaptureContent {
		recorder.metadataReason = "content capture disabled"
		return recorder, nil
	}

	masterKey, err := resolveObservationMasterKey(options)
	if err != nil {
		if errors.Is(err, errObservationSecureKeyUnavailable) {
			recorder.captureContent = false
			recorder.metadataReason = err.Error()
			return recorder, nil
		}
		return nil, err
	}
	recorder.masterKey = masterKey
	return recorder, nil
}

func resolveObservationMasterKey(options ObservationRecorderOptions) ([]byte, error) {
	if len(options.MasterKey) > 0 {
		if len(options.MasterKey) != observationMasterKeySize {
			return nil, fmt.Errorf("observation master key must be %d bytes", observationMasterKeySize)
		}
		return append([]byte(nil), options.MasterKey...), nil
	}
	if options.MasterKeyEnv != "" {
		value := strings.TrimSpace(os.Getenv(options.MasterKeyEnv))
		if value == "" {
			return nil, fmt.Errorf("%w: environment variable %s is empty", errObservationSecureKeyUnavailable, options.MasterKeyEnv)
		}
		key, err := decodeObservationMasterKey(value)
		if err != nil {
			return nil, fmt.Errorf("decode observation key from %s: %w", options.MasterKeyEnv, err)
		}
		return key, nil
	}
	account := strings.TrimSpace(options.KeychainAccount)
	if account == "" {
		account = "default"
	}
	return loadOrCreateObservationMasterKey(account)
}

func decodeObservationMasterKey(value string) ([]byte, error) {
	for _, decode := range []func(string) ([]byte, error){base64.StdEncoding.DecodeString, hex.DecodeString} {
		decoded, err := decode(value)
		if err == nil && len(decoded) == observationMasterKeySize {
			return decoded, nil
		}
	}
	if len(value) == observationMasterKeySize {
		return []byte(value), nil
	}
	return nil, fmt.Errorf("expected 32 raw bytes, 64 hex characters, or base64-encoded 32 bytes")
}

func (r *ObservationRecorder) MetadataOnly() bool {
	return r == nil || !r.captureContent || len(r.masterKey) != observationMasterKeySize
}

func (r *ObservationRecorder) MetadataOnlyReason() string {
	if r == nil {
		return "recorder unavailable"
	}
	return r.metadataReason
}

// Observe implements core.ObservationHandler. Plaintext content is removed
// from the envelope before the event is serialized in every code path.
func (r *ObservationRecorder) Observe(ctx context.Context, envelope core.ObservationEnvelope) error {
	_, err := r.Record(ctx, envelope)
	return err
}

// Record secures optional content, persists the envelope, and returns the
// sanitized envelope containing only its encrypted payload reference. The
// returned value is safe to enqueue for metadata-only OTLP export.
func (r *ObservationRecorder) Record(ctx context.Context, envelope core.ObservationEnvelope) (core.ObservationEnvelope, error) {
	if r == nil || r.store == nil {
		envelope.Content = nil
		return envelope, nil
	}
	ingestedAt := r.now().UTC()
	if envelope.Time.IsZero() {
		envelope.Time = ingestedAt
	}
	envelope.Normalize()
	if suppressHiddenReasoningContent(envelope) {
		envelope.Content = nil
		envelope.Attributes = cloneObservationAttributes(envelope.Attributes)
		envelope.Attributes["content_capture"] = "suppressed_hidden_reasoning"
	}
	// Ingest clients must place prompt/response/tool bodies in Content. Enforce
	// that contract centrally so a malformed external Envelope cannot bypass
	// encryption by hiding content or credentials inside Attributes.
	envelope.Attributes = sanitizeObservationAttributes(envelope.Attributes, r.knownSecrets)
	var payloadErr error
	if envelope.Content != nil && len(envelope.Content.Data) > 0 {
		if r.MetadataOnly() {
			envelope.Attributes = cloneObservationAttributes(envelope.Attributes)
			envelope.Attributes["content_capture"] = "metadata_only"
			envelope.Attributes["content_capture_reason"] = r.metadataReason
		} else {
			contentTime := envelope.Time.UTC()
			if contentTime.After(ingestedAt) {
				contentTime = ingestedAt
			}
			if !contentTime.Add(r.contentRetention).After(ingestedAt) {
				envelope.Attributes = cloneObservationAttributes(envelope.Attributes)
				envelope.Attributes["content_capture"] = "expired_before_ingest"
				envelope.Content = nil
				return envelope, r.store.RecordObservation(ctx, envelope)
			}
			ref, err := r.storeObservationPayload(ctx, *envelope.Content, contentTime)
			if err != nil {
				payloadErr = err
				envelope.Attributes = cloneObservationAttributes(envelope.Attributes)
				envelope.Attributes["content_capture"] = "failed"
			} else {
				envelope.PayloadRef = ref
			}
		}
	}
	envelope.Content = nil
	recordErr := r.store.RecordObservation(ctx, envelope)
	return envelope, errors.Join(payloadErr, recordErr)
}

func suppressHiddenReasoningContent(envelope core.ObservationEnvelope) bool {
	if envelope.Content == nil {
		return false
	}
	kind := strings.ToLower(strings.TrimSpace(envelope.Kind))
	if (strings.Contains(kind, "reasoning") || strings.Contains(kind, "thinking") || strings.Contains(kind, "chain_of_thought")) && !strings.Contains(kind, "summary") {
		return true
	}
	for _, key := range []string{"content_visibility", "reasoning_visibility", "content_class"} {
		value := strings.ToLower(fmt.Sprint(envelope.Attributes[key]))
		if strings.Contains(value, "hidden") || strings.Contains(value, "private_reasoning") || strings.Contains(value, "chain_of_thought") {
			return true
		}
	}
	return false
}

func (r *ObservationRecorder) Handler(ctx context.Context, envelope core.ObservationEnvelope) error {
	return r.Observe(ctx, envelope)
}

func (r *ObservationRecorder) storeObservationPayload(ctx context.Context, content core.ObservationContent, contentTime time.Time) (*core.ObservationPayloadRef, error) {
	now := r.now().UTC()
	if contentTime.IsZero() || contentTime.After(now) {
		contentTime = now
	}
	payloadID := "payload_" + core.NewObservationEventID()
	knownSecrets := append(append([]string(nil), r.knownSecrets...), content.KnownSecrets...)
	redacted, changed := RedactObservationContent(content.Data, knownSecrets)
	keyID := contentTime.Format("2006-01-02")
	dailyKey, err := r.loadOrCreateObservationDataKey(ctx, keyID, contentTime)
	if err != nil {
		return nil, err
	}
	defer clearObservationBytes(dailyKey)
	block, err := aes.NewCipher(dailyKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	contentType := content.ContentType
	if contentType == "" {
		contentType = "text/plain; charset=utf-8"
	}
	type encryptedChunk struct {
		index         int
		nonce         []byte
		ciphertext    []byte
		originalBytes int
	}
	chunks := make([]encryptedChunk, 0, (len(redacted)+observationPayloadChunkBytes-1)/observationPayloadChunkBytes)
	storedBytes := 0
	for offset, index := 0, 0; offset < len(redacted); offset, index = offset+observationPayloadChunkBytes, index+1 {
		end := offset + observationPayloadChunkBytes
		if end > len(redacted) {
			end = len(redacted)
		}
		compressed, err := gzipObservationPayload(redacted[offset:end])
		if err != nil {
			return nil, err
		}
		nonce := make([]byte, aead.NonceSize())
		if _, err := rand.Read(nonce); err != nil {
			return nil, err
		}
		ciphertext := aead.Seal(nil, nonce, compressed, observationPayloadChunkAAD(payloadID, keyID, contentType, index))
		chunks = append(chunks, encryptedChunk{index: index, nonce: nonce, ciphertext: ciphertext, originalBytes: end - offset})
		storedBytes += len(ciphertext)
	}
	digest := sha256.Sum256(redacted)
	expiresAt := contentTime.Add(r.contentRetention)
	tx, err := r.store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `INSERT INTO observation_payloads
		(payload_id,key_id,content_type,compression,encryption,nonce,ciphertext,sha256,original_bytes,stored_bytes,redacted,created_at,expires_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, payloadID, keyID, contentType, "gzip-chunks", "AES-256-GCM", []byte{}, []byte{},
		hex.EncodeToString(digest[:]), len(redacted), storedBytes, changed, observationTime(now), observationTime(expiresAt))
	if err != nil {
		return nil, fmt.Errorf("store encrypted observation payload: %w", err)
	}
	for _, chunk := range chunks {
		if _, err := tx.ExecContext(ctx, `INSERT INTO observation_payload_chunks
			(payload_id,chunk_index,nonce,ciphertext,original_bytes,stored_bytes) VALUES(?,?,?,?,?,?)`,
			payloadID, chunk.index, chunk.nonce, chunk.ciphertext, chunk.originalBytes, len(chunk.ciphertext)); err != nil {
			return nil, fmt.Errorf("store encrypted observation payload chunk: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &core.ObservationPayloadRef{
		ID: payloadID, ContentType: contentType, KeyID: keyID, OriginalBytes: int64(len(redacted)),
		StoredBytes: int64(storedBytes), Redacted: changed, ExpiresAt: expiresAt,
	}, nil
}

func (r *ObservationRecorder) loadOrCreateObservationDataKey(ctx context.Context, keyID string, now time.Time) ([]byte, error) {
	var wrapNonce, wrappedKey []byte
	err := r.store.db.QueryRowContext(ctx, `SELECT wrap_nonce,wrapped_key FROM observation_data_keys WHERE key_id=?`, keyID).Scan(&wrapNonce, &wrappedKey)
	if err == nil {
		return unwrapObservationDataKey(r.masterKey, keyID, wrapNonce, wrappedKey)
	}
	if err != sql.ErrNoRows {
		return nil, err
	}
	dailyKey := make([]byte, observationMasterKeySize)
	if _, err := rand.Read(dailyKey); err != nil {
		return nil, err
	}
	wrapNonce, wrappedKey, err = wrapObservationDataKey(r.masterKey, keyID, dailyKey)
	if err != nil {
		clearObservationBytes(dailyKey)
		return nil, err
	}
	// Retain the wrapped key slightly longer than content, then remove it only
	// after no payload rows reference it.
	expiresAt := now.Add(r.contentRetention + 24*time.Hour)
	result, err := r.store.db.ExecContext(ctx, `INSERT OR IGNORE INTO observation_data_keys
		(key_id,wrap_nonce,wrapped_key,created_at,expires_at) VALUES(?,?,?,?,?)`, keyID, wrapNonce, wrappedKey,
		observationTime(now), observationTime(expiresAt))
	if err != nil {
		clearObservationBytes(dailyKey)
		return nil, err
	}
	inserted, _ := result.RowsAffected()
	if inserted == 1 {
		return dailyKey, nil
	}
	clearObservationBytes(dailyKey)
	if err := r.store.db.QueryRowContext(ctx, `SELECT wrap_nonce,wrapped_key FROM observation_data_keys WHERE key_id=?`, keyID).Scan(&wrapNonce, &wrappedKey); err != nil {
		return nil, err
	}
	return unwrapObservationDataKey(r.masterKey, keyID, wrapNonce, wrappedKey)
}

func wrapObservationDataKey(masterKey []byte, keyID string, dataKey []byte) ([]byte, []byte, error) {
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	return nonce, aead.Seal(nil, nonce, dataKey, []byte(keyID)), nil
}

func unwrapObservationDataKey(masterKey []byte, keyID string, nonce, wrapped []byte) ([]byte, error) {
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	key, err := aead.Open(nil, nonce, wrapped, []byte(keyID))
	if err != nil {
		return nil, fmt.Errorf("unwrap observation data key %s: %w", keyID, err)
	}
	if len(key) != observationMasterKeySize {
		clearObservationBytes(key)
		return nil, fmt.Errorf("observation data key %s has invalid length", keyID)
	}
	return key, nil
}

// ReadPayload decrypts a payload for an authenticated local caller. It never
// returns ciphertext as if it were plaintext and fails in metadata-only mode.
func (r *ObservationRecorder) ReadPayload(ctx context.Context, payloadID string) ([]byte, string, error) {
	if r == nil || r.MetadataOnly() {
		return nil, "", errObservationSecureKeyUnavailable
	}
	var keyID, contentType, compression, encryption, digest string
	var nonce, ciphertext []byte
	err := r.store.db.QueryRowContext(ctx, `SELECT key_id,content_type,compression,encryption,nonce,ciphertext,sha256
		FROM observation_payloads WHERE payload_id=?`, payloadID).Scan(&keyID, &contentType, &compression, &encryption, &nonce, &ciphertext, &digest)
	if err != nil {
		return nil, "", err
	}
	if (compression != "gzip" && compression != "gzip-chunks") || encryption != "AES-256-GCM" {
		return nil, "", fmt.Errorf("unsupported payload encoding %s/%s", compression, encryption)
	}
	dailyKey, err := r.loadObservationDataKey(ctx, keyID)
	if err != nil {
		return nil, "", err
	}
	defer clearObservationBytes(dailyKey)
	block, err := aes.NewCipher(dailyKey)
	if err != nil {
		return nil, "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, "", err
	}
	var plaintext []byte
	if compression == "gzip" {
		compressed, err := aead.Open(nil, nonce, ciphertext, observationPayloadAAD(payloadID, keyID, contentType))
		if err != nil {
			return nil, "", fmt.Errorf("decrypt observation payload: %w", err)
		}
		plaintext, err = gunzipObservationPayload(compressed)
		if err != nil {
			return nil, "", err
		}
	} else {
		rows, err := r.store.db.QueryContext(ctx, `SELECT chunk_index,nonce,ciphertext FROM observation_payload_chunks
			WHERE payload_id=? ORDER BY chunk_index`, payloadID)
		if err != nil {
			return nil, "", err
		}
		defer rows.Close()
		expectedIndex := 0
		for rows.Next() {
			var index int
			var chunkNonce, chunkCiphertext []byte
			if err := rows.Scan(&index, &chunkNonce, &chunkCiphertext); err != nil {
				return nil, "", err
			}
			if index != expectedIndex {
				return nil, "", fmt.Errorf("observation payload chunk sequence is incomplete at %d", expectedIndex)
			}
			compressed, err := aead.Open(nil, chunkNonce, chunkCiphertext, observationPayloadChunkAAD(payloadID, keyID, contentType, index))
			if err != nil {
				return nil, "", fmt.Errorf("decrypt observation payload chunk %d: %w", index, err)
			}
			chunk, err := gunzipObservationPayload(compressed)
			if err != nil {
				return nil, "", err
			}
			plaintext = append(plaintext, chunk...)
			expectedIndex++
		}
		if err := rows.Err(); err != nil {
			return nil, "", err
		}
		if expectedIndex == 0 {
			return nil, "", errors.New("observation payload has no encrypted chunks")
		}
	}
	hash := sha256.Sum256(plaintext)
	if !strings.EqualFold(hex.EncodeToString(hash[:]), digest) {
		clearObservationBytes(plaintext)
		return nil, "", errors.New("observation payload checksum mismatch")
	}
	return plaintext, contentType, nil
}

func (r *ObservationRecorder) loadObservationDataKey(ctx context.Context, keyID string) ([]byte, error) {
	var nonce, wrapped []byte
	if err := r.store.db.QueryRowContext(ctx, `SELECT wrap_nonce,wrapped_key FROM observation_data_keys WHERE key_id=?`, keyID).Scan(&nonce, &wrapped); err != nil {
		return nil, err
	}
	return unwrapObservationDataKey(r.masterKey, keyID, nonce, wrapped)
}

func observationPayloadAAD(payloadID, keyID, contentType string) []byte {
	return []byte(payloadID + "\x00" + keyID + "\x00" + contentType)
}

func observationPayloadChunkAAD(payloadID, keyID, contentType string, index int) []byte {
	return []byte(fmt.Sprintf("%s\x00%s\x00%s\x00chunk:%d", payloadID, keyID, contentType, index))
}

func gzipObservationPayload(data []byte) ([]byte, error) {
	var output bytes.Buffer
	writer, err := gzip.NewWriterLevel(&output, gzip.BestSpeed)
	if err != nil {
		return nil, err
	}
	if _, err := writer.Write(data); err != nil {
		_ = writer.Close()
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func gunzipObservationPayload(data []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}

var (
	observationSensitiveHeader = regexp.MustCompile(`(?im)^(\s*(?:authorization|proxy-authorization|cookie|set-cookie|x-api-key)\s*[:=]\s*)([^\r\n]+)$`)
	observationBearerToken     = regexp.MustCompile(`(?i)\bbearer\s+[a-z0-9._~+\-/=]+`)
	observationQuerySecret     = regexp.MustCompile(`(?i)(\b(?:api[_-]?key|access[_-]?token|auth[_-]?token|password|secret)\s*[=:]\s*)([^\s&;,}\]]+)`)
	observationKnownToken      = regexp.MustCompile(`\b(?:sk-(?:ant-)?[A-Za-z0-9_-]{12,}|gh[pousr]_[A-Za-z0-9_]{12,}|xox[baprs]-[A-Za-z0-9-]{12,})\b`)
	observationSensitiveKey    = regexp.MustCompile(`(?i)^(?:authorization|proxy[_-]?authorization|cookie|set[_-]?cookie|x[_-]?api[_-]?key|api[_-]?key|access[_-]?token|auth[_-]?token|refresh[_-]?token|password|passwd|secret|client[_-]?secret|private[_-]?key|hidden[_-]?reasoning|chain[_-]?of[_-]?thought|reasoning[_-]?content|encrypted[_-]?content)$`)
)

// RedactObservationContent removes known credentials from JSON or free text.
// The returned bytes are safe to encrypt and the boolean reports whether a
// replacement occurred.
func RedactObservationContent(input []byte, knownSecrets []string) ([]byte, bool) {
	output := append([]byte(nil), input...)
	changed := false
	var value any
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.UseNumber()
	if json.Valid(input) && decoder.Decode(&value) == nil {
		if redactObservationJSON(&value) {
			changed = true
		}
		if encoded, err := json.Marshal(value); err == nil {
			output = encoded
		}
	}
	output, changed = replaceObservationRegex(output, observationSensitiveHeader, []byte("${1}[REDACTED]"), changed)
	output, changed = replaceObservationRegex(output, observationBearerToken, []byte("Bearer [REDACTED]"), changed)
	output, changed = replaceObservationRegex(output, observationQuerySecret, []byte("${1}[REDACTED]"), changed)
	output, changed = replaceObservationRegex(output, observationKnownToken, []byte("[REDACTED]"), changed)

	secrets := append([]string(nil), knownSecrets...)
	sort.SliceStable(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, secret := range secrets {
		if secret = strings.TrimSpace(secret); secret == "" {
			continue
		}
		replaced := bytes.ReplaceAll(output, []byte(secret), []byte("[REDACTED]"))
		if !bytes.Equal(replaced, output) {
			changed = true
			output = replaced
		}
	}
	return output, changed
}

func redactObservationJSON(value *any) bool {
	changed := false
	switch current := (*value).(type) {
	case map[string]any:
		for key, child := range current {
			if observationSensitiveKey.MatchString(key) {
				alreadyRedacted, _ := child.(string)
				if child != nil && alreadyRedacted != "[REDACTED]" {
					current[key] = "[REDACTED]"
					changed = true
				}
				continue
			}
			if redactObservationJSON(&child) {
				current[key] = child
				changed = true
			}
		}
	case []any:
		for index, child := range current {
			if redactObservationJSON(&child) {
				current[index] = child
				changed = true
			}
		}
	}
	return changed
}

func replaceObservationRegex(input []byte, expression *regexp.Regexp, replacement []byte, changed bool) ([]byte, bool) {
	output := expression.ReplaceAll(input, replacement)
	return output, changed || !bytes.Equal(input, output)
}

func cloneObservationAttributes(input map[string]any) map[string]any {
	output := make(map[string]any, len(input)+2)
	for key, value := range input {
		output[key] = value
	}
	return output
}

func sanitizeObservationAttributes(input map[string]any, knownSecrets []string) map[string]any {
	if len(input) == 0 {
		return input
	}
	prepared, _ := sanitizeObservationAttributeValue(input).(map[string]any)
	raw, err := json.Marshal(prepared)
	if err != nil {
		return map[string]any{"attributes_sanitized": true}
	}
	redacted, _ := RedactObservationContent(raw, knownSecrets)
	var output map[string]any
	decoder := json.NewDecoder(bytes.NewReader(redacted))
	decoder.UseNumber()
	if decoder.Decode(&output) != nil {
		return map[string]any{"attributes_sanitized": true}
	}
	return output
}

func sanitizeObservationAttributeValue(value any) any {
	switch current := value.(type) {
	case map[string]any:
		output := make(map[string]any, len(current))
		for key, child := range current {
			if observationContentAttributeKey(key) {
				output[key] = "[REDACTED_CONTENT]"
				continue
			}
			output[key] = sanitizeObservationAttributeValue(child)
		}
		return output
	case map[string]string:
		output := make(map[string]any, len(current))
		for key, child := range current {
			if observationContentAttributeKey(key) {
				output[key] = "[REDACTED_CONTENT]"
			} else {
				output[key] = child
			}
		}
		return output
	case []any:
		output := make([]any, len(current))
		for index, child := range current {
			output[index] = sanitizeObservationAttributeValue(child)
		}
		return output
	default:
		return current
	}
}

func observationContentAttributeKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	key = strings.NewReplacer(".", "_", "-", "_").Replace(key)
	if strings.Contains(key, "prompt") || (strings.HasSuffix(key, "_content") && key != "content_type") {
		return true
	}
	switch key {
	case "content", "input", "output", "result", "message", "body", "request_body", "response_body",
		"tool_input", "tool_output", "tool_result", "tool_parameters", "assistant_response", "user_message",
		"full_command", "error_detail", "raw", "raw_api_body", "raw_api_bodies":
		return true
	default:
		return false
	}
}

func clearObservationBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

type ObservationCleanupResult struct {
	Payloads int64
	Events   int64
	Spans    int64
	Traces   int64
	DataKeys int64
	Outbox   int64
}

func (r *ObservationRecorder) Cleanup(ctx context.Context, now time.Time) (ObservationCleanupResult, error) {
	if r == nil || r.store == nil {
		return ObservationCleanupResult{}, nil
	}
	if now.IsZero() {
		now = r.now()
	}
	return r.store.CleanupObservationRetention(ctx, now, r.detailRetention)
}

func (s *Store) CleanupObservationRetention(ctx context.Context, now time.Time, detailRetention time.Duration) (ObservationCleanupResult, error) {
	if detailRetention <= 0 {
		detailRetention = defaultObservationDetailRetention
	}
	cutoff := observationTime(now.UTC().Add(-detailRetention))
	nowText := observationTime(now.UTC())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ObservationCleanupResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var result ObservationCleanupResult
	if _, err := tx.ExecContext(ctx, `DELETE FROM observation_payload_chunks WHERE payload_id IN
		(SELECT payload_id FROM observation_payloads WHERE expires_at<=?)`, nowText); err != nil {
		return ObservationCleanupResult{}, err
	}
	deletions := []struct {
		query string
		args  []any
		count *int64
	}{
		{`DELETE FROM observation_payloads WHERE expires_at<=?`, []any{nowText}, &result.Payloads},
		{`DELETE FROM observation_events WHERE timestamp<?`, []any{cutoff}, &result.Events},
		{`DELETE FROM observation_spans WHERE COALESCE(NULLIF(ended_at,''),started_at)<?`, []any{cutoff}, &result.Spans},
		{`DELETE FROM observation_traces WHERE COALESCE(NULLIF(ended_at,''),started_at)<?`, []any{cutoff}, &result.Traces},
		{`DELETE FROM observation_export_outbox WHERE status IN ('sent','discarded') AND updated_at<?`, []any{cutoff}, &result.Outbox},
		{`DELETE FROM observation_data_keys WHERE expires_at<=? AND NOT EXISTS
			(SELECT 1 FROM observation_payloads WHERE observation_payloads.key_id=observation_data_keys.key_id)`, []any{nowText}, &result.DataKeys},
	}
	for _, deletion := range deletions {
		execResult, err := tx.ExecContext(ctx, deletion.query, deletion.args...)
		if err != nil {
			return ObservationCleanupResult{}, err
		}
		*deletion.count, _ = execResult.RowsAffected()
	}
	if err := tx.Commit(); err != nil {
		return ObservationCleanupResult{}, err
	}
	return result, nil
}
