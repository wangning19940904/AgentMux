package framework

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestFrameworkAuthReportsSignedInAccount(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test")
	}
	claims, err := json.Marshal(map[string]any{
		"email": "dev@example.test",
		"name":  "Dev Example",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_plan_type": "pro",
			"organizations": []map[string]any{
				{"title": "Secondary", "is_default": false},
				{"title": "Acme", "is_default": true},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	idToken := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(claims) + ".signature-never-serialize-me"
	traeExpiry := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)

	tests := []struct {
		kind   string
		bin    string
		script string
		files  map[string]string
		want   AuthAccount
	}{
		{
			kind: "claudecode", bin: "claude",
			script: `echo '{"loggedIn":true,"authMethod":"claude.ai","email":"dev@example.test","orgName":"Acme","subscriptionType":"max"}'`,
			want:   AuthAccount{Email: "dev@example.test", Organization: "Acme", Plan: "max", Method: "claude.ai"},
		},
		{
			kind: "codex", bin: "codex",
			script: `echo 'Logged in using ChatGPT'`,
			files: map[string]string{"CODEX_HOME/auth.json": `{"auth_mode":"chatgpt","tokens":{"id_token":"` + idToken +
				`","access_token":"never-serialize-me","refresh_token":"never-serialize-me"}}`},
			want: AuthAccount{Email: "dev@example.test", Name: "Dev Example", Organization: "Acme", Plan: "pro", Method: "ChatGPT"},
		},
		{
			kind: "cursor", bin: "cursor-agent",
			script: `printf '\033[32m✓\033[0m Logged in as dev@example.test\n'`,
			want:   AuthAccount{Email: "dev@example.test"},
		},
		{
			kind: "traecli", bin: "traecli",
			script: `echo 'Logged in using Trae'`,
			files: map[string]string{"TRAE_HOME/cli/auth.json": `{"auth_mode":"trae","trae":{"access_token":"never-serialize-me","user_id":"dev.example",` +
				`"expires_at":"` + traeExpiry + `","region":"CN","login_method":"git_codebase"}}`},
			want: AuthAccount{UserID: "dev.example", Region: "CN", Method: "git_codebase"},
		},
	}
	for _, test := range tests {
		t.Run(test.kind, func(t *testing.T) {
			bin := t.TempDir()
			homes := map[string]string{"CODEX_HOME": t.TempDir(), "TRAE_HOME": t.TempDir()}
			for key, dir := range homes {
				t.Setenv(key, dir)
			}
			t.Setenv("CURSOR_API_KEY", "")
			t.Setenv("CODEX_API_KEY", "")
			t.Setenv("OPENAI_API_KEY", "")
			t.Setenv("ANTHROPIC_API_KEY", "")
			for name, content := range test.files {
				root, rel, _ := strings.Cut(name, "/")
				path := filepath.Join(homes[root], filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			writeFrameworkExecutable(t, filepath.Join(bin, test.bin), "#!/bin/sh\n"+test.script+"\n")
			t.Setenv("PATH", bin+string(os.PathListSeparator)+"/bin:/usr/bin")

			status := CheckAuth(context.Background(), test.kind)
			if status.State != AuthStateAuthenticated || status.Account == nil {
				t.Fatalf("status = %+v", status)
			}
			if *status.Account != test.want {
				t.Fatalf("account = %+v, want %+v", *status.Account, test.want)
			}
			encoded, err := json.Marshal(status)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), "never-serialize-me") {
				t.Fatalf("status leaked credential material: %s", encoded)
			}
		})
	}
}

func TestFrameworkAuthOmitsAccountWhenSignedOut(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script test")
	}
	bin := t.TempDir()
	writeFrameworkExecutable(t, filepath.Join(bin, "claude"), `#!/bin/sh
echo '{"loggedIn":false,"authMethod":"none","email":"stale@example.test"}'
`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/bin:/usr/bin")
	t.Setenv("ANTHROPIC_API_KEY", "")
	status := CheckAuth(context.Background(), "claudecode")
	if status.State != AuthStateUnauthenticated || status.Account != nil {
		t.Fatalf("status = %+v", status)
	}
}

func TestFrameworkAccountNormalization(t *testing.T) {
	if got := (AuthAccount{Method: "  "}).normalized(); got != nil {
		t.Fatalf("empty account = %+v, want nil", got)
	}
	got := (AuthAccount{Email: " dev@example.test\x1b[0m\n", Name: strings.Repeat("a", maxAccountFieldLength+20)}).normalized()
	if got == nil || got.Email != "dev@example.test" || len([]rune(got.Name)) != maxAccountFieldLength {
		t.Fatalf("normalized = %+v", got)
	}
	if _, ok := decodeJWTClaims("not-a-jwt"); ok {
		t.Fatal("decoded a malformed token")
	}
	if account := cursorAccount([]byte("Not logged in")); account != (AuthAccount{}) {
		t.Fatalf("cursor signed-out account = %+v", account)
	}
}
