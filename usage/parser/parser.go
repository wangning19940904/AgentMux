// Package parser holds per-source usage collectors. Each adapter locates a
// tool's local session data, decodes it, and normalizes into
// core.UsageRecord. Collectors are read-only.
package parser

import (
	"context"
	"fmt"
	"time"

	"github.com/agentnexus/agentnexus/core"
)

// NewCollector builds a collector for the named source. root overrides the
// default data location (used by SSH-synced data, where remote files are
// rsynced into a local staging dir); paths is reserved for per-source path
// overrides.
func NewCollector(source, root string, paths map[string]string) (core.UsageCollector, error) {
	switch source {
	case "claude":
		return &claudeCollector{root: root}, nil
	case "codex":
		return &codexCollector{root: root}, nil
	case "cursor":
		return &cursorCollector{root: root}, nil
	case "gemini":
		return &geminiCollector{root: root}, nil
	default:
		return nil, fmt.Errorf("unknown usage source %q", source)
	}
}

// sinceOK reports whether ts passes the since filter (zero since = all).
func sinceOK(ts, since time.Time) bool {
	return since.IsZero() || !ts.Before(since)
}

var _ = context.Background
