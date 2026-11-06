package superagent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

type scriptedInbox struct {
	calls     []time.Time
	responses []GitHubActivity
}

func (s *scriptedInbox) Activity(_ context.Context, _ core.SuperAgentSource, since time.Time) (GitHubActivity, error) {
	s.calls = append(s.calls, since)
	next := s.responses[0]
	s.responses = s.responses[1:]
	return next, nil
}

func TestGitHubPollerStartsFromNowAndMovesOnGitHubsClock(t *testing.T) {
	h := newHarness(t)
	source := h.source
	source.Config["delivery"] = GitHubDeliveryPoll
	claim := sourceClaim{agent: h.agent, source: source}
	t0 := time.Date(2026, 11, 7, 8, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)
	alice := githubUser{Login: "alice", Type: "User"}
	item := func(key string, sender githubUser, text string, assigned bool) githubItem {
		return githubItem{Key: key, Repo: "acme/app", Number: 5, Title: "Flaky test", Text: text, Sender: sender, Assigned: assigned, At: t1}
	}
	inbox := &scriptedInbox{responses: []GitHubActivity{
		{Now: t0, Interval: 90 * time.Second},
		{Now: t1, Interval: time.Minute, Items: []githubItem{
			item("github:comment:1", alice, "@amux-bot please look", false),
			item("github:comment:2", githubUser{Login: "amux-bot", Type: "User"}, "On it", false),
			item("github:comment:3", githubUser{Login: "ci", Type: "Bot"}, "@amux-bot build failed", false),
			item("github:assigned:4", alice, "Fix the flaky test", true),
		}},
	}}
	h.service.github = inbox
	ctx := context.Background()

	interval, err := h.service.pollGitHub(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if interval != 90*time.Second || len(h.store.triggers) != 0 || !inbox.calls[0].IsZero() {
		t.Fatalf("the first read only sets where to start: interval %v, triggers %+v, since %v", interval, h.store.triggers, inbox.calls[0])
	}
	if _, err := h.service.pollGitHub(ctx, claim); err != nil {
		t.Fatal(err)
	}
	if want := t0.Add(-githubPollOverlap); !inbox.calls[1].Equal(want) {
		t.Fatalf("second read since %v, want %v", inbox.calls[1], want)
	}
	// The agent's own comment and the bot's are dropped.
	if len(h.store.triggers) != 2 {
		t.Fatalf("triggers = %+v", h.store.triggers)
	}
	for index, want := range []string{"github:comment:1", "github:assigned:4"} {
		trigger := h.store.triggers[index]
		if trigger.DedupeKey != want || !trigger.Addressed || trigger.Actor != "alice" || trigger.ThreadKey != "github:acme/app#5" {
			t.Fatalf("trigger %d = %+v", index, trigger)
		}
	}
	var cursor githubCursor
	if err := json.Unmarshal([]byte(h.store.state[source.ID]), &cursor); err != nil || !cursor.Since.Equal(t1) {
		t.Fatalf("cursor = %s (%v)", h.store.state[source.ID], err)
	}
}

func TestGitHubClientReadsTheAgentsThreads(t *testing.T) {
	since := time.Date(2026, 11, 7, 8, 0, 0, 0, time.UTC)
	before := since.Add(-time.Hour).Format(time.RFC3339)
	at := func(minutes int) string { return since.Add(time.Duration(minutes) * time.Minute).Format(time.RFC3339) }
	var server *httptest.Server
	routes := map[string]string{
		"/api/v3/repos/acme/app/issues/5":          `{"number":5,"title":"Flaky","body":"old","user":{"login":"bob"},"created_at":"` + before + `"}`,
		"/api/v3/repos/acme/app/issues/5/comments": `[{"id":10,"body":"edited later","user":{"login":"bob"},"created_at":"` + before + `"},{"id":11,"body":"@amux-bot please fix","user":{"login":"alice"},"created_at":"` + at(3) + `"}]`,
		"/api/v3/repos/acme/app/pulls/7":           `{"number":7,"title":"Speed up","body":"@amux-bot review?","user":{"login":"bob"},"created_at":"` + at(1) + `"}`,
		"/api/v3/repos/acme/app/issues/7/comments": `[]`,
		"/api/v3/repos/acme/app/pulls/7/comments":  `[{"id":21,"body":"nit","user":{"login":"carol"},"created_at":"` + at(2) + `"}]`,
	}
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v3/notifications":
			query := r.URL.Query()
			if query.Get("participating") != "true" || query.Get("all") != "true" || query.Get("since") != since.Format(time.RFC3339) {
				t.Errorf("notifications query = %s", r.URL.RawQuery)
			}
			w.Header().Set("Date", "Sat, 07 Nov 2026 08:05:00 GMT")
			w.Header().Set("X-Poll-Interval", "90")
			_, _ = w.Write([]byte(`[
				{"reason":"mention","subject":{"type":"Issue","url":"` + server.URL + `/api/v3/repos/acme/app/issues/5"},"repository":{"full_name":"acme/app"}},
				{"reason":"assign","subject":{"type":"PullRequest","url":"` + server.URL + `/api/v3/repos/acme/app/pulls/7"},"repository":{"full_name":"acme/app"}},
				{"reason":"mention","subject":{"type":"Issue","url":"` + server.URL + `/api/v3/repos/acme/other/issues/1"},"repository":{"full_name":"acme/other"}},
				{"reason":"subscribed","subject":{"type":"Release","url":"` + server.URL + `/api/v3/repos/acme/app/releases/1"},"repository":{"full_name":"acme/app"}}
			]`))
		case "/api/v3/repos/acme/app/issues/7/events":
			// GitHub lists events oldest first; the newest are on the last page.
			if r.URL.Query().Get("page") != "3" {
				w.Header().Set("Link", `<`+server.URL+`/api/v3/repos/acme/app/issues/7/events?per_page=100&page=2>; rel="next", <`+server.URL+`/api/v3/repos/acme/app/issues/7/events?per_page=100&page=3>; rel="last"`)
				_, _ = w.Write([]byte(`[]`))
				return
			}
			_, _ = w.Write([]byte(`[{"id":30,"event":"assigned","actor":{"login":"alice"},"assignee":{"login":"someone"},"created_at":"` + at(4) + `"},{"id":31,"event":"assigned","actor":{"login":"alice"},"assignee":{"login":"AMUX-bot"},"created_at":"` + at(4) + `"}]`))
		default:
			body, ok := routes[r.URL.Path]
			if !ok {
				t.Errorf("unexpected request %s", r.URL)
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(body))
		}
	}))
	defer server.Close()

	source := core.SuperAgentSource{ID: "gh", Kind: core.SuperAgentSourceGitHub, Config: map[string]string{
		"delivery": GitHubDeliveryPoll, "github_token": "tok", "agent_login": "amux-bot", "repo": "acme/app", "api_base": server.URL + "/api/v3",
	}}
	activity, err := (&GitHubClient{HTTP: server.Client()}).Activity(context.Background(), source, since)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 11, 7, 8, 5, 0, 0, time.UTC); !activity.Now.Equal(want) || activity.Interval != 90*time.Second {
		t.Fatalf("now %v interval %v", activity.Now, activity.Interval)
	}
	var keys []string
	for _, item := range activity.Items {
		keys = append(keys, item.Key)
	}
	// Oldest first, across threads.
	if got := strings.Join(keys, " "); got != "github:opened:acme/app#7 github:review-comment:21 github:comment:11 github:assigned:31" {
		t.Fatalf("items = %s", got)
	}
	assigned := activity.Items[3]
	if !assigned.Assigned || assigned.Sender.Login != "alice" || assigned.Number != 7 || assigned.Text != "@amux-bot review?" {
		t.Fatalf("assignment = %+v", assigned)
	}
}

