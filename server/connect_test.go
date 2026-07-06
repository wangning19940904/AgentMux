package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/agentnexus/agentnexus/config"
	"github.com/agentnexus/agentnexus/core"
	"github.com/agentnexus/agentnexus/store"

	// Register platform adapters for channel type validation.
	_ "github.com/agentnexus/agentnexus/platform"
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
