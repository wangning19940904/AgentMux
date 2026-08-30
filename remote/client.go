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
	knownHostsDir  string
	knownHostsPath string
	hostKeyAlias   string
	hostKeyAlgos   string
	lifecycleMu    sync.Mutex
	closed         bool
	active         int
	dialSlots      chan struct{}
	closedCh       chan struct{}
	closeSignal    sync.Once
	closeOnce      sync.Once
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
	dir, err := os.MkdirTemp("", "agentmux-known-host-")
	if err != nil {
		return nil, fmt.Errorf("create temporary known_hosts directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("secure temporary known_hosts directory: %w", err)
	}
	digest := sha256.Sum256(key.Marshal())
	alias := fmt.Sprintf("agentmux-%x", digest[:12])
	path := filepath.Join(dir, "known_hosts")
	line := fmt.Sprintf("%s %s %s\n", alias, key.Type(), base64.StdEncoding.EncodeToString(key.Marshal()))
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("write temporary known_hosts: %w", err)
	}
	return &openSSHClient{
		executable: executable, host: host, timeout: timeout,
		knownHostsDir: dir, knownHostsPath: path, hostKeyAlias: alias,
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

func (c *openSSHClient) baseArgs() []string {
	seconds := int(c.timeout.Round(time.Second) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	args := []string{
		"-T",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=" + strconv.Itoa(seconds),
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile=" + c.knownHostsPath,
		"-o", "GlobalKnownHostsFile=/dev/null",
		"-o", "HostKeyAlias=" + c.hostKeyAlias,
		"-o", "UpdateHostKeys=no",
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

func (c *openSSHClient) command(ctx context.Context, extra ...string) *exec.Cmd {
	args := append(c.baseArgs(), extra...)
	args = append(args, "--", c.target())
	return exec.CommandContext(ctx, c.executable, args...)
}

func (c *openSSHClient) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, fmt.Errorf("system OpenSSH does not support tunnel network %q", network)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := c.retainKnownHosts(); err != nil {
		return nil, err
	}
	if err := c.acquireTunnelStart(ctx); err != nil {
		c.releaseKnownHosts()
		return nil, err
	}
	release := true
	defer func() {
		if release {
			c.releaseTunnelStart()
			c.releaseKnownHosts()
		}
	}()
	// The context supplied to a net/http DialContext may be canceled as soon
	// as dialing returns. The returned net.Conn owns the subprocess lifetime,
	// so Close—not the short-lived dial context—must stop it.
	cmd := c.command(context.Background(), "-W", address)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open system SSH tunnel input: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("open system SSH tunnel output: %w", err)
	}
	stderr := &cappedBuffer{limit: 64 << 10}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("start system SSH tunnel: %w", err)
	}
	conn := &sshProcessConn{
		stdin: stdin, stdout: stdout, cmd: cmd, stderr: stderr,
		done: make(chan struct{}), remote: sshProcessAddr(address),
		onReady: c.releaseTunnelStart,
	}
	go func() {
		conn.waitErr = cmd.Wait()
		conn.markReady()
		close(conn.done)
		c.releaseKnownHosts()
	}()
	release = false
	return conn, nil
}

func (c *openSSHClient) Run(ctx context.Context, command string, stdin io.Reader) ([]byte, error) {
	if err := c.retainKnownHosts(); err != nil {
		return nil, err
	}
	defer c.releaseKnownHosts()
	cmd := c.command(ctx)
	cmd.Args = append(cmd.Args, command)
	cmd.Stdin = stdin
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("system SSH command: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func (c *openSSHClient) Close() error {
	c.lifecycleMu.Lock()
	c.closed = true
	remove := c.active == 0
	c.lifecycleMu.Unlock()
	c.closeSignal.Do(func() { close(c.closedCh) })
	if remove {
		return c.removeKnownHosts()
	}
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

func (c *openSSHClient) retainKnownHosts() error {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	if c.closed {
		return errors.New("system OpenSSH client is closed")
	}
	c.active++
	return nil
}

func (c *openSSHClient) releaseKnownHosts() {
	c.lifecycleMu.Lock()
	if c.active > 0 {
		c.active--
	}
	remove := c.closed && c.active == 0
	c.lifecycleMu.Unlock()
	if remove {
		_ = c.removeKnownHosts()
	}
}

func (c *openSSHClient) removeKnownHosts() error {
	var err error
	c.closeOnce.Do(func() { err = os.RemoveAll(c.knownHostsDir) })
	return err
}

type sshProcessConn struct {
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	cmd       *exec.Cmd
	stderr    *cappedBuffer
	done      chan struct{}
	waitErr   error
	remote    net.Addr
	onReady   func()
	readyOnce sync.Once
	closeOnce sync.Once
}

func (c *sshProcessConn) Read(data []byte) (int, error) {
	n, err := c.stdout.Read(data)
	if n > 0 || err != nil {
		c.markReady()
	}
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
		c.markReady()
		_ = c.stdin.Close()
		_ = c.stdout.Close()
		if c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		<-c.done
	})
	return nil
}

func (c *sshProcessConn) markReady() {
	c.readyOnce.Do(func() {
		if c.onReady != nil {
			c.onReady()
		}
	})
}

func (c *sshProcessConn) LocalAddr() net.Addr              { return sshProcessAddr("local") }
func (c *sshProcessConn) RemoteAddr() net.Addr             { return c.remote }
func (c *sshProcessConn) SetDeadline(time.Time) error      { return nil }
func (c *sshProcessConn) SetReadDeadline(time.Time) error  { return nil }
func (c *sshProcessConn) SetWriteDeadline(time.Time) error { return nil }

type sshProcessAddr string

func (a sshProcessAddr) Network() string { return "ssh" }
func (a sshProcessAddr) String() string  { return string(a) }

type cappedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *cappedBuffer) Write(data []byte) (int, error) {
	original := len(data)
	remaining := b.limit - b.Len()
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		_, _ = b.Buffer.Write(data)
	}
	return original, nil
}
