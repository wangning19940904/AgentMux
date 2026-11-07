package superagent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

type memoryGrants struct {
	mu     sync.Mutex
	grants map[string]core.FeishuUserGrant
}

func (m *memoryGrants) GetFeishuUserGrant(_ context.Context, channelID string) (*core.FeishuUserGrant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	grant, ok := m.grants[channelID]
	if !ok {
		return nil, nil
	}
	return &grant, nil
}

func (m *memoryGrants) PutFeishuUserGrant(_ context.Context, grant core.FeishuUserGrant) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.grants[grant.ChannelID] = grant
	return nil
}

func (m *memoryGrants) DeleteFeishuUserGrant(_ context.Context, channelID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.grants, channelID)
	return nil
}

// fakeFeishuUser is Feishu's accounts host and OpenAPI for one app, cli_app,
// whose bot is in oc_team but not oc_side.
type fakeFeishuUser struct {
	t            *testing.T
	mu           sync.Mutex
	polls        int
	scope        string
	refreshFail  string
	refreshes    []string
	requests     []string
	searches     []map[string]any
	chatSearches []map[string]any
}

func (fake *fakeFeishuUser) serve(w http.ResponseWriter, r *http.Request) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	auth := r.Header.Get("Authorization")
	fake.requests = append(fake.requests, r.Method+" "+r.URL.RequestURI()+" "+auth)
	write := func(value any) { _ = json.NewEncoder(w).Encode(value) }
	switch path := r.URL.Path; {
	case path == "/oauth/v1/device_authorization":
		user, secret, ok := r.BasicAuth()
		form, _ := url.ParseQuery(string(body))
		if !ok || user != "cli_app" || secret != "s3cret" || form.Get("client_id") != "cli_app" || !strings.HasSuffix(form.Get("scope"), "offline_access") {
			w.WriteHeader(http.StatusUnauthorized)
			write(map[string]any{"error": "invalid_client"})
			return
		}
		write(map[string]any{"device_code": "dc", "user_code": "UC-1", "verification_uri": "https://accounts.feishu.cn/oauth/v1/device/verify", "verification_uri_complete": "https://accounts.feishu.cn/oauth/v1/device/verify?code=UC-1", "expires_in": 600, "interval": 5})
	case path == "/oauth/v3/token" && strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded"):
		form, _ := url.ParseQuery(string(body))
		if form.Get("grant_type") != feishuDeviceGrant || form.Get("device_code") != "dc" || form.Get("client_secret") != "s3cret" {
			write(map[string]any{"error": "invalid_request"})
			return
		}
		fake.polls++
		if fake.polls == 1 {
			w.WriteHeader(http.StatusBadRequest)
			write(map[string]any{"error": "authorization_pending"})
			return
		}
		write(map[string]any{"access_token": "u-1", "refresh_token": "r-1", "expires_in": 7200, "refresh_token_expires_in": 604800, "scope": fake.scope, "token_type": "Bearer"})
	case path == "/oauth/v3/token":
		var refresh map[string]string
		_ = json.Unmarshal(body, &refresh)
		fake.refreshes = append(fake.refreshes, refresh["refresh_token"])
		if fake.refreshFail != "" {
			w.WriteHeader(http.StatusBadRequest)
			write(map[string]any{"error": fake.refreshFail})
			return
		}
		if refresh["grant_type"] != "refresh_token" || refresh["client_id"] != "cli_app" || refresh["client_secret"] != "s3cret" {
			write(map[string]any{"error": "invalid_request"})
			return
		}
		write(map[string]any{"access_token": "u-2", "refresh_token": "r-2", "expires_in": 7200, "refresh_token_expires_in": 604800})
	case path == "/open-apis/auth/v3/tenant_access_token/internal":
		write(map[string]any{"code": 0, "tenant_access_token": "tat"})
	case path == "/open-apis/authen/v1/user_info":
		write(map[string]any{"code": 0, "data": map[string]any{"open_id": "ou_me", "name": "王宁"}})
	case path == "/open-apis/im/v1/chats" && auth == "Bearer tat":
		write(map[string]any{"code": 0, "data": map[string]any{"items": []any{map[string]any{"chat_id": "oc_team", "name": "Team"}}}})
	case path == "/open-apis/im/v1/chats" && auth == "Bearer u-stale":
		w.WriteHeader(http.StatusBadRequest)
		write(map[string]any{"code": 99991677, "msg": "token expired"})
	case path == "/open-apis/im/v1/chats" && strings.HasPrefix(auth, "Bearer u-") && r.URL.Query().Get("types") == "p2p":
		if r.URL.Query().Get("sort_type") != "ByActiveTimeDesc" {
			fake.t.Errorf("recent contacts are read most recent first: %s", r.URL)
		}
		write(map[string]any{"code": 0, "data": map[string]any{"items": []any{
			map[string]any{"chat_id": "oc_p1", "name": "Bob", "p2p_target_id": "ou_b", "p2p_target_type": "user"},
			map[string]any{"chat_id": "oc_p2", "name": "Helper", "p2p_target_id": "ou_bot", "p2p_target_type": "bot"},
			map[string]any{"chat_id": "oc_p3", "name": "张三", "p2p_target_id": "ou_a", "p2p_target_type": "user"},
		}}})
	case path == "/open-apis/im/v1/chats" && strings.HasPrefix(auth, "Bearer u-"):
		if r.URL.Query().Get("sort_type") != "ByActiveTimeDesc" {
			fake.t.Errorf("the person's groups are read most recently active first: %s", r.URL)
		}
		write(map[string]any{"code": 0, "data": map[string]any{"items": []any{map[string]any{"chat_id": "oc_team", "name": "Team"}, map[string]any{"chat_id": "oc_side", "name": "Side"}}}})
	case path == "/open-apis/im/v1/chats/batch_query":
		var lookup map[string][]string
		_ = json.Unmarshal(body, &lookup)
		var items []any
		for _, id := range lookup["chat_ids"] {
			items = append(items, map[string]any{"chat_id": id, "name": "Name of " + id})
		}
		write(map[string]any{"code": 0, "data": map[string]any{"items": items}})
	case path == "/open-apis/im/v2/chats/search":
		var search map[string]any
		_ = json.Unmarshal(body, &search)
		fake.chatSearches = append(fake.chatSearches, search)
		write(map[string]any{"code": 0, "data": map[string]any{"items": []any{
			map[string]any{"meta_data": map[string]any{"chat_id": "oc_team", "name": "Team"}},
			map[string]any{"meta_data": map[string]any{"chat_id": "oc_found", "name": "Found", "external": true}},
			map[string]any{"meta_data": map[string]any{"chat_id": "oc_gone", "name": "Gone", "chat_status": "dissolved"}},
		}, "has_more": true}})
	case path == "/open-apis/application/v6/scopes" && auth == "Bearer tat":
		write(map[string]any{"code": 0, "data": map[string]any{"scopes": []any{
			map[string]any{"scope_name": "im:message", "scope_type": "tenant", "grant_status": 1},
			map[string]any{"scope_name": "im:chat:read", "scope_type": "tenant", "grant_status": 0},
			map[string]any{"scope_name": "im:chat:read", "scope_type": "user", "grant_status": 1},
		}}})
	case path == "/open-apis/im/v1/chats/oc_side/members" && r.URL.Query().Get("member_id_type") == "app_id":
		var add map[string][]string
		_ = json.Unmarshal(body, &add)
		if strings.Join(add["id_list"], ",") != "cli_app" {
			fake.t.Errorf("the bot is added by its app_id: %s", body)
		}
		write(map[string]any{"code": 0, "data": map[string]any{"invalid_id_list": []string{}, "not_existed_id_list": []string{}, "pending_approval_id_list": []string{}}})
	case path == "/open-apis/im/v1/chats/oc_locked/members":
		write(map[string]any{"code": 0, "data": map[string]any{"pending_approval_id_list": []string{"cli_app"}}})
	case path == "/open-apis/im/v1/chats/oc_noscope/members":
		w.WriteHeader(http.StatusBadRequest)
		write(map[string]any{"code": 99991679, "msg": "Unauthorized. required one of these privileges under the user identity: [im:chat.members:write_only, im:chat]"})
	case path == "/open-apis/contact/v3/users/search":
		var search map[string]any
		_ = json.Unmarshal(body, &search)
		fake.searches = append(fake.searches, search)
		person := func(id, zh, en, mail, info string) map[string]any {
			return map[string]any{"id": id, "display_info": info, "meta_data": map[string]any{"i18n_names": map[string]string{"zh_cn": zh, "en_us": en}, "enterprise_mail_address": mail}}
		}
		if filter, _ := search["filter"].(map[string]any); filter["user_ids"] != nil {
			var items []any
			for _, id := range filter["user_ids"].([]any) {
				if id == "ou_a" {
					items = append(items, person("ou_a", "张三", "San", "san@acme.com", "<h>张三</h>\n研发部\n3 天前聊过"))
				}
			}
			write(map[string]any{"code": 0, "data": map[string]any{"items": items}})
			return
		}
		if r.URL.Query().Get("page_token") == "" {
			write(map[string]any{"code": 0, "data": map[string]any{"items": []any{person("ou_a", "张三", "San", "san@acme.com", "<h>张三</h>\n研发部\n3 天前聊过")}, "has_more": true, "page_token": "p2"}})
			return
		}
		write(map[string]any{"code": 0, "data": map[string]any{"items": []any{person("ou_b", "", "Bob", "", "Bob")}, "has_more": false}})
	default:
		fake.t.Errorf("unexpected call %s %s", r.Method, r.URL)
		w.WriteHeader(http.StatusNotFound)
	}
}

