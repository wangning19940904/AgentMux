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
	// SendText sends a plain-text message to a chat.
	SendText(ctx context.Context, chatID, text string) error
	// Close releases the connection.
	Close() error
}
