package framework

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/internal/authflow"
	"github.com/wangning19940904/AgentMux/internal/traeauth"
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
	Kind                 string    `json:"kind"`
	State                AuthState `json:"state"`
	Installed            bool      `json:"installed"`
	LoginSupported       bool      `json:"login_supported"`
	LogoutSupported      bool      `json:"logout_supported"`
	Detail               string    `json:"detail,omitempty"`
	AutoRefreshSupported bool      `json:"auto_refresh_supported,omitempty"`
	ExpiresAt            string    `json:"expires_at,omitempty"`
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

// LoginSessionStatus is the prompt-safe lifecycle view used by the Console
// while the framework-owned CLI waits for browser/device authorization.
type LoginSessionStatus struct {
	SessionID string `json:"session_id"`
	Active    bool   `json:"active"`
	State     string `json:"state"`
	Error     string `json:"error,omitempty"`
}

type frameworkAuthConfig struct {
	statusArgs    []string
	loginArgs     []string
	logoutArgs    []string
	loginEnv      map[string]string
	inputRequired bool
	extraEnvKeys  []string
}

var frameworkAuthConfigs = map[string]frameworkAuthConfig{
	"claudecode": {
		statusArgs: []string{"auth", "status", "--json"}, loginArgs: []string{"auth", "login"}, logoutArgs: []string{"auth", "logout"},
		loginEnv: map[string]string{"BROWSER": "false"}, inputRequired: true,
	},
	"codex": {
		statusArgs: []string{"login", "status"}, loginArgs: []string{"login", "--device-auth"}, logoutArgs: []string{"logout"},
		extraEnvKeys: []string{"CODEX_API_KEY"},
	},
	"cursor": {
		statusArgs: []string{"status"}, loginArgs: []string{"login"}, logoutArgs: []string{"logout"},
		loginEnv: map[string]string{"NO_OPEN_BROWSER": "1"}, extraEnvKeys: []string{"CURSOR_API_KEY"},
	},
	"traecli": {
		statusArgs: []string{"login", "status"}, loginArgs: []string{"login", "--sso-device"}, logoutArgs: []string{"logout"},
	},
}

const (
	frameworkAuthCheckTimeout = 5 * time.Second
	frameworkLogoutTimeout    = 15 * time.Second
	frameworkLoginTimeout     = 15 * time.Minute
	frameworkLoginLinkTimeout = 15 * time.Second
	frameworkLoginSessionTTL  = 10 * time.Minute
)