func newFeishuUserTest(t *testing.T, scope string) (*FeishuAPI, *fakeFeishuUser, *memoryGrants, *time.Time) {
	t.Helper()
	fake := &fakeFeishuUser{t: t, scope: scope}
	server := httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(server.Close)
	grants := &memoryGrants{grants: map[string]core.FeishuUserGrant{}}
	now := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	api := &FeishuAPI{
		Base: server.URL, Accounts: server.URL, HTTP: server.Client(), Grants: grants, Now: func() time.Time { return now },
		Channels: func(_ context.Context, id string) (*core.Channel, error) {
			return &core.Channel{ID: id, Type: "feishu", Config: map[string]string{"app_id": "cli_app", "app_secret": "s3cret"}}, nil
		},
	}
	return api, fake, grants, &now
}

func TestFeishuUserAuthDeviceFlow(t *testing.T) {
	api, _, grants, _ := newFeishuUserTest(t, "im:chat:read contact:user:search offline_access")
	ctx := context.Background()
	if auth, err := api.UserAuth(ctx, "c"); auth != nil || err != nil {
		t.Fatalf("nobody has authorized yet: %+v %v", auth, err)
	}
	if _, err := api.UserChats(ctx, "c"); !errors.Is(err, ErrFeishuUserAuthRequired) {
		t.Fatalf("listing as the person needs their authorization first: %v", err)
	}

	start, err := api.StartUserAuth(ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	if start.DeviceCode != "dc" || start.UserCode != "UC-1" || start.VerificationURL != "https://accounts.feishu.cn/oauth/v1/device/verify?code=UC-1" || start.Interval != 5 {
		t.Fatalf("start = %+v", start)
	}
	encoded, _ := json.Marshal(start)
	if strings.Contains(string(encoded), "dc") {
		t.Fatalf("the device code must not reach the Console: %s", encoded)
	}

	if poll, err := api.PollUserAuth(ctx, "c", "dc"); err != nil || poll.Status != FeishuAuthPending {
		t.Fatalf("before approval: %+v %v", poll, err)
	}
	poll, err := api.PollUserAuth(ctx, "c", "dc")
	if err != nil || poll.Status != FeishuAuthCompleted || poll.User == nil || poll.User.OpenID != "ou_me" || poll.User.Name != "王宁" {
		t.Fatalf("after approval: %+v %v", poll, err)
	}
	if strings.Join(poll.User.Missing, ",") != "im:chat.members:write_only" ||
		poll.User.ApplyURL != "https://open.feishu.cn/app/cli_app/auth?q=im:chat.members:write_only&op_from=openapi&token_type=user" {
		t.Fatalf("a scope the app had not enabled is named with where to enable it: %+v", poll.User)
	}
	grant := grants.grants["c"]
	if grant.AccessToken != "u-1" || grant.RefreshToken != "r-1" || !grant.AccessExpiresAt.Equal(time.Date(2026, 11, 7, 11, 0, 0, 0, time.UTC)) {
		t.Fatalf("grant = %+v", grant)
	}
	encoded, _ = json.Marshal(poll)
	if strings.Contains(string(encoded), "u-1") || strings.Contains(string(encoded), "r-1") {
		t.Fatalf("tokens must not reach the Console: %s", encoded)
	}
	if auth, err := api.UserAuth(ctx, "c"); err != nil || auth == nil || auth.OpenID != "ou_me" {
		t.Fatalf("authorized: %+v %v", auth, err)
	}
}

func TestFeishuUserTokenRefreshes(t *testing.T) {
	api, fake, grants, now := newFeishuUserTest(t, "")
	ctx := context.Background()
	grants.grants["c"] = core.FeishuUserGrant{ChannelID: "c", OpenID: "ou_me", AccessToken: "u-1", RefreshToken: "r-1", AccessExpiresAt: now.Add(time.Hour), RefreshExpiresAt: now.Add(48 * time.Hour)}
	if _, err := api.UserChats(ctx, "c"); err != nil {
		t.Fatal(err)
	}
	if len(fake.refreshes) != 0 {
		t.Fatalf("a current token is used as it is: %v", fake.refreshes)
	}

	*now = now.Add(59 * time.Minute)
	if _, err := api.UserChats(ctx, "c"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(fake.refreshes, ",") != "r-1" {
		t.Fatalf("a token about to expire is refreshed once: %v", fake.refreshes)
	}
	grant := grants.grants["c"]
	if grant.AccessToken != "u-2" || grant.RefreshToken != "r-2" || !grant.AccessExpiresAt.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("the refreshed and rotated tokens are kept: %+v", grant)
	}
	if !strings.Contains(strings.Join(fake.requests, "\n"), "GET /open-apis/im/v1/chats?sort_type=ByActiveTimeDesc&page_size=100 Bearer u-2") {
		t.Fatalf("calls use the new token: %v", fake.requests)
	}

	*now = now.Add(3 * time.Hour)
	fake.refreshFail = "invalid_grant"
	if _, err := api.UserChats(ctx, "c"); !errors.Is(err, ErrFeishuUserAuthRequired) {
		t.Fatalf("a revoked refresh token asks the person to authorize again: %v", err)
	}
	if _, ok := grants.grants["c"]; ok {
		t.Fatal("a grant that can no longer refresh is forgotten")
	}

	grants.grants["c"] = core.FeishuUserGrant{ChannelID: "c", AccessToken: "u-3", RefreshToken: "r-3", AccessExpiresAt: now.Add(-time.Minute), RefreshExpiresAt: now.Add(-time.Second)}
	if auth, err := api.UserAuth(ctx, "c"); auth != nil || err != nil {
		t.Fatalf("an expired refresh token is not shown as authorized: %+v %v", auth, err)
	}
	fake.refreshFail = "server_error"
	grants.grants["c"] = core.FeishuUserGrant{ChannelID: "c", AccessToken: "u-3", RefreshToken: "r-3", AccessExpiresAt: now.Add(-time.Minute)}
	if _, err := api.UserChats(ctx, "c"); err == nil || errors.Is(err, ErrFeishuUserAuthRequired) {
		t.Fatalf("a passing failure is an error but keeps the grant: %v", err)
	}
	if _, ok := grants.grants["c"]; !ok {
		t.Fatal("a passing refresh failure must not forget the grant")
	}
}

func TestFeishuUserGroupsAndAddingTheBot(t *testing.T) {
	api, fake, grants, now := newFeishuUserTest(t, "")
	ctx := context.Background()
	grants.grants["c"] = core.FeishuUserGrant{ChannelID: "c", OpenID: "ou_me", AccessToken: "u-1", AccessExpiresAt: now.Add(time.Hour)}

	chats, err := api.UserChats(ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	if len(chats.Items) != 2 || chats.Items[0].BotIn == nil || !*chats.Items[0].BotIn || chats.Items[1].BotIn == nil || *chats.Items[1].BotIn {
		t.Fatalf("the person's groups say whether the bot is in each: %+v", chats.Items)
	}

	if result, err := api.AddBotToChat(ctx, "c", "oc_side"); err != nil || !result.Added || result.Pending {
		t.Fatalf("add = %+v %v", result, err)
	}
	if !strings.Contains(strings.Join(fake.requests, "\n"), "POST /open-apis/im/v1/chats/oc_side/members?member_id_type=app_id Bearer u-1") {
		t.Fatalf("the bot is added as the person: %v", fake.requests)
	}
	if result, err := api.AddBotToChat(ctx, "c", "oc_locked"); err != nil || result.Added || !result.Pending {
		t.Fatalf("a group that approves members first: %+v %v", result, err)
	}
	if _, err := api.AddBotToChat(ctx, "c", "../oc_side"); err == nil {
		t.Fatal("only chat ids are accepted")
	}
	_, err = api.AddBotToChat(ctx, "c", "oc_noscope")
	var denied *FeishuScopeError
	if !errors.As(err, &denied) || denied.TokenType != "user" ||
		denied.ApplyURL != "https://open.feishu.cn/app/cli_app/auth?q=im:chat.members:write_only,im:chat&op_from=openapi&token_type=user" {
		t.Fatalf("a scope missing for the person links to the user-identity scopes: %v", err)
	}
}

func TestFeishuUserContacts(t *testing.T) {
	api, fake, grants, now := newFeishuUserTest(t, "")
	ctx := context.Background()
	grants.grants["c"] = core.FeishuUserGrant{ChannelID: "c", OpenID: "ou_me", AccessToken: "u-1", AccessExpiresAt: now.Add(time.Hour)}

	people, err := api.UserContacts(ctx, "c", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(people.Items) != 2 || people.Items[0].OpenID != "ou_b" || people.Items[0].Name != "Bob" || people.Items[0].Email != "" ||
		people.Items[1].OpenID != "ou_a" || people.Items[1].Name != "张三" || people.Items[1].Email != "san@acme.com" || people.Items[1].Department != "研发部" {
		t.Fatalf("with nothing typed, the people of the person's recent 1:1 chats, most recent first, bots left out: %+v", people.Items)
	}
	filter, _ := fake.searches[0]["filter"].(map[string]any)
	if ids, _ := filter["user_ids"].([]any); len(ids) != 2 || ids[0] != "ou_b" {
		t.Fatalf("emails and departments are looked up for them: %v", fake.searches[0])
	}

	found, err := api.UserContacts(ctx, "c", " 张 ", nil)
	if err != nil {
		t.Fatal(err)
	}
	if search := fake.searches[len(fake.searches)-1]; search["query"] != "张" || search["filter"] != nil {
		t.Fatalf("typed text searches everyone the person can see: %v", search)
	}
	if len(found.Items) != 1 || found.Items[0].Name != "张三" || found.Items[0].Department != "研发部" {
		t.Fatalf("search results, named in the tenant's language: %+v", found.Items)
	}
	if _, err := api.UserContacts(ctx, "c", "", []string{"ou_a", "dan@acme.com", "ou_a"}); err != nil {
		t.Fatal(err)
	}
	search := fake.searches[len(fake.searches)-1]
	ids, _ := search["filter"].(map[string]any)["user_ids"].([]any)
	if len(ids) != 1 || ids[0] != "ou_a" {
		t.Fatalf("saved senders are looked up by open_id: %v", search)
	}
	if _, err := api.UserContacts(ctx, "c", strings.Repeat("长", 51), nil); err == nil {
		t.Fatal("Feishu takes at most 50 characters")
	}
}

func TestFeishuUserFindsChats(t *testing.T) {
	api, fake, grants, now := newFeishuUserTest(t, "")
	ctx := context.Background()
	grants.grants["c"] = core.FeishuUserGrant{ChannelID: "c", OpenID: "ou_me", AccessToken: "u-1", AccessExpiresAt: now.Add(time.Hour)}

	found, err := api.FindUserChats(ctx, "c", "ops-team", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Items) != 2 || found.Items[0].ChatID != "oc_team" || found.Items[1].ChatID != "oc_found" || !found.Items[1].External || !found.Truncated {
		t.Fatalf("search results, without dissolved groups: %+v", found)
	}
	if found.Items[0].BotIn == nil || !*found.Items[0].BotIn || found.Items[1].BotIn == nil || *found.Items[1].BotIn {
		t.Fatalf("search results say where the bot is: %+v", found.Items)
	}
	search := fake.chatSearches[0]
	types, _ := search["filter"].(map[string]any)["search_types"].([]any)
	if search["query"] != `"ops-team"` || len(types) != 3 || types[2] != "public_joined" {
		t.Fatalf("a hyphenated keyword is quoted, and public groups the person is not in are left out: %v", search)
	}

	named, err := api.FindUserChats(ctx, "c", "", []string{"oc_x", "nope", "oc_x"})
	if err != nil {
		t.Fatal(err)
	}
	if len(named.Items) != 1 || named.Items[0].Name != "Name of oc_x" {
		t.Fatalf("chosen chat ids are named: %+v", named)
	}
	if empty, err := api.FindUserChats(ctx, "c", "", []string{"nope"}); err != nil || len(empty.Items) != 0 {
		t.Fatalf("nothing valid to look up: %+v %v", empty, err)
	}
	if _, err := api.FindUserChats(ctx, "c", strings.Repeat("群", 65), nil); err == nil {
		t.Fatal("Feishu's chat search takes at most 64 characters")
	}
}

func TestFeishuScopeStatus(t *testing.T) {
	api, _, _, _ := newFeishuUserTest(t, "")
	status, err := api.ScopeStatus(context.Background(), "c", []string{"im:message", "im:chat:read", "cardkit:card:write"}, []string{"im:chat:read", "contact:user:search"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(status.Tenant.Missing, ",") != "cardkit:card:write" || strings.Join(status.Tenant.Pending, ",") != "im:chat:read" ||
		status.Tenant.ApplyURL != "https://open.feishu.cn/app/cli_app/auth?q=cardkit:card:write&op_from=openapi&token_type=tenant" {
		t.Fatalf("app scopes: %+v", status.Tenant)
	}
	if strings.Join(status.User.Missing, ",") != "contact:user:search" || len(status.User.Pending) != 0 ||
		status.User.ApplyURL != "https://open.feishu.cn/app/cli_app/auth?q=contact:user:search&op_from=openapi&token_type=user" {
		t.Fatalf("user scopes: %+v", status.User)
	}
}

// A token Feishu stops accepting before its time is refreshed on the next
// call; only one that cannot be refreshed asks for a new authorization.
func TestFeishuUserTokenRejectedEarly(t *testing.T) {
	api, fake, grants, now := newFeishuUserTest(t, "")
	ctx := context.Background()
	grants.grants["c"] = core.FeishuUserGrant{ChannelID: "c", AccessToken: "u-stale", RefreshToken: "r-1", AccessExpiresAt: now.Add(time.Hour)}
	if _, err := api.UserChats(ctx, "c"); err == nil || errors.Is(err, ErrFeishuUserAuthRequired) {
		t.Fatalf("the first call fails without asking to authorize again: %v", err)
	}
	if _, err := api.UserChats(ctx, "c"); err != nil {
		t.Fatalf("the next call refreshes: %v", err)
	}
	if strings.Join(fake.refreshes, ",") != "r-1" || grants.grants["c"].AccessToken != "u-2" {
		t.Fatalf("refreshes=%v grant=%+v", fake.refreshes, grants.grants["c"])
	}

	grants.grants["c"] = core.FeishuUserGrant{ChannelID: "c", AccessToken: "u-stale", AccessExpiresAt: now.Add(time.Hour)}
	if _, err := api.UserChats(ctx, "c"); !errors.Is(err, ErrFeishuUserAuthRequired) {
		t.Fatalf("without a refresh token the person authorizes again: %v", err)
	}
	if _, ok := grants.grants["c"]; ok {
		t.Fatal("that grant is forgotten")
	}
}
