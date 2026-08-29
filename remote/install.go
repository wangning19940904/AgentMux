package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

type remoteUpdateArtifact struct {
	Platform    string
	Arch        string
	Version     string
	SHA256      string
	DataPath    string
	DatabaseURL string
	BackupPath  string
}

const (
	remoteLinuxPostgresURL  = "postgresql:///agentmux?host=/var/run/postgresql&port=5432&sslmode=disable"
	remoteDarwinPostgresURL = "postgresql:///agentmux?host=/tmp&port=5432&sslmode=disable"
)

// installRemoteAgentMux installs a bundled AgentMux CLI into the remote
// user's home directory and registers a persistent user service when the
// platform supports one. Linux installs provision the system PostgreSQL
// package when needed and therefore require passwordless sudo.
func installRemoteAgentMux(ctx context.Context, client remoteClient, host Host) error {
	remoteOS, remoteArch, err := remotePlatform(ctx, client)
	if err != nil {
		return err
	}

	// Reuse an existing CLI when only the daemon is stopped. Otherwise upload
	// the build packaged with the local Console.
	existing, _ := runRemoteCommand(ctx, client,
		`command -v amux 2>/dev/null || command -v agentmux 2>/dev/null || true`)
	if strings.TrimSpace(existing) != "" {
		_, err = runRemoteCommand(ctx, client, `set -eu
mkdir -p "$HOME/.agentmux/bin"
source_path=$(command -v amux 2>/dev/null || command -v agentmux 2>/dev/null)
if [ "$source_path" != "$HOME/.agentmux/bin/amux" ]; then
  cp "$source_path" "$HOME/.agentmux/bin/amux"
  chmod 0755 "$HOME/.agentmux/bin/amux"
fi`)
		if err != nil {
			return fmt.Errorf("prepare existing remote CLI: %w", err)
		}
	} else {
		binary, resolveErr := installerBinary(remoteOS, remoteArch)
		if resolveErr != nil {
			return resolveErr
		}
		if err := uploadRemoteBinary(ctx, client, binary); err != nil {
			return err
		}
	}

	legacyPath, err := discoverRemoteSQLitePath(ctx, client, host.RemoteAddr)
	if err != nil {
		return err
	}
	databaseURL, err := prepareRemotePostgres(ctx, client, remoteOS)
	if err != nil {
		return err
	}
	if err := migrateRemoteSQLite(ctx, client, legacyPath, databaseURL); err != nil {
		return err
	}
	if err := startRemoteServiceAt(ctx, client, remoteOS, host.RemoteAddr, databaseURL); err != nil {
		return err
	}
	return nil
}

func remotePlatform(ctx context.Context, client remoteClient) (string, string, error) {
	output, err := runRemoteCommand(ctx, client, `uname -s && uname -m`)
	if err != nil {
		return "", "", fmt.Errorf("detect remote platform: %w", err)
	}
	parts := strings.Fields(output)
	if len(parts) < 2 {
		return "", "", fmt.Errorf("detect remote platform: unexpected output %q", strings.TrimSpace(output))
	}
	var remoteOS string
	switch strings.ToLower(parts[0]) {
	case "linux":
		remoteOS = "linux"
	case "darwin":
		remoteOS = "darwin"
	default:
		return "", "", fmt.Errorf("automatic AgentMux installation is unsupported on %s", parts[0])
	}
	var arch string
	switch strings.ToLower(parts[1]) {
	case "x86_64", "amd64":
		arch = "amd64"
	case "aarch64", "arm64":
		arch = "arm64"
	default:
		return "", "", fmt.Errorf("automatic AgentMux installation is unsupported on architecture %s", parts[1])
	}
	return remoteOS, arch, nil
}

