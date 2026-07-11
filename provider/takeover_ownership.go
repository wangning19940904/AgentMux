package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/agentnexus/agentnexus/store"
)

const takeoverOwnershipVersion = 2

type ownedLiveValue struct {
	Pointer      string          `json:"pointer"`
	Before       json.RawMessage `json:"before,omitempty"`
	BeforeExists bool            `json:"before_exists"`
	After        json.RawMessage `json:"after,omitempty"`
	AfterExists  bool            `json:"after_exists"`
}

type ownedLiveFile struct {
	Format       string           `json:"format"`
	BeforeHash   string           `json:"before_hash"`
	BeforeExists bool             `json:"before_exists"`
	AfterHash    string           `json:"after_hash,omitempty"`
	Finalized    bool             `json:"finalized,omitempty"`
	Values       []ownedLiveValue `json:"values,omitempty"`
}

type liveFileState struct {
	Raw    []byte
	Exists bool
}

// TakeoverDriftError reports shared config values that no longer match the
// AgentNexus-owned fingerprint. Those values are deliberately preserved.
type TakeoverDriftError struct {
	Paths []string
}

func (e *TakeoverDriftError) Error() string {
	return "takeover config drift detected; preserved third-party values: " + strings.Join(e.Paths, ", ")
}

func makeLiveOwnershipSnapshot(files []string) (string, error) {
	blob := liveBackupBlob{
		Version:   takeoverOwnershipVersion,
		InstallID: randomProxyID("takeover-"),
		Files:     map[string]*string{},
		Ownership: map[string]ownedLiveFile{},
	}
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			blob.Files[path] = nil
			blob.Ownership[path] = ownedLiveFile{Format: liveFileFormat(path), BeforeHash: hashLiveContent(nil, false), BeforeExists: false}
			continue
		}
		if err != nil {
			return "", err
		}
		content := string(raw)
		blob.Files[path] = &content
		blob.Ownership[path] = ownedLiveFile{Format: liveFileFormat(path), BeforeHash: hashLiveContent(raw, true), BeforeExists: true}
	}
	raw, err := json.Marshal(blob)
	return string(raw), err
}

func finalizeLiveOwnershipSnapshot(blobRaw string) (string, error) {
	var blob liveBackupBlob
	if err := json.Unmarshal([]byte(blobRaw), &blob); err != nil {
		return "", fmt.Errorf("corrupt live backup: %w", err)
	}
	if blob.Ownership == nil {
		blob.Ownership = map[string]ownedLiveFile{}
	}
	blob.Version = takeoverOwnershipVersion
	if blob.InstallID == "" {
		blob.InstallID = randomProxyID("takeover-")
	}
	for path, before := range blob.Files {
		current, exists, err := readOptionalFile(path)
		if err != nil {
			return "", err
		}
		entry := blob.Ownership[path]
		entry.Format = liveFileFormat(path)
		entry.BeforeExists = before != nil
		entry.AfterHash = hashLiveContent(current, exists)
		entry.Finalized = true
		beforeRaw, beforeExists := []byte(nil), before != nil
		if before != nil {
			beforeRaw = []byte(*before)
		}
		beforeDoc, err := decodeLiveDocument(entry.Format, beforeRaw, beforeExists)
		if err != nil {
			return "", fmt.Errorf("decode before %s: %w", path, err)
		}
		afterDoc, err := decodeLiveDocument(entry.Format, current, exists)
		if err != nil {
			return "", fmt.Errorf("decode after %s: %w", path, err)
		}
		entry.Values = diffLiveValues("", beforeDoc, beforeExists, afterDoc, exists)
		blob.Ownership[path] = entry
	}
	// Full file images are needed only during the before-write crash window.
	// Once pointer fingerprints are finalized, discard them permanently so the
	// durable journal contains only values AgentNexus actually owns.
	blob.Files = nil
	raw, err := json.Marshal(blob)
	return string(raw), err
}

