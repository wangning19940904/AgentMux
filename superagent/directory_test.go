package superagent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
)

// fakeFeishuDirectory answers the OpenAPI calls the directory makes. Chats
// spans two pages; oc_gone is a group the bot has left.
func fakeFeishuDirectory(t *testing.T, namesDenied bool) (*FeishuAPI, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get("Authorization")+" "+string(body))
		mu.Unlock()
		path, query := r.URL.Path, r.URL.Query()
		switch {
		case strings.HasSuffix(path, "tenant_access_token/internal"):
			_, _ = io.WriteString(w, `{"code":0,"tenant_access_token":"tat"}`)
		case path == "/open-apis/im/v1/chats" && query.Get("page_token") == "":
			_, _ = io.WriteString(w, `{"code":0,"data":{"items":[{"chat_id":"oc_1","name":"Team","external":false}],"has_more":true,"page_token":"p2"}}`)
		case path == "/open-apis/im/v1/chats":
			_, _ = io.WriteString(w, `{"code":0,"data":{"items":[{"chat_id":"oc_2","name":"Partners","external":true}],"has_more":false}}`)
		case path == "/open-apis/im/v1/chats/oc_1/members":
			_, _ = io.WriteString(w, `{"code":0,"data":{"items":[{"member_id":"ou_a","name":"Alice"},{"member_id":"ou_b","name":"Bob"}]}}`)
		case path == "/open-apis/im/v1/chats/oc_2/members":
			_, _ = io.WriteString(w, `{"code":0,"data":{"items":[{"member_id":"ou_b","name":"Bob"},{"member_id":"ou_c","name":"Carol"}]}}`)
		case strings.HasPrefix(path, "/open-apis/im/v1/chats/"):
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"code":232011,"msg":"bot is not in the chat"}`)
		case path == "/open-apis/contact/v3/users/batch_get_id":
			_, _ = io.WriteString(w, `{"code":0,"data":{"user_list":[{"user_id":"ou_d","email":"dan@acme.com"},{"email":"nobody@acme.com"}]}}`)
		case path == "/open-apis/contact/v3/users/batch" && namesDenied:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"code":99991672,"msg":"Access denied. One of the following scopes is required: [contact:user.base:readonly]"}`)
		case path == "/open-apis/contact/v3/users/batch":
			var items []map[string]string
			for _, id := range query["user_ids"] {
				items = append(items, map[string]string{"open_id": id, "name": "Name of " + id})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"items": items}})
		default:
			t.Errorf("unexpected call %s %s", r.Method, r.URL)
		}
	}))
	t.Cleanup(server.Close)
	api := &FeishuAPI{Base: server.URL, HTTP: server.Client(), Channels: func(_ context.Context, id string) (*core.Channel, error) {
		kind := "feishu"
		if id == "slack" {
			kind = "slack"
		}
		return &core.Channel{ID: id, Type: kind, Config: map[string]string{"app_id": "a", "app_secret": "s"}}, nil
	}}
	return api, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), calls...)
	}
}

func TestFeishuDirectoryListsTheBotsChats(t *testing.T) {
	api, calls := fakeFeishuDirectory(t, false)
	chats, err := api.Chats(context.Background(), "c")
	if err != nil {
		t.Fatal(err)
	}
	want := []FeishuChat{{ChatID: "oc_1", Name: "Team"}, {ChatID: "oc_2", Name: "Partners", External: true}}
	if len(chats.Items) != 2 || chats.Items[0] != want[0] || chats.Items[1] != want[1] || chats.Truncated {
		t.Fatalf("chats = %+v", chats)
	}
	for _, call := range calls()[1:] {
		if !strings.Contains(call, "Bearer tat") {
			t.Fatalf("every listing call must use the tenant token: %s", call)
		}
	}
	if _, err := api.Chats(context.Background(), "slack"); err == nil || !strings.Contains(err.Error(), "not a Feishu channel") {
		t.Fatalf("a non-Feishu channel must be refused before calling Feishu: %v", err)
	}
}

func TestFeishuDirectoryMembersMergeGroups(t *testing.T) {
	api, calls := fakeFeishuDirectory(t, false)
	members, err := api.Members(context.Background(), "c", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, user := range members.Items {
		got[user.OpenID] = user.Name + ":" + strings.Join(user.ChatIDs, ",")
	}
	if len(members.Items) != 3 || got["ou_a"] != "Alice:oc_1" || got["ou_b"] != "Bob:oc_1,oc_2" || got["ou_c"] != "Carol:oc_2" {
		t.Fatalf("with no groups given, members of every bot group, each person once: %+v", members.Items)
	}
	tokens := 0
	for _, call := range calls() {
		if strings.Contains(call, "tenant_access_token") {
			tokens++
		}
	}
	if tokens != 1 {
		t.Fatalf("one listing should fetch one tenant token, got %d", tokens)
	}

	members, err = api.Members(context.Background(), "c", []string{"oc_2", "oc_gone"})
	if err != nil {
		t.Fatal(err)
	}
	if len(members.Items) != 2 || members.Items[0].OpenID != "ou_b" || len(members.Warnings) != 1 || !strings.Contains(members.Warnings[0], "oc_gone: 232011") {
		t.Fatalf("an unreadable group is a warning beside the others: %+v", members)
	}
	if _, err := api.Members(context.Background(), "c", []string{"oc_gone"}); err == nil || !strings.Contains(err.Error(), "not in the chat") {
		t.Fatalf("when no group can be read the error is Feishu's: %v", err)
	}
}

func TestFeishuDirectoryUsers(t *testing.T) {
	api, calls := fakeFeishuDirectory(t, false)
	users, err := api.Users(context.Background(), "c", []string{"ou_a"}, []string{"dan@acme.com", "nobody@acme.com"})
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 || users[0].OpenID != "ou_a" || users[0].Name != "Name of ou_a" || users[1].OpenID != "ou_d" || users[1].Email != "dan@acme.com" {
		t.Fatalf("users = %+v", users)
	}
	if !strings.Contains(strings.Join(calls(), "\n"), `/batch_get_id?user_id_type=open_id Bearer tat {"emails":["dan@acme.com","nobody@acme.com"]}`) {
		t.Fatalf("emails must be looked up in the request body: %v", calls())
	}

	denied, _ := fakeFeishuDirectory(t, true)
	users, err = denied.Users(context.Background(), "c", nil, []string{"dan@acme.com"})
	if err != nil || len(users) != 1 || users[0].OpenID != "ou_d" || users[0].Name != "" {
		t.Fatalf("an email still resolves without the scope for names: %+v %v", users, err)
	}
	if _, err := denied.Users(context.Background(), "c", []string{"ou_a"}, nil); err == nil || !strings.Contains(err.Error(), "contact:user.base:readonly") {
		t.Fatalf("a name lookup reports the missing scope: %v", err)
	}
	if users, err := api.Users(context.Background(), "c", nil, nil); err != nil || len(users) != 0 {
		t.Fatalf("an empty lookup asks Feishu nothing: %+v %v", users, err)
	}
	tooMany := make([]string, maxUserLookup+1)
	for index := range tooMany {
		tooMany[index] = fmt.Sprintf("u%d@acme.com", index)
	}
	if _, err := api.Users(context.Background(), "c", nil, tooMany); err == nil {
		t.Fatal("a lookup over Feishu's batch limit must be refused")
	}
}
