package superagent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCheckGitHubToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/user" {
			http.NotFound(w, r)
			return
		}
		switch r.Header.Get("Authorization") {
		case "Bearer ghp_good":
			w.Header().Set("X-OAuth-Scopes", "repo, read:org")
			_, _ = w.Write([]byte(`{"login":"my-agent-bot","name":"Agent"}`))
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer server.Close()

	account, err := CheckGitHubToken(context.Background(), server.Client(), server.URL+"/api/v3/", "ghp_good")
	if err != nil {
		t.Fatal(err)
	}
	if account.Login != "my-agent-bot" || !account.Classic || len(account.Scopes) != 2 || account.Scopes[0] != "repo" {
		t.Fatalf("account = %+v", account)
	}
	if _, err := CheckGitHubToken(context.Background(), server.Client(), server.URL+"/api/v3", "ghp_bad"); !errors.Is(err, ErrGitHubTokenRejected) {
		t.Fatalf("bad token err = %v", err)
	}
	for _, token := range []string{"", redactedValue} {
		if _, err := CheckGitHubToken(context.Background(), server.Client(), server.URL, token); err == nil {
			t.Errorf("token %q accepted", token)
		}
	}
	for _, base := range []string{"ftp://example.com", "https://user:pass@example.com", "not a url"} {
		if _, err := CheckGitHubToken(context.Background(), server.Client(), base, "ghp_good"); err == nil {
			t.Errorf("api base %q accepted", base)
		}
	}
}