// extendLiveOwnershipSnapshot adds files which were not part of the original
// takeover target (for example when a Claude Desktop provider uses a different
// profile root). Their current bytes become the before image before any write.
func extendLiveOwnershipSnapshot(blobRaw string, files []string) (string, error) {
	var blob liveBackupBlob
	if err := json.Unmarshal([]byte(blobRaw), &blob); err != nil {
		return "", fmt.Errorf("corrupt live backup: %w", err)
	}
	if blob.Version < takeoverOwnershipVersion || blob.Ownership == nil {
		return "", errors.New("takeover ownership journal has no per-key fingerprints; disable or repair takeover before rewriting it")
	}
	if blob.Files == nil {
		blob.Files = map[string]*string{}
	}
	for _, path := range files {
		if _, exists := blob.Ownership[path]; exists {
			continue
		}
		raw, exists, err := readOptionalFile(path)
		if err != nil {
			return "", err
		}
		if exists {
			content := string(raw)
			blob.Files[path] = &content
		} else {
			blob.Files[path] = nil
		}
		blob.Ownership[path] = ownedLiveFile{
			Format:       liveFileFormat(path),
			BeforeHash:   hashLiveContent(raw, exists),
			BeforeExists: exists,
		}
	}
	raw, err := json.Marshal(blob)
	return string(raw), err
}

func captureLiveFileStates(files []string) (map[string]liveFileState, error) {
	states := make(map[string]liveFileState, len(files))
	for _, path := range files {
		raw, exists, err := readOptionalFile(path)
		if err != nil {
			return nil, err
		}
		states[path] = liveFileState{Raw: raw, Exists: exists}
	}
	return states, nil
}

// validateLiveOwnershipSnapshot is the compare step of the takeover CAS. A
// third party may freely change unowned keys, but a value whose current bytes
// no longer match our recorded after fingerprint is drift and must not be
// overwritten by a repair or provider hot-switch.
func validateLiveOwnershipSnapshot(blobRaw string) error {
	var blob liveBackupBlob
	if err := json.Unmarshal([]byte(blobRaw), &blob); err != nil {
		return fmt.Errorf("corrupt live backup: %w", err)
	}
	if blob.Version < takeoverOwnershipVersion || len(blob.Ownership) == 0 {
		return errors.New("takeover ownership journal has no per-key fingerprints; disable or repair takeover before rewriting it")
	}
	var drift []string
	paths := make([]string, 0, len(blob.Ownership))
	for path := range blob.Ownership {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		entry := blob.Ownership[path]
		current, exists, err := readOptionalFile(path)
		if err != nil {
			return err
		}
		if entry.AfterHash != "" && hashLiveContent(current, exists) == entry.AfterHash {
			continue
		}
		doc, err := decodeLiveDocument(entry.Format, current, exists)
		if err != nil {
			drift = append(drift, path+" (invalid current document)")
			continue
		}
		for _, value := range entry.Values {
			actual, actualExists := livePointerGet(doc, value.Pointer)
			if !liveValueEqual(actual, actualExists, value.After, value.AfterExists) {
				drift = append(drift, path+value.Pointer)
			}
		}
	}
	if len(drift) > 0 {
		sort.Strings(drift)
		return &TakeoverDriftError{Paths: drift}
	}
	return nil
}

