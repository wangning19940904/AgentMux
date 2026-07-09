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
	// AddReaction marks a message with one emoji and returns the reaction ID.
	AddReaction(ctx context.Context, messageID, emojiType string) (reactionID string, err error)
	// DeleteReaction removes a reaction previously added by this client.
	DeleteReaction(ctx context.Context, messageID, reactionID string) error
	// Close releases the connection.
	Close() error
}
