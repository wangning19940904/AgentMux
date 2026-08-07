package observability

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func transcriptAgentName(runtime string) string {
	if runtime == "claude" {
		return "Claude Code"
	}
	return "Codex"
}

func jsonObservationContent(value any) *core.ObservationContent {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) == 0 {
		return nil
	}
	return &core.ObservationContent{ContentType: "application/json", Data: encoded}
}

func parseTranscriptTime(value string, fallback time.Time) time.Time {
	if value != "" {
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
			if parsed, err := time.Parse(layout, value); err == nil {
				return parsed.UTC()
			}
		}
	}
	return fallback.UTC()
}

func stableHex(value string, bytes int) string {
	digest := sha256.Sum256([]byte(value))
	if bytes <= 0 || bytes > len(digest) {
		bytes = len(digest)
	}
	return hex.EncodeToString(digest[:bytes])
}

func mapObject(raw map[string]any, key string) map[string]any {
	if raw == nil {
		return nil
	}
	value, _ := raw[key].(map[string]any)
	return value
}

func mapString(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := raw[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func mapArray(value any) []map[string]any {
	items, _ := value.([]any)
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if mapped, ok := item.(map[string]any); ok {
			result = append(result, mapped)
		}
	}
	return result
}

func mapInt64(raw map[string]any, key string) int64 {
	if raw == nil {
		return 0
	}
	switch value := raw[key].(type) {
	case float64:
		return int64(value)
	case json.Number:
		parsed, _ := value.Int64()
		return parsed
	case int64:
		return value
	case int:
		return int64(value)
	}
	return 0
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstNonNil(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func cloneMap(input map[string]any) map[string]any {
	result := make(map[string]any, len(input)+1)
	for key, value := range input {
		result[key] = value
	}
	return result
}