// updateLiveOwnershipAfterRewrite advances only the pointers changed by the
// just-completed managed rewrite. It does not recompute a whole-file diff from
// the original snapshot: doing that would accidentally claim unrelated Flux,
// CC Switch, or user keys added while takeover was active.
func updateLiveOwnershipAfterRewrite(blobRaw string, before map[string]liveFileState, files []string) (string, error) {
	var blob liveBackupBlob
	if err := json.Unmarshal([]byte(blobRaw), &blob); err != nil {
		return "", fmt.Errorf("corrupt live backup: %w", err)
	}
	if blob.Version < takeoverOwnershipVersion || blob.Ownership == nil {
		return "", errors.New("takeover ownership journal has no per-key fingerprints")
	}
	for _, path := range files {
		beforeState, ok := before[path]
		if !ok {
			return "", fmt.Errorf("missing pre-write state for %s", path)
		}
		afterRaw, afterExists, err := readOptionalFile(path)
		if err != nil {
			return "", err
		}
		entry, exists := blob.Ownership[path]
		if !exists {
			return "", fmt.Errorf("takeover ownership journal does not cover %s", path)
		}
		beforeDoc, err := decodeLiveDocument(entry.Format, beforeState.Raw, beforeState.Exists)
		if err != nil {
			return "", fmt.Errorf("decode pre-write %s: %w", path, err)
		}
		afterDoc, err := decodeLiveDocument(entry.Format, afterRaw, afterExists)
		if err != nil {
			return "", fmt.Errorf("decode post-write %s: %w", path, err)
		}
		changes := diffLiveValues("", beforeDoc, beforeState.Exists, afterDoc, afterExists)

		// Whole-file exact restore is safe only when the pre-write file still
		// matched the previous complete after image, or this file was newly
		// enrolled and had no prior after image. Otherwise unowned drift must be
		// preserved by the pointer-by-pointer restore path.
		exactRestoreSafe := !entry.Finalized && entry.AfterHash == "" && len(entry.Values) == 0
		if entry.AfterHash != "" && hashLiveContent(beforeState.Raw, beforeState.Exists) == entry.AfterHash {
			exactRestoreSafe = true
		}

		for index := range entry.Values {
			value := &entry.Values[index]
			prior, priorExists := livePointerGet(beforeDoc, value.Pointer)
			current, currentExists := livePointerGet(afterDoc, value.Pointer)
			if liveAnyEqual(prior, priorExists, current, currentExists) {
				continue
			}
			value.After, _ = json.Marshal(current)
			value.AfterExists = currentExists
		}
		for _, change := range changes {
			owned := false
			for _, value := range entry.Values {
				if livePointerOwns(value.Pointer, change.Pointer) {
					owned = true
					break
				}
			}
			if !owned {
				entry.Values = append(entry.Values, change)
			}
		}
		sort.Slice(entry.Values, func(i, j int) bool { return entry.Values[i].Pointer < entry.Values[j].Pointer })
		if exactRestoreSafe {
			entry.AfterHash = hashLiveContent(afterRaw, afterExists)
		} else {
			entry.AfterHash = ""
		}
		entry.Finalized = true
		blob.Ownership[path] = entry
		delete(blob.Files, path)
	}
	if len(blob.Files) == 0 {
		blob.Files = nil
	}
	raw, err := json.Marshal(blob)
	return string(raw), err
}

func livePointerOwns(owner, candidate string) bool {
	return owner == "" || owner == candidate || strings.HasPrefix(candidate, owner+"/")
}

func liveBackupPaths(blobRaw string) ([]string, error) {
	var blob liveBackupBlob
	if err := json.Unmarshal([]byte(blobRaw), &blob); err != nil {
		return nil, fmt.Errorf("corrupt live backup: %w", err)
	}
	files := blob.Files
	if blob.Version >= takeoverOwnershipVersion && len(blob.Ownership) > 0 {
		files = make(map[string]*string, len(blob.Ownership))
		for path := range blob.Ownership {
			files[path] = nil
		}
	}
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}

