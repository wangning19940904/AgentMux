package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/superagent"
)

// fakeFeishuUserDirectory adds what the Console does as the authorizing person.
type fakeFeishuUserDirectory struct {
	fakeFeishuDirectory
	approved bool
	polled   []string
	added    string
	query    string
	userErr  error
}

func (f *fakeFeishuUserDirectory) StartUserAuth(context.Context, string) (superagent.FeishuUserAuthStart, error) {
	return superagent.FeishuUserAuthStart{DeviceCode: "device-secret", UserCode: "UC", VerificationURL: "https://accounts.feishu.cn/verify?code=UC", ExpiresIn: 600, Interval: 5}, nil
}

func (f *fakeFeishuUserDirectory) PollUserAuth(_ context.Context, _ string, deviceCode string) (superagent.FeishuUserAuthPoll, error) {
	f.polled = append(f.polled, deviceCode)
	if !f.approved {
		return superagent.FeishuUserAuthPoll{Status: superagent.FeishuAuthPending}, nil
	}
	return superagent.FeishuUserAuthPoll{Status: superagent.FeishuAuthCompleted, User: &superagent.FeishuUserAuth{OpenID: "ou_me", Name: "Me"}}, nil
}

func (f *fakeFeishuUserDirectory) UserAuth(context.Context, string) (*superagent.FeishuUserAuth, error) {
	if !f.approved {
		return nil, nil
	}
	return &superagent.FeishuUserAuth{OpenID: "ou_me", Name: "Me"}, nil
}

func (f *fakeFeishuUserDirectory) ForgetUserAuth(context.Context, string) error {
	f.approved = false
	return nil
}

func (f *fakeFeishuUserDirectory) UserChats(context.Context, string) (superagent.FeishuChatList, error) {
	in := false
	return superagent.FeishuChatList{Items: []superagent.FeishuChat{{ChatID: "oc_side", Name: "Side", BotIn: &in}}}, f.userErr
}

func (f *fakeFeishuUserDirectory) AddBotToChat(_ context.Context, _, chatID string) (superagent.FeishuAddBotResult, error) {
	f.added = chatID
	return superagent.FeishuAddBotResult{Added: true}, f.userErr
}

func (f *fakeFeishuUserDirectory) UserContacts(_ context.Context, _, query string, _ []string) (superagent.FeishuUserList, error) {
	f.query = query
	return superagent.FeishuUserList{Items: []superagent.FeishuUser{{OpenID: "ou_a", Name: "张三"}}}, f.userErr
}

