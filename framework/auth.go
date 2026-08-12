package framework

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// AuthState describes whether a locally installed framework can use its own
// credentials. Provider-backed Agents do not depend on this state.
type AuthState string

const (
	AuthStateAuthenticated   AuthState = "authenticated"
	AuthStateUnauthenticated AuthState = "unauthenticated"
	AuthStateUnknown         AuthState = "unknown"
)

// AuthStatus is the configuration-time view of a framework's local login.
// Detail is deliberately generic so account identifiers and command output do
// not leak through the HTTP API.
type AuthStatus struct {
	Kind           string    `json:"kind"`
	State          AuthState `json:"state"`
	Installed      bool      `json:"installed"`
	LoginSupported bool      `json:"login_supported"`
	Detail         string    `json:"detail,omitempty"`
}

// LoginResult contains the actionable part of a CLI login flow. The command
// remains alive in the daemon while the user completes browser authorization.
type LoginResult struct {
	Kind             string `json:"kind"`
	SessionID        string `json:"session_id"`
	LoginURL         string `json:"login_url"`
	VerificationCode string `json:"verification_code,omitempty"`
	InputRequired    bool   `json:"input_required,omitempty"`
}

type frameworkAuthConfig struct {
	statusArgs    []string
	loginArgs     []string
	loginEnv      map[string]string
	inputRequired bool
	extraEnvKeys  []string
}

var frameworkAuthConfigs = map[string]frameworkAuthConfig{
	"claudecode": {
		statusArgs: []string{"auth", "status", "--json"}, loginArgs: []string{"auth", "login"},
		loginEnv: map[string]string{"BROWSER": "false"}, inputRequired: true,
	},
	"codex": {
		statusArgs: []string{"login", "status"}, loginArgs: []string{"login", "--device-auth"},
		extraEnvKeys: []string{"CODEX_API_KEY"},
	},
	"cursor": {
		statusArgs: []string{"status"}, loginArgs: []string{"login"},
		loginEnv: map[string]string{"NO_OPEN_BROWSER": "1"}, extraEnvKeys: []string{"CURSOR_API_KEY"},
	},
}

const (
	frameworkAuthCheckTimeout = 5 * time.Second
	frameworkLoginTimeout     = 15 * time.Minute
	frameworkLoginLinkTimeout = 15 * time.Second
)

var (
	frameworkANSISequence = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	frameworkLoginURL     = regexp.MustCompile(`https?://[^\s\x00-\x1f<>"']+`)
	frameworkLoginCode    = regexp.MustCompile(
		`\b[A-Z0-9]{4}[- ][A-Z0-9]{4}\b`,
	)
	frameworkLoginSessions sync.Map // map[string]*frameworkLoginSession
)

// CheckAuth performs a bounded, read-only credential check for a framework.
// Unknown is distinct from unauthenticated: callers should offer Provider
// configuration without claiming a login is missing when the CLI has no
// reliable status command.
func CheckAuth(ctx context.Context, kind string) AuthStatus {
	kind = strings.TrimSpace(kind)
	status := AuthStatus{Kind: kind, State: AuthStateUnknown}
	spec, ok := Lookup(kind)
	if !ok || !spec.Supported {
		status.Detail = "unknown framework"
		return status
	}
	status.Installed = IsInstalled(kind)
	config, supported := frameworkAuthConfigs[kind]
	status.LoginSupported = supported && len(config.loginArgs) > 0
	if !status.Installed {
		status.Detail = "framework is not installed"
		return status
	}
	if frameworkCredentialEnvironmentAvailable(spec, config) {
		status.State = AuthStateAuthenticated
		status.Detail = "credential environment is configured"
		return status
	}
	if len(config.statusArgs) == 0 {
		if spec.KindType == KindSDK && len(spec.EnvRequired) > 0 {
			status.State = AuthStateUnauthenticated
			status.Detail = "framework requires Provider credentials"
		} else {
			status.Detail = "framework does not expose a login status command"
		}
		return status
	}

	checkCtx, cancel := context.WithTimeout(ctx, frameworkAuthCheckTimeout)
	defer cancel()
	binary, err := resolveCLIExecutable(spec.Bin)
	if err != nil {
		status.Installed = false
		status.Detail = "framework executable is unavailable"
		return status
	}
	output, commandErr := frameworkCommandContext(checkCtx, binary, config.statusArgs...).CombinedOutput()
	status.State = classifyFrameworkAuth(kind, output, commandErr)
	switch status.State {
	case AuthStateAuthenticated:
		status.Detail = "CLI reports a local login"
	case AuthStateUnauthenticated:
		status.Detail = "CLI reports that login is required"
	default:
		status.Detail = "could not determine CLI login state"
	}
	return status
}

