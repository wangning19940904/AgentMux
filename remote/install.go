package remote

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// installRemoteAgentMux installs a bundled AgentMux CLI into the remote
// user's home directory and registers a persistent user service when the
// platform supports one. It intentionally never requires root privileges.
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

	if err := startRemoteService(ctx, client, remoteOS, host.RemoteAddr); err != nil {
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
		candidates = append(candidates, filepath.Join(workingDir, "dist", name))
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
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open local AgentMux binary: %w", err)
	}
	defer file.Close()

	output, err := client.Run(ctx, `set -eu
umask 077
mkdir -p "$HOME/.agentmux/bin"
tmp="$HOME/.agentmux/bin/.amux-upload-$$"
trap 'rm -f "$tmp"' EXIT
cat > "$tmp"
chmod 0755 "$tmp"
mv "$tmp" "$HOME/.agentmux/bin/amux"
	trap - EXIT`, file)
	if err != nil {
		return fmt.Errorf("upload AgentMux binary: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func startRemoteService(ctx context.Context, client remoteClient, remoteOS, addr string) error {
	var command string
	switch remoteOS {
	case "linux":
		command = `set -eu
mkdir -p "$HOME/.config/systemd/user" "$HOME/.agentmux"
cat > "$HOME/.config/systemd/user/agentmux.service" <<'AGENTMUX_UNIT'
[Unit]
Description=AgentMux
After=network-online.target

[Service]
ExecStart=%h/.agentmux/bin/amux client --addr ` + addr + ` --sqlite-path %h/.agentmux/agentmux.db --web
Restart=on-failure
RestartSec=2

[Install]
WantedBy=default.target
AGENTMUX_UNIT
if ! command -v systemctl >/dev/null 2>&1 ||
   ! systemctl --user daemon-reload >/dev/null 2>&1 ||
   ! systemctl --user enable agentmux.service >/dev/null 2>&1 ||
   ! systemctl --user restart agentmux.service >/dev/null 2>&1; then
  nohup "$HOME/.agentmux/bin/amux" client --addr ` + shellQuote(addr) + ` --sqlite-path "$HOME/.agentmux/agentmux.db" --web > "$HOME/.agentmux/agentmux.log" 2>&1 < /dev/null &
fi`
	case "darwin":
		command = `set -eu
mkdir -p "$HOME/Library/LaunchAgents" "$HOME/.agentmux"
cat > "$HOME/Library/LaunchAgents/com.agentmux.client.plist" <<'AGENTMUX_PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>com.agentmux.client</string>
<key>ProgramArguments</key><array><string>/bin/sh</string><string>-lc</string><string>exec "$HOME/.agentmux/bin/amux" client --addr ` + addr + ` --sqlite-path "$HOME/.agentmux/agentmux.db" --web &gt;&gt; "$HOME/.agentmux/agentmux.log" 2&gt;&amp;1</string></array>
<key>RunAtLoad</key><true/><key>KeepAlive</key><true/>
</dict></plist>
AGENTMUX_PLIST
uid=$(id -u)
launchctl bootout "gui/$uid/com.agentmux.client" >/dev/null 2>&1 || true
if ! launchctl bootstrap "gui/$uid" "$HOME/Library/LaunchAgents/com.agentmux.client.plist" >/dev/null 2>&1; then
  nohup "$HOME/.agentmux/bin/amux" client --addr ` + shellQuote(addr) + ` --sqlite-path "$HOME/.agentmux/agentmux.db" --web > "$HOME/.agentmux/agentmux.log" 2>&1 < /dev/null &
fi`
	default:
		return fmt.Errorf("unsupported remote platform %q", remoteOS)
	}
	if output, err := runRemoteCommand(ctx, client, command); err != nil {
		return fmt.Errorf("start user service: %w: %s", err, strings.TrimSpace(output))
	}
	return nil
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