func TestFeishuUserRoutes(t *testing.T) {
	srv, st := newTestServer(t)
	directory := &fakeFeishuUserDirectory{}
	srv.SetSuperAgents(nil, directory)
	if err := st.UpsertChannel(context.Background(), &core.Channel{ID: "fs", Name: "Team bot", Type: "feishu"}); err != nil {
		t.Fatal(err)
	}

	recorder := doJSON(t, srv, http.MethodGet, "/api/v1/super-agents/feishu/user-auth?channel_id=fs", nil)
	if recorder.Code != http.StatusOK || strings.TrimSpace(recorder.Body.String()) != `{"user":null}` {
		t.Fatalf("not yet authorized: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = doJSON(t, srv, http.MethodPost, "/api/v1/super-agents/feishu/user-auth/start?channel_id=fs", nil)
	var started struct {
		SessionID       string `json:"session_id"`
		VerificationURL string `json:"verification_url"`
		UserCode        string `json:"user_code"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &started); err != nil || started.SessionID == "" || started.VerificationURL == "" || started.UserCode != "UC" {
		t.Fatalf("start: %d %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "device-secret") {
		t.Fatalf("the device code stays in the daemon: %s", recorder.Body.String())
	}

	poll := func(channel, session string) string {
		recorder := doJSON(t, srv, http.MethodPost, "/api/v1/super-agents/feishu/user-auth/poll?channel_id="+channel, map[string]string{"session_id": session})
		var body struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal(recorder.Body.Bytes(), &body)
		return body.Status
	}
	if status := poll("fs", started.SessionID); status != "pending" {
		t.Fatalf("before approval: %s", status)
	}
	if err := st.UpsertChannel(context.Background(), &core.Channel{ID: "other", Name: "Other bot", Type: "feishu"}); err != nil {
		t.Fatal(err)
	}
	if status := poll("other", started.SessionID); status != "expired" {
		t.Fatalf("a session belongs to the channel it was started for: %s", status)
	}
	directory.approved = true
	if status := poll("fs", started.SessionID); status != "completed" {
		t.Fatalf("after approval: %s", status)
	}
	if strings.Join(directory.polled, ",") != "device-secret,device-secret" {
		t.Fatalf("polls redeem the stored device code: %v", directory.polled)
	}
	if status := poll("fs", started.SessionID); status != "expired" {
		t.Fatalf("a finished session is gone: %s", status)
	}

	recorder = doJSON(t, srv, http.MethodGet, "/api/v1/super-agents/feishu/my-chats?channel_id=fs", nil)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"bot_in":false`) {
		t.Fatalf("my chats: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = doJSON(t, srv, http.MethodPost, "/api/v1/super-agents/feishu/my-chats/add-bot?channel_id=fs", map[string]string{"chat_id": "oc_side"})
	if recorder.Code != http.StatusOK || directory.added != "oc_side" || !strings.Contains(recorder.Body.String(), `"added":true`) {
		t.Fatalf("add bot: %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder = doJSON(t, srv, http.MethodPost, "/api/v1/super-agents/feishu/my-chats/add-bot?channel_id=fs", map[string]string{}); recorder.Code != http.StatusBadRequest {
		t.Fatalf("add bot needs a chat: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = doJSON(t, srv, http.MethodPost, "/api/v1/super-agents/feishu/contacts?channel_id=fs", map[string]any{"query": "张"})
	if recorder.Code != http.StatusOK || directory.query != "张" || !strings.Contains(recorder.Body.String(), "ou_a") {
		t.Fatalf("contacts: %d %s", recorder.Code, recorder.Body.String())
	}

	directory.userErr = superagent.ErrFeishuUserAuthRequired
	recorder = doJSON(t, srv, http.MethodGet, "/api/v1/super-agents/feishu/my-chats?channel_id=fs", nil)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), `"user_auth_required":true`) {
		t.Fatalf("a lapsed authorization asks for a new one: %d %s", recorder.Code, recorder.Body.String())
	}
	directory.userErr = &superagent.FeishuScopeError{Code: 99991679, Msg: "denied", Scopes: []string{"im:chat"}, TokenType: "user", ApplyURL: "https://open.feishu.cn/app/a/auth?q=im:chat&op_from=openapi&token_type=user"}
	recorder = doJSON(t, srv, http.MethodPost, "/api/v1/super-agents/feishu/my-chats/add-bot?channel_id=fs", map[string]string{"chat_id": "oc_side"})
	if recorder.Code != http.StatusBadGateway || !strings.Contains(recorder.Body.String(), `"token_type":"user"`) || !strings.Contains(recorder.Body.String(), "token_type=user") {
		t.Fatalf("a scope missing for the person: %d %s", recorder.Code, recorder.Body.String())
	}

	recorder = doJSON(t, srv, http.MethodDelete, "/api/v1/super-agents/feishu/user-auth?channel_id=fs", nil)
	if recorder.Code != http.StatusOK || directory.approved {
		t.Fatalf("forget: %d %s", recorder.Code, recorder.Body.String())
	}

	plain := &fakeFeishuDirectory{}
	srv.SetSuperAgents(nil, plain)
	if recorder = doJSON(t, srv, http.MethodGet, "/api/v1/super-agents/feishu/my-chats?channel_id=fs", nil); recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("a directory that cannot act as a person: %d %s", recorder.Code, recorder.Body.String())
	}
}
