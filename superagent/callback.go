package superagent

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/wangning19940904/AgentMux/core"
)

// ErrBadCallback means a callback verified but did not carry a request.
var ErrBadCallback = errors.New("invalid callback payload")

// defaultCallbackActor names the caller when a payload does not.
const defaultCallbackActor = "callback"

// CallbackDelivery is one request to a custom callback Source as the transport
// received it. Signature verification needs the exact bytes, so Body is never
// re-encoded.
type CallbackDelivery struct {
	// Signature is "sha256=" plus the hex HMAC-SHA256 of Body under the secret.
	Signature string
	// Token is the secret itself, for a caller that cannot sign.
	Token string
	Body  []byte
}

// callbackPayload is what any system sends to ask the agent for work. Only
// text is required.
type callbackPayload struct {
	// ID makes redelivery idempotent. Empty means the body's digest.
	ID string `json:"id"`
	// Thread groups events about one subject, so later events and the Tickets
	// they started stay together. Empty means each event is its own thread.
	Thread string `json:"thread"`
	// Actor names who or what is asking, matched against the allowed list.
	Actor string `json:"actor"`
	Title string `json:"title"`
	Text  string `json:"text"`
}

// HandleCallback verifies and ingests one delivery. The result says whether it
// was new: a repeat of a stored id is success, so callers can retry freely.
func (s *Service) HandleCallback(ctx context.Context, sourceID string, delivery CallbackDelivery) (bool, error) {
	claim, ok := s.claims.source(sourceID)
	if !ok || claim.source.Kind != core.SuperAgentSourceCallback {
		return false, ErrNotFound
	}
	if !callbackVerified(claim.source.Config["webhook_secret"], delivery) {
		return false, ErrUnauthorized
	}
	trigger, err := parseCallback(claim.source, delivery.Body)
	if err != nil {
		return false, err
	}
	return s.Ingest(ctx, claim.agent, claim.source, trigger)
}

func callbackVerified(secret string, delivery CallbackDelivery) bool {
	if secret == "" {
		return false
	}
	if delivery.Signature != "" {
		return verifyGitHubSignature(secret, delivery.Signature, delivery.Body)
	}
	return delivery.Token != "" && subtle.ConstantTimeCompare([]byte(delivery.Token), []byte(secret)) == 1
}

func parseCallback(source core.SuperAgentSource, body []byte) (core.SuperAgentTrigger, error) {
	var payload callbackPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return core.SuperAgentTrigger{}, fmt.Errorf("%w: %v", ErrBadCallback, err)
	}
	text := strings.TrimSpace(payload.Text)
	if text == "" {
		return core.SuperAgentTrigger{}, fmt.Errorf("%w: text is required", ErrBadCallback)
	}
	key := strings.TrimSpace(payload.ID)
	if key == "" {
		digest := sha256.Sum256(body)
		key = hex.EncodeToString(digest[:])
	}
	thread := strings.TrimSpace(payload.Thread)
	if thread == "" {
		thread = key
	}
	actor := strings.TrimSpace(payload.Actor)
	if actor == "" {
		actor = defaultCallbackActor
	}
	if instructions := strings.TrimSpace(source.Config["instructions"]); instructions != "" {
		text += "\n\n" + instructions
	}
	reply, _ := json.Marshal(feishuReply{
		ChannelID: strings.TrimSpace(source.Config["channel_id"]),
		ChatID:    strings.TrimSpace(source.Config["notify_chat_id"]),
	})
	return core.SuperAgentTrigger{
		DedupeKey: key,
		ThreadKey: "callback:" + source.ID + ":" + thread,
		Actor:     actor, Addressed: true,
		Title: strings.TrimSpace(payload.Title), Text: text, Reply: reply,
	}, nil
}