func frameworkCredentialEnvironmentAvailable(spec Spec, config frameworkAuthConfig) bool {
	keys := append([]string(nil), spec.EnvRequired...)
	keys = append(keys, config.extraEnvKeys...)
	for _, key := range keys {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return true
		}
	}
	return false
}

func classifyFrameworkAuth(kind string, output []byte, commandErr error) AuthState {
	if kind == "claudecode" {
		var payload struct {
			LoggedIn bool `json:"loggedIn"`
		}
		if json.Unmarshal(output, &payload) == nil {
			if payload.LoggedIn {
				return AuthStateAuthenticated
			}
			return AuthStateUnauthenticated
		}
	}
	text := strings.ToLower(stripTerminalControls(string(output)))
	for _, marker := range []string{
		"not logged in", "not authenticated", "authentication required", "login required", "log in required",
	} {
		if strings.Contains(text, marker) {
			return AuthStateUnauthenticated
		}
	}
	for _, marker := range []string{"logged in", "login successful", "authenticated"} {
		if strings.Contains(text, marker) {
			return AuthStateAuthenticated
		}
	}
	if commandErr != nil {
		return AuthStateUnknown
	}
	return AuthStateUnknown
}

type frameworkLoginSession struct {
	kind          string
	inputRequired bool
	stdin         io.WriteCloser
	cancel        context.CancelFunc
	done          chan error
	updates       chan loginOutputUpdate
}

type loginOutputUpdate struct {
	url  string
	code string
}

// StartLogin starts a framework-owned browser/device login and waits only for
// the actionable URL. The underlying process continues polling in the daemon,
// which is required for device authorization to finish after this call returns.
func StartLogin(kind string) (LoginResult, error) {
	kind = strings.TrimSpace(kind)
	spec, ok := Lookup(kind)
	if !ok || !spec.Supported {
		return LoginResult{}, fmt.Errorf("unknown framework %q", kind)
	}
	config, ok := frameworkAuthConfigs[kind]
	if !ok || len(config.loginArgs) == 0 {
		return LoginResult{}, fmt.Errorf("framework %q does not support in-app login", kind)
	}
	binary, err := resolveCLIExecutable(spec.Bin)
	if err != nil {
		return LoginResult{}, fmt.Errorf("framework %q is not installed", kind)
	}

	loginCtx, cancel := context.WithTimeout(context.Background(), frameworkLoginTimeout)
	cmd := frameworkCommandContext(loginCtx, binary, config.loginArgs...)
	cmd.Env = frameworkLoginEnvironment(config.loginEnv)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return LoginResult{}, fmt.Errorf("prepare %s login input: %w", kind, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		_ = stdin.Close()
		return LoginResult{}, fmt.Errorf("prepare %s login output: %w", kind, err)
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		cancel()
		_ = stdin.Close()
		_ = stdout.Close()
		return LoginResult{}, fmt.Errorf("start %s login: %w", kind, err)
	}

	sessionID := frameworkLoginSessionID()
	session := &frameworkLoginSession{
		kind: kind, inputRequired: config.inputRequired, stdin: stdin, cancel: cancel,
		done: make(chan error, 1), updates: make(chan loginOutputUpdate, 8),
	}
	frameworkLoginSessions.Store(sessionID, session)
	go collectFrameworkLoginOutput(sessionID, session, cmd, stdout)

	result := LoginResult{Kind: kind, SessionID: sessionID, InputRequired: config.inputRequired}
	timeout := time.NewTimer(frameworkLoginLinkTimeout)
	defer timeout.Stop()
	var settle *time.Timer
	var settleC <-chan time.Time
	for {
		select {
		case update := <-session.updates:
			if update.url != "" {
				result.LoginURL = update.url
			}
			if update.code != "" {
				result.VerificationCode = update.code
			}
			if result.LoginURL != "" {
				if settle == nil {
					settle = time.NewTimer(200 * time.Millisecond)
				} else {
					if !settle.Stop() {
						select {
						case <-settle.C:
						default:
						}
					}
					settle.Reset(200 * time.Millisecond)
				}
				settleC = settle.C
			}
		case <-settleC:
			return result, nil
		case waitErr := <-session.done:
			// A short-lived login command can exit immediately after printing its
			// URL and verification code on separate lines. In that case both the
			// completion signal and one or more buffered updates may be ready, and
			// select is allowed to choose completion first. Drain the buffered
			// output before returning so callers never observe a partial result.
		drainUpdates:
			for {
				select {
				case update := <-session.updates:
					if update.url != "" {
						result.LoginURL = update.url
					}
					if update.code != "" {
						result.VerificationCode = update.code
					}
				default:
					break drainUpdates
				}
			}
			if result.LoginURL != "" {
				return result, nil
			}
			if waitErr == nil {
				waitErr = errors.New("login command exited before returning a URL")
			}
			return LoginResult{}, fmt.Errorf("start %s login: %w", kind, waitErr)
		case <-timeout.C:
			session.cancel()
			return LoginResult{}, fmt.Errorf("%s login did not return a URL", kind)
		}
	}
}

