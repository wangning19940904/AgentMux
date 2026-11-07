package superagent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

var (
	// ErrGitHubTokenRejected means GitHub did not accept the token at all.
	ErrGitHubTokenRejected = errors.New("GitHub rejected this token")
	// ErrGitHubTokenMissing means there was no token to check.
	ErrGitHubTokenMissing = errors.New("token is required")
)

const githubRepoPages = 3 // 300 repositories, most recently pushed first

// GitHubAccount is the account a token acts as and the repositories it can
// reach. Scopes lists a classic or OAuth token's scopes; fine-grained tokens
// have permissions instead and report none.
type GitHubAccount struct {
	Login          string       `json:"login"`
	Name           string       `json:"name,omitempty"`
	AvatarURL      string       `json:"avatar_url,omitempty"`
	Scopes         []string     `json:"scopes"`
	Classic        bool         `json:"classic"`
	Repos          []GitHubRepo `json:"repos"`
	ReposTruncated bool         `json:"repos_truncated,omitempty"`
}

// GitHubRepo is a repository the token's account can read. CanPush is whether
// workers acting as that account could push to it.
type GitHubRepo struct {
	FullName string `json:"full_name"`
	Private  bool   `json:"private"`
	CanPush  bool   `json:"can_push"`
}

// GitHubAccountRequest is a token the setup form holds. A saved token reads
// back redacted, so then the Super Agent and Source name the stored one.
type GitHubAccountRequest struct {
	Token        string `json:"token"`
	APIBase      string `json:"api_base"`
	SuperAgentID string `json:"super_agent_id,omitempty"`
	SourceID     string `json:"source_id,omitempty"`
}

// CheckGitHubAccount says whose a token is and what it can reach, so the form
// fills in the agent's login and offers its repositories instead of having
// them typed. The token itself is never returned.
func (s *Service) CheckGitHubAccount(ctx context.Context, client *http.Client, request GitHubAccountRequest) (GitHubAccount, error) {
	token := strings.TrimSpace(request.Token)
	if token == redactedValue {
		stored, err := s.storedSourceSecret(ctx, request.SuperAgentID, request.SourceID, "github_token")
		if err != nil {
			return GitHubAccount{}, err
		}
		token = stored
	}
	return CheckGitHubToken(ctx, client, request.APIBase, token)
}

func (s *Service) storedSourceSecret(ctx context.Context, agentID, sourceID, key string) (string, error) {
	if agentID == "" || sourceID == "" {
		return "", errors.New("a saved token needs its Super Agent and trigger")
	}
	agent, err := s.store.GetSuperAgent(ctx, agentID)
	if err != nil {
		return "", err
	}
	if agent != nil {
		for _, source := range agent.Sources {
			if source.ID == sourceID {
				return source.Config[key], nil
			}
		}
	}
	return "", ErrNotFound
}

// CheckGitHubToken asks GitHub who a token belongs to and lists the
// repositories that account can reach.
func CheckGitHubToken(ctx context.Context, client *http.Client, apiBase, token string) (GitHubAccount, error) {
	token = strings.TrimSpace(token)
	if token == "" || token == redactedValue {
		return GitHubAccount{}, ErrGitHubTokenMissing
	}
	base, err := gitHubAPIBase(apiBase)
	if err != nil {
		return GitHubAccount{}, err
	}
	if client == nil {
		client = &http.Client{}
	}
	api := githubAPI{client: client, base: base, token: token}
	var account GitHubAccount
	header, err := api.get(ctx, "/user", &account)
	if err != nil {
		return GitHubAccount{}, err
	}
	if account.Login == "" {
		return GitHubAccount{}, errors.New("GitHub returned no account for this token")
	}
	account.Classic = strings.HasPrefix(token, "ghp_") || strings.HasPrefix(token, "gho_")
	account.Scopes = splitList(header.Get("X-OAuth-Scopes"))
	account.Repos, account.ReposTruncated, err = listGitHubRepos(ctx, api)
	if err != nil {
		return GitHubAccount{}, fmt.Errorf("list repositories: %w", err)
	}
	return account, nil
}

func listGitHubRepos(ctx context.Context, api githubAPI) ([]GitHubRepo, bool, error) {
	repos := []GitHubRepo{}
	for page := 1; page <= githubRepoPages; page++ {
		var batch []struct {
			FullName    string `json:"full_name"`
			Private     bool   `json:"private"`
			Permissions struct {
				Push bool `json:"push"`
			} `json:"permissions"`
		}
		query := url.Values{"affiliation": {"owner,collaborator,organization_member"}, "sort": {"pushed"},
			"per_page": {"100"}, "page": {strconv.Itoa(page)}}
		if _, err := api.get(ctx, "/user/repos?"+query.Encode(), &batch); err != nil {
			return nil, false, err
		}
		for _, repo := range batch {
			repos = append(repos, GitHubRepo{FullName: repo.FullName, Private: repo.Private, CanPush: repo.Permissions.Push})
		}
		if len(batch) < 100 {
			return repos, false, nil
		}
	}
	return repos, true, nil
}

// AcceptGitHubInvitation accepts, as the agent, a pending invitation to repo.
// It reports false when there is none, as for an existing collaborator.
func (s *Service) AcceptGitHubInvitation(ctx context.Context, client *http.Client, request GitHubAccountRequest, repo string) (bool, error) {
	token := strings.TrimSpace(request.Token)
	if token == redactedValue {
		stored, err := s.storedSourceSecret(ctx, request.SuperAgentID, request.SourceID, "github_token")
		if err != nil {
			return false, err
		}
		token = stored
	}
	if token == "" {
		return false, ErrGitHubTokenMissing
	}
	base, err := gitHubAPIBase(request.APIBase)
	if err != nil {
		return false, err
	}
	if client == nil {
		client = &http.Client{}
	}
	api := githubAPI{client: client, base: base, token: token}
	var invitations []struct {
		ID         int64 `json:"id"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if _, err := api.get(ctx, "/user/repository_invitations?per_page=100", &invitations); err != nil {
		return false, err
	}
	for _, invitation := range invitations {
		if strings.EqualFold(invitation.Repository.FullName, repo) {
			if _, err := api.send(ctx, http.MethodPatch, "/user/repository_invitations/"+strconv.FormatInt(invitation.ID, 10), nil); err != nil {
				return false, fmt.Errorf("accept the invitation to %s: %w", repo, err)
			}
			return true, nil
		}
	}
	return false, nil
}

// GitHubHost is the host gh signs in to for an API base: github.com, or a
// GitHub Enterprise server's own name.
func GitHubHost(apiBase string) (string, error) {
	base, err := gitHubAPIBase(apiBase)
	if err != nil {
		return "", err
	}
	if base == defaultGitHubAPI {
		return "github.com", nil
	}
	parsed, _ := url.Parse(base)
	return parsed.Host, nil
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
