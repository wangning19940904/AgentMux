package core

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type admissionTestPlatform struct {
	*modeTestPlatform
	chatCalls int
	chatErr   error
}

func (p *admissionTestPlatform) ConversationChat(context.Context, string) (ConversationChatInfo, error) {
	p.chatCalls++
	return ConversationChatInfo{}, p.chatErr
}

type admissionTestStore struct {
	senderConversationStore
	taskCalls int
	taskErr   error
	stateErr  error
	dedicated string
	stateKeys []string
}

func (s *admissionTestStore) HasChannelSourceTask(context.Context, string, string) (bool, error) {
	s.taskCalls++
	return false, s.taskErr
}

func (s *admissionTestStore) GetChannelChatState(_ context.Context, _ string, key string) (string, error) {
	s.stateKeys = append(s.stateKeys, key)
	if strings.HasPrefix(key, "dedicated:") {
		return s.dedicated, s.stateErr
	}
	return "", s.stateErr
}

func (s *admissionTestStore) SetChannelChatState(context.Context, string, string, string) error {
	return s.stateErr
}

func TestChannelAdmissionIgnoresUnmentionedMessagesBeforeFailures(t *testing.T) {
	for _, failure := range []string{"none", "DNS", "task store", "chat state"} {
		for _, kind := range []string{"group", "topic_group", "thread", "mention all", "command", "pre-routed"} {
			for _, scope := range []string{ReplyScopeDMAndMentions, ReplyScopeMentionsOnly} {
				t.Run(failure+"/"+kind+"/"+scope, func(t *testing.T) {
					engine := NewEngine(nil, nil)
					rt, platform := newRemoteControlTestRuntime(engine)
					rt.channel.Config[ChannelConfigReplyScope] = scope
					p := &admissionTestPlatform{modeTestPlatform: &modeTestPlatform{fakePlatform: platform}}
					rt.platform = p
					store := &admissionTestStore{}
					engine.SetConversationStore(store)
					switch failure {
					case "DNS":
						p.chatErr = errors.New("Origin DNS Error")
					case "task store":
						store.taskErr = errors.New("database unavailable")
					case "chat state":
						store.stateErr = errors.New("database unavailable")
					}
					msg := &Message{ID: "message", ChannelID: rt.channel.ID, Platform: "feishu",
						ChatID: "chat", ChatType: "group", UserID: "member", Text: "ordinary group message"}
					switch kind {
					case "topic_group":
						msg.ChatType = "topic_group"
					case "thread":
						msg.RootID, msg.ThreadID = "root", "thread"
					case "mention all":
						msg.MentionAll = true
					case "command":
						msg.Text = "/status"
					case "pre-routed":
						msg.ConversationKey = "chat:chat"
					}
					var events []HookEvent
					engine.SetEventSink(func(event HookEvent, _ map[string]string) { events = append(events, event) })

					engine.handle(context.Background(), msg)

					if len(platform.replies) != 0 || len(platform.sends) != 0 {
						t.Fatalf("unmentioned message produced output: replies=%v sends=%v", platform.replies, platform.sends)
					}
					if p.chatCalls != 0 || store.taskCalls != 0 {
						t.Fatalf("unmentioned message reached routing/task lookup: chat=%d tasks=%d", p.chatCalls, store.taskCalls)
					}
					if len(events) != 0 || rt.agent.(*remoteControlTestAgent).starts != 0 {
						t.Fatalf("unmentioned message reached hooks/agent: events=%v", events)
					}
					for _, key := range store.stateKeys {
						if key != "dedicated:chat" {
							t.Fatalf("unmentioned message reached route state: %q", key)
						}
					}
				})
			}
		}
	}
}

func TestChannelAdmissionPreservesExplicitRequests(t *testing.T) {
	for _, kind := range []string{"mention", "private", "all scope", "dedicated", "callback"} {
		for _, failure := range []string{"none", "DNS", "task store"} {
			t.Run(kind+"/"+failure, func(t *testing.T) {
				engine := NewEngine(nil, nil)
				rt, platform := newRemoteControlTestRuntime(engine)
				p := &admissionTestPlatform{modeTestPlatform: &modeTestPlatform{fakePlatform: platform}}
				rt.platform = p
				store := &admissionTestStore{}
				engine.SetConversationStore(store)
				msg := &Message{ID: "message", ChannelID: rt.channel.ID, Platform: "feishu",
					ChatID: "chat", ChatType: "group", UserID: "member", Text: "/status"}
				switch kind {
				case "mention":
					msg.MentionedBot = true
				case "private":
					msg.ChatType = "p2p"
				case "all scope":
					rt.channel.Config[ChannelConfigReplyScope] = ReplyScopeAll
				case "dedicated":
					store.dedicated = "member"
				case "callback":
					msg.MentionedBot = true
					msg.Callback = &CallbackEvent{Type: "card.action.trigger"}
				}
				want := "任务状态：空闲"
				if failure == "DNS" {
					p.chatErr = errors.New("Origin DNS Error")
					if kind != "private" && kind != "dedicated" {
						want = "会话路由失败：Origin DNS Error"
					}
				}
				if failure == "task store" {
					store.taskErr = errors.New("database unavailable")
					if kind != "callback" {
						want = "读取消息状态失败：database unavailable"
					}
				}

				engine.handle(context.Background(), msg)

				if len(platform.replies) != 1 || !strings.Contains(platform.replies[0], want) {
					t.Fatalf("replies=%v, want %q", platform.replies, want)
				}
			})
		}
	}
}

func TestChannelAdmissionLogOnlyMessageCannotReplyOnStoreFailure(t *testing.T) {
	engine := NewEngine(nil, nil)
	rt, platform := newRemoteControlTestRuntime(engine)
	store := &admissionTestStore{taskErr: errors.New("database unavailable"), stateErr: errors.New("database unavailable")}
	engine.SetConversationStore(store)
	engine.handle(context.Background(), &Message{ID: "message", ChannelID: rt.channel.ID,
		ChatID: "chat", ChatType: "group", LogOnly: true})
	if len(platform.replies) != 0 || store.taskCalls != 0 || len(store.stateKeys) != 0 {
		t.Fatalf("log-only message performed reply-capable work: replies=%v tasks=%d state=%v", platform.replies, store.taskCalls, store.stateKeys)
	}
}
