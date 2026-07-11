package feishu

import (
	"context"

	"github.com/agentnexus/agentnexus/core"
)

// clientAPI abstracts the Feishu client so the adapter can compile and be
// tested without live credentials. The concrete implementation wraps the
// official Lark SDK.
type clientAPI interface {
	// Listen opens the long connection and forwards inbound messages tagged
	// with the given project, until ctx is cancelled.
	Listen(ctx context.Context, project string, inbound chan<- *core.Message) error
	// SendText sends a plain-text message to a chat and returns its message ID.
	SendText(ctx context.Context, chatID, text string) (messageID string, err error)
	// UpdateText replaces a previously sent plain-text message.
	UpdateText(ctx context.Context, messageID, text string) error
	// SendCard posts an interactive card and returns its message ID so it can
	// be updated in place later.
	SendCard(ctx context.Context, chatID, text string, done, failed bool) (messageID string, err error)
	// UpdateCard replaces the content of a previously sent card message.
	UpdateCard(ctx context.Context, messageID, text string, done, failed bool) error
	// SendModelPickerCard posts an interactive model selector for the
	// conversation that originated msg.
	SendModelPickerCard(ctx context.Context, msg *core.Message, state core.ModelPickerState) (messageID string, err error)
	// SendRuntimeSettingsPickerCard posts the unified model/effort/speed card.
	SendRuntimeSettingsPickerCard(ctx context.Context, msg *core.Message, state core.RuntimeSettingsPickerState) (messageID string, err error)
	// UpdateRuntimeSettingsPickerCard patches the original picker message.
	UpdateRuntimeSettingsPickerCard(ctx context.Context, messageID string, msg *core.Message, state core.RuntimeSettingsPickerState) error
	// BeginStreamCard creates a CardKit streaming card entity and sends it to
	// the chat, returning the card entity ID used for subsequent streaming
	// text updates. This is the native "typewriter" streaming path; it
	// requires the cardkit:card:write permission.
	BeginStreamCard(ctx context.Context, chatID string) (cardID string, err error)
	// StreamCardText pushes the full accumulated text to the card's streaming
	// text element. sequence must strictly increase across calls on the same
	// card. Feishu computes the delta server-side and renders it as typing.
	StreamCardText(ctx context.Context, cardID, text string, sequence int) error
	// FinishStreamCard finalizes a streaming card: it writes the terminal text,
	// styles the header for done/failed, and closes streaming mode.
	FinishStreamCard(ctx context.Context, cardID, text string, sequence int, failed bool) error
	// AddReaction marks a message with one emoji and returns the reaction ID.
	AddReaction(ctx context.Context, messageID, emojiType string) (reactionID string, err error)
	// DeleteReaction removes a reaction previously added by this client.
	DeleteReaction(ctx context.Context, messageID, reactionID string) error
	// Close releases the connection.
	Close() error
}
