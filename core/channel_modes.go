package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

const ChannelConfigPrivateMode = "private_chat_mode"
const ChannelConfigGroupMode = "group_chat_mode"

type ChannelChatStateStore interface {
	GetChannelChatState(context.Context, string, string) (string, error)
	SetChannelChatState(context.Context, string, string, string) error
}
type ConversationChatInfo struct {
	Topic   bool
	OwnerID string
}
type ConversationPlatform interface {
	ConversationChat(context.Context, string) (ConversationChatInfo, error)
	CanManageConversationChat(context.Context, string, string) (bool, error)
	CreateConversationGroup(context.Context, string, string, string) (string, error)
}
type ConversationModeState struct {
	Mode    string
	Private bool
	UserID  string
	Notice  string
}
type ConversationModeReplier interface {
	ReplyConversationMode(context.Context, *Message, ConversationModeState) error
}
type ConversationModeAction struct {
	Mode   string
	UserID string
}

func ValidConversationMode(private bool, mode string) bool {
	if private {
		return mode == "chat" || mode == "thread" || mode == "group"
	}
	return mode == "chat" || mode == "chat-topic" || mode == "new-topic"
}

func (rt *channelRuntime) chatState(ctx context.Context, key string) (string, error) {
	if rt.owner != nil {
		if store, ok := rt.owner.conversations.(ChannelChatStateStore); ok {
			return store.GetChannelChatState(ctx, rt.channel.ID, key)
		}
	}
	rt.routeMu.Lock()
	defer rt.routeMu.Unlock()
	return rt.routeState[key], nil
}
func (rt *channelRuntime) setChatState(ctx context.Context, key, value string) error {
	if rt.owner != nil {
		if store, ok := rt.owner.conversations.(ChannelChatStateStore); ok {
			return store.SetChannelChatState(ctx, rt.channel.ID, key, value)
		}
	}
	rt.routeMu.Lock()
	defer rt.routeMu.Unlock()
	if rt.routeState == nil {
		rt.routeState = map[string]string{}
	}
	rt.routeState[key] = value
	return nil
}
func (rt *channelRuntime) conversationMode(ctx context.Context, msg *Message) (string, error) {
	mode, err := rt.chatState(ctx, "mode:"+msg.ChatID)
	if err != nil {
		return "", err
	}
	private := isDirectChatType(msg.ChatType)
	if ValidConversationMode(private, mode) {
		return mode, nil
	}
	if private {
		mode = rt.channel.Config[ChannelConfigPrivateMode]
		if !ValidConversationMode(true, mode) {
			mode = "chat"
		}
	} else {
		mode = rt.channel.Config[ChannelConfigGroupMode]
		if !ValidConversationMode(false, mode) {
			mode = "chat-topic"
		}
	}
	return mode, nil
}

