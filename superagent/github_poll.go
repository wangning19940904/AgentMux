package superagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// A GitHub Source with delivery "poll" reads the agent account's notifications
// instead of waiting for webhooks, so GitHub never has to reach this machine.
// It sees what the account takes part in: mentions, assignments, and follow-ups
// on Issues and pull requests it has commented on.

const (
	GitHubDeliveryPoll = "poll"

	githubPollInterval = time.Minute
	// Each read starts a little before the previous one ended, so a message
	// GitHub was still indexing is not lost; dedupe keys drop the repeats.
	githubPollOverlap = time.Minute
	githubPageSize    = 50
	githubMaxPages    = 5
	maxGitHubBytes    = 5 << 20
)

func githubPolls(source core.SuperAgentSource) bool {
	return source.Kind == core.SuperAgentSourceGitHub && strings.TrimSpace(source.Config["delivery"]) == GitHubDeliveryPoll
}

// GitHubInbox reads what happened since a moment in the Issues and pull
// requests the Source's account takes part in. A zero since only reads
// GitHub's clock, which is where a new Source starts.
type GitHubInbox interface {
	Activity(ctx context.Context, source core.SuperAgentSource, since time.Time) (GitHubActivity, error)
}

// GitHubActivity holds messages oldest first. Now is GitHub's clock when it was
// read: the next read starts there, so this machine's clock never matters.
type GitHubActivity struct {
	Now      time.Time
	Interval time.Duration
	Items    []githubItem
}

type githubCursor struct {
	Since time.Time `json:"since"`
}

// pollGitHub ingests what is new and returns how long GitHub asks to wait
// before the next read. The cursor advances only once every message is stored.
func (s *Service) pollGitHub(ctx context.Context, claim sourceClaim) (time.Duration, error) {
	raw, err := s.store.GetSuperAgentSourceState(ctx, claim.source.ID)
	if err != nil {
		return 0, err
	}
	var cursor githubCursor
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &cursor); err != nil {
			return 0, fmt.Errorf("decode GitHub position: %w", err)
		}
	}
	since := time.Time{}
	if !cursor.Since.IsZero() {
		since = cursor.Since.Add(-githubPollOverlap)
	}
	activity, err := s.github.Activity(ctx, claim.source, since)
	if err != nil {
		return 0, err
	}
	for _, item := range activity.Items {
		if trigger, ok := githubItemTrigger(claim.source, item); ok {
			if _, err := s.Ingest(ctx, claim.agent, claim.source, trigger); err != nil {
				return 0, err
			}
		}
	}
	data, err := json.Marshal(githubCursor{Since: activity.Now.UTC()})
	if err != nil {
		return 0, err
	}
	return activity.Interval, s.store.SetSuperAgentSourceState(ctx, claim.agent.ID, claim.source.ID, string(data))
}

// pollGitHubSources reads each polling Source when GitHub's requested interval
// has passed. next is owned by the one poller goroutine.
func (s *Service) pollGitHubSources(ctx context.Context, next map[string]time.Time) {
	for _, claim := range s.claims.ofKind(core.SuperAgentSourceGitHub) {
		if !githubPolls(claim.source) || s.now().Before(next[claim.source.ID]) {
			continue
		}
		pollCtx, cancel := context.WithTimeout(ctx, githubPollInterval)
		interval, err := s.pollGitHub(pollCtx, claim)
		cancel()
		if ctx.Err() != nil {
			return
		}
		// The ticker fires about every minute; a little slack keeps a 60s
		// interval from skipping a tick.
		next[claim.source.ID] = s.now().Add(max(interval, githubPollInterval) - 5*time.Second)
		s.recordSourceError(claim.source.ID, err)
	}
}

// GitHubClient reads notifications and threads over GitHub's REST API with the
// Source's token. Notifications need a classic token or an OAuth token.
type GitHubClient struct {
	HTTP *http.Client
}

