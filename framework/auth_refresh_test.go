package framework

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestTraeAuthStatusRejectsExpiredNativeLogin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	home, bin := t.TempDir(), t.TempDir()
	t.Setenv("TRAE_HOME", home)
	t.Setenv("PATH", bin)
	writeFrameworkExecutable(t, filepath.Join(bin, "traecli"), "#!/bin/sh\necho 'Logged in using Trae'\n")
	if err := os.MkdirAll(filepath.Join(home, "cli"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, expired := range []bool{true, false} {
		expires := time.Now().Add(time.Hour)
		if expired {
			expires = time.Now().Add(-time.Hour)
		}
		payload := fmt.Sprintf(`{"auth_mode":"trae","trae":{"expires_at":%q,"access_token":"never-serialize-me"}}`, expires.Format(time.RFC3339))
		if err := os.WriteFile(filepath.Join(home, "cli", "auth.json"), []byte(payload), 0o600); err != nil {
			t.Fatal(err)
		}
		status := CheckAuth(context.Background(), "traecli")
		want := AuthStateAuthenticated
		if expired {
			want = AuthStateUnauthenticated
		}
		if status.State != want || !status.AutoRefreshSupported || status.ExpiresAt == "" || strings.Contains(fmt.Sprint(status), "never-serialize-me") {
			t.Fatalf("status=%+v", status)
		}
	}
}
