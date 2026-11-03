package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// Claude Code's metadata.user_id is either a JSON identity object or the older
// user_<device>_account_<account>_session_<uuid> string. Only its session component
// can identify a conversation; hashing avoids forwarding account/device data.
func anthropicResponseCacheKey(body map[string]any) string {
	metadata, _ := body["metadata"].(map[string]any)
	session := strings.TrimSpace(stringValue(metadata["session_id"]))
	if session == "" {
		userID := stringValue(metadata["user_id"])
		var identity map[string]any
		if json.Unmarshal([]byte(userID), &identity) == nil {
			session = strings.TrimSpace(stringValue(identity["session_id"]))
		} else if strings.HasPrefix(userID, "user_") {
			if _, suffix, ok := strings.Cut(userID, "_session_"); ok {
				session = strings.TrimSpace(suffix)
			}
		}
	}
	if session == "" {
		return ""
	}
	hash := sha256.Sum256([]byte("agentmux:claude-session:" + session))
	return "agentmux-claude-" + hex.EncodeToString(hash[:])
}
