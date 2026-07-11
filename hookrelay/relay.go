// Package hookrelay implements the small, fail-open transport used by native
// Claude Code and Codex hooks. It deliberately has no dependency on the main
// AgentNexus process: hooks can deliver to a Unix socket or spool an encrypted
// event while the gateway is offline.
package hookrelay

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// MaxSocketWait is the hard upper bound for connecting and writing to the
	// local collector. Native agent execution must never wait on telemetry.
	MaxSocketWait = 200 * time.Millisecond
	maxInputBytes = 64 << 20
	spoolAAD      = "agentnexus-hook-spool-v1"
)

// Options configures one hook delivery.
type Options struct {
	Source     string
	SocketPath string
	SpoolDir   string
	KeyPath    string
	Timeout    time.Duration
	Now        func() time.Time
	Random     io.Reader
}

// Delivery reports where the hook payload was accepted.
type Delivery struct {
	Socket  bool   `json:"socket"`
	Spooled bool   `json:"spooled"`
	Path    string `json:"path,omitempty"`
}

// Message is the newline-delimited JSON sent to the local collector.
type Message struct {
	Version    int             `json:"version"`
	Source     string          `json:"source"`
	ReceivedAt time.Time       `json:"received_at"`
	Payload    json.RawMessage `json:"payload"`
}

