package remote

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestOpenSSHClientPinsNegotiationToVerifiedHostKeyAlgorithm(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("system OpenSSH is not installed")
	}
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := ssh.NewPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	client, err := newOpenSSHClient(Host{Host: "example.invalid", Port: 22, User: "tester"}, publicKey, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	cmd, cleanup, err := client.command(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	args := strings.Join(cmd.Args, " ")
	if !strings.Contains(args, "HostKeyAlgorithms="+publicKey.Type()) {
		t.Fatalf("OpenSSH args do not pin %s: %s", publicKey.Type(), args)
	}
	knownHosts, err := os.ReadFile(commandKnownHostsPath(t, cmd))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(knownHosts), client.hostKeyAlias+" "+publicKey.Type()+" ") {
		t.Fatalf("known_hosts does not contain the pinned key: %s", knownHosts)
	}
}

func TestOpenSSHRSAHostKeyAlgorithmsPreferSHA2(t *testing.T) {
	key := typedPublicKey{algorithm: ssh.KeyAlgoRSA}
	algorithms := openSSHHostKeyAlgorithms(key)
	if algorithms != strings.Join([]string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}, ",") {
		t.Fatalf("RSA host algorithms = %q", algorithms)
	}
}

func TestOpenSSHRSACertificateAlgorithmsPreferSHA2(t *testing.T) {
	key := typedPublicKey{algorithm: ssh.CertAlgoRSAv01}
	algorithms := openSSHHostKeyAlgorithms(key)
	want := strings.Join([]string{ssh.CertAlgoRSASHA512v01, ssh.CertAlgoRSASHA256v01, ssh.CertAlgoRSAv01}, ",")
	if algorithms != want {
		t.Fatalf("RSA certificate host algorithms = %q, want %q", algorithms, want)
	}
}

func TestOpenSSHClientClosePreservesPreparedCommandPins(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("system OpenSSH is not installed")
	}
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := ssh.NewPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	client, err := newOpenSSHClient(Host{Host: "example.invalid", Port: 22, User: "tester"}, publicKey, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	cmd, cleanup, err := client.command(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	path := commandKnownHostsPath(t, cmd)
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("pinned key was removed while an SSH process was active: %v", err)
	}
	if _, cleanup, err := client.command(context.Background()); err == nil {
		cleanup()
		t.Fatal("closed OpenSSH client accepted a new process")
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("pinned key was not removed after the active process exited: %v", err)
	}
}

func commandKnownHostsPath(t *testing.T, cmd *exec.Cmd) string {
	t.Helper()
	for _, arg := range cmd.Args {
		if value, ok := strings.CutPrefix(arg, "UserKnownHostsFile="); ok {
			path, err := strconv.Unquote(value)
			if err != nil {
				t.Fatal(err)
			}
			return path
		}
	}
	t.Fatal("SSH command has no pinned known_hosts file")
	return ""
}

