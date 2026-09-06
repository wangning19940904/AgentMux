package remote

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// remoteClient is the subset of an SSH connection needed by remote control.
// The native implementation is preferred; the OpenSSH implementation lets
// imported SSH Config aliases reuse authentication methods such as GSSAPI
// that golang.org/x/crypto/ssh does not implement on its own.
type remoteClient interface {
	DialContext(context.Context, string, string) (net.Conn, error)
	Run(context.Context, string, io.Reader) ([]byte, error)
	Close() error
}

type verifiedHostKeyClient interface {
	remoteClient
	VerifiedHostKey() ssh.PublicKey
}

type nativeSSHClient struct {
	client      *ssh.Client
	verifiedKey ssh.PublicKey
}

func (c *nativeSSHClient) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return c.client.DialContext(ctx, network, address)
}

func (c *nativeSSHClient) Run(ctx context.Context, command string, stdin io.Reader) ([]byte, error) {
	session, err := c.client.NewSession()
	if err != nil {
		return nil, err
	}
	defer session.Close()
	session.Stdin = stdin
	type result struct {
		output []byte
		err    error
	}
	done := make(chan result, 1)
	go func() {
		output, runErr := session.CombinedOutput(command)
		done <- result{output: output, err: runErr}
	}()
	select {
	case <-ctx.Done():
		_ = session.Close()
		return nil, ctx.Err()
	case result := <-done:
		return result.output, result.err
	}
}

func (c *nativeSSHClient) Close() error { return c.client.Close() }

func (c *nativeSSHClient) VerifiedHostKey() ssh.PublicKey { return c.verifiedKey }

type openSSHClient struct {
	executable     string
	host           Host
	timeout        time.Duration
	knownHostsLine string
	hostKeyAlias   string
	hostKeyAlgos   string
	lifecycleMu    sync.Mutex
	closed         bool
	dialSlots      chan struct{}
	closedCh       chan struct{}
	closeSignal    sync.Once
}

var newOpenSSHRemoteClient = func(host Host, key ssh.PublicKey, timeout time.Duration) (remoteClient, error) {
	return newOpenSSHClient(host, key, timeout)
}

func newOpenSSHClient(host Host, key ssh.PublicKey, timeout time.Duration) (*openSSHClient, error) {
	if key == nil {
		return nil, errors.New("system OpenSSH fallback has no verified host key")
	}
	executable, err := exec.LookPath("ssh")
	if err != nil {
		return nil, errors.New("system OpenSSH is not available for SSH Config authentication")
	}
	digest := sha256.Sum256(key.Marshal())
	alias := fmt.Sprintf("agentmux-%x", digest[:12])
	line := fmt.Sprintf("%s %s %s\n", alias, key.Type(), base64.StdEncoding.EncodeToString(key.Marshal()))
	return &openSSHClient{
		executable: executable, host: host, timeout: timeout,
		knownHostsLine: line, hostKeyAlias: alias,
		hostKeyAlgos: openSSHHostKeyAlgorithms(key), dialSlots: make(chan struct{}, 1),
		closedCh: make(chan struct{}),
	}, nil
}

func openSSHHostKeyAlgorithms(key ssh.PublicKey) string {
	if key == nil {
		return ""
	}
	switch key.Type() {
	case ssh.KeyAlgoRSA:
		// known_hosts stores RSA keys as ssh-rsa even when the handshake uses
		// an RSA-SHA2 signature. Prefer modern signatures while keeping the
		// already verified key as the only accepted host identity.
		return strings.Join([]string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}, ",")
	case ssh.CertAlgoRSAv01:
		// RSA host certificates likewise keep the legacy certificate key
		// format while negotiating a modern RSA-SHA2 signature algorithm.
		return strings.Join([]string{ssh.CertAlgoRSASHA512v01, ssh.CertAlgoRSASHA256v01, ssh.CertAlgoRSAv01}, ",")
	default:
		return key.Type()
	}
}