type encryptedSpool struct {
	Version    int    `json:"version"`
	Algorithm  string `json:"algorithm"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

// DefaultOptions returns paths under the supplied home directory. Callers
// normally use os.UserHomeDir; tests and integration previews pass a temp HOME.
func DefaultOptions(home string) Options {
	root := filepath.Join(home, ".agentnexus", "observability")
	return Options{
		Source:     "unknown",
		SocketPath: filepath.Join(home, ".agentnexus", "run", "observability.sock"),
		SpoolDir:   filepath.Join(root, "hook-spool"),
		KeyPath:    filepath.Join(root, "hook-spool.key"),
		Timeout:    MaxSocketWait,
	}
}

// Relay reads one native hook event. It attempts a bounded socket write first
// and falls back to an AES-256-GCM encrypted, one-event-per-file spool. The
// command entrypoint intentionally ignores the returned error so observability
// remains fail-open; tests and embedding callers can still inspect failures.
func Relay(ctx context.Context, input io.Reader, opts Options) (Delivery, error) {
	opts = normalizeOptions(opts)
	payload, err := readPayload(input)
	if err != nil {
		return Delivery{}, err
	}
	msg := Message{
		Version:    1,
		Source:     opts.Source,
		ReceivedAt: opts.Now().UTC(),
		Payload:    payload,
	}
	wire, err := json.Marshal(msg)
	if err != nil {
		return Delivery{}, err
	}
	wire = append(wire, '\n')

	socketErr := writeSocket(ctx, opts.SocketPath, wire, opts.Timeout)
	if socketErr == nil {
		return Delivery{Socket: true}, nil
	}
	path, spoolErr := writeEncryptedSpool(wire, opts)
	if spoolErr != nil {
		return Delivery{}, errors.Join(socketErr, spoolErr)
	}
	return Delivery{Spooled: true, Path: path}, nil
}

func normalizeOptions(opts Options) Options {
	if strings.TrimSpace(opts.Source) == "" {
		opts.Source = "unknown"
	}
	if opts.Timeout <= 0 || opts.Timeout > MaxSocketWait {
		opts.Timeout = MaxSocketWait
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Random == nil {
		opts.Random = rand.Reader
	}
	return opts
}

func readPayload(input io.Reader) (json.RawMessage, error) {
	limited := io.LimitReader(input, maxInputBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read hook input: %w", err)
	}
	if len(raw) > maxInputBytes {
		return nil, fmt.Errorf("hook input exceeds %d bytes", maxInputBytes)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		raw = []byte("{}")
	}
	if json.Valid(raw) {
		return json.RawMessage(raw), nil
	}
	wrapped, err := json.Marshal(map[string]any{
		"invalid_json": true,
		"raw_base64":   base64.StdEncoding.EncodeToString(raw),
	})
	if err != nil {
		return nil, err
	}
	return json.RawMessage(wrapped), nil
}

func writeSocket(parent context.Context, path string, payload []byte, timeout time.Duration) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("observability socket path is empty")
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return fmt.Errorf("connect observability socket: %w", err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetWriteDeadline(deadline)
	}
	if _, err := conn.Write(payload); err != nil {
		return fmt.Errorf("write observability socket: %w", err)
	}
	return nil
}

func writeEncryptedSpool(plaintext []byte, opts Options) (string, error) {
	if opts.SpoolDir == "" || opts.KeyPath == "" {
		return "", errors.New("encrypted spool paths are not configured")
	}
	if err := secureMkdirAll(opts.SpoolDir); err != nil {
		return "", err
	}
	key, err := loadOrCreateKey(opts.KeyPath, opts.Random)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(opts.Random, nonce); err != nil {
		return "", fmt.Errorf("generate spool nonce: %w", err)
	}
	sealed := gcm.Seal(nil, nonce, plaintext, []byte(spoolAAD))
	envelope, err := json.Marshal(encryptedSpool{
		Version:    1,
		Algorithm:  "AES-256-GCM",
		Nonce:      base64.RawStdEncoding.EncodeToString(nonce),
		Ciphertext: base64.RawStdEncoding.EncodeToString(sealed),
	})
	if err != nil {
		return "", err
	}

	randomName := make([]byte, 6)
	if _, err := io.ReadFull(opts.Random, randomName); err != nil {
		return "", fmt.Errorf("generate spool filename: %w", err)
	}
	name := fmt.Sprintf("%020d-%s.anxspool", opts.Now().UnixNano(), base64.RawURLEncoding.EncodeToString(randomName))
	path := filepath.Join(opts.SpoolDir, name)
	if err := atomicWriteExclusive(path, envelope, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func loadOrCreateKey(path string, random io.Reader) ([]byte, error) {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("spool key is not a regular file: %s", path)
		}
		key, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if len(key) != 32 {
			return nil, fmt.Errorf("spool key %s must contain 32 bytes", path)
		}
		return key, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := secureMkdirAll(filepath.Dir(path)); err != nil {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(random, key); err != nil {
		return nil, fmt.Errorf("generate spool key: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".agentnexus-key-*")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if _, err := tmp.Write(key); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	// Linking a fully synced temp file publishes the key atomically and refuses
	// replacement. Concurrent first writers simply load the winner's key.
	if err := os.Link(tmpPath, path); os.IsExist(err) {
		return loadExistingKey(path)
	} else if err != nil {
		return nil, err
	}
	syncDirectory(filepath.Dir(path))
	return key, nil
}

func secureMkdirAll(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

func atomicWriteExclusive(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".agentnexus-spool-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Hard-linking within the same directory is atomic and refuses replacement,
	// unlike rename on Unix. An extreme filename collision drops only this event.
	if err := os.Link(tmpPath, path); err != nil {
		return fmt.Errorf("publish encrypted spool: %w", err)
	}
	syncDirectory(dir)
	return nil
}

func syncDirectory(path string) {
	if directory, err := os.Open(path); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
}

// DecryptSpool is intended for the collector and tests. It authenticates the
// envelope before returning the original newline-delimited Message.
func DecryptSpool(path, keyPath string) ([]byte, error) {
	key, err := loadExistingKey(keyPath)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var envelope encryptedSpool
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}
	if envelope.Version != 1 || envelope.Algorithm != "AES-256-GCM" {
		return nil, fmt.Errorf("unsupported spool envelope version or algorithm")
	}
	nonce, err := base64.RawStdEncoding.DecodeString(envelope.Nonce)
	if err != nil {
		return nil, err
	}
	sealed, err := base64.RawStdEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, nonce, sealed, []byte(spoolAAD))
}

func loadExistingKey(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("spool key is not a regular file: %s", path)
	}
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("spool key %s must contain 32 bytes", path)
	}
	return key, nil
}