func restoreOwnedLiveFiles(blobRaw string) error {
	var blob liveBackupBlob
	if err := json.Unmarshal([]byte(blobRaw), &blob); err != nil {
		return fmt.Errorf("corrupt live backup: %w", err)
	}
	// Version-one backups predate per-key fingerprints. Retain the legacy
	// restore path so existing installations can migrate once without data loss.
	if blob.Version < takeoverOwnershipVersion || len(blob.Ownership) == 0 {
		return restoreLegacyLiveFiles(blob)
	}
	var drift []string
	paths := make([]string, 0, len(blob.Ownership))
	for path := range blob.Ownership {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		entry := blob.Ownership[path]
		current, exists, err := readOptionalFile(path)
		if err != nil {
			return err
		}
		// The before-only journal is persisted before a shared-file rewrite.
		// If the process crashes before fingerprints are finalized, exact restore
		// is the only recovery path and is safe regardless of how far the write got.
		if !entry.Finalized && entry.AfterHash == "" && len(entry.Values) == 0 {
			before, ok := blob.Files[path]
			if !ok {
				return fmt.Errorf("takeover crash journal is missing before image for %s", path)
			}
			if err := restoreExactLiveFile(path, before); err != nil {
				return err
			}
			continue
		}
		doc, err := decodeLiveDocument(entry.Format, current, exists)
		if err != nil {
			drift = append(drift, path+" (invalid current document)")
			continue
		}
		changed := false
		for _, value := range entry.Values {
			actual, actualExists := livePointerGet(doc, value.Pointer)
			if !liveValueEqual(actual, actualExists, value.After, value.AfterExists) {
				drift = append(drift, path+value.Pointer)
				continue
			}
			beforeValue, err := decodeOwnedValue(value.Before, value.BeforeExists)
			if err != nil {
				return err
			}
			if err := livePointerSet(doc, value.Pointer, beforeValue, value.BeforeExists); err != nil {
				return err
			}
			changed = true
		}
		if changed {
			object, _ := doc.(map[string]any)
			if !entry.BeforeExists && len(object) == 0 {
				if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
					return err
				}
			} else if err := writeLiveDocument(path, entry.Format, doc); err != nil {
				return err
			}
		}
	}
	if len(drift) > 0 {
		sort.Strings(drift)
		return &TakeoverDriftError{Paths: drift}
	}
	return nil
}

func restoreLegacyLiveFiles(blob liveBackupBlob) error {
	for path, content := range blob.Files {
		if err := restoreExactLiveFile(path, content); err != nil {
			return err
		}
	}
	return nil
}

