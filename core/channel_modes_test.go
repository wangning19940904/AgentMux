package core

import (
	"context"
	"testing"
)

type modeTestPlatform struct {
	*fakePlatform
	topic, admin bool
	groups       int
	groupErr     error
}

func (p *modeTestPlatform) ConversationChat(context.Context, string) (ConversationChatInfo, error) {
	return ConversationChatInfo{Topic: p.topic}, nil
}
func (p *modeTestPlatform) CanManageConversationChat(context.Context, string, string) (bool, error) {
	return p.admin, nil
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
	msg.UserID = "admin"
	e.handleConversationMode(context.Background(), rt, msg)
	mode, _ = rt.conversationMode(context.Background(), msg)
	if mode != "new-topic" {
		t.Fatal("admin could not change mode")
	}
}
