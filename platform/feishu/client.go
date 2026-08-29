package feishu

import (
	"context"

	"github.com/wangning19940904/AgentMux/core"
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
	SendCard(ctx context.Context, chatID, text string, done, failed bool, images []streamCardImage, control *streamCardControl) (messageID string, err error)
	// UpdateCard replaces the content of a previously sent card message.
	UpdateCard(ctx context.Context, messageID, text string, done, failed bool, images []streamCardImage, control *streamCardControl) error
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
	BeginStreamCard(ctx context.Context, chatID string, control *streamCardControl) (cardID string, err error)
	// StreamCardText pushes the full accumulated text to the card's streaming
	// text element. sequence must strictly increase across calls on the same
	// card. Feishu computes the delta server-side and renders it as typing.
	StreamCardText(ctx context.Context, cardID, text string, sequence int) error
	// InsertStreamCardImage adds an uploaded image after an existing element
	// without replacing the still-streaming markdown component.
	InsertStreamCardImage(ctx context.Context, cardID, targetElementID string, sequence int, image streamCardImage) error
	// FinishStreamCard finalizes a streaming card: it writes the terminal text,
	// styles the header for done/failed, and closes streaming mode.
	FinishStreamCard(ctx context.Context, cardID, text string, sequence int, failed bool, images []streamCardImage, control *streamCardControl) error
	// AddReaction marks a message with one emoji and returns the reaction ID.
	AddReaction(ctx context.Context, messageID, emojiType string) (reactionID string, err error)
	// DeleteReaction removes a reaction previously added by this client.
	DeleteReaction(ctx context.Context, messageID, reactionID string) error
	// Close releases the connection.
	Close() error
}

// threadReplyClient is implemented by the real Feishu/Lark client. Keeping it
// optional preserves lightweight test clients while allowing production
// replies and streaming cards to remain inside the originating topic.
type threadReplyClient interface {
	ReplyText(ctx context.Context, messageID, text string) (replyMessageID string, err error)
	ReplyCard(ctx context.Context, messageID, text string, done, failed bool, images []streamCardImage, control *streamCardControl) (replyMessageID string, err error)
	BeginStreamCardReply(ctx context.Context, messageID string, control *streamCardControl) (cardID string, err error)
}

// attachmentClient is optional so lightweight test clients do not need to
// implement upload APIs. The production Lark client supports both chat sends
// and native thread replies after uploading the artifact.
type attachmentClient interface {
	SendImage(ctx context.Context, chatID, fileName string, data []byte) (messageID string, err error)
	ReplyImage(ctx context.Context, messageID, fileName string, data []byte) (replyMessageID string, err error)
	SendFile(ctx context.Context, chatID, fileName string, data []byte) (messageID string, err error)
	ReplyFile(ctx context.Context, messageID, fileName string, data []byte) (replyMessageID string, err error)
}

// cardImageUploader is deliberately narrower than attachmentClient: an image
// embedded in a card needs the uploaded image key, not a second image message.
type cardImageUploader interface {
	uploadImage(ctx context.Context, fileName string, data []byte) (imageKey string, err error)
}

type interactionCardClient interface {
	SendAgentInteractionCard(ctx context.Context, msg *core.Message, task core.ChannelTask, interaction core.ChannelInteraction) (messageID string, err error)
	UpdateAgentInteractionCard(ctx context.Context, messageID string, interaction core.ChannelInteraction, outcome string) error
}

type helpCardClient interface {
	SendHelpCard(ctx context.Context, msg *core.Message, state core.HelpCardState) (messageID string, err error)
}

type meetingControlClient interface {
	MeetingInvitations() []core.MeetingInvitation
	RespondMeetingInvitation(ctx context.Context, invitationID, nonce, decision string) (core.MeetingInvitation, error)
	JoinMeetingDirect(ctx context.Context, meetingNumber string) (core.MeetingJoinResult, error)
}

type meetingActivityClient interface {
	ActiveMeetings() []core.ActiveMeeting
	MeetingActivity(meetingID string) (core.MeetingDetail, error)
	SendMeetingMessage(ctx context.Context, meetingID, text, uuid string) error
	UserActiveMeetings(ctx context.Context, userID string) ([]core.ActiveMeeting, error)
	MeetingPromptContext(meetingID string) string
	UpsertMeetingTurn(turn core.MeetingTurn)
}

var _ meetingActivityClient = (*larkClient)(nil)
