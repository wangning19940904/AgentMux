package framework

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func unsetProxyEnvironment(t *testing.T) {
	t.Helper()
	for _, pair := range proxyEnvironmentPairs {
		for _, key := range pair {
			value, present := os.LookupEnv(key)
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if present {
					_ = os.Setenv(key, value)
				} else {
					_ = os.Unsetenv(key)
				}
			})
		}
	}
}

func fakeProxyShell(t *testing.T, body string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	path := filepath.Join(t.TempDir(), "shell")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", path)
}

func TestImportShellProxiesForDaemonAndChildren(t *testing.T) {
	unsetProxyEnvironment(t)
	fakeProxyShell(t, `
export http_proxy='http://proxy.example:8118'
export https_proxy='http://proxy.example:8118'
export no_proxy='localhost,127.0.0.1,::1,code.example'
export PRIVATE_AUTH_TOKEN='must-not-be-imported'
printf 'profile banner\n'
exec /bin/sh -c "$2"
`)
	values, err := readShellProxyEnvironment(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 3 || values["http_proxy"] != "http://proxy.example:8118" || values["PRIVATE_AUTH_TOKEN"] != "" {
		t.Fatalf("unexpected proxy snapshot: %#v", values)
	}
	keys, err := InheritShellProxyEnvironment(context.Background())
	if err != nil || len(keys) != 3 {
		t.Fatalf("import keys=%v err=%v", keys, err)
	}
	if os.Getenv("https_proxy") != values["https_proxy"] || os.Getenv("no_proxy") != values["no_proxy"] {
		t.Fatal("proxies not available to child environment")
	}
}

func TestExplicitServiceProxyOverridesShellIncludingEmptyValues(t *testing.T) {
	unsetProxyEnvironment(t)
	t.Setenv("HTTP_PROXY", "http://service.example:8080")
	t.Setenv("https_proxy", "")
	t.Setenv("NO_PROXY", "localhost,internal.example")
	fakeProxyShell(t, `
export http_proxy='http://shell.example:8118'
export HTTPS_PROXY='http://shell.example:8118'
export no_proxy='wrong.example'
export ALL_PROXY='socks5://shell.example:1080'
exec /bin/sh -c "$2"
`)
	keys, err := InheritShellProxyEnvironment(context.Background())
	if err != nil || len(keys) != 1 || keys[0] != "ALL_PROXY" {
		t.Fatalf("import keys=%v err=%v", keys, err)
	}
	if os.Getenv("HTTP_PROXY") != "http://service.example:8080" || os.Getenv("https_proxy") != "" || os.Getenv("NO_PROXY") != "localhost,internal.example" {
		t.Fatal("service proxy settings were overwritten")
	}
	if _, ok := os.LookupEnv("http_proxy"); ok {
		t.Fatal("lowercase shell value shadows explicit uppercase proxy")
	}
	if _, ok := os.LookupEnv("HTTPS_PROXY"); ok {
		t.Fatal("explicitly disabled proxy was re-enabled")
	}
}

func TestShellProxyCaptureHonorsCancellationAndRedactsDiagnostics(t *testing.T) {
	unsetProxyEnvironment(t)
	fakeProxyShell(t, "echo 'private-auth-diagnostic' >&2\nexec /bin/sleep 30\n")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	keys, err := InheritShellProxyEnvironment(ctx)
	if err == nil || len(keys) != 0 || strings.Contains(err.Error(), "private-auth-diagnostic") || time.Since(start) > 3*time.Second {
		t.Fatalf("capture keys=%v err=%v", keys, err)
	}
}

func TestLiveShellProxyInheritance(t *testing.T) {
	if os.Getenv("AGENTMUX_LIVE_SHELL_PROXY") != "1" {
		t.Skip("requires a host with exported shell proxy settings")
	}
	keys, err := InheritShellProxyEnvironment(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("https_proxy") == "" && os.Getenv("HTTPS_PROXY") == "" {
		t.Fatal("shell HTTPS proxy was not inherited")
	}
	t.Logf("imported proxy variable names: %v", keys)
}
