package core

import (
	"context"
	"time"
)

// UsageRecord is a single normalized usage row produced by a parser. Every
// data-source adapter (Claude, Codex, Cursor, Gemini, ...) normalizes into
// this shape so the aggregator and clients share one schema.
type UsageRecord struct {
	Source           string    `json:"source"` // claude, codex, cursor...
	SessionID        string    `json:"session_id"`
	ConversationID   string    `json:"conversation_id,omitempty"`
	TraceID          string    `json:"trace_id,omitempty"`
	TurnID           string    `json:"turn_id,omitempty"`
	RequestID        string    `json:"request_id,omitempty"`
	RuntimeID        string    `json:"runtime_id,omitempty"`
	Project          string    `json:"project"`
	Model            string    `json:"model"`
	Timestamp        time.Time `json:"timestamp"`
	InputTokens      int64     `json:"input_tokens"`
	OutputTokens     int64     `json:"output_tokens"`
	CacheReadTokens  int64     `json:"cache_read_tokens"`
	CacheWriteTokens int64     `json:"cache_write_tokens"`
	Tool             string    `json:"tool,omitempty"` // tool_use name
	CostUSD          float64   `json:"cost_usd"`
	Host             string    `json:"host,omitempty"` // "" = local, else SSH host
	Requests         int64     `json:"requests,omitempty"`
}

// UsageCollector reads a local (or remote-synced) data source and yields
// normalized usage records.
type UsageCollector interface {
	// Source returns the collector id, e.g. "claude".
	Source() string
	// Collect scans the data source and returns records since the given time
	// (zero = all). Implementations must be read-only.
	Collect(ctx context.Context, since time.Time) ([]UsageRecord, error)
}
