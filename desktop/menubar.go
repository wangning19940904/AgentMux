//go:build desktop
// +build desktop

package main

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
)

const menuBarHelperName = "AgentNexusMenuBar"

type menuBarProcess struct {
	cmd *exec.Cmd
}

func (a *App) startMenuBar(log *slog.Logger, addr string) {
	if goruntime.GOOS != "darwin" {
		return
	}
	helperPath, ok := findMenuBarHelper()
	if !ok {
		log.Info("menubar helper not found; build with `make desktop` to bundle it")
		return
	}

	cmd := exec.CommandContext(a.ctx, helperPath)
	cmd.Env = menuBarEnv(addr)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		log.Warn("start menubar helper", "path", helperPath, "err", err)
		return
	}
	a.menubar = &menuBarProcess{cmd: cmd}

	go func() {
		err := cmd.Wait()
		if a.ctx.Err() == nil && err != nil {
			log.Warn("menubar helper exited", "err", err)
		}
	}()
}

func findMenuBarHelper() (string, bool) {
	var candidates []string
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), menuBarHelperName))
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(cwd, "macos-menubar", menuBarHelperName),
			filepath.Join(cwd, "..", "macos-menubar", menuBarHelperName),
		)
	}

	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() {
			return candidate, true
		}
	}
	return "", false
}

func menuBarEnv(addr string) []string {
	env := append([]string{}, os.Environ()...)
	env = append(env,
		"ANX_ADDR="+daemonBaseURL(addr),
		fmt.Sprintf("ANX_PARENT_PID=%d", os.Getpid()),
	)
	if exe, err := os.Executable(); err == nil {
		if bundle := appBundlePath(exe); bundle != "" {
			env = append(env, "ANX_APP_BUNDLE="+bundle)
		}
	}
	return env
}

func daemonBaseURL(addr string) string {
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return strings.TrimRight(addr, "/")
	}
	host, port, err := net.SplitHostPort(addr)
	if err == nil {
		switch host {
		case "", "0.0.0.0", "::":
			host = "127.0.0.1"
		}
		return "http://" + net.JoinHostPort(host, port)
	}
	return "http://" + strings.TrimRight(addr, "/")
}

func appBundlePath(executable string) string {
	macOSDir := filepath.Dir(executable)
	contentsDir := filepath.Dir(macOSDir)
	bundleDir := filepath.Dir(contentsDir)
	if filepath.Base(macOSDir) == "MacOS" &&
		filepath.Base(contentsDir) == "Contents" &&
		strings.HasSuffix(bundleDir, ".app") {
		return bundleDir
	}
	return ""
}
