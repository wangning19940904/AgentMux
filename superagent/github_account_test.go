package superagent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// fakeGitHubAccounts answers /user and /user/repos for two tokens: "ghp_good"
// reaches two repositories, "ghp_many" more than the form lists.
func fakeGitHubAccounts(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token != "ghp_good" && token != "ghp_many" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v3/user":
			w.Header().Set("X-OAuth-Scopes", "repo, read:org")
			_, _ = w.Write([]byte(`{"login":"my-agent-bot","name":"Agent"}`))
		case "/api/v3/user/repos":
			if got := r.URL.Query().Get("affiliation"); got != "owner,collaborator,organization_member" {
				t.Errorf("affiliation = %q", got)
			}
			if token == "ghp_good" {
				_, _ = w.Write([]byte(`[{"full_name":"acme/app","private":true,"permissions":{"push":true}},{"full_name":"acme/docs","permissions":{"push":false}}]`))
				return
			}
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			items := make([]string, 100)
			for index := range items {
				items[index] = fmt.Sprintf(`{"full_name":"acme/r%d-%d"}`, page, index)
			}
			_, _ = w.Write([]byte("[" + strings.Join(items, ",") + "]"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestCheckGitHubToken(t *testing.T) {
	server := fakeGitHubAccounts(t)
	account, err := CheckGitHubToken(context.Background(), server.Client(), server.URL+"/api/v3/", "ghp_good")
	if err != nil {
		t.Fatal(err)
	}
	if account.Login != "my-agent-bot" || !account.Classic || len(account.Scopes) != 2 || account.Scopes[0] != "repo" {
		t.Fatalf("account = %+v", account)
	}
	if len(account.Repos) != 2 || account.Repos[0] != (GitHubRepo{FullName: "acme/app", Private: true, CanPush: true}) || account.Repos[1].CanPush || account.ReposTruncated {
		t.Fatalf("repos = %+v truncated %v", account.Repos, account.ReposTruncated)
	}
	many, err := CheckGitHubToken(context.Background(), server.Client(), server.URL+"/api/v3", "ghp_many")
	if err != nil {
		t.Fatal(err)
	}
	if len(many.Repos) != 300 || !many.ReposTruncated {
		t.Fatalf("a long list stops at %d repos, truncated %v", len(many.Repos), many.ReposTruncated)
	}
	if _, err := CheckGitHubToken(context.Background(), server.Client(), server.URL+"/api/v3", "ghp_bad"); !errors.Is(err, ErrGitHubTokenRejected) {
		t.Fatalf("bad token err = %v", err)
	}
	for _, token := range []string{"", redactedValue} {
		if _, err := CheckGitHubToken(context.Background(), server.Client(), server.URL, token); !errors.Is(err, ErrGitHubTokenMissing) {
			t.Errorf("token %q: %v", token, err)
		}
	}
	for _, base := range []string{"ftp://example.com", "https://user:pass@example.com", "not a url"} {
		if _, err := CheckGitHubToken(context.Background(), server.Client(), base, "ghp_good"); err == nil {
			t.Errorf("api base %q accepted", base)
		}
	}
}

// A saved trigger reads its token back redacted; checking it uses the stored one.
func TestCheckGitHubAccountUsesTheSavedToken(t *testing.T) {
	server := fakeGitHubAccounts(t)
	h := newHarness(t)
	h.agent.Sources[0].Config["github_token"] = "ghp_good"
	if err := h.store.UpsertSuperAgent(context.Background(), h.agent); err != nil {
		t.Fatal(err)
	}
	request := GitHubAccountRequest{Token: redactedValue, APIBase: server.URL + "/api/v3", SuperAgentID: h.agent.ID, SourceID: h.source.ID}
	account, err := h.service.CheckGitHubAccount(context.Background(), server.Client(), request)
	if err != nil || account.Login != "my-agent-bot" || len(account.Repos) != 2 {
		t.Fatalf("account = %+v, err %v", account, err)
	}
	request.SourceID = "missing"
	if _, err := h.service.CheckGitHubAccount(context.Background(), server.Client(), request); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown trigger: %v", err)
	}
	request.SuperAgentID = ""
	if _, err := h.service.CheckGitHubAccount(context.Background(), server.Client(), request); err == nil {
		t.Fatal("a redacted token without its trigger was accepted")
	}
}

func TestAcceptGitHubInvitationAsTheAgent(t *testing.T) {
	var accepted []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gho_agent" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/user/repository_invitations":
			_, _ = w.Write([]byte(`[{"id":7,"repository":{"full_name":"acme/app"}},{"id":8,"repository":{"full_name":"acme/web"}}]`))
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/user/repository_invitations/"):
			accepted = append(accepted, strings.TrimPrefix(r.URL.Path, "/user/repository_invitations/"))
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	h := newHarness(t)
	request := GitHubAccountRequest{Token: "gho_agent", APIBase: server.URL}
	ok, err := h.service.AcceptGitHubInvitation(context.Background(), server.Client(), request, "ACME/app")
	if err != nil || !ok || strings.Join(accepted, ",") != "7" {
		t.Fatalf("accepted %v (%v, %v)", accepted, ok, err)
	}
	// An existing collaborator has no invitation to accept.
	if ok, err := h.service.AcceptGitHubInvitation(context.Background(), server.Client(), request, "acme/docs"); err != nil || ok {
		t.Fatalf("no invitation: %v, %v", ok, err)
	}
}

func TestGitHubHost(t *testing.T) {
	for base, want := range map[string]string{"": "github.com", "https://api.github.com/": "github.com", "https://git.acme.com/api/v3": "git.acme.com"} {
		if got, err := GitHubHost(base); err != nil || got != want {
			t.Errorf("GitHubHost(%q) = %q, %v", base, got, err)
		}
	}
}