type githubNotification struct {
	Reason  string `json:"reason"`
	Subject struct {
		URL  string `json:"url"`
		Type string `json:"type"`
	} `json:"subject"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

type githubThread struct {
	Number    int        `json:"number"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	User      githubUser `json:"user"`
	CreatedAt time.Time  `json:"created_at"`
}

type githubComment struct {
	ID        int64      `json:"id"`
	Body      string     `json:"body"`
	User      githubUser `json:"user"`
	CreatedAt time.Time  `json:"created_at"`
}

type githubIssueEvent struct {
	ID        int64       `json:"id"`
	Event     string      `json:"event"`
	Actor     githubUser  `json:"actor"`
	Assignee  *githubUser `json:"assignee"`
	CreatedAt time.Time   `json:"created_at"`
}

var githubRepoName = regexp.MustCompile(`^[\w.-]+/[\w.-]+$`)

func (c *GitHubClient) Activity(ctx context.Context, source core.SuperAgentSource, since time.Time) (GitHubActivity, error) {
	api, err := newGitHubAPI(c.HTTP, source)
	if err != nil {
		return GitHubActivity{}, err
	}
	if since.IsZero() {
		header, err := api.get(ctx, "/notifications?participating=true&per_page=1", nil)
		if err != nil {
			return GitHubActivity{}, err
		}
		return GitHubActivity{Now: githubServerTime(header), Interval: githubPollWait(header)}, nil
	}
	var notifications []githubNotification
	var first http.Header
	for page := 1; page <= githubMaxPages; page++ {
		var batch []githubNotification
		query := url.Values{"participating": {"true"}, "all": {"true"}, "since": {since.UTC().Format(time.RFC3339)},
			"per_page": {strconv.Itoa(githubPageSize)}, "page": {strconv.Itoa(page)}}
		header, err := api.get(ctx, "/notifications?"+query.Encode(), &batch)
		if err != nil {
			return GitHubActivity{}, err
		}
		if first == nil {
			first = header
		}
		notifications = append(notifications, batch...)
		if len(batch) < githubPageSize {
			break
		}
	}
	activity := GitHubActivity{Now: githubServerTime(first), Interval: githubPollWait(first)}
	want := strings.TrimSpace(source.Config["repo"])
	login := strings.TrimSpace(source.Config["agent_login"])
	for _, notification := range notifications {
		if want != "" && !strings.EqualFold(want, notification.Repository.FullName) {
			continue
		}
		items, err := api.threadItems(ctx, notification, login, since)
		if err != nil {
			return GitHubActivity{}, fmt.Errorf("read %s: %w", notification.Repository.FullName, err)
		}
		activity.Items = append(activity.Items, items...)
	}
	slices.SortStableFunc(activity.Items, func(a, b githubItem) int { return a.At.Compare(b.At) })
	return activity, nil
}

// threadItems lists the messages on one Issue or pull request since a moment:
// the opening post if it is that new, comments, review comments, and the
// assignment to the agent that the notification may be about.
func (a githubAPI) threadItems(ctx context.Context, notification githubNotification, login string, since time.Time) ([]githubItem, error) {
	kind := notification.Subject.Type
	repo := notification.Repository.FullName
	if (kind != "Issue" && kind != "PullRequest") || !githubRepoName.MatchString(repo) {
		return nil, nil
	}
	var thread githubThread
	if _, err := a.get(ctx, notification.Subject.URL, &thread); err != nil {
		return nil, err
	}
	base := fmt.Sprintf("/repos/%s/issues/%d", repo, thread.Number)
	item := func(key string, sender githubUser, text string, at time.Time) githubItem {
		return githubItem{Key: "github:" + key, Repo: repo, Number: thread.Number, Title: thread.Title, Text: text, Sender: sender, At: at}
	}
	var items []githubItem
	if !thread.CreatedAt.Before(since) {
		items = append(items, item(fmt.Sprintf("opened:%s#%d", strings.ToLower(repo), thread.Number), thread.User, thread.Body, thread.CreatedAt))
	}
	query := "?" + url.Values{"since": {since.UTC().Format(time.RFC3339)}, "per_page": {"100"}}.Encode()
	lists := [][2]string{{"comment", base + "/comments" + query}}
	if kind == "PullRequest" {
		lists = append(lists, [2]string{"review-comment", fmt.Sprintf("/repos/%s/pulls/%d/comments%s", repo, thread.Number, query)})
	}
	for _, list := range lists {
		prefix, path := list[0], list[1]
		var comments []githubComment
		if _, err := a.get(ctx, path, &comments); err != nil {
			return nil, err
		}
		for _, comment := range comments {
			// since matches edits too; an old comment edited now is not new.
			if !comment.CreatedAt.Before(since) {
				items = append(items, item(prefix+":"+strconv.FormatInt(comment.ID, 10), comment.User, comment.Body, comment.CreatedAt))
			}
		}
	}
	if notification.Reason == "assign" {
		events, err := a.latestIssueEvents(ctx, base+"/events?per_page=100")
		if err != nil {
			return nil, err
		}
		for _, event := range events {
			if event.Event == "assigned" && event.Assignee != nil && strings.EqualFold(event.Assignee.Login, login) && !event.CreatedAt.Before(since) {
				assigned := item("assigned:"+strconv.FormatInt(event.ID, 10), event.Actor, thread.Body, event.CreatedAt)
				assigned.Assigned = true
				items = append(items, assigned)
			}
		}
	}
	return items, nil
}

