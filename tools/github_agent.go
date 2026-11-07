package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/wangning19940904/AgentMux/internal/authflow"
	"github.com/wangning19940904/AgentMux/internal/procutil"
)

// A Super Agent's GitHub account signs in through a gh profile of its own
// (GH_CONFIG_DIR), as DeepSeek's dsh-superagent sets up its agent, so the
// operator's gh login is never touched. Here the profile lives only while the
// sign-in runs: once gh holds the token, AgentMux hands it to the trigger and
// deletes the profile, so nothing is left in the keychain or on disk.

// ErrGitHubCLIMissing means gh is not installed where AgentMux can find it.
var ErrGitHubCLIMissing = errors.New("the GitHub CLI (gh) is not installed")

const githubAgentLoginTimeout = 15 * time.Minute // a device code's lifetime

var (
	githubAgentLogins = authflow.NewRegistry(cliAuthSessionTTL)
	// Tokens wait here, by session id, until the Console takes them once.
	githubAgentTokens sync.Map

	githubHostName  = regexp.MustCompile(`^[A-Za-z0-9.-]+(:[0-9]+)?$`)
	githubLoginName = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	githubRepoName  = regexp.MustCompile(`^[\w.-]+/[\w.-]+$`)
)

// GitHubAgentLogin is a sign-in snapshot. Token is set once, on the first read
// after gh finished.
type GitHubAgentLogin struct {
	CLIAuthSession
	Token string `json:"token,omitempty"`
}

// StartGitHubAgentLogin starts gh's one-time-code sign-in for host in a fresh
// profile and returns as soon as gh shows the code.
func StartGitHubAgentLogin(host string) (GitHubAgentLogin, error) {
	if !githubHostName.MatchString(host) {
		return GitHubAgentLogin{}, fmt.Errorf("invalid GitHub host %q", host)
	}
	executable, err := resolveCLIExecutable("gh")
	if err != nil {
		return GitHubAgentLogin{}, ErrGitHubCLIMissing
	}
	ctx, cancel := context.WithTimeout(context.Background(), githubAgentLoginTimeout)
	session, created := githubAgentLogins.Create("github-agent:"+host, false, cancel)
	if created {
		go runGitHubAgentLogin(ctx, session, executable, host)
	}
	snapshot, err := waitForCLIAuthLink(session)
	return GitHubAgentLogin{CLIAuthSession: snapshot}, err
}

// GetGitHubAgentLogin returns a sign-in's state, and its token the first time
// it is read after succeeding.
func GetGitHubAgentLogin(sessionID string) (GitHubAgentLogin, bool) {
	session, ok := githubAgentLogins.Get(sessionID)
	if !ok {
		return GitHubAgentLogin{}, false
	}
	login := GitHubAgentLogin{CLIAuthSession: cliAuthSnapshot(session.Snapshot())}
	if token, found := githubAgentTokens.LoadAndDelete(login.SessionID); found {
		login.Token = token.(string)
	}
	return login, true
}

func CancelGitHubAgentLogin(sessionID string) error {
	session, ok := githubAgentLogins.Get(sessionID)
	if !ok {
		return errors.New("GitHub sign-in was not found")
	}
	session.Cancel("GitHub sign-in was cancelled")
	githubAgentTokens.Delete(sessionID)
	return nil
}

func runGitHubAgentLogin(ctx context.Context, session *authflow.Session, executable, host string) {
	defer githubAgentLogins.Release(session)
	dir, err := os.MkdirTemp("", "agentmux-gh-agent-")
	if err != nil {
		session.Finish(CLIAuthSessionFailed, err.Error())
		return
	}
	defer os.RemoveAll(dir)
	env := githubAgentEnv(dir)
	args := []string{"auth", "login", "--hostname", host, "--git-protocol", "https", "--web", "--skip-ssh-key", "--scopes", "notifications"}
	// Plain-text storage keeps the token inside the profile this function
	// deletes, instead of the keychain, where it would outlive the sign-in.
	help, _ := githubCommand(ctx, executable, env, "auth", "login", "--help").CombinedOutput()
	if !strings.Contains(string(help), "--insecure-storage") {
		session.Finish(CLIAuthSessionFailed, "this GitHub CLI is too old to sign in a second account; update it in Tools")
		return
	}
	args = append(args, "--insecure-storage")
	if strings.Contains(string(help), "--clipboard") {
		args = append(args, "--clipboard=false") // the Console shows the code; leave the clipboard alone
	}
	if !session.BeginPhase("login") {
		return
	}
	cmd := githubCommand(ctx, executable, env, args...)
	output, err := cmd.StdoutPipe()
	if err != nil {
		session.Finish(CLIAuthSessionFailed, err.Error())
		return
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		session.Finish(CLIAuthSessionFailed, err.Error())
		return
	}
	var last string
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 0, 4096), 512*1024)
	for scanner.Scan() {
		line := authflow.StripControls(scanner.Text())
		if url, code := authflow.ActionableURL(line), authflow.ActionableCode(line); url != "" || code != "" {
			session.Actionable(url, code)
		} else if strings.TrimSpace(line) != "" {
			last = strings.TrimSpace(line)
		}
	}
	if err := cmd.Wait(); err != nil {
		finishCLIAuthCommandError(ctx, session, "GitHub", errors.New(firstNonEmptyString(last, err.Error())))
		return
	}
	token, err := githubCommand(ctx, executable, env, "auth", "token", "--hostname", host).Output()
	if err != nil || strings.TrimSpace(string(token)) == "" {
		session.Finish(CLIAuthSessionFailed, "gh signed in but returned no token")
		return
	}
	githubAgentTokens.Store(session.Snapshot().SessionID, strings.TrimSpace(string(token)))
	// A token nobody collects does not outlive its session's snapshot.
	sessionID := session.Snapshot().SessionID
	time.AfterFunc(cliAuthSessionTTL, func() { githubAgentTokens.Delete(sessionID) })
	session.Finish(CLIAuthSessionSucceeded, "")
}