func collectFrameworkLoginOutput(sessionID string, session *frameworkLoginSession, cmd interface{ Wait() error }, output io.ReadCloser) {
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 0, 4096), 256*1024)
	for scanner.Scan() {
		line := scanner.Text()
		update := loginOutputUpdate{url: actionableLoginURL(line), code: actionableLoginCode(line)}
		if update.url == "" && update.code == "" {
			continue
		}
		select {
		case session.updates <- update:
		default:
		}
	}
	waitErr := cmd.Wait()
	if scanErr := scanner.Err(); scanErr != nil && waitErr == nil {
		waitErr = scanErr
	}
	_ = session.stdin.Close()
	session.cancel()
	frameworkLoginSessions.Delete(sessionID)
	session.done <- waitErr
}

// CompleteLogin supplies the browser-returned code for frameworks whose CLI
// requires it to be pasted back into the waiting process (currently Claude).
func CompleteLogin(sessionID, code string) error {
	sessionID = strings.TrimSpace(sessionID)
	code = strings.TrimSpace(code)
	if sessionID == "" || code == "" {
		return errors.New("login session and code are required")
	}
	if len(code) > 8192 {
		return errors.New("login code is too long")
	}
	raw, ok := frameworkLoginSessions.Load(sessionID)
	if !ok {
		return errors.New("login session is no longer active")
	}
	session := raw.(*frameworkLoginSession)
	if !session.inputRequired {
		return fmt.Errorf("framework %q does not accept a pasted login code", session.kind)
	}
	if _, err := io.WriteString(session.stdin, code+"\n"); err != nil {
		return fmt.Errorf("submit %s login code: %w", session.kind, err)
	}
	return nil
}

func frameworkLoginEnvironment(overrides map[string]string) []string {
	if len(overrides) == 0 {
		return os.Environ()
	}
	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if ok {
			if _, replaced := overrides[key]; replaced {
				continue
			}
		}
		env = append(env, entry)
	}
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	return env
}

func actionableLoginURL(line string) string {
	match := frameworkLoginURL.FindString(line)
	return strings.TrimRight(match, ".,;:!?)]}")
}

func actionableLoginCode(line string) string {
	return strings.ReplaceAll(frameworkLoginCode.FindString(stripTerminalControls(line)), " ", "-")
}

func stripTerminalControls(value string) string {
	value = frameworkANSISequence.ReplaceAllString(value, "")
	var b strings.Builder
	for _, r := range value {
		if r == '\n' || r == '\t' || r >= 0x20 && r != 0x7f {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func frameworkLoginSessionID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err == nil {
		return hex.EncodeToString(b)
	}
	return fmt.Sprintf("%d", time.Now().UnixNano())
}
