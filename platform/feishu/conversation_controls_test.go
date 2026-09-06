package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	"github.com/wangning19940904/AgentMux/core"
)

func TestQueueCardsTrackStateAndHideUnavailableSteer(t *testing.T) {
	msg := &core.Message{ChatID: "dm", ChatType: "p2p"}
	task := core.ChannelTask{ID: "task", Status: core.ChannelTaskQueued, ControlNonce: "secret", QueuePosition: 2, CanSteer: true, ConversationKey: "chat:dm"}
	raw := buildQueueTaskCard(msg, task, "publish B")
	for _, part := range []string{"调整方向", "取消", "第 2 项", queueControlAction, "secret"} {
		if !strings.Contains(raw, part) {
			t.Fatalf("missing %s: %s", part, raw)
		}
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["schema"] != "2.0" || parsed["body"] == nil || strings.Contains(raw, `"tag":"action"`) {
		t.Fatal("queue card must use schema-2 callback layout")
	}
	task.CanSteer = false
	raw = buildQueueTaskCard(msg, task, "publish B")
	if strings.Contains(raw, `"action":"steer"`) {
		t.Fatal("unsupported steer action shown")
	}
	task.Status = core.ChannelTaskSteered
	raw = buildQueueTaskCard(msg, task, "publish B")
	if strings.Contains(raw, queueControlAction) || !strings.Contains(raw, "已追加") {
		t.Fatal("terminal card still actionable")
	}
}

func TestQueueCallbackPreservesTaskAndRedactsNonce(t *testing.T) {
	client := &larkClient{platform: "feishu"}
	event := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Operator: &callback.Operator{OpenID: "user"},
		Context:  &callback.Context{OpenChatID: "chat", OpenMessageID: "card"},
		Action: &callback.CallBackAction{Value: map[string]interface{}{
			modelPickerActionKey: queueControlAction, "task_id": "queued", "nonce": "queue-secret",
			"action": "steer", "chat_id": "chat", "chat_type": "p2p", "conversation_key": "chat:chat",
		}},
	}}
	msg, ok := client.messageFromCardAction("channel:test", event)
	if !ok || msg.LogOnly || msg.ChannelTaskAction == nil || msg.ChannelTaskAction.Nonce != "queue-secret" || msg.ChannelTaskAction.TaskID != "queued" {
		t.Fatalf("callback = %+v", msg)
	}
	if strings.Contains(msg.Callback.ActionValue, "queue-secret") {
		t.Fatal("nonce leaked to audit log")
	}
}
func TestConversationModeCardsAndPrivateThreadReply(t *testing.T) {
	msg := &core.Message{ID: "seed", ChatID: "dm", ChatType: "p2p", ReplyInThread: true}
	if !shouldReplyInThread(msg) {
		t.Fatal("private topic was sent outside thread")
	}
	raw := buildConversationModeCard(msg, core.ConversationModeState{Private: true, UserID: "user", Mode: "thread"})
	for _, mode := range []string{"chat", "thread", "group"} {
		if !strings.Contains(raw, `"mode":"`+mode+`"`) {
			t.Fatal("missing private mode")
		}
	}
	msg.ChatMode = "chat"
	msg.ConversationKey = "chat:dm"
	msg.ReplyInThread = false
	msg.RootID = "quoted"
	msg.ThreadID = "thread"
	if shouldReplyInThread(msg) {
		t.Fatal("continuous private chat unexpectedly replied in thread")
	}
}
func TestSessionChatCreationRetriesSameUUID(t *testing.T) {
	var uuids []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "tenant_access_token") {
			fmt.Fprint(w, `{"code":0,"tenant_access_token":"token","expire":7200}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/im/v1/chats") {
			uuids = append(uuids, r.URL.Query().Get("uuid"))
			if len(uuids) == 1 {
				fmt.Fprint(w, `{"code":99991500,"msg":"temporary"}`)
				return
			}
			fmt.Fprint(w, `{"code":0,"data":{"chat_id":"created"}}`)
			return
		}
		t.Errorf("unexpected request %s", r.URL.Path)
	}))
	defer server.Close()
	client := &larkClient{api: lark.NewClient("test-app", "test-secret", lark.WithOpenBaseUrl(server.URL))}
	id, err := client.CreateSessionChat(context.Background(), "user", "title", "channel:seed")
	if err != nil || id != "created" || len(uuids) != 2 || uuids[0] == "" || uuids[0] != uuids[1] {
		t.Fatalf("id=%s err=%v uuids=%v", id, err, uuids)
	}
}
