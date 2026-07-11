//go:build windows

package provider

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type liveFileLock struct {
	path string
	file *os.File
}

func acquireLiveFileLock(ctx context.Context, path string) (*liveFileLock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	for {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if err == nil {
			return &liveFileLock{path: path, file: file}, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("lock %s: %w", path, ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (l *liveFileLock) Close() error {
	if l == nil {
		return nil
	}
	if l.file != nil {
		_ = l.file.Close()
	}
	return os.Remove(l.path)
}
