package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	sessionstore "github.com/wangning19940904/AgentMux/sessions"
)

type testConversationSender struct {
	channelID      string
	conversationID string
	text           string
}

func (s *testConversationSender) SendToProject(context.Context, string, string) error {
	return nil
}

func (s *testConversationSender) SendToConversation(_ context.Context, channelID, conversationID, text string) (string, error) {
	s.channelID = channelID
	s.conversationID = conversationID
	s.text = text
	return "console answer", nil
}

func TestSessionRowsIncludeAgentAndChannelContext(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()
	now := time.Now().UTC()
	agent := core.AgentInstance{
		ID: "agent-research", Name: "Research Agent", RuntimeID: "codex",
		Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := st.UpsertAgentInstance(ctx, &agent); err != nil {
		t.Fatal(err)
	}
	channel := core.Channel{
		ID: "channel-feishu", Name: "Research group", Type: "feishu",
		AgentID: agent.ID, Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := st.UpsertChannel(ctx, &channel); err != nil {
		t.Fatal(err)
	}
	conversation, _, err := st.GetOrCreateConversation(ctx, core.Conversation{
		Scope: "channel:" + channel.ID, ConversationKey: "chat:oc_research",
		ChatID: "oc_research", ChatType: "group", AgentID: agent.ID,
		WorkDir: "/tmp/research", NativeSessionID: "thread-research",
	})
	if err != nil {
		t.Fatal(err)
	}
	native := []sessionstore.Meta{{
		ProviderID: "codex", Surface: "app-server", SessionID: "thread-research",
		Title: "Native title", ProjectDir: "/tmp/research", Available: true,
		LastActiveAt: now,
	}, {
		ProviderID: "claudecode", Surface: "cli", SessionID: "local-only",
		Title: "Local session", Available: true, LastActiveAt: now.Add(-time.Minute),
	}}

	rows, err := srv.enrichSessionRows(ctx, native, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2: %+v", len(rows), rows)
	}
	channelRow := rows[0]
	if channelRow.ConversationID != conversation.ID || channelRow.AgentName != agent.Name ||
		channelRow.ChannelName != channel.Name || channelRow.ChannelType != channel.Type ||
		channelRow.Origin != "channel" || !channelRow.CanChat {
		t.Fatalf("channel row missing context: %+v", channelRow)
	}
	if rows[1].Origin != "local" || rows[1].ConversationID != "" {
		t.Fatalf("local row was not preserved: %+v", rows[1])
	}
}

func TestSessionMessageSendUsesConversationSender(t *testing.T) {
	srv, _ := newTestServer(t)
	sender := &testConversationSender{}
	srv.sender = sender

	rec := doJSON(t, srv, http.MethodPost, "/api/v1/sessions/messages", map[string]string{
		"channel_id": "channel-1", "conversation_id": "conversation-1", "text": "hello",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		OK     bool   `json:"ok"`
		Answer string `json:"answer"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.OK || response.Answer != "console answer" {
		t.Fatalf("response = %+v", response)
	}
	if sender.channelID != "channel-1" || sender.conversationID != "conversation-1" || sender.text != "hello" {
		t.Fatalf("sender request = %+v", sender)
	}
}
