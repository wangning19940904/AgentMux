package remote

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Ask OpenSSH for the effective proxy rather than duplicating Include, Match,
// wildcard and ProxyJump/ProxyCommand precedence rules. Only local SSH config
// supplies executable commands; the API persists an alias, never a command.
func sshProxyCommand(ctx context.Context, host Host, timeout time.Duration) (*exec.Cmd, error) {
	if host.SSHAlias == "" {
		return nil, nil
	}
	executable, err := exec.LookPath("ssh")
	if err != nil {
		return nil, fmt.Errorf("resolve SSH Config: system OpenSSH is required: %w", err)
	}
	cmd := exec.CommandContext(ctx, executable, "-G", "-p", strconv.Itoa(host.Port), "-l", host.User, "--", host.SSHAlias)
	cmd.WaitDelay = time.Second
	var stderr cappedBuffer
	stderr.limit = 16 << 10
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return nil, sshAttemptError(ctx, fmt.Errorf("resolve SSH Config for %s: %w: %s", host.SSHAlias, err, strings.TrimSpace(stderr.String())))
	}
	options := make(map[string]string)
	for _, line := range strings.Split(string(output), "\n") {
		key, value, ok := strings.Cut(line, " ")
		if ok {
			options[key] = strings.TrimSpace(value)
		}
	}
	proxyJump, proxyCommand := options["proxyjump"], options["proxycommand"]
	if strings.EqualFold(proxyJump, "none") {
		proxyJump = ""
	}
	if strings.EqualFold(proxyCommand, "none") {
		proxyCommand = ""
	}
	if proxyJump == "" && proxyCommand == "" {
		return nil, nil
	}
	resolved := host
	if options["hostname"] != "" {
		resolved.Host = options["hostname"]
	}
	if proxyJump != "" {
		jump, err := expandSSHProxyTokens(proxyJump, resolved)
		if err != nil {
			return nil, err
		}
		return sshJumpCommand(executable, jump, resolved, timeout)
	}
	if options["proxyusefdpass"] == "yes" {
		return nil, fmt.Errorf("SSH ProxyUseFdpass is unsupported; use a ProxyCommand that forwards stdin/stdout")
	}
	command, err := expandSSHProxyTokens(proxyCommand, resolved)
	if err != nil {
		return nil, err
	}
	shell, err := exec.LookPath("sh")
	if err != nil {
		return nil, fmt.Errorf("SSH ProxyCommand requires a POSIX shell: %w", err)
	}
	// Like OpenSSH, execute the locally configured command through a shell.
	// exec makes the proxy itself own the pipes and process lifetime.
	return exec.Command(shell, "-c", "exec "+command), nil
}

func expandSSHProxyTokens(command string, host Host) (string, error) {
	tokens := map[byte]string{
		'%': "%", 'h': host.Host, 'n': host.SSHAlias,
		'p': strconv.Itoa(host.Port), 'r': host.User,
	}
	var out strings.Builder
	for i := 0; i < len(command); i++ {
		if command[i] != '%' {
			out.WriteByte(command[i])
			continue
		}
		i++
		if i == len(command) {
			return "", fmt.Errorf("SSH proxy command has an incomplete %% token")
		}
		value, ok := tokens[command[i]]
		if !ok {
			return "", fmt.Errorf("SSH proxy command has unsupported token %%%c", command[i])
		}
		// Tokens are expanded without quoting by OpenSSH, including inside
		// existing quotes. Reject shell syntax in API-supplied token values
		// instead of allowing a hostname or username to become local code.
		if command[i] != '%' {
			for _, char := range value {
				if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("._-:@%+[]=,", char)) {
					return "", fmt.Errorf("SSH proxy token %%%c contains unsafe characters", command[i])
				}
			}
		}
		out.WriteString(value)
	}
	return out.String(), nil
}

func sshJumpCommand(executable, jump string, host Host, timeout time.Duration) (*exec.Cmd, error) {
	// Connect to the last hop; OpenSSH handles the preceding hops and each
	// hop's own SSH config, credentials and known_hosts checks.
	hops := strings.Split(jump, ",")
	last := hops[len(hops)-1]
	if !strings.HasPrefix(last, "ssh://") {
		last = "ssh://" + last
	}
	u, err := url.Parse(last)
	if err != nil || u.Scheme != "ssh" || u.Hostname() == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("invalid SSH ProxyJump destination")
	}
	seconds := max(1, int(timeout.Round(time.Second)/time.Second))
	args := []string{
		"-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
		"-o", "ConnectTimeout=" + strconv.Itoa(seconds),
		"-o", "ControlMaster=no", "-o", "ControlPath=none",
		"-o", "ClearAllForwardings=yes", "-o", "ForwardAgent=no",
		"-o", "PermitLocalCommand=no", "-o", "RequestTTY=no",
		"-W", net.JoinHostPort(host.Host, strconv.Itoa(host.Port)),
	}
	if u.User != nil {
		if _, set := u.User.Password(); set {
			return nil, fmt.Errorf("passwords in SSH ProxyJump destinations are unsupported")
		}
		args = append(args, "-l", u.User.Username())
	}
	if port := u.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return nil, fmt.Errorf("invalid SSH ProxyJump port")
		}
		args = append(args, "-p", port)
	}
	if len(hops) > 1 {
		args = append(args, "-J", strings.Join(hops[:len(hops)-1], ","))
	}
	args = append(args, "--", u.Hostname())
	return exec.Command(executable, args...), nil
}

// This is a raw SSH transport, not an authenticated connection to the target.
// The normal Go handshake still verifies the target key before authentication.
func startSSHProxy(cmd *exec.Cmd, address string) (net.Conn, error) {
	stop := prepareSSHProxyProcess(cmd)
	cmd.WaitDelay = time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	stderr := &sshTunnelLog{cappedBuffer: cappedBuffer{limit: 16 << 10}}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("start SSH proxy: %w", err)
	}
	conn := &sshProcessConn{
		stdin: stdin, stdout: stdout, cmd: cmd, stderr: stderr,
		done: make(chan struct{}), remote: sshProcessAddr(address), stopProcess: stop,
	}
	go func() {
		conn.waitErr = cmd.Wait()
		close(conn.done)
	}()
	return conn, nil
}