// githubAgentEnv runs gh against the agent's profile only. gh prefers a token
// in the environment over any sign-in, and that token could be the operator's,
// so those variables are dropped rather than passed on. No browser opens: it
// would be signed in to GitHub as the operator.
func githubAgentEnv(dir string) []string {
	env := []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GH_HOST", "GH_CONFIG_DIR", "BROWSER", "GH_BROWSER":
			continue
		}
		env = append(env, entry)
	}
	return append(env, "GH_CONFIG_DIR="+dir, "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1", "BROWSER=false", "GH_BROWSER=false")
}

// githubCommand runs gh with env, or with the operator's environment when env
// is nil.
func githubCommand(ctx context.Context, executable string, env []string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, executable, args...)
	procutil.Prepare(cmd)
	cmd.Env = env
	if env == nil {
		cmd.Env = os.Environ()
	}
	cmd.Dir = durableCLICommandDir()
	return cmd
}

// GitHubOperator is the person running AgentMux as their own gh profile knows
// them, and the repositories they administer, to invite the agent to.
type GitHubOperator struct {
	Installed bool     `json:"installed"`
	Login     string   `json:"login,omitempty"`
	Repos     []string `json:"repos"`
	Error     string   `json:"error,omitempty"`
}

// LookupGitHubOperator reads the operator's own gh login. It only reads.
func LookupGitHubOperator(ctx context.Context, host string) GitHubOperator {
	operator := GitHubOperator{Repos: []string{}}
	executable, err := resolveCLIExecutable("gh")
	if err != nil {
		return operator
	}
	operator.Installed = true
	if !githubHostName.MatchString(host) {
		operator.Error = fmt.Sprintf("invalid GitHub host %q", host)
		return operator
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	login, err := githubCommand(ctx, executable, nil, "api", "--hostname", host, "user", "--jq", ".login").Output()
	if err != nil {
		operator.Error = "gh is not signed in on this computer"
		return operator
	}
	operator.Login = strings.TrimSpace(string(login))
	repos, err := githubCommand(ctx, executable, nil, "api", "--hostname", host,
		"user/repos?affiliation=owner,collaborator,organization_member&sort=pushed&per_page=100",
		"--jq", "[.[] | select(.permissions.admin) | .full_name]").Output()
	if err == nil {
		_ = json.Unmarshal(repos, &operator.Repos)
	}
	return operator
}

// InviteGitHubCollaborator invites login to repo with write access, as the
// operator. GitHub answers an existing collaborator with no invitation.
func InviteGitHubCollaborator(ctx context.Context, host, repo, login string) error {
	if !githubHostName.MatchString(host) || !githubRepoName.MatchString(repo) || !githubLoginName.MatchString(login) {
		return errors.New("invalid repository or account")
	}
	executable, err := resolveCLIExecutable("gh")
	if err != nil {
		return ErrGitHubCLIMissing
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	output, err := githubCommand(ctx, executable, nil, "api", "--hostname", host, "--method", "PUT",
		fmt.Sprintf("repos/%s/collaborators/%s", repo, login), "-f", "permission=push").CombinedOutput()
	if err != nil {
		return fmt.Errorf("invite @%s to %s: %s", login, repo, firstNonEmptyString(lastLine(string(output)), err.Error()))
	}
	return nil
}

func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(authflow.StripControls(text)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
