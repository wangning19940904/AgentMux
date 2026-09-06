package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func TestChannelAvatarAPIRoutingAndAuthorization(t *testing.T) {
	s, st, tenant, tenantToken := newTenantServer(t)
	var calls atomic.Int32
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.URL.Path {
		case "/open-apis/auth/v3/app_access_token/internal":
			_, _ = w.Write([]byte(`{"code":0,"app_access_token":"app-token"}`))
		case "/open-apis/bot/v3/info":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "bot": map[string]string{"app_name": "Bot", "avatar_url": upstream.URL + "/avatar.png"},
			})
		case "/avatar.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("avatar-bytes"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	oldBase := channelBotOpenAPIBase["feishu"]
	channelBotOpenAPIBase["feishu"] = upstream.URL
	t.Cleanup(func() { channelBotOpenAPIBase["feishu"] = oldBase })
	now := time.Now().UTC()
	for _, ch := range []core.Channel{
		{ID: "owned", OwnerTenantID: tenant.ID},
		{ID: "private", OwnerTenantID: "another-tenant"},
	} {
		ch.Name, ch.Type = "Bot", "feishu"
		ch.Config = map[string]string{"app_id": "app", "app_secret": "secret"}
		ch.Visibility = core.VisibilityPrivate
		ch.CreatedAt, ch.UpdatedAt = now, now
		if err := st.UpsertChannel(context.Background(), &ch); err != nil {
			t.Fatal(err)
		}
	}

	request := func(path, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8765"+path, nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		s.withAuth(s.mux).ServeHTTP(w, r)
		return w
	}
	listed := request("/api/v1/channels", tenantToken)
	var channels []apiChannel
	if err := json.Unmarshal(listed.Body.Bytes(), &channels); err != nil || len(channels) != 1 {
		t.Fatalf("channels = %s, error = %v", listed.Body.String(), err)
	}
	if got := channels[0].BotAvatarProxyURL; got != "/api/v1/channel-avatar?id=owned" {
		t.Fatalf("avatar URL = %q; must stay on the Console origin", got)
	}

	for _, tt := range []struct {
		name, path, token string
		status            int
	}{
		{"owner", channels[0].BotAvatarProxyURL, tenantToken, http.StatusOK},
		{"admin", "/api/v1/channel-avatar?id=private", "admin-secret", http.StatusOK},
		{"unauthenticated", channels[0].BotAvatarProxyURL, "", http.StatusUnauthorized},
		{"other tenant", "/api/v1/channel-avatar?id=private", tenantToken, http.StatusNotFound},
		{"missing channel", "/api/v1/channel-avatar?id=missing", tenantToken, http.StatusNotFound},
		{"missing id", "/api/v1/channel-avatar", tenantToken, http.StatusBadRequest},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before := calls.Load()
			response := request(tt.path, tt.token)
			if response.Code != tt.status {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, tt.status, response.Body.String())
			}
			if tt.status == http.StatusOK {
				if response.Body.String() != "avatar-bytes" || response.Header().Get("Content-Type") != "image/png" {
					t.Fatalf("avatar response = %s, headers = %v", response.Body.String(), response.Header())
				}
				if got := response.Header().Get("Cache-Control"); got != "private, max-age=300" {
					t.Fatalf("Cache-Control = %q", got)
				}
			} else if calls.Load() != before {
				t.Fatal("rejected request fetched bot data")
			}
		})
	}
}
