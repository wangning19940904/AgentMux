package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
)

func TestAgentConversationModesAPI(t *testing.T) {
	addTestRuntimeToPath(t, "codex")
	s, st := newTestServer(t)
	post := func(agent core.AgentInstance) core.AgentInstance {
		t.Helper()
		rec := doJSON(t, s, http.MethodPost, "/api/v1/agent-instances", agent)
		if rec.Code != http.StatusOK {
			t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
		}
		var saved core.AgentInstance
		if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
			t.Fatal(err)
		}
		return saved
	}
	saved := post(core.AgentInstance{Name: "Modes", RuntimeID: "codex", Enabled: true})
	if saved.PrivateChatMode != "chat" || saved.GroupChatMode != "chat-topic" {
		t.Fatalf("new Agent defaults = %+v", saved)
	}
	saved.PrivateChatMode, saved.GroupChatMode = "thread", "new-topic"
	saved = post(saved)
	stored, err := st.GetAgentInstance(context.Background(), saved.ID)
	if err != nil || stored == nil || stored.PrivateChatMode != "thread" || stored.GroupChatMode != "new-topic" {
		t.Fatalf("stored modes = %+v, err = %v", stored, err)
	}
	// A client predating these fields must not reset saved defaults.
	saved.PrivateChatMode, saved.GroupChatMode = "", ""
	saved = post(saved)
	if saved.PrivateChatMode != "thread" || saved.GroupChatMode != "new-topic" {
		t.Fatalf("omitted modes reset: %+v", saved)
	}
	for _, field := range []string{"private_chat_mode", "group_chat_mode"} {
		invalid := saved
		if field == "private_chat_mode" {
			invalid.PrivateChatMode = "chat-topic"
		} else {
			invalid.GroupChatMode = "thread"
		}
		rec := doJSON(t, s, http.MethodPost, "/api/v1/agent-instances", invalid)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), field) {
			t.Fatalf("invalid mode: %d %s", rec.Code, rec.Body.String())
		}
	}
	// Legacy records retain per-channel behavior until the user chooses modes.
	legacy := core.AgentInstance{ID: "legacy-agent", Name: "Legacy", RuntimeID: "codex"}
	if err := st.UpsertAgentInstance(context.Background(), &legacy); err != nil {
		t.Fatal(err)
	}
	legacy = post(legacy)
	if legacy.PrivateChatMode != "" || legacy.GroupChatMode != "" {
		t.Fatalf("legacy modes overwritten: %+v", legacy)
	}
}
