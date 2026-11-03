package superagent

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/wangning19940904/AgentMux/core"
)

// ErrUnauthorized means a webhook's signature did not verify.
var ErrUnauthorized = errors.New("invalid webhook signature")

// GitHubDelivery is one webhook request as the transport received it. Signature
// verification needs the exact bytes, so Body is never re-encoded.
type GitHubDelivery struct {
	Event      string
	DeliveryID string
	Signature  string
	Body       []byte
}

// HandleGitHubWebhook verifies and ingests one delivery. A delivery that
// verifies but is not a request is success: GitHub must not retry it.
func (s *Service) HandleGitHubWebhook(ctx context.Context, sourceID string, delivery GitHubDelivery) error {
	claim, ok := s.claims.source(sourceID)
	if !ok || claim.source.Kind != core.SuperAgentSourceGitHub {
		return ErrNotFound
	}
	if !verifyGitHubSignature(claim.source.Config["webhook_secret"], delivery.Signature, delivery.Body) {
		return ErrUnauthorized
	}
	trigger, ok, err := parseGitHubEvent(claim.source, delivery)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	_, err = s.Ingest(ctx, claim.agent, claim.source, trigger)
	return err
}

func verifyGitHubSignature(secret, signature string, body []byte) bool {
	digest, found := strings.CutPrefix(signature, "sha256=")
	if secret == "" || !found {
		return false
	}
	provided, err := hex.DecodeString(digest)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(provided, mac.Sum(nil))
}

type githubUser struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

type githubPayload struct {
	Action     string `json:"action"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Sender   githubUser  `json:"sender"`
	Assignee *githubUser `json:"assignee"`
	Comment  *struct {
		Body string `json:"body"`
	} `json:"comment"`
	Issue *struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
	} `json:"issue"`
	PullRequest *struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
	} `json:"pull_request"`
}

// githubReply addresses a comment on an Issue or pull request.
type githubReply struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
}

// parseGitHubEvent keeps only human messages on Issues and pull requests in
// the configured repository. The agent's own comments and other bots stop here,
// so its replies can never trigger it again.
func parseGitHubEvent(source core.SuperAgentSource, delivery GitHubDelivery) (core.SuperAgentTrigger, bool, error) {
	var payload githubPayload
	if err := json.Unmarshal(delivery.Body, &payload); err != nil {
		return core.SuperAgentTrigger{}, false, fmt.Errorf("decode GitHub payload: %w", err)
	}
	login := strings.TrimSpace(source.Config["agent_login"])
	sender := payload.Sender.Login
	if sender == "" || strings.EqualFold(sender, login) || payload.Sender.Type == "Bot" {
		return core.SuperAgentTrigger{}, false, nil
	}
	repo := payload.Repository.FullName
	if want := strings.TrimSpace(source.Config["repo"]); want != "" && !strings.EqualFold(want, repo) {
		return core.SuperAgentTrigger{}, false, nil
	}
	var number int
	var title, text string
	assigned := false
	switch {
	case delivery.Event == "issue_comment" && payload.Action == "created" && payload.Comment != nil && payload.Issue != nil:
		number, title, text = payload.Issue.Number, payload.Issue.Title, payload.Comment.Body
	case delivery.Event == "pull_request_review_comment" && payload.Action == "created" && payload.Comment != nil && payload.PullRequest != nil:
		number, title, text = payload.PullRequest.Number, payload.PullRequest.Title, payload.Comment.Body
	case delivery.Event == "issues" && payload.Issue != nil && (payload.Action == "opened" || payload.Action == "assigned"):
		number, title, text = payload.Issue.Number, payload.Issue.Title, payload.Issue.Body
		assigned = payload.Action == "assigned" && payload.Assignee != nil && strings.EqualFold(payload.Assignee.Login, login)
		if payload.Action == "assigned" && !assigned {
			return core.SuperAgentTrigger{}, false, nil
		}
	case delivery.Event == "pull_request" && payload.PullRequest != nil && (payload.Action == "opened" || payload.Action == "assigned"):
		number, title, text = payload.PullRequest.Number, payload.PullRequest.Title, payload.PullRequest.Body
		assigned = payload.Action == "assigned" && payload.Assignee != nil && strings.EqualFold(payload.Assignee.Login, login)
		if payload.Action == "assigned" && !assigned {
			return core.SuperAgentTrigger{}, false, nil
		}
	default:
		return core.SuperAgentTrigger{}, false, nil
	}
	reply, _ := json.Marshal(githubReply{Repo: repo, Number: number})
	dedupe := delivery.DeliveryID
	if dedupe == "" {
		digest := sha256.Sum256(delivery.Body)
		dedupe = hex.EncodeToString(digest[:])
	}
	return core.SuperAgentTrigger{
		DedupeKey: dedupe,
		ThreadKey: fmt.Sprintf("github:%s#%d", strings.ToLower(repo), number),
		Actor:     sender, Addressed: assigned || mentionsLogin(text, login),
		Title: fmt.Sprintf("%s#%d %s", repo, number, title), Text: text, Reply: reply,
	}, true, nil
}

// mentionsLogin matches a GitHub mention, not a prefix of a longer login or an
// email address that happens to contain it.
func mentionsLogin(text, login string) bool {
	if login == "" {
		return false
	}
	pattern, err := regexp.Compile(`(?i)(^|[^\w@/.-])@` + regexp.QuoteMeta(login) + `($|[^\w-])`)
	return err == nil && pattern.MatchString(text)
}
