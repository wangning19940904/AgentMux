package remote

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"
)

func TestStartRemoteServiceUsesSelfContainedSQLite(t *testing.T) {
	client := &recordingRemoteClient{}
	if err := startRemoteService(context.Background(), client, "linux", "127.0.0.1:8765"); err != nil {
		t.Fatal(err)
	}
	if len(client.commands) != 1 {
		t.Fatalf("commands = %d, want 1", len(client.commands))
	}
	command := client.commands[0]
	if !strings.Contains(command, `--sqlite-path %h/.agentmux/agentmux.db`) ||
		!strings.Contains(command, `--sqlite-path "$HOME/.agentmux/agentmux.db"`) {
		t.Fatalf("remote service does not use SQLite:\n%s", command)
	}
	if strings.Contains(command, "database setup") {
		t.Fatalf("remote service still tries to provision PostgreSQL:\n%s", command)
	}
}

type recordingRemoteClient struct{ commands []string }

func (*recordingRemoteClient) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, nil
}

func (c *recordingRemoteClient) Run(_ context.Context, command string, _ io.Reader) ([]byte, error) {
	c.commands = append(c.commands, command)
	return nil, nil
}

func (*recordingRemoteClient) Close() error { return nil }
