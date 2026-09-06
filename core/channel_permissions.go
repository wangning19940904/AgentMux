package core

import "context"

// isChatManager uses the platform's current group roles, never a channel-local
// user list. Call it outside task-state locks because it may query the platform.
func (rt *channelRuntime) isChatManager(ctx context.Context, msg *Message) bool {
	if rt == nil || msg == nil || msg.UserID == "" || msg.ChatID == "" || isDirectChatType(msg.ChatType) {
		return false
	}
	platform, ok := rt.platform.(ConversationPlatform)
	if !ok {
		return false
	}
	allowed, err := platform.CanManageConversationChat(ctx, msg.ChatID, msg.UserID)
	return err == nil && allowed
}
