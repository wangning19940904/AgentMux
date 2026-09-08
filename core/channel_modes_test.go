package core

import (
	"context"
	"testing"
)

type modeTestPlatform struct {
	*fakePlatform
	topic, admin  bool
	groups        int
	groupErr      error
	permissionErr error
}

func (p *modeTestPlatform) ConversationChat(context.Context, string) (ConversationChatInfo, error) {
	return ConversationChatInfo{Topic: p.topic}, nil
}
func (p *modeTestPlatform) CanManageConversationChat(context.Context, string, string) (bool, error) {
	return p.admin, p.permissionErr
}
func (p *modeTestPlatform) CreateConversationGroup(context.Context, string, string, string) (string, error) {
	p.groups++
	if p.groupErr != nil {
		return "", p.groupErr
	}
	return "created-group", nil
}

func TestConversationModeRoutingMatrix(t *testing.T) {
	for _, tc := range []struct {
		name, chatType, mode, root, thread, want string
		topic                                    bool
	}{
		{"private default", "p2p", "", "root", "thread", "chat:chat", false},
		{"private thread seed", "p2p", "thread", "", "", "root:message", false},
		{"private thread reply", "p2p", "thread", "root", "thread", "root:root", false},
		{"regular default", "group", "", "", "", "chat:chat", false},
		{"regular quote", "group", "", "quoted", "", "chat:chat", false},
		{"regular native topic", "group", "", "", "thread", "root:message", false},
		{"regular native reply", "group", "", "root", "thread", "root:root", false},
		{"regular shared", "group", "chat", "root", "thread", "chat:chat", false},
		{"regular new topic", "group", "new-topic", "", "", "root:message", false},
		{"topic group seed", "group", "chat", "", "", "root:message", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := NewEngine(nil, NewHookRunner(nil, nil))
			rt, _ := newRemoteControlTestRuntime(e)
			rt.platform = &modeTestPlatform{fakePlatform: newFakePlatform("feishu"), topic: tc.topic}
			rt.channel.Config[ChannelConfigPrivateMode] = tc.mode
			rt.channel.Config[ChannelConfigGroupMode] = tc.mode
			msg := &Message{ID: "message", ChatID: "chat", ChatType: tc.chatType, RootID: tc.root, ThreadID: tc.thread, UserID: "member", Text: "test"}
			if err := rt.resolveChannelRoute(context.Background(), msg); err != nil {
				t.Fatal(err)
			}
			if msg.ConversationKey != tc.want {
				t.Fatalf("got %s want %s", msg.ConversationKey, tc.want)
			}
		})
	}
}
func TestConversationModeOverrideAndGroupBirth(t *testing.T) {
	e := NewEngine(nil, NewHookRunner(nil, nil))
	rt, _ := newRemoteControlTestRuntime(e)
	p := &modeTestPlatform{fakePlatform: newFakePlatform("feishu")}
	rt.platform = p
	ctx := context.Background()
	msg := &Message{ID: "seed", ChatID: "dm", ChatType: "p2p", UserID: "member", Text: "do work"}
	if err := rt.setChatState(ctx, "mode:dm", "group"); err != nil {
		t.Fatal(err)
	}
	first := *msg
	second := *msg
	if err := rt.resolveChannelRoute(ctx, &first); err != nil {
		t.Fatal(err)
	}
	if err := rt.resolveChannelRoute(ctx, &second); err != nil {
		t.Fatal(err)
	}
	if p.groups != 1 || first.ConversationKey != "chat:created-group" || first.ID != "" {
		t.Fatalf("groups=%d message=%+v", p.groups, first)
	}
	reply := &Message{ID: "next", ChatID: "created-group", ChatType: "group", UserID: "member", Text: "next"}
	if err := rt.resolveChannelRoute(ctx, reply); err != nil {
		t.Fatal(err)
	}
	if !reply.MentionedBot || reply.ConversationKey != first.ConversationKey {
		t.Fatal("dedicated group did not retain routing")
	}
}
func TestModeChangesRequireGroupManager(t *testing.T) {
	e := NewEngine(nil, NewHookRunner(nil, nil))
	rt, _ := newRemoteControlTestRuntime(e)
	rt.platform = &modeTestPlatform{fakePlatform: newFakePlatform("feishu")}
	msg := &Message{ChatID: "group", ChatType: "group", UserID: "member", Text: "/mode new-topic"}
	e.handleConversationMode(context.Background(), rt, msg)
	mode, _ := rt.conversationMode(context.Background(), msg)
	if mode != "chat-topic" {
		t.Fatal("member changed group mode")
	}
	rt.platform.(*modeTestPlatform).admin = true
	msg.UserID = "admin"
	e.handleConversationMode(context.Background(), rt, msg)
	mode, _ = rt.conversationMode(context.Background(), msg)
	if mode != "new-topic" {
		t.Fatal("admin could not change mode")
	}
}

func TestAgentConversationModePrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, chatType, agentMode, legacyMode, override, wantMode, wantKey string
		thread, topic, replyInThread                                       bool
	}{
		{"group default", "group", "", "", "", "chat-topic", "chat:chat", false, false, false},
		{"private default", "p2p", "", "", "", "chat", "chat:chat", false, false, false},
		{"legacy group", "group", "", "new-topic", "", "new-topic", "root:message", false, false, true},
		{"legacy private", "p2p", "", "thread", "", "thread", "root:message", false, false, true},
		{"agent group overrides channel", "group", "new-topic", "chat", "", "new-topic", "root:message", false, false, true},
		{"agent private overrides channel", "p2p", "chat", "thread", "", "chat", "chat:chat", true, false, false},
		{"group chat override", "group", "new-topic", "chat-topic", "chat", "chat", "chat:chat", true, false, false},
		{"private chat override", "p2p", "chat", "group", "thread", "thread", "root:root", true, false, true},
		{"agent native topic", "group", "chat-topic", "chat", "", "chat-topic", "root:root", true, false, true},
		{"agent continuous group", "group", "chat", "new-topic", "", "chat", "chat:chat", true, false, false},
		{"topic group stays isolated", "group", "chat", "", "chat", "chat", "root:message", false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			rt := &channelRuntime{
				owner: NewEngine(nil, NewHookRunner(nil, nil)),
				channel: Channel{ID: "channel", Type: "feishu", Config: map[string]string{
					ChannelConfigPrivateMode: tc.legacyMode, ChannelConfigGroupMode: tc.legacyMode,
				}},
				workspace: WorkspaceInitOptions{PrivateChatMode: tc.agentMode, GroupChatMode: tc.agentMode},
				platform:  &modeTestPlatform{fakePlatform: newFakePlatform("feishu"), topic: tc.topic},
			}
			if tc.override != "" {
				if err := rt.setChatState(ctx, "mode:chat", tc.override); err != nil {
					t.Fatal(err)
				}
			}
			msg := &Message{ID: "message", ChatID: "chat", ChatType: tc.chatType}
			if tc.thread {
				msg.ThreadID, msg.RootID = "thread", "root"
			}
			mode, err := rt.conversationMode(ctx, msg)
			if err != nil || mode != tc.wantMode {
				t.Fatalf("mode = %q, err = %v", mode, err)
			}
			if err := rt.resolveChannelRoute(ctx, msg); err != nil {
				t.Fatal(err)
			}
			if msg.ConversationKey != tc.wantKey || msg.ReplyInThread != tc.replyInThread {
				t.Fatalf("route = %s, reply in thread = %v", msg.ConversationKey, msg.ReplyInThread)
			}
		})
	}
}
