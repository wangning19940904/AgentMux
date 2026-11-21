package cliagents

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/wangning19940904/AgentMux/agent/internal/runner"
)

// A Super Agent worker's Codex turn should outlive the AgentMux process that
// started it, so an update or a crash does not cut it off. Its app-server
// therefore runs detached: in its own session, listening on a unix socket
// under AgentMux's state directory, where the next AgentMux process finds it
// and rejoins the turn with thread/resume.
//
// There is one app-server per Codex configuration (binary and environment),
// and per generation of it: the Codex version and app-server arguments. After
// a Codex update new turns go to a new generation, while the old one keeps
// its running turns and is stopped once it is idle.
//
// Layout: <root>/<configuration>/<generation>/{sock,pid,used,server.log}.

// codexServersRoot is where detached app-servers keep their sockets.
var codexServersRoot = func() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "agentmux", "codex-servers"), nil
}

var (
	// codexServerStartTimeout bounds how long a new app-server may take to
	// listen. A first start after an update can be slow.
	codexServerStartTimeout = time.Minute
	// codexServerUnusedAfter is how long an app-server of a configuration no
	// longer in use is kept before it is stopped, once idle.
	codexServerUnusedAfter = 24 * time.Hour
	// codexVersionTTL is how long a Codex version check is reused.
	codexVersionTTL = 30 * time.Second
)

// codexServerMu serializes starting and stopping app-servers in this process;
// lockFile does the same across processes.
var codexServerMu sync.Mutex

type codexServerSpec struct {
	binary string
	// args come before --listen.
	args []string
	env  map[string]string
}

// configuration names the Codex binary and environment.
func (s codexServerSpec) configuration() string {
	hash := sha256.New()
	hash.Write([]byte(s.binary))
	keys := make([]string, 0, len(s.env))
	for key := range s.env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		hash.Write([]byte{0})
		hash.Write([]byte(key + "=" + s.env[key]))
	}
	return hex.EncodeToString(hash.Sum(nil))[:12]
}

// generation names the installed Codex version and the arguments.
func (s codexServerSpec) generation(ctx context.Context) (string, error) {
	version, err := codexVersion(ctx, s)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	hash.Write([]byte(version))
	for _, arg := range s.args {
		hash.Write([]byte{0})
		hash.Write([]byte(arg))
	}
	return hex.EncodeToString(hash.Sum(nil))[:8], nil
}

type codexVersionEntry struct {
	version string
	at      time.Time
}

var codexVersions sync.Map

// codexVersion asks the binary for its version: an npm-installed Codex is a
// script whose own file does not change on update.
func codexVersion(ctx context.Context, spec codexServerSpec) (string, error) {
	key := spec.binary + "\x00" + spec.configuration()
	if cached, ok := codexVersions.Load(key); ok {
		if entry := cached.(codexVersionEntry); time.Since(entry.at) < codexVersionTTL {
			return entry.version, nil
		}
	}
	versionCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(versionCtx, spec.binary, "--version")
	cmd.Env = runner.BuildEnv(spec.env)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("check the Codex version: %w", err)
	}
	version := strings.TrimSpace(string(output))
	codexVersions.Store(key, codexVersionEntry{version: version, at: time.Now()})
	return version, nil
}

// acquireCodexServer returns the socket of the app-server for spec's current
// generation, starting one when none is listening. Idle app-servers of older
// generations are stopped on the way.
func acquireCodexServer(ctx context.Context, spec codexServerSpec) (string, error) {
	root, err := codexServersRoot()
	if err != nil {
		return "", err
	}
	generation, err := spec.generation(ctx)
	if err != nil {
		return "", err
	}
	configDir := filepath.Join(root, spec.configuration())
	dir := filepath.Join(configDir, generation)
	codexServerMu.Lock()
	defer codexServerMu.Unlock()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	unlock, _, err := lockFile(filepath.Join(configDir, ".lock"), true)
	if err != nil {
		return "", err
	}
	defer unlock()
	touchCodexServer(dir)
	sock := filepath.Join(dir, "sock")
	if !codexServerListening(sock) {
		stopCodexServer(dir)
		if err := startCodexServer(ctx, spec, dir); err != nil {
			return "", err
		}
	}
	retireCodexServers(ctx, root, spec.configuration(), generation)
	return sock, nil
}

