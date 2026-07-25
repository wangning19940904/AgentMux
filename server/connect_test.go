package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/config"
	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/provider"
	"github.com/wangning19940904/AgentMux/store"

	// Register agent adapters for agent instance validation.
	_ "github.com/wangning19940904/AgentMux/agent"
	// Register platform adapters for channel type validation.
	_ "github.com/wangning19940904/AgentMux/platform"
)

func newTestServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return New(config.Default(), nil, st, nil, nil), st
}

func newTestServerWithProvider(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return New(config.Default(), nil, st, provider.NewManager(st), nil), st
}

func doJSON(t *testing.T, s *Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	return rec
}

func TestChannelUpsertValidationAndSecretRoundTrip(t *testing.T) {
	s, st := newTestServer(t)
	ctx := context.Background()

	// Unknown platform type is rejected.
	rec := doJSON(t, s, http.MethodPost, "/api/v1/channels", core.Channel{Name: "x", Type: "nope"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown type: code = %d", rec.Code)
	}

	// Valid telegram channel with a secret.
	rec = doJSON(t, s, http.MethodPost, "/api/v1/channels", core.Channel{
		Name: "ops", Type: "telegram",
		Config:  map[string]string{"token": "tg-secret", "note": "hello"},
		Enabled: false,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("upsert: code = %d body = %s", rec.Code, rec.Body.String())
	}
	var saved core.Channel
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.ID == "" || saved.Config["token"] != "<redacted>" || saved.Config["note"] != "hello" {
		t.Fatalf("saved = %+v", saved)
	}

	// List redacts secrets too.
	rec = doJSON(t, s, http.MethodGet, "/api/v1/channels", nil)
	var listed []apiChannel
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Config["token"] != "<redacted>" {
		t.Fatalf("listed = %+v", listed)
	}

	// Round-tripping the redacted value must preserve the stored secret.
	saved.Config["note"] = "updated"
	rec = doJSON(t, s, http.MethodPost, "/api/v1/channels", saved)
	if rec.Code != http.StatusOK {
		t.Fatalf("round-trip: code = %d body = %s", rec.Code, rec.Body.String())
	}
	stored, err := st.GetChannel(ctx, saved.ID)
	if err != nil || stored == nil {
		t.Fatal(err)
	}
	if stored.Config["token"] != "tg-secret" || stored.Config["note"] != "updated" {
		t.Fatalf("stored after round-trip = %+v", stored.Config)
	}
}

func TestFeishuChannelConfigDefaultsValidationAndSecretRoundTrip(t *testing.T) {
	s, st := newTestServer(t)
	ctx := context.Background()

	rec := doJSON(t, s, http.MethodPost, "/api/v1/channels", core.Channel{
		Name: "Feishu", Type: "feishu",
		Config: map[string]string{
			"app_id":                            "cli_test",
			"app_secret":                        "secret",
			core.ChannelConfigAckReaction:       "yes",
			core.ChannelConfigAckReactionEmojis: " OK, THANKS, ",
		},
		Enabled: false,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("upsert: code = %d body = %s", rec.Code, rec.Body.String())
	}
	var saved core.Channel
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Config["app_secret"] != "<redacted>" {
		t.Fatalf("saved secret = %q", saved.Config["app_secret"])
	}
	want := map[string]string{
		core.ChannelConfigReplyScope:        core.ReplyScopeDMAndMentions,
		core.ChannelConfigReplyMode:         core.ReplyModeStreamMessage,
		core.ChannelConfigAckReaction:       "true",
		core.ChannelConfigAckReactionEmojis: "OK,THANKS",
	}
	for k, v := range want {
		if saved.Config[k] != v {
			t.Fatalf("saved config[%s] = %q, want %q; config=%+v", k, saved.Config[k], v, saved.Config)
		}
	}

	stored, err := st.GetChannel(ctx, saved.ID)
	if err != nil || stored == nil {
		t.Fatal(err)
	}
	if stored.Config["app_secret"] != "secret" {
		t.Fatalf("stored secret = %q", stored.Config["app_secret"])
	}
	for k, v := range want {
		if stored.Config[k] != v {
			t.Fatalf("stored config[%s] = %q, want %q; config=%+v", k, stored.Config[k], v, stored.Config)
		}
	}

	saved.Config["app_secret"] = "<redacted>"
	saved.Config[core.ChannelConfigReplyMode] = core.ReplyModeStreamCard
	rec = doJSON(t, s, http.MethodPost, "/api/v1/channels", saved)
	if rec.Code != http.StatusOK {
		t.Fatalf("round-trip: code = %d body = %s", rec.Code, rec.Body.String())
	}
	stored, err = st.GetChannel(ctx, saved.ID)
	if err != nil || stored == nil {
		t.Fatal(err)
	}
	if stored.Config["app_secret"] != "secret" || stored.Config[core.ChannelConfigReplyMode] != core.ReplyModeStreamCard {
		t.Fatalf("stored after round-trip = %+v", stored.Config)
	}

	invalids := []core.Channel{
		{Name: "bad scope", Type: "feishu", Config: map[string]string{"app_id": "cli_test", "app_secret": "secret", core.ChannelConfigReplyScope: "direct"}},
		{Name: "bad mode", Type: "feishu", Config: map[string]string{"app_id": "cli_test", "app_secret": "secret", core.ChannelConfigReplyMode: "lark_cli"}},
		{Name: "bad ack", Type: "feishu", Config: map[string]string{"app_id": "cli_test", "app_secret": "secret", core.ChannelConfigAckReaction: "sometimes"}},
	}
	for _, ch := range invalids {
		rec = doJSON(t, s, http.MethodPost, "/api/v1/channels", ch)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: code = %d body = %s", ch.Name, rec.Code, rec.Body.String())
		}
	}
}

func TestPlatformsIncludeLarkAlias(t *testing.T) {
	s, _ := newTestServer(t)
	rec := doJSON(t, s, http.MethodGet, "/api/v1/platforms", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("platforms: code = %d body = %s", rec.Code, rec.Body.String())
	}
	var platforms []string
	if err := json.Unmarshal(rec.Body.Bytes(), &platforms); err != nil {
		t.Fatal(err)
	}
	if !containsString(platforms, "feishu") || !containsString(platforms, "lark") {
		t.Fatalf("platforms = %+v, want feishu and lark", platforms)
	}
}

func TestAgentDefaultModelValidation(t *testing.T) {
	s, st := newTestServerWithProvider(t)
	ctx := context.Background()
	p := &core.Provider{
		ID:        "relay",
		Name:      "Relay",
		BaseURL:   "http://relay.local",
		Model:     "gpt-5",
		Meta:      core.ProviderMeta{SupportedModels: []string{"gpt-5-mini", "gpt-5"}},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := st.UpsertProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := st.SetActiveProvider(ctx, "codex", p.ID); err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, s, http.MethodPost, "/api/v1/agent-instances", core.AgentInstance{
		Name:         "Research",
		RuntimeID:    "codex",
		ProviderTool: "codex",
		DefaultModel: "gpt-5-mini",
		Enabled:      true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("valid default model: code = %d body = %s", rec.Code, rec.Body.String())
	}
	var saved core.AgentInstance
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.DefaultModel != "gpt-5-mini" {
		t.Fatalf("saved default model = %q", saved.DefaultModel)
	}

	rec = doJSON(t, s, http.MethodPost, "/api/v1/agent-instances", core.AgentInstance{
		Name:         "Bad model",
		RuntimeID:    "codex",
		ProviderTool: "codex",
		DefaultModel: "missing-model",
		Enabled:      true,
	})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "not supported") {
		t.Fatalf("invalid default model: code = %d body = %s", rec.Code, rec.Body.String())
	}

	override := core.AgentInstance{
		Name:         "Override",
		RuntimeID:    "codex",
		ProviderTool: "codex",
		ProviderID:   "relay",
		DefaultModel: "gpt-5",
		Enabled:      true,
	}
	if err := s.normalizeAgentInstance(ctx, &override); err != nil {
		t.Fatalf("override valid model: %v", err)
	}
	override.ID = ""
	override.DefaultModel = "nope"
	if err := s.normalizeAgentInstance(ctx, &override); err == nil {
		t.Fatal("override invalid model unexpectedly passed")
	}
}

func TestChannelsListIncludesFeishuBotInfo(t *testing.T) {
	s, _ := newTestServer(t)
	oldBase := channelBotOpenAPIBase["feishu"]
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open-apis/auth/v3/app_access_token/internal":
			if r.Method != http.MethodPost {
				t.Fatalf("token method = %s", r.Method)
			}
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["app_id"] != "cli_test" || body["app_secret"] != "secret" {
				t.Fatalf("token body = %+v", body)
			}
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","app_access_token":"app-token"}`))
		case "/open-apis/bot/v3/info":
			if got := r.Header.Get("Authorization"); got != "Bearer app-token" {
				t.Fatalf("auth header = %q", got)
			}
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","bot":{"app_name":"Wang Bot","avatar_url":"` + upstream.URL + `/avatar.png","open_id":"ou_1"}}`))
		case "/avatar.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("png"))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer upstream.Close()
	channelBotOpenAPIBase["feishu"] = upstream.URL
	t.Cleanup(func() { channelBotOpenAPIBase["feishu"] = oldBase })

	rec := doJSON(t, s, http.MethodPost, "/api/v1/channels", core.Channel{
		Name: "Feishu Bot", Type: "feishu",
		Config:  map[string]string{"app_id": "cli_test", "app_secret": "secret"},
		Enabled: true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("upsert: code = %d body = %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodGet, "/api/v1/channels", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: code = %d body = %s", rec.Code, rec.Body.String())
	}
	var listed []apiChannel
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 {
		t.Fatalf("listed = %+v", listed)
	}
	if listed[0].BotName != "Wang Bot" || listed[0].BotAvatarURL != upstream.URL+"/avatar.png" || listed[0].BotOpenID != "ou_1" {
		t.Fatalf("bot info = %+v", listed[0])
	}
	if !strings.Contains(listed[0].BotAvatarProxyURL, "/channel-avatar?id="+listed[0].ID) {
		t.Fatalf("avatar proxy = %q", listed[0].BotAvatarProxyURL)
	}
	if listed[0].Config["app_secret"] != "<redacted>" {
		t.Fatalf("config = %+v", listed[0].Config)
	}
	rec = doJSON(t, s, http.MethodGet, "/channel-avatar?id="+listed[0].ID, nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "png" {
		t.Fatalf("avatar proxy: code = %d body = %q", rec.Code, rec.Body.String())
	}
}

func TestFeishuSetupBeginAndPollLarkSwitch(t *testing.T) {
	s, _ := newTestServer(t)
	var calls []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/feishu/oauth/v1/app/registration" {
			switch r.FormValue("action") {
			case "init":
				calls = append(calls, "feishu:init")
				_, _ = w.Write([]byte(`{"supported_auth_methods":["client_secret"]}`))
			case "begin":
				calls = append(calls, "feishu:begin")
				_, _ = w.Write([]byte(`{"device_code":"dev-1","verification_uri_complete":"https://example.test/qr","interval":1,"expire_in":600}`))
			case "poll":
				calls = append(calls, "feishu:poll")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"authorization_pending","user_info":{"tenant_brand":"lark"}}`))
			default:
				t.Fatalf("unexpected feishu action %q", r.FormValue("action"))
			}
			return
		}
		if r.URL.Path == "/lark/oauth/v1/app/registration" {
			calls = append(calls, "lark:"+r.FormValue("action"))
			_, _ = w.Write([]byte(`{"client_id":"cli_lark","client_secret":"sec_lark","user_info":{"tenant_brand":"lark","open_id":"ou_1"}}`))
			return
		}
		t.Fatalf("unexpected path %s", r.URL.Path)
	}))
	defer upstream.Close()

	oldFeishu, oldLark := feishuAccountsBaseURL, larkAccountsBaseURL
	feishuAccountsBaseURL = upstream.URL + "/feishu"
	larkAccountsBaseURL = upstream.URL + "/lark"
	defer func() {
		feishuAccountsBaseURL = oldFeishu
		larkAccountsBaseURL = oldLark
	}()

	rec := doJSON(t, s, http.MethodPost, "/api/v1/setup/feishu/begin", map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("begin: code = %d body = %s", rec.Code, rec.Body.String())
	}
	var begin struct {
		DeviceCode string `json:"device_code"`
		QRURL      string `json:"qr_url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &begin); err != nil {
		t.Fatal(err)
	}
	if begin.DeviceCode != "dev-1" || begin.QRURL == "" {
		t.Fatalf("begin = %+v", begin)
	}

	rec = doJSON(t, s, http.MethodPost, "/api/v1/setup/feishu/poll", map[string]string{"device_code": "dev-1"})
	if rec.Code != http.StatusOK {
		t.Fatalf("poll: code = %d body = %s", rec.Code, rec.Body.String())
	}
	var poll struct {
		Status      string `json:"status"`
		AppID       string `json:"app_id"`
		AppSecret   string `json:"app_secret"`
		Platform    string `json:"platform"`
		OwnerOpenID string `json:"owner_open_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &poll); err != nil {
		t.Fatal(err)
	}
	if poll.Status != "completed" || poll.Platform != "lark" || poll.AppID != "cli_lark" || poll.AppSecret != "sec_lark" || poll.OwnerOpenID != "ou_1" {
		t.Fatalf("poll = %+v", poll)
	}
	if got := strings.Join(calls, ","); got != "feishu:init,feishu:begin,feishu:poll,lark:poll" {
		t.Fatalf("calls = %s", got)
	}
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func TestTriggerValidation(t *testing.T) {
	s, _ := newTestServer(t)

	cases := []struct {
		name string
		tr   core.Trigger
		code int
	}{
		{"missing name", core.Trigger{Kind: core.TriggerCron, CronExpr: "* * * * *", Prompt: "p"}, http.StatusBadRequest},
		{"bad kind", core.Trigger{Name: "x", Kind: "nope"}, http.StatusBadRequest},
		{"cron missing expr", core.Trigger{Name: "x", Kind: core.TriggerCron, Prompt: "p"}, http.StatusBadRequest},
		{"cron invalid expr", core.Trigger{Name: "x", Kind: core.TriggerCron, CronExpr: "banana", Prompt: "p"}, http.StatusBadRequest},
		{"cron missing prompt", core.Trigger{Name: "x", Kind: core.TriggerCron, CronExpr: "* * * * *"}, http.StatusBadRequest},
		{"cron ok", core.Trigger{Name: "x", Kind: core.TriggerCron, CronExpr: "0 9 * * *", Prompt: "p"}, http.StatusOK},
		{"bad session mode", core.Trigger{Name: "x", Kind: core.TriggerCron, CronExpr: "0 9 * * *", Prompt: "p", SessionMode: "weird"}, http.StatusBadRequest},
		{"event missing action", core.Trigger{Name: "x", Kind: core.TriggerEvent, Event: "error"}, http.StatusBadRequest},
		{"event ok", core.Trigger{Name: "x", Kind: core.TriggerEvent, Event: "error", ActionType: "http", ActionTarget: "http://127.0.0.1:1/x"}, http.StatusOK},
		{"webhook ok", core.Trigger{Name: "x", Kind: core.TriggerWebhook, Prompt: "p"}, http.StatusOK},
	}
	for _, tc := range cases {
		rec := doJSON(t, s, http.MethodPost, "/api/v1/triggers", tc.tr)
		if rec.Code != tc.code {
			t.Fatalf("%s: code = %d, want %d (body %s)", tc.name, rec.Code, tc.code, rec.Body.String())
		}
		if tc.code == http.StatusOK && tc.tr.Kind == core.TriggerWebhook {
			var saved core.Trigger
			_ = json.Unmarshal(rec.Body.Bytes(), &saved)
			if saved.Token == "" {
				t.Fatalf("%s: webhook trigger got no generated token", tc.name)
			}
		}
	}
}

func TestInboundHookAuth(t *testing.T) {
	s, st := newTestServer(t)
	ctx := context.Background()

	rec := doJSON(t, s, http.MethodPost, "/hook/missing", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing trigger: code = %d", rec.Code)
	}

	tr := &core.Trigger{
		ID: "trigger-hook", Name: "ci", Kind: core.TriggerWebhook,
		Prompt: "review", Token: "sekret", Enabled: true,
	}
	if err := st.UpsertTrigger(ctx, tr); err != nil {
		t.Fatal(err)
	}

	// Wrong/absent token is rejected.
	rec = doJSON(t, s, http.MethodPost, "/hook/trigger-hook", map[string]string{"prompt": "x"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: code = %d", rec.Code)
	}

	// Correct token but no connect runtime: 503 (persist-only mode).
	req := httptest.NewRequest(http.MethodPost, "/hook/trigger-hook?token=sekret", bytes.NewBufferString(`{"prompt":"x"}`))
	rr := httptest.NewRecorder()
	s.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("no runtime: code = %d body = %s", rr.Code, rr.Body.String())
	}

	// Disabled triggers are refused.
	tr.Enabled = false
	if err := st.UpsertTrigger(ctx, tr); err != nil {
		t.Fatal(err)
	}
	rec = doJSON(t, s, http.MethodPost, "/hook/trigger-hook?token=sekret", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("disabled: code = %d", rec.Code)
	}
}

func TestParseHookInput(t *testing.T) {
	mk := func(body string) *http.Request {
		return httptest.NewRequest(http.MethodPost, "/hook/x", bytes.NewBufferString(body))
	}
	if got := parseHookInput(mk(`{"prompt":"do it"}`)); got != "do it" {
		t.Fatalf("prompt only = %q", got)
	}
	if got := parseHookInput(mk(`{"prompt":"do it","payload":{"a":1}}`)); got != "do it\n\nPayload:\n{\"a\":1}" {
		t.Fatalf("prompt+payload = %q", got)
	}
	if got := parseHookInput(mk("raw text")); got != "raw text" {
		t.Fatalf("raw = %q", got)
	}
	if got := parseHookInput(mk("")); got != "" {
		t.Fatalf("empty = %q", got)
	}
}

func TestCodexRemoteControlChannelValidation(t *testing.T) {
	s, st := newTestServer(t)
	ctx := context.Background()
	for _, agent := range []*core.AgentInstance{
		{ID: "agent-codex", Name: "Codex", RuntimeID: "codex", Enabled: true},
		{ID: "agent-other", Name: "Other", RuntimeID: "claudecode", Enabled: true},
	} {
		if err := st.UpsertAgentInstance(ctx, agent); err != nil {
			t.Fatal(err)
		}
	}
	channel := core.Channel{
		Name: "Remote Codex", Type: "feishu", AgentID: "agent-codex",
		Config: map[string]string{
			"app_id": "cli_test", "app_secret": "secret",
			core.ChannelConfigCodexControlEnabled: "true",
		},
	}
	rec := doJSON(t, s, http.MethodPost, "/api/v1/channels", channel)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "allowed or admin") {
		t.Fatalf("missing whitelist: code=%d body=%s", rec.Code, rec.Body.String())
	}

	channel.AgentID = "agent-other"
	channel.Config[core.ChannelConfigAllowedUserIDs] = "ou_member"
	rec = doJSON(t, s, http.MethodPost, "/api/v1/channels", channel)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Codex Agent") {
		t.Fatalf("wrong runtime: code=%d body=%s", rec.Code, rec.Body.String())
	}

	channel.AgentID = "agent-codex"
	channel.Config[core.ChannelConfigAdminUserIDs] = "ou_admin"
	rec = doJSON(t, s, http.MethodPost, "/api/v1/channels", channel)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid remote control channel: code=%d body=%s", rec.Code, rec.Body.String())
	}
	var saved core.Channel
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Config[core.ChannelConfigCodexMaxQueue] != "20" ||
		saved.Config[core.ChannelConfigCodexTurnTimeout] != "20" {
		t.Fatalf("remote control defaults = %+v", saved.Config)
	}
}
