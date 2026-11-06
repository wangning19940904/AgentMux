package superagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ErrGitHubTokenRejected means GitHub did not accept the token at all.
var ErrGitHubTokenRejected = errors.New("GitHub rejected this token")

// GitHubAccount is the account a token acts as. Scopes lists a classic or OAuth
// token's scopes; fine-grained tokens have permissions instead and report none.
type GitHubAccount struct {
	Login     string   `json:"login"`
	Name      string   `json:"name,omitempty"`
	AvatarURL string   `json:"avatar_url,omitempty"`
	Scopes    []string `json:"scopes"`
	Classic   bool     `json:"classic"`
}

// CheckGitHubToken asks GitHub who a token belongs to, so the setup form can
// fill in the agent's login instead of having it typed.
func CheckGitHubToken(ctx context.Context, client *http.Client, apiBase, token string) (GitHubAccount, error) {
	token = strings.TrimSpace(token)
	if token == "" || token == redactedValue {
		return GitHubAccount{}, errors.New("token is required")
	}
	base, err := gitHubAPIBase(apiBase)
	if err != nil {
		return GitHubAccount{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/user", nil)
	if err != nil {
		return GitHubAccount{}, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/vnd.github+json")
	response, err := client.Do(request)
	if err != nil {
		return GitHubAccount{}, fmt.Errorf("reach GitHub: %w", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode == http.StatusUnauthorized {
		return GitHubAccount{}, ErrGitHubTokenRejected
	}
	if response.StatusCode >= 300 {
		return GitHubAccount{}, fmt.Errorf("GitHub answered %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	var account GitHubAccount
	if err := json.Unmarshal(body, &account); err != nil || account.Login == "" {
		return GitHubAccount{}, fmt.Errorf("GitHub returned no account for this token")
	}
	account.Classic = strings.HasPrefix(token, "ghp_") || strings.HasPrefix(token, "gho_")
	account.Scopes = splitList(response.Header.Get("X-OAuth-Scopes"))
	return account, nil
}

// gitHubAPIBase accepts github.com's API or a GitHub Enterprise one.
func gitHubAPIBase(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return defaultGitHubAPI, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil {
		return "", fmt.Errorf("api_base must be an http(s) URL such as https://github.example.com/api/v3")
	}
	return raw, nil
}