func TestOpenSSHClientSurvivesTemporaryFileCleanup(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("system OpenSSH is not installed")
	}
	// Use real OpenSSH and a local SSH server. The temp root includes spaces,
	// since UserKnownHostsFile is parsed as an SSH config list even in argv.
	tempRoot := filepath.Join(t.TempDir(), "SSH temp files")
	if err := os.Mkdir(tempRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(name, tempRoot)
	}
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	keyBlock, err := ssh.MarshalPrivateKey(privateKey, "")
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "id_ecdsa")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(keyBlock), 0o600); err != nil {
		t.Fatal(err)
	}
	sshHost, sshPort := startSSHForwarderWithHostSigners(t, signer.PublicKey(), signer)
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"ok":true,"version":"after-cleanup"}`)
	}))
	t.Cleanup(apiServer.Close)
	host := Host{Host: sshHost, Port: sshPort, User: "tester", KeyPath: keyPath, RemoteAddr: apiServer.Listener.Addr().String()}
	client, err := newOpenSSHClient(host, signer.PublicKey(), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, removeDirectory := range []bool{false, true} {
		// Model an idle client's previous pin file being removed externally.
		old, cleanup, err := client.command(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(cleanup)
		path := commandKnownHostsPath(t, old)
		if removeDirectory {
			err = os.RemoveAll(filepath.Dir(path))
		} else {
			err = os.Remove(path)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Run(ctx, "true", nil); err != nil {
			t.Fatalf("command after temp cleanup (directory=%v): %v", removeDirectory, err)
		}
		status, err := requestStatus(ctx, client, host)
		if err != nil || status["version"] != "after-cleanup" {
			t.Fatalf("tunnel after temp cleanup (directory=%v): status=%v, err=%v", removeDirectory, status, err)
		}
		cleanup()
	}
	// Concurrent fleet reads must own separate pin files and clean up on exit.
	errors := make(chan error, 6)
	for i := 0; i < cap(errors); i++ {
		go func() {
			_, err := client.Run(ctx, "true", nil)
			errors <- err
		}()
	}
	for i := 0; i < cap(errors); i++ {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	// Fresh files must still reject an untrusted key of the SAME algorithm.
	wrongPrivate, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wrongSigner, err := ssh.NewSignerFromKey(wrongPrivate)
	if err != nil {
		t.Fatal(err)
	}
	wrongClient, err := newOpenSSHClient(host, wrongSigner.PublicKey(), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = wrongClient.Close() })
	if output, err := wrongClient.Run(ctx, "true", nil); err == nil || !strings.Contains(string(output), "Host key verification failed") {
		t.Fatalf("expected strict rejection of untrusted ECDSA key: output=%s, err=%v", output, err)
	}
	// Startup errors also release pins; they must not build up on every retry.
	client.executable = filepath.Join(t.TempDir(), "missing-ssh")
	if _, err := client.Run(ctx, "true", nil); err == nil {
		t.Fatal("missing SSH executable unexpectedly ran")
	}
	if _, err := client.DialContext(ctx, "tcp", host.RemoteAddr); err == nil {
		t.Fatal("missing SSH executable unexpectedly opened a tunnel")
	}
	entries, err := os.ReadDir(tempRoot)
	if err != nil || len(entries) != 0 {
		t.Fatalf("SSH processes leaked pin files: entries=%v, err=%v", entries, err)
	}
}

func TestOpenSSHClientSerializesTunnelStartup(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("system OpenSSH is not installed")
	}
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := ssh.NewPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	client, err := newOpenSSHClient(Host{Host: "example.invalid", Port: 22, User: "tester"}, publicKey, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.acquireTunnelStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	acquired := make(chan error, 1)
	go func() { acquired <- client.acquireTunnelStart(context.Background()) }()
	select {
	case err := <-acquired:
		t.Fatalf("second tunnel start was not serialized: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	client.releaseTunnelStart()
	select {
	case err := <-acquired:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("second tunnel start did not resume")
	}
	client.releaseTunnelStart()
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if err := client.acquireTunnelStart(context.Background()); err == nil {
		t.Fatal("closed OpenSSH client accepted another tunnel start")
	}
}

func TestOpenSSHClientTunnelFailureDoesNotInvalidateSharedDialer(t *testing.T) {
	if shouldInvalidateAfterTunnelFailure(&openSSHClient{}) {
		t.Fatal("system OpenSSH subprocess failure invalidated the shared dialer")
	}
	if !shouldInvalidateAfterTunnelFailure(&malformedChannelRemoteClient{}) {
		t.Fatal("native SSH channel failure did not invalidate the persistent connection")
	}
}

func TestOpenSSHTunnelsDoNotWaitForApplicationTraffic(t *testing.T) {
	client, address := openSSHTunnelFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	first, err := client.DialContext(ctx, "tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	// An idle connection (or a slow HTTP response) must not hold the SSH
	// handshake slot. Neither tunnel has sent any application bytes yet.
	second, err := client.DialContext(ctx, "tcp", address)
	if err != nil {
		t.Fatalf("idle tunnel blocked the next connection: %v", err)
	}
	defer second.Close()
}

func TestOpenSSHTunnelStartupCancellationReleasesSlot(t *testing.T) {
	client, address := openSSHTunnelFixture(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			defer conn.Close()
			_, _ = io.Copy(io.Discard, conn)
		}
	}()
	original := client.host
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	client.host.Port, _ = strconv.Atoi(port)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	conn, err := client.DialContext(ctx, "tcp", address)
	if conn != nil {
		conn.Close()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unconfirmed tunnel returned %v", err)
	}
	client.host = original
	ctx, cancel = context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err = client.DialContext(ctx, "tcp", address)
	if err != nil {
		t.Fatalf("cancelled handshake stranded the next dial: %v", err)
	}
	conn.Close()
}

func openSSHTunnelFixture(t *testing.T) (*openSSHClient, string) {
	t.Helper()
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("system OpenSSH is not installed")
	}
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(private, "")
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "id_ecdsa")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	host, port := startSSHForwarderWithHostSigners(t, signer.PublicKey(), signer)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }))
	t.Cleanup(api.Close)
	client, err := newOpenSSHClient(Host{Host: host, Port: port, User: "tester", KeyPath: keyPath}, signer.PublicKey(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client, api.Listener.Addr().String()
}

type typedPublicKey struct{ algorithm string }

func TestSSHTunnelLogRecognizesSplitConfirmationAndLimitsDiagnostics(t *testing.T) {
	log := &sshTunnelLog{cappedBuffer: cappedBuffer{limit: 64}, ready: make(chan struct{})}
	for _, part := range []string{"debug1: channel 4: new stdio-", "forward [stdio-forward]\r\n", "debug2: channel 1: open confirm rwindow 1 rmax 1\n"} {
		_, _ = log.Write([]byte(part))
	}
	select {
	case <-log.ready:
		t.Fatal("unrelated channel marked tunnel ready")
	default:
	}
	_, _ = io.Copy(log, io.LimitReader(strings.NewReader("debug2: channel 4: open confirm rwindow 1 rmax 1\n"+strings.Repeat("x", 10000)), 20000))
	select {
	case <-log.ready:
	default:
		t.Fatal("stdio confirmation was not recognized")
	}
	if len(log.cappedBuffer.String()) > 64 {
		t.Fatal("io.Copy bypassed the diagnostic size limit")
	}
}

func TestSSHProcessConnReadDeadline(t *testing.T) {
	client, address := openSSHTunnelFixture(t)
	conn, err := client.DialContext(context.Background(), "tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(30 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	_, err = conn.Read(make([]byte, 1))
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("read deadline returned %v", err)
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
}

func (k typedPublicKey) Type() string                      { return k.algorithm }
func (typedPublicKey) Marshal() []byte                     { return []byte("test") }
func (typedPublicKey) Verify([]byte, *ssh.Signature) error { return nil }
