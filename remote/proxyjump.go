package remote

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const sshProxyLookupTimeout = 5 * time.Second

// dialSSHTransport returns the byte stream that carries the SSH handshake.
// A ProxyJump alias only changes this stream: the Go client still authenticates
// the target end to end, so the pinned host key is verified through the jump.
func (m *Manager) dialSSHTransport(ctx context.Context, host Host, address string) (net.Conn, error) {
	if jump := lookupSSHProxyJump(ctx, host.SSHAlias); jump != "" {
		conn, err := dialViaProxyJump(jump, address, m.timeout)
		if err != nil {
			return nil, fmt.Errorf("connect SSH %s via ProxyJump %s: %w", address, jump, err)
		}
		return conn, nil
	}
	dialer := net.Dialer{Timeout: m.timeout}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("connect SSH %s: %w", address, err)
	}
	return conn, nil
}

// lookupSSHProxyJump asks ssh(1) rather than the import parser so Match blocks,
// Includes and edits made after import resolve exactly as they do in a terminal.
// Without a usable answer the host keeps its direct connection.
func lookupSSHProxyJump(ctx context.Context, alias string) string {
	if alias == "" {
		return ""
	}
	executable, err := exec.LookPath("ssh")
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, sshProxyLookupTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, executable, "-G", "--", alias).Output()
	if err != nil {
		return ""
	}
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		key, value, found := strings.Cut(strings.TrimSpace(scanner.Text()), " ")
		if found && strings.EqualFold(key, "proxyjump") && !strings.EqualFold(value, "none") {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// proxyJumpArgs connects to the last hop itself, going through any earlier hops
// with -J. Each hop keeps its own SSH Config settings and known_hosts checks.
func proxyJumpArgs(jump, address string, timeout time.Duration) ([]string, error) {
	hops := strings.Split(jump, ",")
	last := strings.TrimSpace(hops[len(hops)-1])
	if last == "" || strings.HasPrefix(last, "-") {
		return nil, fmt.Errorf("invalid ProxyJump %q", jump)
	}
	// Unlike ProxyJump, an ssh destination only accepts a port in URI form.
	if !strings.HasPrefix(last, "ssh://") && strings.Contains(last, ":") {
		last = "ssh://" + last
	}
	seconds := int(timeout.Round(time.Second) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	args := []string{
		"-T",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=" + strconv.Itoa(seconds),
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"-o", "ClearAllForwardings=yes",
		"-o", "ControlMaster=no",
		"-o", "ControlPath=none",
	}
	if len(hops) > 1 {
		args = append(args, "-J", strings.Join(hops[:len(hops)-1], ","))
	}
	return append(args, "-W", address, "--", last), nil
}

// The returned connection owns the subprocess, which outlives the short dial
// context. Handshake bytes only flow once the jump has opened the forward, and
// a failed jump surfaces its stderr through the connection's first Read.
func dialViaProxyJump(jump, address string, timeout time.Duration) (net.Conn, error) {
	executable, err := exec.LookPath("ssh")
	if err != nil {
		return nil, errors.New("system OpenSSH is required for ProxyJump")
	}
	args, err := proxyJumpArgs(jump, address, timeout)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(executable, args...)
	cmd.WaitDelay = sshProcessWaitDelay
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open ProxyJump input: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("open ProxyJump output: %w", err)
	}
	stderr := &sshTunnelLog{cappedBuffer: cappedBuffer{limit: 64 << 10}, ready: make(chan struct{})}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("start ProxyJump: %w", err)
	}
	conn := &sshProcessConn{
		stdin: stdin, stdout: stdout, cmd: cmd, stderr: stderr,
		done: make(chan struct{}), remote: sshProcessAddr(address),
	}
	go func() {
		conn.waitErr = cmd.Wait()
		close(conn.done)
	}()
	return conn, nil
}
