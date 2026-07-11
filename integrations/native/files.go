package native

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func fileHash(path string) (string, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", fmt.Errorf("refusing non-regular file %s", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hashTree(root string) (string, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("plugin root must be a real directory: %s", root)
	}
	var paths []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("plugin asset contains symlink: %s", path)
		}
		if entry.Type().IsRegular() {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, path := range paths {
		rel, _ := filepath.Rel(root, path)
		_, _ = io.WriteString(h, filepath.ToSlash(rel))
		_, _ = h.Write([]byte{0})
		f, err := os.Open(path)
		if err != nil {
			return "", err
		}
		_, copyErr := io.Copy(h, f)
		closeErr := f.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func handlerFingerprint(hooksPath string) (string, error) {
	raw, err := os.ReadFile(hooksPath)
	if err != nil {
		return "", err
	}
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		return "", err
	}
	var commands []string
	collectCommands(root, &commands)
	sort.Strings(commands)
	if len(commands) == 0 {
		return "", errors.New("hook asset has no command handlers")
	}
	return hashBytes([]byte(strings.Join(commands, "\n"))), nil
}

func collectCommands(value any, commands *[]string) {
	switch typed := value.(type) {
	case map[string]any:
		if command, ok := typed["command"].(string); ok {
			*commands = append(*commands, command)
		}
		for _, child := range typed {
			collectCommands(child, commands)
		}
	case []any:
		for _, child := range typed {
			collectCommands(child, commands)
		}
	}
}

func atomicWriteCAS(path string, data []byte, mode os.FileMode, expectedHash string) (string, error) {
	current, err := fileHash(path)
	if err != nil {
		return "", err
	}
	if current != expectedHash {
		return "", fmt.Errorf("%w for %s: expected %q, found %q", ErrCAS, path, expectedHash, current)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".agentnexus-write-*")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	// Re-read immediately before rename. The operation runs under our lock, and
	// this second CAS also catches uncooperative external writers.
	current, err = fileHash(path)
	if err != nil {
		return "", err
	}
	if current != expectedHash {
		return "", fmt.Errorf("%w for %s during commit: expected %q, found %q", ErrCAS, path, expectedHash, current)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return "", err
	}
	if dirHandle, err := os.Open(dir); err == nil {
		_ = dirHandle.Sync()
		_ = dirHandle.Close()
	}
	return hashBytes(data), nil
}

func removeFileCAS(path, expectedHash string) error {
	current, err := fileHash(path)
	if err != nil {
		return err
	}
	if current == "" {
		return nil
	}
	if current != expectedHash {
		return fmt.Errorf("%w for %s: expected %q, found %q", ErrCAS, path, expectedHash, current)
	}
	return os.Remove(path)
}

type fileLock struct{ path string }

func acquireFileLock(ctx context.Context, path string) (*fileLock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			metadata := fmt.Sprintf("pid=%d time=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339Nano))
			_, _ = io.WriteString(f, metadata)
			_ = f.Close()
			return &fileLock{path: path}, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		if info, statErr := os.Stat(path); statErr == nil && time.Since(info.ModTime()) > 2*time.Minute {
			// A stale create-exclusive lock is safe to remove after a generous
			// timeout. Active operations refresh by completing well before this.
			_ = os.Remove(path)
			continue
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, fmt.Errorf("timed out waiting for integration lock %s", path)
		case <-ticker.C:
		}
	}
}

func (l *fileLock) release() { _ = os.Remove(l.path) }

func randomInstallID(random io.Reader) (string, error) {
	if random == nil {
		random = rand.Reader
	}
	raw := make([]byte, 16)
	if _, err := io.ReadFull(random, raw); err != nil {
		return "", err
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(raw)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32], nil
}