var githubLastPage = regexp.MustCompile(`<([^>]+)>;\s*rel="last"`)

// latestIssueEvents reads the newest page of a thread's events: GitHub lists
// them oldest first, and an assignment worth acting on is recent.
func (a githubAPI) latestIssueEvents(ctx context.Context, path string) ([]githubIssueEvent, error) {
	var events []githubIssueEvent
	header, err := a.get(ctx, path, &events)
	if err != nil {
		return nil, err
	}
	if match := githubLastPage.FindStringSubmatch(header.Get("Link")); match != nil {
		events = nil
		if _, err := a.get(ctx, match[1], &events); err != nil {
			return nil, err
		}
	}
	return events, nil
}

type githubAPI struct {
	client *http.Client
	base   string
	token  string
}

func newGitHubAPI(client *http.Client, source core.SuperAgentSource) (githubAPI, error) {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	token := strings.TrimSpace(source.Config["github_token"])
	if token == "" {
		return githubAPI{}, errors.New("github_token is required to read the agent's notifications")
	}
	base, err := gitHubAPIBase(source.Config["api_base"])
	if err != nil {
		return githubAPI{}, err
	}
	return githubAPI{client: client, base: base, token: token}, nil
}

// get reads a path under the API base, or an absolute URL GitHub returned. The
// token is only ever sent to the configured API.
func (a githubAPI) get(ctx context.Context, target string, out any) (http.Header, error) {
	return a.send(ctx, http.MethodGet, target, out)
}

func (a githubAPI) send(ctx context.Context, method, target string, out any) (http.Header, error) {
	if strings.HasPrefix(target, "/") {
		target = a.base + target
	} else if !strings.HasPrefix(target, a.base+"/") {
		return nil, fmt.Errorf("GitHub returned a link outside %s", a.base)
	}
	request, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+a.token)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := a.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxGitHubBytes))
	if err != nil {
		return nil, err
	}
	switch {
	case response.StatusCode == http.StatusUnauthorized:
		return nil, fmt.Errorf("%w; check the agent's token", ErrGitHubTokenRejected)
	case response.StatusCode >= 300:
		var problem struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body, &problem)
		return nil, fmt.Errorf("GitHub answered %s: %s", response.Status, firstNonEmpty(problem.Message, string(body)))
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return nil, fmt.Errorf("decode GitHub response: %w", err)
		}
	}
	return response.Header, nil
}

func githubServerTime(header http.Header) time.Time {
	if at, err := http.ParseTime(header.Get("Date")); err == nil {
		return at.UTC()
	}
	return time.Now().UTC()
}

func githubPollWait(header http.Header) time.Duration {
	if seconds, err := strconv.Atoi(header.Get("X-Poll-Interval")); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return githubPollInterval
}
