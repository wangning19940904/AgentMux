package framework

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/internal/procutil"
)

var proxyEnvironmentPairs = [][2]string{
	{"http_proxy", "HTTP_PROXY"}, {"https_proxy", "HTTPS_PROXY"},
	{"all_proxy", "ALL_PROXY"}, {"no_proxy", "NO_PROXY"},
}

const proxyEnvironmentMarker = "\x1eAGENTMUX_PROXY_ENV\x00"

// InheritShellProxyEnvironment runs once during daemon composition, before
// HTTP clients and Agent processes start. Service managers do not load shell
// profiles, so explicitly import their exported proxy variables. Existing
// service environment takes precedence, including explicitly empty values.
// Only proxy variables are captured; shell output and credentials are never
// included in logs. Individual Agent env overrides still take precedence.
func InheritShellProxyEnvironment(ctx context.Context) ([]string, error) {
	if runtime.GOOS == "windows" {
		return nil, nil
	}
	values, err := readShellProxyEnvironment(ctx)
	if err != nil {
		return nil, err
	}
	var imported []string
	for _, pair := range proxyEnvironmentPairs {
		_, lowerSet := os.LookupEnv(pair[0])
		_, upperSet := os.LookupEnv(pair[1])
		if lowerSet || upperSet {
			continue // Do not shadow an explicit value with its other-case alias.
		}
		for _, key := range pair {
			if value, ok := values[key]; ok {
				if err := os.Setenv(key, value); err != nil {
					return imported, errors.New("could not import shell proxy settings")
				}
				imported = append(imported, key)
			}
		}
	}
	return imported, nil
}

func readShellProxyEnvironment(ctx context.Context) (map[string]string, error) {
	shell := strings.TrimSpace(os.Getenv("SHELL"))
	if shell == "" {
		shell = "/bin/bash"
		if runtime.GOOS == "darwin" {
			shell = "/bin/zsh"
		}
		if _, err := os.Stat(shell); err != nil {
			shell = "/bin/sh"
		}
	}
	if !filepath.IsAbs(shell) {
		return nil, errors.New("login shell path must be absolute")
	}
	// A POSIX child works even when the interactive login shell is fish. The
	// child sees only exported values, matching normal CLI subprocess behavior.
	script := "printf '\\036AGENTMUX_PROXY_ENV\\000';\n"
	for _, pair := range proxyEnvironmentPairs {
		for _, key := range pair {
			script += "if [ \"${" + key + "+x}\" = x ]; then printf '" + key + "=%s\\000' \"$" + key + "\"; fi\n"
		}
	}
	command := "exec /bin/sh -c '" + strings.ReplaceAll(script, "'", "'\\''") + "'"
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, shell, "-ilc", command)
	cmd.Dir = durableFrameworkCommandDir()
	cmd.Stderr = io.Discard
	procutil.Prepare(cmd)
	output, err := cmd.Output()
	if err != nil {
		return nil, errors.New("could not read login shell proxy settings within 5 seconds")
	}
	return parseShellProxyEnvironment(output)
}

func parseShellProxyEnvironment(output []byte) (map[string]string, error) {
	index := bytes.LastIndex(output, []byte(proxyEnvironmentMarker))
	if index < 0 {
		return nil, errors.New("login shell did not return proxy settings")
	}
	allowed := make(map[string]bool)
	for _, pair := range proxyEnvironmentPairs {
		for _, key := range pair {
			allowed[key] = true
		}
	}
	values := make(map[string]string)
	for _, entry := range bytes.Split(output[index+len(proxyEnvironmentMarker):], []byte{0}) {
		key, value, ok := strings.Cut(string(entry), "=")
		if ok && allowed[key] {
			values[key] = value
		}
	}
	return values, nil
}
