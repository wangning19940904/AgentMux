package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeGH puts a gh on PATH that logs each call as "GH_CONFIG_DIR|GH_TOKEN|args"
// and signs in once the approved file exists.
func fakeGH(t *testing.T) (log, approved string) {
	t.Helper()
	dir := t.TempDir()
	log, approved = filepath.Join(dir, "calls.log"), filepath.Join(dir, "approved")
	script := `#!/bin/sh
echo "$GH_CONFIG_DIR|$GH_TOKEN|$*" >> "$FAKE_GH_LOG"
case "$*" in
  "auth login --help") echo "      --insecure-storage   plain text"; echo "  -c, --clipboard   copy"; exit 0 ;;
  "auth login "*)
    echo "! First copy your one-time code: ABCD-1234" >&2
    echo "Open this URL to continue in your web browser: https://github.com/login/device" >&2
    while [ ! -f "$FAKE_GH_APPROVED" ]; do sleep 0.05; done
    echo "token: gho_agent" > "$GH_CONFIG_DIR/hosts.yml"; exit 0 ;;
  "auth token "*) echo "gho_agent"; exit 0 ;;
  "api --hostname github.com user --jq .login") echo "operator-login"; exit 0 ;;
  "api --hostname github.com user/repos"*) echo '["acme/app","acme/web"]'; exit 0 ;;
  "api --hostname github.com --method PUT "*) echo '{}'; exit 0 ;;
esac
echo "unexpected: $*" >&2; exit 1
`
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_GH_LOG", log)
	t.Setenv("FAKE_GH_APPROVED", approved)
	t.Setenv("GH_TOKEN", "operator-token")
	return log, approved
}

func calls(t *testing.T, log string) []string {
	t.Helper()
	data, _ := os.ReadFile(log)
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func TestGitHubAgentSignsInThroughItsOwnProfile(t *testing.T) {
	log, approved := fakeGH(t)
	started, err := StartGitHubAgentLogin("github.com")
	if err != nil {
		t.Fatal(err)
	}
	if started.VerificationCode != "ABCD-1234" || started.Token != "" {
		t.Fatalf("started = %+v", started)
	}
	if err := os.WriteFile(approved, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var done GitHubAgentLogin
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		done, _ = GetGitHubAgentLogin(started.SessionID)
		if done.State.Terminal() {
			break
		}
	}
	if done.State != CLIAuthSessionSucceeded || done.Token != "gho_agent" {
		t.Fatalf("finished = %+v", done)
	}
	if again, _ := GetGitHubAgentLogin(started.SessionID); again.Token != "" {
		t.Fatal("the token must be handed over only once")
	}

	var profile string
	for _, call := range calls(t, log) {
		parts := strings.SplitN(call, "|", 3)
		if parts[0] == "" || parts[1] != "" {
			t.Errorf("an agent command ran outside its profile or with the operator's token: %q", call)
		}
		profile = parts[0]
		if strings.HasPrefix(parts[2], "auth login --hostname") {
			for _, flag := range []string{"--insecure-storage", "--clipboard=false", "--scopes notifications", "--web"} {
				if !strings.Contains(parts[2], flag) {
					t.Errorf("login %q lacks %s", parts[2], flag)
				}
			}
		}
	}
	if _, err := os.Stat(profile); !os.IsNotExist(err) {
		t.Fatalf("the agent's gh profile %s must be removed: %v", profile, err)
	}
}

func TestGitHubOperatorUsesTheirOwnLogin(t *testing.T) {
	log, _ := fakeGH(t)
	operator := LookupGitHubOperator(context.Background(), "github.com")
	if !operator.Installed || operator.Login != "operator-login" || strings.Join(operator.Repos, ",") != "acme/app,acme/web" {
		t.Fatalf("operator = %+v", operator)
	}
	if err := InviteGitHubCollaborator(context.Background(), "github.com", "acme/app", "agent-bot"); err != nil {
		t.Fatal(err)
	}
	all := calls(t, log)
	if invite := all[len(all)-1]; invite != "|operator-token|api --hostname github.com --method PUT repos/acme/app/collaborators/agent-bot -f permission=push" {
		t.Fatalf("invite ran as %q", invite)
	}
	for _, bad := range [][2]string{{"acme/app;rm", "agent-bot"}, {"acme/app", "agent bot"}, {"acme/app", "-agent"}} {
		if err := InviteGitHubCollaborator(context.Background(), "github.com", bad[0], bad[1]); err == nil {
			t.Errorf("invite %v accepted", bad)
		}
	}
	if _, err := StartGitHubAgentLogin("github.com/evil"); err == nil {
		t.Fatal("a host with a path was accepted")
	}
}