// Routing locks cover only admission and group birth, never an agent turn.
func (rt *channelRuntime) resolveChannelRoute(ctx context.Context, msg *Message) error {
	if msg.ConversationKey != "" || !isFeishuLikeChannel(rt.channel.Type) || msg.MeetingID != "" || msg.LogOnly {
		msg.ConversationKey = ResolveConversationKey(msg)
		return nil
	}
	lock, _ := rt.routingLocks.LoadOrStore(msg.ChatID, &sync.Mutex{})
	mu := lock.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	mode, err := rt.conversationMode(ctx, msg)
	if err != nil {
		return err
	}
	private := isDirectChatType(msg.ChatType)
	dedicated, err := rt.chatState(ctx, "dedicated:"+msg.ChatID)
	if err != nil {
		return err
	}
	if dedicated != "" {
		mode = "chat"
		msg.MentionedBot = true
	}
	realThread := msg.ThreadID != ""
	topicGroup := false
	platform, hasPlatform := rt.platform.(ConversationPlatform)
	if !private && dedicated == "" && hasPlatform {
		info, err := platform.ConversationChat(ctx, msg.ChatID)
		if err != nil {
			return err
		}
		topicGroup = info.Topic
	}
	msg.ChatMode = "chat"
	if topicGroup {
		msg.ChatMode = "topic"
	}
	if private && mode == "chat" || !private && !topicGroup && (mode == "chat" || mode == "chat-topic" && !realThread) {
		msg.ConversationKey = "chat:" + msg.ChatID
		return nil
	}
	root := msg.RootID
	if !realThread || root == "" {
		root = msg.ID
	}
	key := "root:" + root
	for _, alias := range []string{msg.ThreadID, root} {
		if alias == "" {
			continue
		}
		value, err := rt.chatState(ctx, "topic:"+alias)
		if err != nil {
			return err
		}
		if value != "" {
			key = value
			break
		}
	}
	// Resume legacy topic keys without changing an existing native session.
	if lookup, ok := rt.owner.conversations.(interface {
		FindTopicConversationKey(context.Context, string, string, string) (string, error)
	}); ok {
		existing, err := lookup.FindTopicConversationKey(ctx, rt.scope(), "root:"+root, "thread:"+msg.ThreadID)
		if err != nil {
			return err
		}
		if existing != "" {
			key = existing
		}
	}
	if private && mode == "group" && !realThread && !strings.HasPrefix(strings.TrimSpace(msg.Text), "/") {
		if !hasPlatform {
			return fmt.Errorf("此渠道不支持创建会话群")
		}
		birthKey := "group:" + msg.ID
		raw, err := rt.chatState(ctx, birthKey)
		if err != nil {
			return err
		}
		var birth conversationGroupBirth
		if raw != "" {
			if err = json.Unmarshal([]byte(raw), &birth); err != nil {
				return err
			}
		}
		if birth.ChatID == "" && !birth.Fallback {
			birth.Message = cloneChannelMessage(msg)
			if err = rt.saveGroupBirth(ctx, birthKey, birth); err != nil {
				return err
			}
			title := []rune(strings.TrimSpace(msg.Text))
			if len(title) > 36 {
				title = title[:36]
			}
			birth.ChatID, err = platform.CreateConversationGroup(ctx, msg.UserID, string(title), rt.channel.ID+":"+msg.ID)
			if err != nil {
				if _, definite := err.(*ConversationGroupRejectedError); !definite {
					return fmt.Errorf("建群结果待确认，请发送 /retry-group %s 重试同一请求：%w", msg.ID, err)
				}
				birth.Fallback = true
				_ = rt.platform.Reply(ctx, msg, "创建会话群失败，将在当前私聊的独立话题中继续："+err.Error())
			}
			if err = rt.saveGroupBirth(ctx, birthKey, birth); err != nil {
				return err
			}
		}
		if birth.ChatID != "" {
			if err = rt.setChatState(ctx, "dedicated:"+birth.ChatID, msg.UserID); err != nil {
				return err
			}
			if err = rt.setChatState(ctx, "mode:"+birth.ChatID, "chat"); err != nil {
				return err
			}
			_ = rt.platform.Reply(ctx, msg, "已创建专属会话群，任务将在群内继续。")
			msg.ChatID = birth.ChatID
			msg.ChatType = "group"
			msg.RootID = ""
			msg.ThreadID = ""
			msg.ParentID = ""
			msg.ID = ""
			msg.MentionedBot = true
			msg.ConversationKey = "chat:" + birth.ChatID
			return nil
		}
	}
	msg.ConversationKey = key
	// Force private thread replies to remain in the thread too.
	msg.ReplyInThread = true
	for _, alias := range []string{msg.ThreadID, root} {
		if alias != "" {
			if err = rt.setChatState(ctx, "topic:"+alias, key); err != nil {
				return err
			}
		}
	}
	return nil
}

type conversationGroupBirth struct {
	ChatID   string
	Fallback bool
	Message  *Message
}

