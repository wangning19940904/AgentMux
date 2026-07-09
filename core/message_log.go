package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DefaultLogRoot returns ~/.agentnexus/logs, the base directory where channel
// message logs are written.
func DefaultLogRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".agentnexus", "logs")
}

// MessageLogger appends inbound channel messages to per-channel JSONL files
// under <root>/channels/<channelID>.jsonl. It is safe for concurrent use.
type MessageLogger struct {
	root string
	mu   sync.Mutex
}

// NewMessageLogger builds a MessageLogger rooted at root. When root is empty it
// falls back to DefaultLogRoot.
func NewMessageLogger(root string) *MessageLogger {
	if root == "" {
		root = DefaultLogRoot()
	}
	return &MessageLogger{root: root}
}

// ChannelLogPath returns the absolute JSONL log path for a channel id.
func (l *MessageLogger) ChannelLogPath(channelID string) string {
	return filepath.Join(l.root, "channels", channelID+".jsonl")
}

// Log appends one structured record for a channel message. The data map is the
// same payload used for hook events; a logged_at timestamp is added.
func (l *MessageLogger) Log(channelID string, data map[string]string) error {
	if l == nil || channelID == "" {
		return nil
	}
	record := make(map[string]string, len(data)+1)
	for k, v := range data {
		record[k] = v
	}
	record["logged_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	line, err := json.Marshal(record)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	path := l.ChannelLogPath(channelID)
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(line)
	return err
}
