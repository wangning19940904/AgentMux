package remote

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"os"
	"os/exec"
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
	args := strings.Join(client.baseArgs(), " ")
	if !strings.Contains(args, "HostKeyAlgorithms="+publicKey.Type()) {
		t.Fatalf("OpenSSH args do not pin %s: %s", publicKey.Type(), args)
	}
	knownHosts, err := os.ReadFile(client.knownHostsPath)
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

func TestOpenSSHClientDefersPinnedKeyRemovalUntilActiveProcessesExit(t *testing.T) {
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
	if err := client.retainKnownHosts(); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(client.knownHostsPath); err != nil {
		t.Fatalf("pinned key was removed while an SSH process was active: %v", err)
	}
	if err := client.retainKnownHosts(); err == nil {
		t.Fatal("closed OpenSSH client accepted a new process")
	}
	client.releaseKnownHosts()
	if _, err := os.Stat(client.knownHostsPath); !os.IsNotExist(err) {
		t.Fatalf("pinned key was not removed after the active process exited: %v", err)
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

type typedPublicKey struct{ algorithm string }

func (k typedPublicKey) Type() string                      { return k.algorithm }
func (typedPublicKey) Marshal() []byte                     { return []byte("test") }
func (typedPublicKey) Verify([]byte, *ssh.Signature) error { return nil }