func (c *openSSHClient) baseArgs(knownHostsPath string) []string {
	seconds := int(c.timeout.Round(time.Second) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	args := []string{
		"-T",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=" + strconv.Itoa(seconds),
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile=" + strconv.Quote(knownHostsPath),
		"-o", "GlobalKnownHostsFile=/dev/null",
		"-o", "HostKeyAlias=" + c.hostKeyAlias,
		"-o", "UpdateHostKeys=no",
		"-o", "ControlMaster=no",
		"-o", "ControlPath=none",
		"-p", strconv.Itoa(c.host.Port),
		"-l", c.host.User,
	}
	if c.hostKeyAlgos != "" {
		args = append(args, "-o", "HostKeyAlgorithms="+c.hostKeyAlgos)
	}
	if c.host.KeyPath != "" {
		args = append(args, "-i", c.host.KeyPath)
	}
	return args
}

func (c *openSSHClient) target() string {
	if c.host.SSHAlias != "" {
		return c.host.SSHAlias
	}
	return c.host.Host
}

// A cached client retains only the verified public key, never a temporary file
// path. System temp cleaners can remove files while the desktop stays running
// for days. Give each subprocess a fresh pin file, owned until that process
// exits, so neither idle cleanup nor another request can invalidate its trust.
func (c *openSSHClient) command(ctx context.Context, extra ...string) (*exec.Cmd, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	c.lifecycleMu.Lock()
	closed := c.closed
	c.lifecycleMu.Unlock()
	if closed {
		return nil, nil, errors.New("system OpenSSH client is closed")
	}
	dir, err := os.MkdirTemp("", "agentmux-known-host-")
	if err != nil {
		return nil, nil, fmt.Errorf("create temporary known_hosts directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	path := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(path, []byte(c.knownHostsLine), 0o600); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("write temporary known_hosts: %w", err)
	}
	args := append(c.baseArgs(path), extra...)
	args = append(args, "--", c.target())
	return exec.CommandContext(ctx, c.executable, args...), cleanup, nil
}

func (c *openSSHClient) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, fmt.Errorf("system OpenSSH does not support tunnel network %q", network)
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := c.acquireTunnelStart(ctx); err != nil {
		return nil, err
	}
	defer c.releaseTunnelStart()
	// The context supplied to a net/http DialContext may be canceled as soon
	// as dialing returns. The returned net.Conn owns the subprocess lifetime,
	// so Close—not the short-lived dial context—must stop it.
	cmd, cleanup, err := c.command(context.Background(), "-vv", "-o", "LogLevel=DEBUG2", "-o", "ClearAllForwardings=yes", "-W", address)
	if err != nil {
		return nil, err
	}
	started := false
	defer func() {
		if !started {
			cleanup()
		}
	}()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open system SSH tunnel input: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("open system SSH tunnel output: %w", err)
	}
	stderr := &sshTunnelLog{cappedBuffer: cappedBuffer{limit: 64 << 10}, ready: make(chan struct{})}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("start system SSH tunnel: %w", err)
	}
	started = true
	conn := &sshProcessConn{
		stdin: stdin, stdout: stdout, cmd: cmd, stderr: stderr,
		done: make(chan struct{}), remote: sshProcessAddr(address),
	}
	go func() {
		conn.waitErr = cmd.Wait()
		cleanup()
		close(conn.done)
	}()
	// Serialize only the SSH channel handshake. Waiting for the first HTTP
	// response keeps this slot occupied by slow requests, idle connections,
	// and streams, eventually starving every other request to the host.
	select {
	case <-stderr.ready:
		if err := ctx.Err(); err != nil {
			_ = conn.Close()
			return nil, err
		}
		return conn, nil
	case <-ctx.Done():
		_ = conn.Close()
		return nil, ctx.Err()
	case <-c.closedCh:
		_ = conn.Close()
		return nil, errors.New("system OpenSSH client is closed")
	case <-conn.done:
		_ = conn.Close()
		return nil, fmt.Errorf("system SSH tunnel startup failed: %v: %s", conn.waitErr, stderr.String())
	}
}

func (c *openSSHClient) Run(ctx context.Context, command string, stdin io.Reader) ([]byte, error) {
	cmd, cleanup, err := c.command(ctx)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	cmd.Args = append(cmd.Args, command)
	cmd.Stdin = stdin
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, sshAttemptError(ctx, fmt.Errorf("system SSH command: %w: %s", err, strings.TrimSpace(string(output))))
	}
	return output, nil
}

func (c *openSSHClient) Close() error {
	c.lifecycleMu.Lock()
	c.closed = true
	c.lifecycleMu.Unlock()
	c.closeSignal.Do(func() { close(c.closedCh) })
	return nil
}

