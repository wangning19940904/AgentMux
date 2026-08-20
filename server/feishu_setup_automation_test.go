package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestFeishuAutomationPageExtraction(t *testing.T) {
	html := `<script>window.csrfToken = "csrf-value"; window.user = {"id":"user-123","name":"Owner","tenantId":"tenant-1"};</script>`
	if got := extractFeishuCSRF(html); got != "csrf-value" {
		t.Fatalf("csrf = %q", got)
	}
	if got := extractFeishuOpenPlatformUserID(html); got != "user-123" {
		t.Fatalf("user id = %q", got)
	}
	if got := extractFeishuOpenPlatformUserID(`window.user = {"name":"brace } in string","id":"user-456"};`); got != "user-456" {
		t.Fatalf("balanced user id = %q", got)
	}
}

func TestFeishuAutomationCatalogAndEventExtraction(t *testing.T) {
	payload := map[string]any{"data": map[string]any{
		"appScopes":  []any{map[string]any{"scope_name": "im:message", "scope_id": "scope-1"}},
		"userScopes": []any{map[string]any{"name": "contact:user.base:readonly", "id": "scope-2"}},
	}}
	catalog := map[string]string{}
	collectFeishuScopeEntries(payload, catalog)
	if catalog["im:message"] != "scope-1" || catalog["contact:user.base:readonly"] != "scope-2" {
		t.Fatalf("scope catalog = %+v", catalog)
	}
	events := collectFeishuEventIDs(map[string]any{"data": map[string]any{
		"appEvents": []any{"im.message.receive_v1", map[string]any{"id": "vc.bot.meeting_invited_v1"}},
		"callbacks": []any{map[string]any{"id": "card.action.trigger"}},
	}})
	want := []string{"card.action.trigger", "im.message.receive_v1", "vc.bot.meeting_invited_v1"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("event ids = %+v, want %+v", events, want)
	}
}

func TestNextFeishuVersionIncludesDrafts(t *testing.T) {
	payload := map[string]any{"data": map[string]any{"versions": []any{
		map[string]any{"appVersion": "1.2.3"},
		map[string]any{"appVersion": "2.0.9"},
		map[string]any{"appVersion": "invalid"},
	}}}
	if got := nextFeishuVersion(payload); got != "2.0.10" {
		t.Fatalf("next version = %q", got)
	}
	if got := nextFeishuVersion(map[string]any{}); got != "0.0.1" {
		t.Fatalf("empty next version = %q", got)
	}
}

func TestFeishuAutomationHTTPFlow(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /accounts/qrlogin/init", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Flow-Key", "flow-one")
		writeJSON(w, http.StatusOK, map[string]any{"code": 0, "data": map[string]any{"step_info": map[string]any{"token": "qr-token"}}})
	})
	var testServer *httptest.Server
	mux.HandleFunc("POST /accounts/qrlogin/polling", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Flow-Key") != "flow-one" {
			t.Fatalf("flow key = %q", r.Header.Get("X-Flow-Key"))
		}
		writeJSON(w, http.StatusOK, map[string]any{"code": 0, "data": map[string]any{
			"next_step": "enter_app", "step_info": map[string]any{"status": 3, "cross_login_uri": testServer.URL + "/cross"},
		}})
	})
	mux.HandleFunc("GET /cross", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ask")) })
	mux.HandleFunc("GET /app/cli_test/auth", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<script>window.csrfToken="csrf-one";window.user={"id":"owner-one","name":"Owner"};</script>`))
	})
	mux.HandleFunc("POST /developers/v1/scope/all/cli_test", func(w http.ResponseWriter, _ *http.Request) {
		var scopes []any
		for index, name := range agentMuxFeishuScopes {
			scopes = append(scopes, map[string]any{"scope_name": name, "scope_id": "scope-" + strconv.Itoa(index)})
		}
		writeJSON(w, http.StatusOK, map[string]any{"code": 0, "data": map[string]any{"appScopes": scopes}})
	})
	for _, path := range []string{
		"/developers/v1/scope/update/cli_test", "/developers/v1/robot/switch/cli_test",
		"/developers/v1/event/switch/cli_test", "/developers/v1/event/update/cli_test",
		"/developers/v1/callback/switch/cli_test", "/developers/v1/callback/update/cli_test",
		"/developers/v1/publish/commit/cli_test/version-one",
	} {
		path := path
		mux.HandleFunc("POST "+path, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-CSRF-Token") != "csrf-one" {
				t.Fatalf("csrf header = %q for %s", r.Header.Get("X-CSRF-Token"), path)
			}
			writeJSON(w, http.StatusOK, map[string]any{"code": 0})
		})
	}
	mux.HandleFunc("POST /developers/v1/event/cli_test", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"code": 0, "data": map[string]any{"eventMode": 4, "appEvents": agentMuxFeishuEvents}})
	})
	mux.HandleFunc("POST /developers/v1/callback/cli_test", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"code": 0, "data": map[string]any{"callbackMode": 4, "callbacks": []string{"card.action.trigger"}}})
	})
	mux.HandleFunc("POST /developers/v1/app_version/list/cli_test", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"code": 0, "data": map[string]any{"versions": []any{map[string]any{"appVersion": "1.0.0"}}}})
	})
	mux.HandleFunc("POST /developers/v1/app_version/create/cli_test", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["appVersion"] != "1.0.1" {
			t.Fatalf("version payload = %+v", body)
		}
		writeJSON(w, http.StatusOK, map[string]any{"code": 0, "data": map[string]any{"versionId": "version-one"}})
	})
	testServer = httptest.NewServer(mux)
	defer testServer.Close()

	oldAccounts, oldAsk, oldOpen := feishuAutomationAccountsOrigin, feishuAutomationAskOrigin, feishuAutomationOpenOrigin
	feishuAutomationAccountsOrigin, feishuAutomationAskOrigin, feishuAutomationOpenOrigin = testServer.URL, testServer.URL, testServer.URL
	t.Cleanup(func() {
		feishuAutomationAccountsOrigin, feishuAutomationAskOrigin, feishuAutomationOpenOrigin = oldAccounts, oldAsk, oldOpen
	})

	server, _ := newTestServer(t)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/setup/feishu/automation/begin", bytes.NewReader([]byte(`{}`)))
	request.RemoteAddr = "127.0.0.1:1234"
	recorder := httptest.NewRecorder()
	server.handleFeishuAutomationBegin(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("begin code=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var begun map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &begun)
	sessionID, _ := begun["session_id"].(string)
	if sessionID == "" || !strings.Contains(fmt.Sprint(begun["qr_payload"]), "qr-token") {
		t.Fatalf("begin response = %+v", begun)
	}

	pollBody, _ := json.Marshal(map[string]string{"session_id": sessionID})
	request = httptest.NewRequest(http.MethodPost, "/api/v1/setup/feishu/automation/poll", bytes.NewReader(pollBody))
	request.RemoteAddr = "127.0.0.1:1234"
	recorder = httptest.NewRecorder()
	server.handleFeishuAutomationPoll(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "completed") {
		t.Fatalf("poll code=%d body=%s", recorder.Code, recorder.Body.String())
	}

	configureBody, _ := json.Marshal(map[string]any{"session_id": sessionID, "app_id": "cli_test", "publish": true, "visibility": "owner"})
	request = httptest.NewRequest(http.MethodPost, "/api/v1/setup/feishu/automation/configure", bytes.NewReader(configureBody))
	request.RemoteAddr = "127.0.0.1:1234"
	recorder = httptest.NewRecorder()
	server.handleFeishuAutomationConfigure(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "version-one") {
		t.Fatalf("configure code=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