func installerBinary(remoteOS, remoteArch string) (string, error) {
	if configured := strings.TrimSpace(os.Getenv("AGENTMUX_REMOTE_BINARY")); configured != "" {
		if err := validateInstallerBinary(configured); err != nil {
			return "", fmt.Errorf("AGENTMUX_REMOTE_BINARY: %w", err)
		}
		return configured, nil
	}

	name := "amux-" + remoteOS + "-" + remoteArch
	var candidates []string
	if executable, err := os.Executable(); err == nil {
		dir := filepath.Dir(executable)
		candidates = append(candidates,
			filepath.Join(dir, "agentmux-remote", name),
			filepath.Join(dir, "..", "Resources", "agentmux-remote", name),
			filepath.Join(dir, name),
		)
		base := strings.ToLower(filepath.Base(executable))
		insideApp := strings.Contains(filepath.ToSlash(executable), ".app/Contents/MacOS/")
		if remoteOS == runtime.GOOS && remoteArch == runtime.GOARCH && !insideApp &&
			(base == "amux" || base == "agentmux") {
			candidates = append(candidates, executable)
		}
	}
	if workingDir, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(workingDir, "dist", name),
			filepath.Join(workingDir, "desktop", "build", "remote-assets", name),
		)
		matches, _ := filepath.Glob(filepath.Join(workingDir, "dist", "amux-*-"+remoteOS+"-"+remoteArch))
		candidates = append(candidates, matches...)
	}

	for _, candidate := range candidates {
		if validateInstallerBinary(candidate) == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf(
		"no AgentMux binary is packaged for %s/%s; set AGENTMUX_REMOTE_BINARY or rebuild the desktop app with remote assets",
		remoteOS, remoteArch,
	)
}

func validateInstallerBinary(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("installer is not a regular file")
	}
	if info.Size() == 0 {
		return errors.New("installer is empty")
	}
	return nil
}

func uploadRemoteBinary(ctx context.Context, client remoteClient, path string) error {
	if _, err := uploadRemoteBinaryCandidate(ctx, client, path); err != nil {
		return err
	}
	return activateRemoteBinary(ctx, client)
}