var frameworkLoginSessions = authflow.NewRegistry(frameworkLoginSessionTTL)

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
	status.LogoutSupported = supported && len(config.logoutArgs) > 0
	if !status.Installed {
		status.Detail = "framework is not installed"
		return status
	}
	if frameworkCredentialEnvironmentAvailable(spec, config) {
		status.State = AuthStateAuthenticated
		status.LogoutSupported = false
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
	if kind == "traecli" {
		if meta, err := traeauth.ReadMetadata(nil); err == nil && meta.Managed && !meta.ExpiresAt.IsZero() {
			status.AutoRefreshSupported = true
			status.ExpiresAt = meta.ExpiresAt.UTC().Format(time.RFC3339)
			if !meta.ExpiresAt.After(time.Now()) || traeauth.NeedsLogin(nil) {
				status.State = AuthStateUnauthenticated
				status.Detail = "TRAE login is no longer valid; automatic renewal or a new login is required"
				return status
			}
		}
	}
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

// Logout removes credentials owned by the framework CLI. Environment-backed
// credentials are intentionally left alone because the daemon cannot safely
// mutate its parent process environment.
func Logout(ctx context.Context, kind string) (AuthStatus, error) {
	kind = strings.TrimSpace(kind)
	spec, ok := Lookup(kind)
	if !ok || !spec.Supported {
		return AuthStatus{}, fmt.Errorf("unknown framework %q", kind)
	}
	config, ok := frameworkAuthConfigs[kind]
	if !ok || len(config.logoutArgs) == 0 {
		return AuthStatus{}, fmt.Errorf("framework %q does not support in-app logout", kind)
	}
	if frameworkCredentialEnvironmentAvailable(spec, config) {
		return CheckAuth(ctx, kind), fmt.Errorf("framework credentials are provided by the environment and cannot be removed here")
	}
	binary, err := resolveCLIExecutable(spec.Bin)
	if err != nil {
		return AuthStatus{}, fmt.Errorf("framework %q is not installed", kind)
	}
	logoutCtx, cancel := context.WithTimeout(ctx, frameworkLogoutTimeout)
	defer cancel()
	if err := frameworkCommandContext(logoutCtx, binary, config.logoutArgs...).Run(); err != nil {
		return CheckAuth(ctx, kind), fmt.Errorf("framework %q logout failed: %w", kind, err)
	}
	return CheckAuth(ctx, kind), nil
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
	text := strings.ToLower(authflow.StripControls(string(output)))
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
	session, created := frameworkLoginSessions.Create(kind, config.inputRequired, cancel)
	if !created {
		return waitForFrameworkLoginLink(kind, session, nil)
	}
	cmd := frameworkCommandContext(loginCtx, binary, config.loginArgs...)
	cmd.Env = authflow.MergeEnvironment(os.Environ(), config.loginEnv)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		session.Finish(authflow.StateFailed, "framework login could not prepare input")
		frameworkLoginSessions.Release(session)
		return LoginResult{}, fmt.Errorf("prepare %s login input: %w", kind, err)
	}
	if err := session.AttachInput(stdin); err != nil {
		session.Finish(authflow.StateFailed, "framework login could not prepare input")
		frameworkLoginSessions.Release(session)
		_ = stdin.Close()
		return LoginResult{}, fmt.Errorf("prepare %s login input: %w", kind, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		session.Finish(authflow.StateFailed, "framework login could not prepare output")
		frameworkLoginSessions.Release(session)
		_ = session.CloseInput()
		return LoginResult{}, fmt.Errorf("prepare %s login output: %w", kind, err)
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		session.Finish(authflow.StateFailed, "framework login could not start")
		frameworkLoginSessions.Release(session)
		_ = session.CloseInput()
		_ = stdout.Close()
		return LoginResult{}, fmt.Errorf("start %s login: %w", kind, err)
	}

	processDone := make(chan error, 1)
	go collectFrameworkLoginOutput(loginCtx, session, cmd, stdout, processDone)
	return waitForFrameworkLoginLink(kind, session, processDone)
}

func waitForFrameworkLoginLink(kind string, session *authflow.Session, processDone <-chan error) (LoginResult, error) {
	snapshot := session.Snapshot()
	result := LoginResult{
		Kind: kind, SessionID: snapshot.SessionID, LoginURL: snapshot.LoginURL,
		VerificationCode: snapshot.VerificationCode, InputRequired: snapshot.InputRequired,
	}
	if result.LoginURL != "" {
		return result, nil
	}
	timeout := time.NewTimer(frameworkLoginLinkTimeout)
	defer timeout.Stop()
	var settle *time.Timer
	var settleC <-chan time.Time
	for {
		select {
		case <-session.Updates():
			snapshot = session.Snapshot()
			result.LoginURL = snapshot.LoginURL
			result.VerificationCode = snapshot.VerificationCode
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
		case waitErr := <-processDone:
			snapshot = session.Snapshot()
			result.LoginURL = snapshot.LoginURL
			result.VerificationCode = snapshot.VerificationCode
			if result.LoginURL != "" {
				return result, nil
			}
			if waitErr == nil {
				waitErr = errors.New("login command exited before returning a URL")
			}
			return LoginResult{}, fmt.Errorf("start %s login: %w", kind, waitErr)
		case <-session.Done():
			snapshot = session.Snapshot()
			result.LoginURL = snapshot.LoginURL
			result.VerificationCode = snapshot.VerificationCode
			if result.LoginURL != "" {
				return result, nil
			}
			message := snapshot.Error
			if message == "" {
				message = "login command exited before returning a URL"
			}
			return LoginResult{}, fmt.Errorf("start %s login: %s", kind, message)
		case <-timeout.C:
			session.Finish(authflow.StateFailed, kind+" login did not return an authorization link")
			_ = session.CloseInput()
			return LoginResult{}, fmt.Errorf("%s login did not return a URL", kind)
		}
	}
}

func collectFrameworkLoginOutput(loginCtx context.Context, session *authflow.Session, cmd interface{ Wait() error }, output io.ReadCloser, processDone chan<- error) {
	defer frameworkLoginSessions.Release(session)
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 0, 4096), 256*1024)
	for scanner.Scan() {
		line := scanner.Text()
		session.Actionable(authflow.ActionableURL(line), authflow.ActionableCode(line))
	}
	waitErr := cmd.Wait()
	if scanErr := scanner.Err(); scanErr != nil && waitErr == nil {
		waitErr = scanErr
	}
	_ = session.CloseInput()
	state := session.Snapshot().State
	switch {
	case state.Terminal():
	case errors.Is(loginCtx.Err(), context.DeadlineExceeded):
		session.Finish(authflow.StateFailed, "framework login timed out")
	case waitErr != nil:
		session.Finish(authflow.StateFailed, "framework login failed")
	case CheckAuth(context.Background(), session.Snapshot().Subject).State == AuthStateAuthenticated:
		session.Finish(authflow.StateSucceeded, "")
	default:
		session.Finish(authflow.StateFailed, "login command ended before authentication was confirmed")
	}
	processDone <- waitErr
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
	session, ok := frameworkLoginSessions.Get(sessionID)
	if !ok {
		return errors.New("login session is no longer active")
	}
	if err := session.WriteInput(code, 8192); err != nil {
		return fmt.Errorf("submit %s login code: %w", session.Snapshot().Subject, err)
	}
	return nil
}

// GetLoginSession reports whether the daemon still owns the login subprocess.
// Authentication readiness remains authoritative through CheckAuth; this
// lifecycle bit lets the UI distinguish "still waiting" from "ended without
// authenticating" without exposing CLI output or account identifiers.
func GetLoginSession(sessionID string) LoginSessionStatus {
	sessionID = strings.TrimSpace(sessionID)
	session, ok := frameworkLoginSessions.Get(sessionID)
	if !ok {
		return LoginSessionStatus{SessionID: sessionID, Active: false, State: "unknown"}
	}
	snapshot := session.Snapshot()
	return LoginSessionStatus{
		SessionID: sessionID,
		Active:    snapshot.State.Active(),
		State:     string(snapshot.State),
		Error:     snapshot.Error,
	}
}

// CancelLogin stops an active framework login subprocess. Recently completed
// sessions are retained briefly so polling clients can observe the outcome.
func CancelLogin(sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	session, ok := frameworkLoginSessions.Get(sessionID)
	if !ok {
		return errors.New("framework login session was not found")
	}
	session.Cancel("")
	_ = session.CloseInput()
	return nil
}
