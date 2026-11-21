package core

import (
	"strings"
	"testing"
)

func TestActiveTurnIdentityNamesTheRunningTurnOnly(t *testing.T) {
	engine := NewEngine(nil, nil)
	runtime := &channelRuntime{
		owner: engine, channel: Channel{ID: "ch", Type: "feishu", AgentID: "a-channel"},
		controlTasks: map[string]*channelControlState{}, directTurns: map[string]*directChannelTurn{},
	}
	runtime.controlTasks["root:1"] = &channelControlState{active: &runtimeChannelTask{
		msg:        &Message{ChatID: "oc_1", ChatType: "p2p", UserID: "ou_alice"},
		generation: &channelAgentGeneration{workspace: WorkspaceInitOptions{AgentID: "a1", MemoryScope: "team"}},
	}}
	runtime.directTurns["root:2"] = &directChannelTurn{msg: &Message{ChatID: "oc_2", ChatType: "group", UserID: "ou_bob"}}
	runtime.controlTasks["root:3"] = &channelControlState{}
	engine.channels[runtime.channel.ID] = runtime

	got, err := engine.ActiveTurnIdentity("ch", "root:1")
	if err != nil || got.AgentID != "a1" || got.MemoryScope != "team" || got.UserID != "ou_alice" || got.ChatType != "p2p" {
		t.Fatalf("queued turn identity = %+v, %v", got, err)
	}
	got, err = engine.ActiveTurnIdentity("ch", "root:2")
	if err != nil || got.AgentID != "a-channel" || got.UserID != "ou_bob" || got.ChatType != "group" {
		t.Fatalf("direct turn identity = %+v, %v", got, err)
	}
	for _, key := range []string{"root:3", "root:9", ""} {
		if _, err := engine.ActiveTurnIdentity("ch", key); err == nil {
			t.Fatalf("conversation %q without a running turn has an identity", key)
		}
	}
	if _, err := engine.ActiveTurnIdentity("other", "root:1"); err == nil {
		t.Fatal("a channel that is not running has an identity")
	}
}

func TestPromptUserTextUndoesMemoryAndChannelWrapping(t *testing.T) {
	msg := &Message{ID: "m1", ChatID: "oc_1", ChatType: "p2p", UserID: "ou_1", Text: "下周把数据库迁移到 PG 18"}
	wrapped := channelMessageForAgent(Channel{Type: "feishu"}, msg).Text
	if !strings.HasPrefix(wrapped, feishuMessagePromptIntro) {
		t.Fatalf("wrapped prompt = %q", wrapped)
	}
	withMemory := memoryContextOpen + "\n- [global] note" + memoryContextClose + wrapped
	if got := ChannelPromptUserText(StripMemoryContext(withMemory)); got != msg.Text {
		t.Fatalf("user text = %q, want %q", got, msg.Text)
	}
	withAttachments := promptWithAttachmentReferences(wrapped, []AgentAttachment{{Kind: "file", Name: "plan.md", Path: "/tmp/plan.md"}}, true)
	if got := ChannelPromptUserText(withAttachments); !strings.HasPrefix(got, msg.Text+"\n\n") || !strings.Contains(got, "/tmp/plan.md") {
		t.Fatalf("user text with attachments = %q", got)
	}
	for _, plain := range []string{"just text", memoryContextOpen + " unterminated"} {
		if got := ChannelPromptUserText(StripMemoryContext(plain)); got != plain {
			t.Fatalf("plain prompt %q became %q", plain, got)
		}
	}
	if !strings.Contains(channelInteractionContract, "<delivery_cli> history search --channel-id") {
		t.Fatal("the channel contract does not tell agents about history search")
	}
}

func TestPrivateChatTypeFailsClosed(t *testing.T) {
	for _, c := range []struct {
		chatType, user string
		private        bool
	}{
		{"p2p", "u", true}, {"", "u", true}, {"direct", "u", true},
		{"group", "u", false}, {"topic_group", "u", false}, {"p2p", "", false},
	} {
		if got := PrivateChatType(c.chatType, c.user); got != c.private {
			t.Errorf("PrivateChatType(%q, %q) = %v", c.chatType, c.user, got)
		}
	}
}