func uploadRemoteBinaryCandidate(ctx context.Context, client remoteClient, binaryPath string) (string, error) {
	file, err := os.Open(binaryPath)
	if err != nil {
		return "", fmt.Errorf("open local AgentMux binary: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	reader := io.TeeReader(file, hash)

	output, err := client.Run(ctx, `set -eu
umask 077
mkdir -p "$HOME/.agentmux/bin"
tmp="$HOME/.agentmux/bin/.amux-upload-$$"
trap 'rm -f "$tmp"' EXIT
cat > "$tmp"
chmod 0755 "$tmp"
mv "$tmp" "$HOME/.agentmux/bin/.amux-next"
trap - EXIT`, reader)
	if err != nil {
		return "", fmt.Errorf("upload AgentMux binary: %w: %s", err, strings.TrimSpace(string(output)))
	}
	checksum := hex.EncodeToString(hash.Sum(nil))
	remoteChecksum, err := runRemoteCommand(ctx, client, `set -eu
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum "$HOME/.agentmux/bin/.amux-next" | awk '{print $1}'
else
  shasum -a 256 "$HOME/.agentmux/bin/.amux-next" | awk '{print $1}'
fi`)
	if err != nil {
		return "", fmt.Errorf("verify uploaded AgentMux binary: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(remoteChecksum), checksum) {
		return "", fmt.Errorf("uploaded AgentMux checksum mismatch: local %s, remote %s", checksum, remoteChecksum)
	}
	return checksum, nil
}

func activateRemoteBinary(ctx context.Context, client remoteClient) error {
	output, err := client.Run(ctx, `set -eu
target="$HOME/.agentmux/bin/amux"
candidate="$HOME/.agentmux/bin/.amux-next"
test -x "$candidate"
if [ -f "$target" ]; then
  cp -p "$target" "$HOME/.agentmux/bin/amux.previous"
fi
mv "$candidate" "$target"
chmod 0755 "$target"`, nil)
	if err != nil {
		return fmt.Errorf("activate remote AgentMux binary: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func updateRemoteAgentMux(ctx context.Context, client remoteClient, host Host) (remoteUpdateArtifact, error) {
	remoteOS, remoteArch, err := remotePlatform(ctx, client)
	if err != nil {
		return remoteUpdateArtifact{}, err
	}
	binary, err := installerBinary(remoteOS, remoteArch)
	if err != nil {
		return remoteUpdateArtifact{}, err
	}
	dataPath, err := discoverRemoteSQLitePath(ctx, client, host.RemoteAddr)
	if err != nil {
		return remoteUpdateArtifact{}, err
	}
	checksum, err := uploadRemoteBinaryCandidate(ctx, client, binary)
	if err != nil {
		return remoteUpdateArtifact{}, err
	}
	versionOutput, err := runRemoteCommand(ctx, client, `"$HOME/.agentmux/bin/.amux-next" version`)
	if err != nil {
		return remoteUpdateArtifact{}, fmt.Errorf("validate uploaded AgentMux binary: %w", err)
	}
	version := strings.TrimSpace(strings.TrimPrefix(versionOutput, "amux "))
	backupPath, err := stopAndBackupRemoteService(ctx, client, remoteOS, host.RemoteAddr, dataPath)
	if err != nil {
		return remoteUpdateArtifact{}, err
	}
	artifact := remoteUpdateArtifact{
		Platform: remoteOS, Arch: remoteArch, Version: version, SHA256: checksum,
		DataPath: dataPath, BackupPath: backupPath,
	}
	if err := activateRemoteBinary(ctx, client); err != nil {
		return artifact, updateFailureWithBackup(err, backupPath)
	}
	databaseURL, err := prepareRemotePostgres(ctx, client, remoteOS)
	if err != nil {
		return artifact, updateFailureWithBackup(err, backupPath)
	}
	artifact.DatabaseURL = databaseURL
	if err := migrateRemoteSQLite(ctx, client, dataPath, databaseURL); err != nil {
		return artifact, updateFailureWithBackup(err, backupPath)
	}
	if err := startRemoteServiceAt(ctx, client, remoteOS, host.RemoteAddr, databaseURL); err != nil {
		return artifact, updateFailureWithBackup(err, backupPath)
	}
	return artifact, nil
}

func updateFailureWithBackup(err error, backupPath string) error {
	if strings.TrimSpace(backupPath) == "" {
		return err
	}
	return fmt.Errorf("%w (database backup: %s)", err, backupPath)
}

func discoverRemoteSQLitePath(ctx context.Context, client remoteClient, addr string) (string, error) {
	_, portValue, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("parse remote AgentMux address: %w", err)
	}
	portNumber, err := strconv.Atoi(portValue)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return "", fmt.Errorf("invalid remote AgentMux port %q", portValue)
	}
	command := `set -eu
pid=""
if command -v lsof >/dev/null 2>&1; then
  pid=$(lsof -nP -iTCP:` + strconv.Itoa(portNumber) + ` -sTCP:LISTEN -t 2>/dev/null | head -n 1 || true)
elif command -v ss >/dev/null 2>&1; then
  pid=$(ss -ltnp 'sport = :` + strconv.Itoa(portNumber) + `' 2>/dev/null | sed -n 's/.*pid=\([0-9][0-9]*\).*/\1/p' | head -n 1 || true)
fi
if [ -n "$pid" ] && [ -d "/proc/$pid/fd" ]; then
  for fd in /proc/"$pid"/fd/*; do
    candidate=$(readlink -f "$fd" 2>/dev/null || true)
    case "$candidate" in
      *.db|*.sqlite|*.sqlite3) printf '%s\n' "$candidate"; exit 0 ;;
    esac
  done
fi
if [ -f "$HOME/.agentmux/agentmux.db" ]; then
  printf '%s\n' "$HOME/.agentmux/agentmux.db"
elif [ -f "$HOME/.agentnexus/agentnexus.db" ]; then
  printf '%s\n' "$HOME/.agentnexus/agentnexus.db"
else
  printf '%s\n' "$HOME/.agentmux/agentmux.db"
fi`
	value, err := runRemoteCommand(ctx, client, command)
	if err != nil {
		return "", fmt.Errorf("discover remote AgentMux data path: %w", err)
	}
	value = strings.TrimSpace(value)
	if !path.IsAbs(value) || strings.ContainsAny(value, "\r\n\x00") {
		return "", fmt.Errorf("remote AgentMux returned unsafe data path %q", value)
	}
	return value, nil
}

func stopAndBackupRemoteService(
	ctx context.Context,
	client remoteClient,
	remoteOS, addr, dataPath string,
) (string, error) {
	_, portValue, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("parse remote AgentMux address: %w", err)
	}
	portNumber, err := strconv.Atoi(portValue)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return "", fmt.Errorf("invalid remote AgentMux port %q", portValue)
	}
	var stopManaged string
	switch remoteOS {
	case "linux":
		stopManaged = `systemctl --user stop agentmux.service >/dev/null 2>&1 || true`
	case "darwin":
		stopManaged = `uid=$(id -u)
launchctl bootout "gui/$uid/com.agentmux.client" >/dev/null 2>&1 || true`
	default:
		return "", fmt.Errorf("unsupported remote platform %q", remoteOS)
	}
	command := `set -eu
pid=""
if command -v lsof >/dev/null 2>&1; then
  pid=$(lsof -nP -iTCP:` + strconv.Itoa(portNumber) + ` -sTCP:LISTEN -t 2>/dev/null | head -n 1 || true)
elif command -v ss >/dev/null 2>&1; then
  pid=$(ss -ltnp 'sport = :` + strconv.Itoa(portNumber) + `' 2>/dev/null | sed -n 's/.*pid=\([0-9][0-9]*\).*/\1/p' | head -n 1 || true)
fi
` + stopManaged + `
if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
  kill -TERM "$pid"
  attempts=0
  while kill -0 "$pid" 2>/dev/null && [ "$attempts" -lt 50 ]; do
    sleep 0.2
    attempts=$((attempts + 1))
  done
  if kill -0 "$pid" 2>/dev/null; then
    echo "remote AgentMux process $pid did not stop after SIGTERM" >&2
    exit 1
  fi
fi
backup=""
data_path=` + shellQuote(dataPath) + `
if [ -f "$data_path" ]; then
  mkdir -p "$HOME/.agentmux/backups"
  backup="$HOME/.agentmux/backups/agentmux-pre-update-$(date +%Y%m%d-%H%M%S).db"
  cp -p "$data_path" "$backup"
fi
printf '%s\n' "$backup"`
	backupPath, err := runRemoteCommand(ctx, client, command)
	if err != nil {
		return "", fmt.Errorf("stop and back up remote AgentMux: %w", err)
	}
	return strings.TrimSpace(backupPath), nil
}

func prepareRemotePostgres(ctx context.Context, client remoteClient, remoteOS string) (string, error) {
	var command, databaseURL string
	switch remoteOS {
	case "linux":
		command = `set -eu
mkdir -p "$HOME/.agentmux"
if ! command -v psql >/dev/null 2>&1 || ! command -v pg_isready >/dev/null 2>&1; then
  if ! sudo -n true >/dev/null 2>&1; then
    echo "PostgreSQL is missing and passwordless sudo is unavailable" >&2
    exit 1
  fi
  if command -v apt-get >/dev/null 2>&1; then
    sudo -n apt-get update -qq
    sudo -n env DEBIAN_FRONTEND=noninteractive apt-get install -y -qq postgresql postgresql-client
  elif command -v dnf >/dev/null 2>&1; then
    sudo -n dnf install -y postgresql-server postgresql
  elif command -v yum >/dev/null 2>&1; then
    sudo -n yum install -y postgresql-server postgresql
  else
    echo "install PostgreSQL before installing AgentMux" >&2
    exit 1
  fi
fi
port=5432
if command -v pg_lsclusters >/dev/null 2>&1; then
  detected_port=$(pg_lsclusters --no-header 2>/dev/null | awk '$4 == "online" { print $3; exit }')
  if [ -n "$detected_port" ]; then
    port="$detected_port"
  fi
fi
case "$port" in
  ''|*[!0-9]*) echo "invalid PostgreSQL port: $port" >&2; exit 1 ;;
esac
if ! sudo -n true >/dev/null 2>&1; then
  echo "passwordless sudo is required to provision the AgentMux PostgreSQL role" >&2
  exit 1
fi
if ! pg_isready -q -h /var/run/postgresql -p "$port"; then
  sudo -n systemctl enable --now postgresql.service >/dev/null 2>&1 || \
    sudo -n systemctl start postgresql.service >/dev/null 2>&1 || true
fi
attempts=0
until pg_isready -q -h /var/run/postgresql -p "$port"; do
  attempts=$((attempts + 1))
  if [ "$attempts" -ge 60 ]; then
    echo "PostgreSQL did not become ready on /var/run/postgresql:$port" >&2
    exit 1
  fi
  sleep 0.5
done
role=$(id -un)
case "$role" in
  ''|*[!A-Za-z0-9_.-]*) echo "unsupported PostgreSQL role name: $role" >&2; exit 1 ;;
esac
if [ "$(sudo -n -u postgres psql -h /var/run/postgresql -p "$port" -d postgres -Atqc "SELECT 1 FROM pg_roles WHERE rolname='$role'")" != "1" ]; then
  sudo -n -u postgres createuser -h /var/run/postgresql -p "$port" --login "$role"
fi
if [ "$(sudo -n -u postgres psql -h /var/run/postgresql -p "$port" -d postgres -Atqc "SELECT 1 FROM pg_database WHERE datname='agentmux'")" != "1" ]; then
  sudo -n -u postgres createdb -h /var/run/postgresql -p "$port" --owner="$role" agentmux
fi
sudo -n -u postgres psql -h /var/run/postgresql -p "$port" -d postgres -v ON_ERROR_STOP=1 -qc "ALTER DATABASE agentmux OWNER TO \"$role\""
database_url="postgresql:///agentmux?host=/var/run/postgresql&port=$port&sslmode=disable"
"$HOME/.agentmux/bin/amux" --database-url "$database_url" database setup
printf '\nAGENTMUX_DATABASE_URL=%s\n' "$database_url"`
	case "darwin":
		databaseURL = remoteDarwinPostgresURL
		command = `set -eu
"$HOME/.agentmux/bin/amux" --database-url ` + shellQuote(databaseURL) + ` database setup`
	default:
		return "", fmt.Errorf("unsupported remote platform %q", remoteOS)
	}
	output, err := runRemoteCommand(ctx, client, command)
	if err != nil {
		return "", fmt.Errorf("prepare remote PostgreSQL: %w: %s", err, strings.TrimSpace(output))
	}
	if remoteOS == "linux" {
		const prefix = "AGENTMUX_DATABASE_URL="
		for _, line := range strings.Split(output, "\n") {
			line = strings.TrimSpace(line)
			if value := strings.TrimSpace(strings.TrimPrefix(line, prefix)); strings.HasPrefix(line, prefix) && strings.HasPrefix(value, "postgresql:///") {
				databaseURL = value
			}
		}
		if databaseURL == "" {
			return "", fmt.Errorf("prepare remote PostgreSQL: missing database URL in output %q", output)
		}
	}
	return databaseURL, nil
}

func migrateRemoteSQLite(ctx context.Context, client remoteClient, source, databaseURL string) error {
	if strings.TrimSpace(source) == "" {
		return nil
	}
	command := `set -eu
source=` + shellQuote(source) + `
database_url=` + shellQuote(databaseURL) + `
marker="$HOME/.agentmux/postgres/sqlite-migration-complete"
if [ ! -f "$source" ] || [ -f "$marker" ]; then
  exit 0
fi
mkdir -p "$HOME/.agentmux/postgres" "$HOME/.agentmux/backups"
backup="$HOME/.agentmux/backups/agentmux-pre-postgres-$(date +%Y%m%d-%H%M%S).db"
"$HOME/.agentmux/bin/amux" --database-url "$database_url" database migrate-sqlite \
  --source "$source" --backup "$backup" --observations-since 30d --resume \
  > "$HOME/.agentmux/postgres/sqlite-migration.json"
{
  printf 'source=%s\n' "$source"
  printf 'backup=%s\n' "$backup"
  printf 'completed_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
} > "$marker"`
	if output, err := runRemoteCommand(ctx, client, command); err != nil {
		return fmt.Errorf("migrate remote SQLite to PostgreSQL: %w: %s", err, strings.TrimSpace(output))
	}
	return nil
}

func startRemoteServiceAt(ctx context.Context, client remoteClient, remoteOS, addr, databaseURL string) error {
	serviceDatabaseURL := systemdQuoteArg(databaseURL)
	shellDatabaseURL := shellQuote(databaseURL)
	var command string
	switch remoteOS {
	case "linux":
		command = `set -eu
mkdir -p "$HOME/.config/systemd/user" "$HOME/.agentmux"
cat > "$HOME/.config/systemd/user/agentmux.service" <<'AGENTMUX_UNIT'
[Unit]
Description=AgentMux
After=network-online.target postgresql.service

[Service]
ExecStart=%h/.agentmux/bin/amux --database-url ` + serviceDatabaseURL + ` client --addr ` + systemdQuoteArg(addr) + ` --web
Restart=on-failure
RestartSec=2

[Install]
WantedBy=default.target
AGENTMUX_UNIT
if ! command -v systemctl >/dev/null 2>&1 ||
   ! systemctl --user daemon-reload >/dev/null 2>&1 ||
   ! systemctl --user enable agentmux.service >/dev/null 2>&1 ||
   ! systemctl --user restart agentmux.service >/dev/null 2>&1; then
  nohup "$HOME/.agentmux/bin/amux" --database-url ` + shellDatabaseURL + ` client --addr ` + shellQuote(addr) + ` --web > "$HOME/.agentmux/agentmux.log" 2>&1 < /dev/null &
fi`
	case "darwin":
		launchCommand := `exec "$HOME/.agentmux/bin/amux" --database-url ` + shellDatabaseURL + ` client --addr ` + shellQuote(addr) + ` --web >> "$HOME/.agentmux/agentmux.log" 2>&1`
		command = `set -eu
mkdir -p "$HOME/Library/LaunchAgents" "$HOME/.agentmux"
cat > "$HOME/Library/LaunchAgents/com.agentmux.client.plist" <<'AGENTMUX_PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>com.agentmux.client</string>
<key>ProgramArguments</key><array><string>/bin/sh</string><string>-lc</string><string>` + html.EscapeString(launchCommand) + `</string></array>
<key>RunAtLoad</key><true/><key>KeepAlive</key><true/>
</dict></plist>
AGENTMUX_PLIST
uid=$(id -u)
launchctl bootout "gui/$uid/com.agentmux.client" >/dev/null 2>&1 || true
if ! launchctl bootstrap "gui/$uid" "$HOME/Library/LaunchAgents/com.agentmux.client.plist" >/dev/null 2>&1; then
  nohup "$HOME/.agentmux/bin/amux" --database-url ` + shellDatabaseURL + ` client --addr ` + shellQuote(addr) + ` --web > "$HOME/.agentmux/agentmux.log" 2>&1 < /dev/null &
fi`
	default:
		return fmt.Errorf("unsupported remote platform %q", remoteOS)
	}
	if output, err := runRemoteCommand(ctx, client, command); err != nil {
		return fmt.Errorf("start user service: %w: %s", err, strings.TrimSpace(output))
	}
	return nil
}

func systemdQuoteArg(value string) string {
	return strconv.Quote(value)
}

func remoteAgentMuxLog(ctx context.Context, client remoteClient) (string, error) {
	return runRemoteCommand(ctx, client,
		`tail -n 8 "$HOME/.agentmux/bootstrap.log" 2>/dev/null || true
tail -n 12 "$HOME/.agentmux/agentmux.log" 2>/dev/null || true
journalctl --user -u agentmux.service -n 12 --no-pager 2>/dev/null || true`)
}

func runRemoteCommand(ctx context.Context, client remoteClient, command string) (string, error) {
	output, err := client.Run(ctx, command, nil)
	return strings.TrimSpace(string(output)), err
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