func TestGitHubClientSendsTheTokenOnlyToItsAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"reason":"mention","subject":{"type":"Issue","url":"https://elsewhere.example/repos/acme/app/issues/5"},"repository":{"full_name":"acme/app"}}]`))
	}))
	defer server.Close()
	source := core.SuperAgentSource{Kind: core.SuperAgentSourceGitHub, Config: map[string]string{"github_token": "tok", "agent_login": "bot", "api_base": server.URL}}
	_, err := (&GitHubClient{HTTP: server.Client()}).Activity(context.Background(), source, time.Now())
	if err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("err = %v", err)
	}
}

func TestGitHubDeliveryDecidesWhatIsRequired(t *testing.T) {
	source := func(config map[string]string) core.SuperAgentSource {
		return core.SuperAgentSource{Kind: core.SuperAgentSourceGitHub, Enabled: true, AllowedActors: []string{"alice"}, Config: config}
	}
	if err := validateSource(source(map[string]string{"delivery": "poll", "agent_login": "bot", "github_token": "tok"})); err != nil {
		t.Fatalf("polling needs no webhook secret: %v", err)
	}
	if err := validateSource(source(map[string]string{"delivery": "poll", "agent_login": "bot", "webhook_secret": "s"})); err == nil || !strings.Contains(err.Error(), "github_token") {
		t.Fatalf("polling without a token: %v", err)
	}
	if err := validateSource(source(map[string]string{"agent_login": "bot", "github_token": "tok"})); err == nil || !strings.Contains(err.Error(), "webhook_secret") {
		t.Fatalf("a webhook without a secret: %v", err)
	}
	if err := validateSource(source(map[string]string{"delivery": "carrier pigeon", "agent_login": "bot"})); err == nil {
		t.Fatal("an unknown delivery was accepted")
	}
	if got := requiredSecrets(source(map[string]string{"delivery": "poll"})); len(got) != 1 || got[0] != "github_token" {
		t.Fatalf("required secrets = %v", got)
	}
}
