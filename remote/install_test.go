package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStartRemoteServiceUsesPostgres(t *testing.T) {
	client := &recordingRemoteClient{}
	if err := startRemoteServiceAt(context.Background(), client, "linux", "127.0.0.1:8765", remoteLinuxPostgresURL); err != nil {
		t.Fatal(err)
	}
	if len(client.commands) != 1 {
		t.Fatalf("commands = %d, want 1", len(client.commands))
	}
	command := client.commands[0]
	if !strings.Contains(command, `--database-url "postgresql:///agentmux?host=/var/run/postgresql&port=5432&sslmode=disable"`) ||
		strings.Contains(command, `--sqlite-path`) {
		t.Fatalf("remote service does not use PostgreSQL exclusively:\n%s", command)
	}
	if !strings.Contains(command, "After=network-online.target postgresql.service") {
		t.Fatalf("remote service does not wait for PostgreSQL:\n%s", command)
	}
}

func TestPrepareRemotePostgresProvisionsLinuxDatabase(t *testing.T) {
	client := &recordingRemoteClient{}
	databaseURL, err := prepareRemotePostgres(context.Background(), client, "linux", "bridge-secret")
	if err != nil {
		t.Fatal(err)
	}
	if databaseURL != remoteLinuxPostgresURL || len(client.commands) != 1 {
		t.Fatalf("database URL = %q, commands = %d", databaseURL, len(client.commands))
	}
	command := client.commands[0]
	for _, want := range []string{
		"apt-get install -y -qq postgresql postgresql-client",
		`if ! pg_isready -q -h /var/run/postgresql -p "$port"`,
		"createuser -h /var/run/postgresql",
		"createdb -h /var/run/postgresql",
		`detected_port=$(pg_lsclusters`,
		`AGENTMUX_BRIDGE_TOKEN='bridge-secret'`,
		`database setup`,
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("PostgreSQL preparation does not contain %q:\n%s", want, command)
		}
	}
}

func TestMigrateRemoteSQLiteWritesCompletionMarker(t *testing.T) {
	client := &recordingRemoteClient{}
	if err := migrateRemoteSQLite(
		context.Background(), client, "/home/tiger/.agentnexus/agentnexus.db", remoteLinuxPostgresURL,
	); err != nil {
		t.Fatal(err)
	}
	if len(client.commands) != 1 {
		t.Fatalf("commands = %d, want 1", len(client.commands))
	}
	command := client.commands[0]
	for _, want := range []string{
		`database migrate-sqlite`,
		`--observations-since 30d --resume`,
		`agentmux-pre-postgres-`,
		`sqlite-migration-complete`,
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("SQLite migration does not contain %q:\n%s", want, command)
		}
	}
}

func TestUpdateRemoteAgentMuxPreservesLegacyDatabase(t *testing.T) {
	binaryPath := filepath.Join(t.TempDir(), "amux-linux-amd64")
	if err := os.WriteFile(binaryPath, []byte("packaged-agentmux"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTMUX_REMOTE_BINARY", binaryPath)
	client := &updateRecordingRemoteClient{}
	artifact, err := updateRemoteAgentMux(context.Background(), client, Host{
		RemoteAddr: "127.0.0.1:8765",
	})
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Platform != "linux" || artifact.Arch != "amd64" || artifact.Version != "test-build" {
		t.Fatalf("artifact platform/version = %+v", artifact)
	}
	if artifact.DataPath != "/home/tiger/.agentnexus/agentnexus.db" ||
		artifact.DatabaseURL != remoteLinuxPostgresURL ||
		artifact.BackupPath != "/home/tiger/.agentmux/backups/pre-update.db" {
		t.Fatalf("artifact paths = %+v", artifact)
	}
	if artifact.SHA256 == "" || artifact.SHA256 != client.uploadChecksum {
		t.Fatalf("artifact checksum = %q, uploaded = %q", artifact.SHA256, client.uploadChecksum)
	}
	joined := strings.Join(client.commands, "\n---\n")
	if !strings.Contains(joined, `kill -TERM "$pid"`) ||
		!strings.Contains(joined, `database migrate-sqlite`) ||
		!strings.Contains(joined, `--database-url "postgresql:///agentmux?host=/var/run/postgresql&port=5432&sslmode=disable"`) ||
		strings.Contains(joined, `--sqlite-path`) {
		t.Fatalf("update did not stop, migrate, and restart on PostgreSQL:\n%s", joined)
	}
}

type recordingRemoteClient struct{ commands []string }

func (*recordingRemoteClient) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, nil
}

func (c *recordingRemoteClient) Run(_ context.Context, command string, _ io.Reader) ([]byte, error) {
	c.commands = append(c.commands, command)
	if strings.Contains(command, "AGENTMUX_DATABASE_URL=") {
		return []byte("AGENTMUX_DATABASE_URL=" + remoteLinuxPostgresURL + "\n"), nil
	}
	return nil, nil
}

func (*recordingRemoteClient) Close() error { return nil }

type updateRecordingRemoteClient struct {
	commands       []string
	uploadChecksum string
}

func (*updateRecordingRemoteClient) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, nil
}

func (c *updateRecordingRemoteClient) Run(_ context.Context, command string, stdin io.Reader) ([]byte, error) {
	c.commands = append(c.commands, command)
	switch {
	case command == `uname -s && uname -m`:
		return []byte("Linux\nx86_64\n"), nil
	case strings.Contains(command, `cat > "$tmp"`):
		hash := sha256.New()
		if _, err := io.Copy(hash, stdin); err != nil {
			return nil, err
		}
		c.uploadChecksum = hex.EncodeToString(hash.Sum(nil))
		return nil, nil
	case strings.Contains(command, "sha256sum"):
		return []byte(c.uploadChecksum + "\n"), nil
	case strings.Contains(command, `.amux-next" version`):
		return []byte("amux test-build\n"), nil
	case strings.Contains(command, "for fd in /proc"):
		return []byte("/home/tiger/.agentnexus/agentnexus.db\n"), nil
	case strings.Contains(command, "agentmux-pre-update"):
		return []byte("/home/tiger/.agentmux/backups/pre-update.db\n"), nil
	case strings.Contains(command, "AGENTMUX_DATABASE_URL="):
		return []byte("AGENTMUX_DATABASE_URL=" + remoteLinuxPostgresURL + "\n"), nil
	default:
		return nil, nil
	}
}

func (*updateRecordingRemoteClient) Close() error { return nil }
