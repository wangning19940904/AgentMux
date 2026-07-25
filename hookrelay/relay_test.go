package hookrelay

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRelayWritesUnixSocket(t *testing.T) {
	home := t.TempDir()
	opts := DefaultOptions(home)
	opts.Source = "codex"
	// Darwin limits sockaddr_un paths to 104 bytes. testing.T.TempDir can be
	// deeply nested, so keep the socket itself in a short system temp path.
	socketDir, err := os.MkdirTemp("", "amux-hook-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	opts.SocketPath = filepath.Join(socketDir, "o.sock")
	if err := os.MkdirAll(filepath.Dir(opts.SocketPath), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", opts.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	received := make(chan []byte, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		line, _ := bufio.NewReader(conn).ReadBytes('\n')
		received <- line
	}()

	delivery, err := Relay(context.Background(), strings.NewReader(`{"hook_event_name":"PreToolUse"}`), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !delivery.Socket || delivery.Spooled {
		t.Fatalf("unexpected delivery: %+v", delivery)
	}
	select {
	case line := <-received:
		var msg Message
		if err := json.Unmarshal(line, &msg); err != nil {
			t.Fatal(err)
		}
		if msg.Source != "codex" || !bytes.Contains(msg.Payload, []byte("PreToolUse")) {
			t.Fatalf("unexpected message: %+v", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for socket message")
	}
	if entries, err := os.ReadDir(opts.SpoolDir); err == nil && len(entries) != 0 {
		t.Fatalf("socket success unexpectedly created spool files: %v", entries)
	}
}

func TestRelayFallsBackToEncryptedSpool(t *testing.T) {
	home := t.TempDir()
	opts := DefaultOptions(home)
	opts.Source = "claude"
	opts.Now = func() time.Time { return time.Unix(123, 456) }
	secret := `{"hook_event_name":"PostToolUse","tool_response":"do-not-store-plaintext"}`

	start := time.Now()
	delivery, err := Relay(context.Background(), strings.NewReader(secret), opts)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > MaxSocketWait {
		t.Fatalf("offline relay took %v, expected less than %v", elapsed, MaxSocketWait)
	}
	if !delivery.Spooled || delivery.Socket || delivery.Path == "" {
		t.Fatalf("unexpected delivery: %+v", delivery)
	}
	raw, err := os.ReadFile(delivery.Path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("do-not-store-plaintext")) || bytes.Contains(raw, []byte("PostToolUse")) {
		t.Fatalf("spool contains plaintext: %s", raw)
	}
	plain, err := DecryptSpool(delivery.Path, opts.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	var msg Message
	if err := json.Unmarshal(bytes.TrimSpace(plain), &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Source != "claude" || !bytes.Contains(msg.Payload, []byte("do-not-store-plaintext")) {
		t.Fatalf("decrypted message mismatch: %+v", msg)
	}
	if info, err := os.Stat(opts.KeyPath); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("key permissions too broad: %o", info.Mode().Perm())
	}
}

func TestRelayRejectsSymlinkKey(t *testing.T) {
	home := t.TempDir()
	opts := DefaultOptions(home)
	if err := os.MkdirAll(filepath.Dir(opts.KeyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, "foreign-key")
	if err := os.WriteFile(target, make([]byte, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, opts.KeyPath); err != nil {
		t.Fatal(err)
	}
	if _, err := Relay(context.Background(), strings.NewReader(`{}`), opts); err == nil {
		t.Fatal("expected symlink key to be rejected")
	}
}