// findCodexServerWithThread returns the socket of a live app-server of spec's
// configuration that has threadID loaded, or "" when none has.
func findCodexServerWithThread(ctx context.Context, spec codexServerSpec, threadID string) (string, error) {
	root, err := codexServersRoot()
	if err != nil {
		return "", err
	}
	generations, _ := os.ReadDir(filepath.Join(root, spec.configuration()))
	for _, generation := range generations {
		if !generation.IsDir() {
			continue
		}
		sock := filepath.Join(root, spec.configuration(), generation.Name(), "sock")
		if !codexServerListening(sock) {
			continue
		}
		threads, err := codexServerThreads(ctx, sock, false)
		if err != nil {
			continue
		}
		if _, loaded := threads[threadID]; loaded {
			return sock, nil
		}
	}
	return "", nil
}

func touchCodexServer(dir string) {
	now := time.Now()
	path := filepath.Join(dir, "used")
	if os.Chtimes(path, now, now) != nil {
		_ = os.WriteFile(path, nil, 0o600)
	}
}

func startCodexServer(ctx context.Context, spec codexServerSpec, dir string) error {
	sock := filepath.Join(dir, "sock")
	_ = os.Remove(sock)
	logPath := filepath.Join(dir, "server.log")
	if info, err := os.Stat(logPath); err == nil && info.Size() > 1<<20 {
		_ = os.Rename(logPath, logPath+".1")
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	args := append(append([]string(nil), spec.args...), "--listen", "unix://"+sock)
	cmd := exec.Command(spec.binary, args...)
	cmd.Dir = dir
	if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
	}
	cmd.Env = runner.BuildEnv(spec.env)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = detachedProcAttr()
	if err := cmd.Start(); err != nil {
		return err
	}
	pid := cmd.Process.Pid
	_ = os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(pid)), 0o600)
	exited := make(chan error, 1)
	// Reaped while this process lives; after that it belongs to init.
	go func() { exited <- cmd.Wait() }()
	deadline := time.NewTimer(codexServerStartTimeout)
	defer deadline.Stop()
	for !codexServerListening(sock) {
		select {
		case err := <-exited:
			return fmt.Errorf("codex app-server exited at start: %v%s", err, logTail(logPath))
		case <-deadline.C:
			signalProcessGroup(pid, sigKill)
			return fmt.Errorf("codex app-server did not listen within %s%s", codexServerStartTimeout, logTail(logPath))
		case <-ctx.Done():
			// It keeps starting; the next caller finds it.
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	return nil
}

func logTail(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	data = bytes.TrimSpace(data)
	if len(data) > 2048 {
		data = data[len(data)-2048:]
	}
	if len(data) == 0 {
		return ""
	}
	return ": " + string(data)
}

// stopCodexServer ends the app-server recorded in dir, if it is still the
// process that was started there.
func stopCodexServer(dir string) {
	data, err := os.ReadFile(filepath.Join(dir, "pid"))
	_ = os.Remove(filepath.Join(dir, "pid"))
	if err != nil {
		return
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	if !processAlive(pid) || !codexServerProcess(pid, dir) {
		return
	}
	signalProcessGroup(pid, sigTerm)
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if !processAlive(pid) {
			return
		}
	}
	signalProcessGroup(pid, sigKill)
}

// codexServerProcess guards against a reused pid: the process must be the
// app-server listening in dir.
func codexServerProcess(pid int, dir string) bool {
	output, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	return err == nil && strings.Contains(string(output), filepath.Join(dir, "sock"))
}

// retireCodexServers stops idle app-servers that new turns no longer use:
// older generations of this configuration, and those of configurations not
// used for codexServerUnusedAfter. A server with a running turn is kept.
func retireCodexServers(ctx context.Context, root, configuration, current string) {
	configs, _ := os.ReadDir(root)
	for _, config := range configs {
		if !config.IsDir() {
			continue
		}
		configDir := filepath.Join(root, config.Name())
		generations, _ := os.ReadDir(configDir)
		var stale []string
		for _, generation := range generations {
			if !generation.IsDir() || config.Name() == configuration && generation.Name() == current {
				continue
			}
			dir := filepath.Join(configDir, generation.Name())
			if config.Name() == configuration || unusedSince(dir, codexServerUnusedAfter) {
				stale = append(stale, dir)
			}
		}
		if len(stale) == 0 {
			continue
		}
		unlock := func() {}
		if config.Name() != configuration {
			// This process already holds its own configuration's lock.
			release, locked, err := lockFile(filepath.Join(configDir, ".lock"), false)
			if err != nil || !locked {
				continue
			}
			unlock = release
		}
		for _, dir := range stale {
			if busy := codexServerBusy(ctx, filepath.Join(dir, "sock")); busy {
				continue
			}
			stopCodexServer(dir)
			_ = os.RemoveAll(dir)
		}
		unlock()
	}
}

func unusedSince(dir string, age time.Duration) bool {
	info, err := os.Stat(filepath.Join(dir, "used"))
	return err != nil || time.Since(info.ModTime()) > age
}

// codexServerBusy reports whether the app-server at sock has a turn running.
// One that does not answer counts as idle.
func codexServerBusy(ctx context.Context, sock string) bool {
	if !codexServerListening(sock) {
		return false
	}
	threads, err := codexServerThreads(ctx, sock, true)
	if err != nil {
		return false
	}
	for _, active := range threads {
		if active {
			return true
		}
	}
	return false
}

// codexServerThreads lists the app-server's loaded threads and, with status,
// whether each has a turn running.
func codexServerThreads(ctx context.Context, sock string, status bool) (map[string]bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	client, err := newCodexSocketClient(ctx, sock)
	if err != nil {
		return nil, err
	}
	defer client.close()
	threads := map[string]bool{}
	cursor := ""
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		result, err := client.call(ctx, "thread/loaded/list", params)
		if err != nil {
			return nil, err
		}
		ids, _ := result["data"].([]any)
		for _, raw := range ids {
			if id, _ := raw.(string); id != "" {
				threads[id] = false
			}
		}
		cursor, _ = result["nextCursor"].(string)
		if cursor == "" || len(ids) == 0 {
			break
		}
	}
	if !status {
		return threads, nil
	}
	for id := range threads {
		result, err := client.call(ctx, "thread/read", map[string]any{"threadId": id, "includeTurns": false})
		if err != nil {
			return nil, err
		}
		threads[id] = firstString(nestedMap(nestedMap(result, "thread"), "status"), "type") == "active"
	}
	return threads, nil
}

// codexSocketTarget is the path to dial. Codex keeps the socket itself in a
// short temporary path and links it from the one it was given, because unix
// socket paths are short.
func codexSocketTarget(sock string) string {
	if resolved, err := filepath.EvalSymlinks(sock); err == nil {
		return resolved
	}
	return sock
}

func codexServerListening(sock string) bool {
	conn, err := net.DialTimeout("unix", codexSocketTarget(sock), time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// newCodexSocketClient connects to a detached app-server, which speaks the
// same JSON-RPC as on stdio, one message per WebSocket text frame.
func newCodexSocketClient(ctx context.Context, sock string) (*codexAppClient, error) {
	target := codexSocketTarget(sock)
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", target)
		},
	}
	conn, response, err := dialer.DialContext(ctx, "ws://localhost/", nil)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("connect to the codex app-server: %w", err)
	}
	client := &codexAppClient{
		ws: conn, nextID: 1, pending: map[int]chan codexRPCResponse{},
		sessions: map[string]*codexSession{}, done: make(chan struct{}),
	}
	go client.readLoop()
	if err := client.initialize(ctx); err != nil {
		_ = client.close()
		return nil, err
	}
	return client, nil
}
