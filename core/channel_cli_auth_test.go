package core

import (
	"context"
	"strings"
	"testing"

	toolpkg "github.com/wangning19940904/AgentMux/tools"
)

func TestChannelCLIAuthRequestRecognizesLarkSetupIntent(t *testing.T) {
	for _, text := range []string{
		"帮我初始化一下larkcli，把二维码或者链接发给我",
		"请登录 lark-cli",
		"Lark CLI auth setup",
	} {
		if id, ok := channelCLIAuthRequest(text); !ok || id != "lark-cli" {
			t.Fatalf("request %q = %q, %v", text, id, ok)
		}
	}
	for _, text := range []string{"lark-cli 怎么用", "初始化项目", "已完成"} {
		if id, ok := channelCLIAuthRequest(text); ok {
			t.Fatalf("non-auth request %q matched %q", text, id)
		}
	}
}

func TestChannelCLIAuthBypassesAgentAndReturnsLink(t *testing.T) {
	restoreChannelCLIAuthStubs(t)
	startChannelCLIAuth = func(id string, force bool) (toolpkg.CLIAuthSession, error) {
		if id != "lark-cli" || force {
			t.Fatalf("start args = %q, %v", id, force)
		}
		return toolpkg.CLIAuthSession{
			ID: id, SessionID: "auth-1", Phase: "setup", State: toolpkg.CLIAuthSessionWaiting,
			LoginURL: "https://open.feishu.cn/page/cli?user_code=TEST-CODE",
		}, nil
	}

	engine := NewEngine(nil, NewHookRunner(nil, nil))
	platform := newFakePlatform("feishu")
	runtime := &channelRuntime{
		owner: engine, channel: Channel{ID: "channel-1", Type: "feishu"}, platform: platform,
		controlTasks: map[string]*channelControlState{}, cliAuth: map[string]channelCLIAuthPending{},
	}
	engine.channels[runtime.channel.ID] = runtime
	msg := &Message{
		ChannelID: runtime.channel.ID, ChatID: "chat-1", ChatType: "p2p", ConversationKey: "chat:chat-1",
		UserID: "user-1", Text: "帮我初始化一下 lark-cli，把链接发给我",
	}
	engine.handleChannelMessageDirect(context.Background(), msg, eventData(msg))

	if len(platform.replies) != 1 || !strings.Contains(platform.replies[0], "https://open.feishu.cn/page/cli?user_code=TEST-CODE") ||
		!strings.Contains(platform.replies[0], "本轮已经结束") {
		t.Fatalf("auth replies = %#v", platform.replies)
	}
	pending, ok := runtime.channelCLIAuthPending("chat:chat-1")
	if !ok || pending.SessionID != "auth-1" || pending.ControllerID != "user-1" {
		t.Fatalf("pending auth = %+v, %v", pending, ok)
	}
}

func TestChannelCLIAuthCompletionReturnsNextPhaseLink(t *testing.T) {
	restoreChannelCLIAuthStubs(t)
	checkChannelCLIAuth = func(context.Context, string) toolpkg.CLIAuthStatus {
		return toolpkg.CLIAuthStatus{ID: "lark-cli", State: toolpkg.CLIAuthUnauthenticated, Installed: true}
	}
	getChannelCLIAuth = func(sessionID string) (toolpkg.CLIAuthSession, bool) {
		return toolpkg.CLIAuthSession{
			ID: "lark-cli", SessionID: sessionID, Phase: "login", State: toolpkg.CLIAuthSessionWaiting,
			LoginURL: "https://open.feishu.cn/auth/next",
		}, true
	}

	engine := NewEngine(nil, NewHookRunner(nil, nil))
	platform := newFakePlatform("feishu")
	runtime := &channelRuntime{
		owner: engine, channel: Channel{ID: "channel-1", Type: "feishu"}, platform: platform,
		controlTasks: map[string]*channelControlState{},
		cliAuth: map[string]channelCLIAuthPending{
			"chat:chat-1": {CLIID: "lark-cli", SessionID: "auth-1", ControllerID: "user-1", Phase: "setup", LoginURL: "https://old"},
		},
	}
	engine.channels[runtime.channel.ID] = runtime
	msg := &Message{
		ChannelID: runtime.channel.ID, ChatID: "chat-1", ChatType: "p2p", ConversationKey: "chat:chat-1",
		UserID: "user-1", Text: "已完成",
	}
	engine.handleChannelMessageDirect(context.Background(), msg, eventData(msg))
	if len(platform.replies) != 1 || !strings.Contains(platform.replies[0], "https://open.feishu.cn/auth/next") {
		t.Fatalf("completion replies = %#v", platform.replies)
	}
	pending, ok := runtime.channelCLIAuthPending("chat:chat-1")
	if !ok || pending.Phase != "login" || pending.LoginURL != "https://open.feishu.cn/auth/next" {
		t.Fatalf("next pending auth = %+v, %v", pending, ok)
	}
}

func TestChannelCLIAuthStopCancelsManagedProcess(t *testing.T) {
	restoreChannelCLIAuthStubs(t)
	cancelled := ""
	cancelChannelCLIAuth = func(sessionID string) error {
		cancelled = sessionID
		return nil
	}
	engine := NewEngine(nil, NewHookRunner(nil, nil))
	platform := newFakePlatform("feishu")
	runtime := &channelRuntime{
		owner: engine, channel: Channel{ID: "channel-1", Type: "feishu"}, platform: platform,
		controlTasks: map[string]*channelControlState{},
		cliAuth: map[string]channelCLIAuthPending{
			"chat:chat-1": {CLIID: "lark-cli", SessionID: "auth-1", ControllerID: "user-1"},
		},
	}
	engine.channels[runtime.channel.ID] = runtime
	msg := &Message{
		ChannelID: runtime.channel.ID, ChatID: "chat-1", ChatType: "p2p", ConversationKey: "chat:chat-1",
		UserID: "user-1", Text: "/stop",
	}
	engine.handleChannelMessageDirect(context.Background(), msg, eventData(msg))
	if cancelled != "auth-1" || len(platform.replies) != 1 || !strings.Contains(platform.replies[0], "已停止") {
		t.Fatalf("cancelled = %q, replies = %#v", cancelled, platform.replies)
	}
	if _, ok := runtime.channelCLIAuthPending("chat:chat-1"); ok {
		t.Fatal("cancelled auth remained pending")
	}
}

func restoreChannelCLIAuthStubs(t *testing.T) {
	t.Helper()
	oldCheck := checkChannelCLIAuth
	oldStart := startChannelCLIAuth
	oldGet := getChannelCLIAuth
	oldCancel := cancelChannelCLIAuth
	t.Cleanup(func() {
		checkChannelCLIAuth = oldCheck
		startChannelCLIAuth = oldStart
		getChannelCLIAuth = oldGet
		cancelChannelCLIAuth = oldCancel
	})
}