func restoreExactLiveFile(path string, content *string) error {
	if content == nil {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return store.AtomicWrite(path, []byte(*content), 0o600)
}

func readOptionalFile(path string) ([]byte, bool, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	return raw, err == nil, err
}

func liveFileFormat(path string) string {
	if strings.EqualFold(filepath.Ext(path), ".toml") {
		return "toml"
	}
	return "json"
}

func decodeLiveDocument(format string, raw []byte, exists bool) (any, error) {
	if !exists || len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{}, nil
	}
	var out map[string]any
	if format == "toml" {
		if _, err := toml.Decode(string(raw), &out); err != nil {
			return nil, err
		}
	} else if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

func writeLiveDocument(path, format string, doc any) error {
	object, _ := doc.(map[string]any)
	if object == nil {
		object = map[string]any{}
	}
	if format == "toml" {
		var buf bytes.Buffer
		if err := toml.NewEncoder(&buf).Encode(object); err != nil {
			return err
		}
		return store.AtomicWrite(path, buf.Bytes(), 0o600)
	}
	raw, err := json.MarshalIndent(object, "", "  ")
	if err != nil {
		return err
	}
	return store.AtomicWrite(path, raw, 0o600)
}

func diffLiveValues(pointer string, before any, beforeExists bool, after any, afterExists bool) []ownedLiveValue {
	beforeMap, beforeMapOK := before.(map[string]any)
	afterMap, afterMapOK := after.(map[string]any)
	if beforeMapOK && afterMapOK {
		keys := map[string]bool{}
		for key := range beforeMap {
			keys[key] = true
		}
		for key := range afterMap {
			keys[key] = true
		}
		ordered := make([]string, 0, len(keys))
		for key := range keys {
			ordered = append(ordered, key)
		}
		sort.Strings(ordered)
		var out []ownedLiveValue
		for _, key := range ordered {
			bv, bok := beforeMap[key]
			av, aok := afterMap[key]
			out = append(out, diffLiveValues(pointer+"/"+escapeJSONPointer(key), bv, bok, av, aok)...)
		}
		return out
	}
	if liveAnyEqual(before, beforeExists, after, afterExists) {
		return nil
	}
	braw, _ := json.Marshal(before)
	araw, _ := json.Marshal(after)
	return []ownedLiveValue{{
		Pointer: pointer, Before: braw, BeforeExists: beforeExists,
		After: araw, AfterExists: afterExists,
	}}
}

func liveAnyEqual(left any, leftExists bool, right any, rightExists bool) bool {
	if leftExists != rightExists {
		return false
	}
	if !leftExists {
		return true
	}
	leftRaw, _ := json.Marshal(left)
	rightRaw, _ := json.Marshal(right)
	return bytes.Equal(leftRaw, rightRaw)
}

func liveValueEqual(actual any, actualExists bool, expected json.RawMessage, expectedExists bool) bool {
	value, err := decodeOwnedValue(expected, expectedExists)
	return err == nil && liveAnyEqual(actual, actualExists, value, expectedExists)
}

func decodeOwnedValue(raw json.RawMessage, exists bool) (any, error) {
	if !exists {
		return nil, nil
	}
	var value any
	if len(raw) == 0 {
		return nil, nil
	}
	return value, json.Unmarshal(raw, &value)
}

func livePointerGet(doc any, pointer string) (any, bool) {
	if pointer == "" {
		return doc, true
	}
	current := doc
	for _, part := range pointerParts(pointer) {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func livePointerSet(doc any, pointer string, value any, exists bool) error {
	parts := pointerParts(pointer)
	if len(parts) == 0 {
		return errors.New("cannot replace live config document root")
	}
	current, ok := doc.(map[string]any)
	if !ok {
		return errors.New("live config root is not an object")
	}
	for _, part := range parts[:len(parts)-1] {
		next, _ := current[part].(map[string]any)
		if next == nil {
			next = map[string]any{}
			current[part] = next
		}
		current = next
	}
	key := parts[len(parts)-1]
	if !exists {
		delete(current, key)
	} else {
		current[key] = value
	}
	return nil
}

func pointerParts(pointer string) []string {
	pointer = strings.TrimPrefix(pointer, "/")
	if pointer == "" {
		return nil
	}
	raw := strings.Split(pointer, "/")
	for i := range raw {
		raw[i] = strings.ReplaceAll(strings.ReplaceAll(raw[i], "~1", "/"), "~0", "~")
	}
	return raw
}

func escapeJSONPointer(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "~", "~0"), "/", "~1")
}

func hashLiveContent(raw []byte, exists bool) string {
	prefix := []byte("missing\x00")
	if exists {
		prefix = []byte("present\x00")
	}
	sum := sha256.Sum256(append(prefix, raw...))
	return hex.EncodeToString(sum[:])
}

func withLiveFileLocks(ctx context.Context, files []string, fn func() error) error {
	paths := append([]string(nil), files...)
	sort.Strings(paths)
	unique := paths[:0]
	for _, path := range paths {
		if len(unique) == 0 || unique[len(unique)-1] != path {
			unique = append(unique, path)
		}
	}
	locks := make([]*liveFileLock, 0, len(unique))
	for _, path := range unique {
		lock, err := acquireLiveFileLock(ctx, path+".agentnexus.lock")
		if err != nil {
			for i := len(locks) - 1; i >= 0; i-- {
				_ = locks[i].Close()
			}
			return err
		}
		locks = append(locks, lock)
	}
	defer func() {
		for i := len(locks) - 1; i >= 0; i-- {
			_ = locks[i].Close()
		}
	}()
	return fn()
}

func detectForeignLoopbackRouting(files []string, ownBaseURL string) error {
	ownBaseURL = strings.TrimRight(strings.ToLower(ownBaseURL), "/")
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		lower := strings.ToLower(string(raw))
		loopback := strings.Contains(lower, "127.0.0.1") || strings.Contains(lower, "localhost") || strings.Contains(lower, "[::1]")
		if !loopback && !strings.Contains(string(raw), ProxyManagedToken) {
			continue
		}
		if ownBaseURL != "" && strings.Contains(lower, ownBaseURL) && strings.Contains(string(raw), ProxyManagedToken) {
			return fmt.Errorf("%s already points at an unmanaged AgentNexus proxy; repair ownership explicitly before takeover", path)
		}
		return fmt.Errorf("%s is owned by another loopback router (for example CC Switch or Flux); refusing takeover", path)
	}
	return nil
}
