package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	sessionstore "github.com/wangning19940904/AgentMux/sessions"
)

type testConversationSender struct {
	channelID      string
	conversationID string
	text           string
	runtimeState   core.ConversationRuntimeState
	runtimeChannel string
	runtimeKey     string
	stoppedTaskID  string
	terminalCalls  int
}

func (s *testConversationSender) SendToChannel(context.Context, core.ChannelDelivery) error {
	return nil
}

func (s *testConversationSender) SendToConversation(_ context.Context, channelID, conversationID, text string) (string, error) {
	s.channelID = channelID
	s.conversationID = conversationID
	s.text = text
	return "console answer", nil
}

func (s *testConversationSender) ConversationRuntimeState(_ context.Context, channelID, conversationKey string) (core.ConversationRuntimeState, error) {
	s.runtimeChannel = channelID
	s.runtimeKey = conversationKey
	return s.runtimeState, nil
}

func (s *testConversationSender) StopConversation(_ context.Context, channelID, conversationKey, expectedTaskID string) (core.ConversationRuntimeState, error) {
	s.runtimeChannel = channelID
	s.runtimeKey = conversationKey
	s.stoppedTaskID = expectedTaskID
	return core.ConversationRuntimeState{Status: core.ConversationStatusStopping, CanStop: true, TaskID: expectedTaskID}, nil
}

func (s *testConversationSender) TerminalSessionInfo(context.Context, string, core.Conversation) (core.TerminalSessionInfo, error) {
	s.terminalCalls++
	return core.TerminalSessionInfo{Backend: "tmux", Available: true}, nil
}

func (s *testConversationSender) TerminalSnapshot(context.Context, string, core.Conversation) (string, error) {
	return "", nil
}

func (s *testConversationSender) WriteTerminal(context.Context, string, core.Conversation, string, bool) error {
	return nil
}

func (s *testConversationSender) ResizeTerminal(context.Context, string, core.Conversation, int, int) error {
	return nil
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

func TestSessionRowsDoNotResumeHistoricalTerminalSessions(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()
	now := time.Now().UTC()
	agent := core.AgentInstance{
		ID: "agent-terminal", Name: "Terminal Agent", RuntimeID: "codex", SessionBackend: "tmux",
		Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := st.UpsertAgentInstance(ctx, &agent); err != nil {
		t.Fatal(err)
	}
	channel := core.Channel{
		ID: "channel-terminal", Name: "Terminal channel", Type: "feishu",
		AgentID: agent.ID, Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := st.UpsertChannel(ctx, &channel); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.GetOrCreateConversation(ctx, core.Conversation{
		Scope: "channel:" + channel.ID, ConversationKey: "chat:terminal", AgentID: agent.ID,
		NativeSessionID: "persisted-tmux-session", WorkDir: "/tmp/terminal",
	}); err != nil {
		t.Fatal(err)
	}
	sender := &testConversationSender{}
	srv.sender = sender
	rows, err := srv.enrichSessionRows(ctx, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].TerminalBackend != "tmux" {
		t.Fatalf("session rows = %+v", rows)
	}
	if sender.terminalCalls != 0 {
		t.Fatalf("list resumed %d historical terminal sessions", sender.terminalCalls)
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

func TestSessionRowsExposeRuntimeStateAndStopActiveConversation(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()
	now := time.Now().UTC()
	agent := core.AgentInstance{
		ID: "agent-stop", Name: "Stop Agent", RuntimeID: "codex",
		Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := st.UpsertAgentInstance(ctx, &agent); err != nil {
		t.Fatal(err)
	}
	channel := core.Channel{
		ID: "channel-stop", Name: "Stop channel", Type: "feishu",
		AgentID: agent.ID, Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := st.UpsertChannel(ctx, &channel); err != nil {
		t.Fatal(err)
	}
	conversation, _, err := st.GetOrCreateConversation(ctx, core.Conversation{
		Scope: "channel:" + channel.ID, ConversationKey: "chat:stop",
		ChatID: "stop", ChatType: "group", AgentID: agent.ID, WorkDir: "/tmp/stop",
	})
	if err != nil {
		t.Fatal(err)
	}
	sender := &testConversationSender{runtimeState: core.ConversationRuntimeState{
		Status: string(core.ChannelTaskRunning), CanStop: true, TaskID: "task-live",
	}}
	srv.sender = sender

	rows, err := srv.enrichSessionRows(ctx, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].RunStatus != string(core.ChannelTaskRunning) || !rows[0].CanStop || rows[0].ActiveTaskID != "task-live" {
		t.Fatalf("session rows = %+v", rows)
	}

	payload, err := json.Marshal(map[string]string{
		"channel_id": channel.ID, "conversation_id": conversation.ID, "active_task_id": "task-live",
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/stop", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:43210"
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body.String())
	}
	if sender.runtimeChannel != channel.ID || sender.runtimeKey != conversation.ConversationKey || sender.stoppedTaskID != "task-live" {
		t.Fatalf("stop request = channel %q key %q task %q", sender.runtimeChannel, sender.runtimeKey, sender.stoppedTaskID)
	}
}