func (rt *channelRuntime) saveGroupBirth(ctx context.Context, key string, birth conversationGroupBirth) error {
	b, err := json.Marshal(birth)
	if err != nil {
		return err
	}
	return rt.setChatState(ctx, key, string(b))
}

type ConversationGroupRejectedError struct{ Reason string }

func (e *ConversationGroupRejectedError) Error() string { return e.Reason }

func (e *Engine) handleConversationMode(ctx context.Context, rt *channelRuntime, msg *Message) bool {
	text := strings.TrimSpace(msg.Text)
	if strings.HasPrefix(text, "/retry-group ") {
		id := strings.TrimSpace(strings.TrimPrefix(text, "/retry-group "))
		raw, err := rt.chatState(ctx, "group:"+id)
		var birth conversationGroupBirth
		if err != nil || json.Unmarshal([]byte(raw), &birth) != nil || birth.Message == nil || birth.Message.UserID != msg.UserID {
			_ = rt.platform.Reply(ctx, msg, "找不到可重试的建群请求。")
			return true
		}

		if store, ok := e.conversations.(interface {
			HasChannelSourceTask(context.Context, string, string) (bool, error)
		}); ok {
			exists, readErr := store.HasChannelSourceTask(ctx, rt.channel.ID, birth.Message.SourceMessageID)
			if readErr != nil || exists {
				_ = rt.platform.Reply(ctx, msg, "该请求已被接收，或暂时无法确认状态，请查看会话群后再试。")
				return true
			}
		}
		retry := cloneChannelMessage(birth.Message)
		if err = rt.resolveChannelRoute(ctx, retry); err != nil {
			_ = rt.platform.Reply(ctx, msg, err.Error())
			return true
		}
		e.handleChannelMessage(ctx, retry, eventData(retry))
		return true
	}
	if msg.ConversationModeAction == nil && text != "/mode" && !strings.HasPrefix(text, "/mode ") {
		return false
	}
	if !isFeishuLikeChannel(rt.channel.Type) {
		_ = rt.platform.Reply(ctx, msg, "此渠道暂不支持切换会话模式。")
		return true
	}
	mode := strings.TrimSpace(strings.TrimPrefix(text, "/mode"))
	if msg.ConversationModeAction != nil {
		if msg.ConversationModeAction.UserID != msg.UserID {
			_ = rt.platform.Reply(ctx, msg, "请使用自己的 /mode 选择卡。")
			return true
		}
		mode = msg.ConversationModeAction.Mode
	}
	private := isDirectChatType(msg.ChatType)
	if !private && msg.ChatMode == "" {
		if platform, ok := rt.platform.(ConversationPlatform); ok {
			if info, err := platform.ConversationChat(ctx, msg.ChatID); err == nil && info.Topic {
				msg.ChatMode = "topic"
			}
		}
	}
	notice := ""
	if mode != "" {
		if !ValidConversationMode(private, mode) {
			notice = "不支持的模式。"
		} else {
			allowed := private || rt.isChatManager(ctx, msg)
			if !allowed {
				notice = "只有群主或群管理员可以切换群模式。"
			} else if msg.ChatMode == "topic" {
				notice = "话题群始终按话题隔离。"
			} else if err := rt.setChatState(ctx, "mode:"+msg.ChatID, mode); err != nil {
				notice = "保存模式失败：" + err.Error()
			} else {
				notice = "模式已更新，后续消息生效；已有任务继续在原会话执行。"
			}
		}
	}
	current, err := rt.conversationMode(ctx, msg)
	if err != nil {
		notice = err.Error()
	}
	if p, ok := rt.platform.(ConversationModeReplier); ok {
		_ = p.ReplyConversationMode(ctx, msg, ConversationModeState{Mode: current, Private: private, UserID: msg.UserID, Notice: notice})
	} else {
		_ = rt.platform.Reply(ctx, msg, "当前模式："+current+"\n"+notice)
	}
	return true
}