func (c *openSSHClient) acquireTunnelStart(ctx context.Context) error {
	select {
	case <-c.closedCh:
		return errors.New("system OpenSSH client is closed")
	default:
	}
	select {
	case c.dialSlots <- struct{}{}:
		select {
		case <-c.closedCh:
			c.releaseTunnelStart()
			return errors.New("system OpenSSH client is closed")
		default:
			return nil
		}
	case <-ctx.Done():
		return ctx.Err()
	case <-c.closedCh:
		return errors.New("system OpenSSH client is closed")
	}
}

func (c *openSSHClient) releaseTunnelStart() {
	select {
	case <-c.dialSlots:
	default:
	}
}

type sshProcessConn struct {
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	cmd       *exec.Cmd
	stderr    *sshTunnelLog
	done      chan struct{}
	waitErr   error
	remote    net.Addr
	closeOnce sync.Once
}

func (c *sshProcessConn) Read(data []byte) (int, error) {
	n, err := c.stdout.Read(data)
	if err == io.EOF {
		<-c.done
		if c.waitErr != nil {
			return n, fmt.Errorf("system SSH tunnel: %w: %s", c.waitErr, c.stderr.String())
		}
	}
	return n, err
}

func (c *sshProcessConn) Write(data []byte) (int, error) {
	n, err := c.stdin.Write(data)
	if err != nil {
		select {
		case <-c.done:
			if c.waitErr != nil {
				return n, fmt.Errorf("system SSH tunnel: %w: %s", c.waitErr, c.stderr.String())
			}
		default:
		}
	}
	return n, err
}

func (c *sshProcessConn) Close() error {
	c.closeOnce.Do(func() {
		_ = c.stdin.Close()
		_ = c.stdout.Close()
		if c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		<-c.done
	})
	return nil
}

func (c *sshProcessConn) LocalAddr() net.Addr  { return sshProcessAddr("local") }
func (c *sshProcessConn) RemoteAddr() net.Addr { return c.remote }
func (c *sshProcessConn) SetDeadline(deadline time.Time) error {
	return errors.Join(c.SetReadDeadline(deadline), c.SetWriteDeadline(deadline))
}
func (c *sshProcessConn) SetReadDeadline(deadline time.Time) error {
	return c.stdout.(*os.File).SetReadDeadline(deadline)
}
func (c *sshProcessConn) SetWriteDeadline(deadline time.Time) error {
	return c.stdin.(*os.File).SetWriteDeadline(deadline)
}

// OpenSSH DEBUG2 reports channel confirmation before any application bytes.
// Track the stdio channel explicitly, and handle split/batched stderr writes.
// Debug output stays internal; only actionable diagnostics are returned.
type sshTunnelLog struct {
	cappedBuffer
	pending   []byte
	channel   string
	ready     chan struct{}
	readyOnce sync.Once
}

func (l *sshTunnelLog) Write(data []byte) (int, error) {
	n, err := l.cappedBuffer.Write(data)
	l.pending = append(l.pending, data...)
	for {
		index := bytes.IndexByte(l.pending, '\n')
		if index < 0 {
			break
		}
		line := strings.TrimSpace(string(l.pending[:index]))
		l.pending = l.pending[index+1:]
		if rest, ok := strings.CutPrefix(line, "debug1: channel "); ok {
			channel, detail, found := strings.Cut(rest, ": ")
			if _, parseErr := strconv.Atoi(channel); found && parseErr == nil && (strings.HasPrefix(detail, "new stdio-forward") || strings.HasPrefix(detail, "new [stdio-forward]")) {
				l.channel = channel
			}
		}
		if l.channel != "" && strings.HasPrefix(line, "debug2: channel "+l.channel+": open confirm ") {
			l.readyOnce.Do(func() { close(l.ready) })
		}
	}
	if len(l.pending) > 8192 {
		l.pending = l.pending[len(l.pending)-8192:]
	}
	return n, err
}

func (l *sshTunnelLog) String() string {
	var lines []string
	for _, line := range strings.Split(l.cappedBuffer.String(), "\n") {
		if strings.HasPrefix(line, "debug1:") || strings.HasPrefix(line, "debug2:") || strings.HasPrefix(line, "debug3:") || strings.HasPrefix(line, "OpenSSH_") {
			continue
		}
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

type sshProcessAddr string

func (a sshProcessAddr) Network() string { return "ssh" }
func (a sshProcessAddr) String() string  { return string(a) }

type cappedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (b *cappedBuffer) String() string { return b.buffer.String() }

func (b *cappedBuffer) Write(data []byte) (int, error) {
	original := len(data)
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		_, _ = b.buffer.Write(data)
	}
	return original, nil
}
