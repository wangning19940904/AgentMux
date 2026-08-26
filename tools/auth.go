package tools

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
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/wangning19940904/AgentMux/internal/procutil"
)

// CLIAuthState is the sanitized configuration/login state exposed to the
// console. setup_required is intentionally separate from unauthenticated:
// lark-cli must create or bind an application before it can authorize a user.
type CLIAuthState string

const (
	CLIAuthAuthenticated   CLIAuthState = "authenticated"
	CLIAuthUnauthenticated CLIAuthState = "unauthenticated"
	CLIAuthSetupRequired   CLIAuthState = "setup_required"
	CLIAuthUnknown         CLIAuthState = "unknown"
)

// CLIAuthStatus reports whether a managed CLI is ready to make authenticated
// calls. Detail never contains command output, tokens, or account identifiers.
type CLIAuthStatus struct {
	ID             string       `json:"id"`
	State          CLIAuthState `json:"state"`
	Installed      bool         `json:"installed"`
	LoginSupported bool         `json:"login_supported"`
	Detail         string       `json:"detail,omitempty"`
}

type CLIAuthSessionState string

const (
	CLIAuthSessionStarting  CLIAuthSessionState = "starting"
	CLIAuthSessionWaiting   CLIAuthSessionState = "waiting"
	CLIAuthSessionSucceeded CLIAuthSessionState = "succeeded"
	CLIAuthSessionFailed    CLIAuthSessionState = "failed"
	CLIAuthSessionCancelled CLIAuthSessionState = "cancelled"
)

// CLIAuthSession is a prompt-safe snapshot of a background setup/login flow.
// The daemon owns the child process while App/Web UI opens LoginURL locally.
type CLIAuthSession struct {
	ID               string              `json:"id"`
	SessionID        string              `json:"session_id"`
	Phase            string              `json:"phase"`
	State            CLIAuthSessionState `json:"state"`
	LoginURL         string              `json:"login_url,omitempty"`
	VerificationCode string              `json:"verification_code,omitempty"`
	Error            string              `json:"error,omitempty"`
	StartedAt        time.Time           `json:"started_at"`
	UpdatedAt        time.Time           `json:"updated_at"`
}

type cliAuthConfig struct {
	statusArgs []string
	setupArgs  []string
	loginArgs  []string
	loginEnv   map[string]string
	classify   func([]byte, error) CLIAuthState
}

var cliAuthConfigs = map[string]cliAuthConfig{
	"lark-cli": {
		statusArgs: []string{"auth", "status"},
		setupArgs:  []string{"config", "init", "--new", "--brand", "feishu", "--lang", "zh"},
		// Blocking mode lets the daemon keep polling while the browser is open.
		// --recommend requests the CLI-maintained recommended scope set instead
		// of silently asking for every permission available on the platform.
		loginArgs: []string{"auth", "login", "--json", "--recommend"},
		classify:  classifyLarkCLIAuth,
	},
	"github-cli": {
		statusArgs: []string{"auth", "status", "--hostname", "github.com"},
		loginArgs:  []string{"auth", "login", "--hostname", "github.com", "--git-protocol", "https", "--web", "--skip-ssh-key"},
		loginEnv: map[string]string{
			"BROWSER":            "false",
			"GH_FORCE_TTY":       "1",
			"GH_PROMPT_DISABLED": "1",
		},
		classify: classifyGitHubCLIAuth,
	},
}

const (
	cliAuthCheckTimeout    = 8 * time.Second
	cliAuthWorkflowTimeout = 20 * time.Minute
	cliAuthLinkTimeout     = 20 * time.Second
	cliAuthSessionTTL      = 10 * time.Minute
)

var (
	cliAuthANSISequence = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	cliAuthURL          = regexp.MustCompile(`https?://[^\s\x00-\x1f<>"']+`)
	cliAuthCode         = regexp.MustCompile(`\b[A-Z0-9]{4}[- ][A-Z0-9]{4}\b`)

	cliAuthSessions sync.Map // map[string]*cliAuthRuntimeSession
	cliAuthActiveMu sync.Mutex
	cliAuthActive   = map[string]string{} // cli id -> session id
)

type cliAuthRuntimeSession struct {
	mu        sync.RWMutex
	snapshot  CLIAuthSession
	cancel    context.CancelFunc
	ready     chan struct{}
	readyOnce sync.Once
}

// CheckCLIAuth performs a bounded, read-only status check for a managed CLI.
func CheckCLIAuth(ctx context.Context, id string) CLIAuthStatus {
	id = strings.TrimSpace(id)
	status := CLIAuthStatus{ID: id, State: CLIAuthUnknown}
	spec, ok := lookupCLI(id)
	if !ok {
		status.Detail = "unknown CLI"
		return status
	}
	config, supported := cliAuthConfigs[id]
	status.LoginSupported = supported && spec.LoginSupported && len(config.loginArgs) > 0
	if _, err := resolveCLIExecutable(spec.Bin); err != nil {
		status.Detail = "CLI is not installed"
		return status
	}
	status.Installed = true
	if !status.LoginSupported || len(config.statusArgs) == 0 {
		status.Detail = "CLI does not expose a supported login flow"
		return status
	}

	runCtx, cancel := context.WithTimeout(ctx, cliAuthCheckTimeout)
	defer cancel()
	cmd := cliAuthCommand(runCtx, spec, config.statusArgs, nil)
	output, commandErr := cmd.CombinedOutput()
	status.State = config.classify(output, commandErr)
	switch status.State {
	case CLIAuthAuthenticated:
		status.Detail = "CLI reports a local login"
	case CLIAuthUnauthenticated:
		status.Detail = "CLI reports that login is required"
	case CLIAuthSetupRequired:
		status.Detail = "CLI application setup is required"
	default:
		status.Detail = "could not determine CLI login state"
	}
	return status
}

// StartCLIAuth starts or returns the active login workflow for id. A setup
// phase is automatically followed by the user-login phase when required.
func StartCLIAuth(id string, force bool) (CLIAuthSession, error) {
	id = strings.TrimSpace(id)
	spec, ok := lookupCLI(id)
	if !ok {
		return CLIAuthSession{}, fmt.Errorf("unknown CLI %q", id)
	}
	config, ok := cliAuthConfigs[id]
	if !ok || !spec.LoginSupported || len(config.loginArgs) == 0 {
		return CLIAuthSession{}, fmt.Errorf("CLI %q does not support in-app login", id)
	}
	if _, err := resolveCLIExecutable(spec.Bin); err != nil {
		return CLIAuthSession{}, fmt.Errorf("CLI %q is not installed", id)
	}

	cliAuthActiveMu.Lock()
	if sessionID := cliAuthActive[id]; sessionID != "" {
		if raw, exists := cliAuthSessions.Load(sessionID); exists {
			session := raw.(*cliAuthRuntimeSession)
			cliAuthActiveMu.Unlock()
			return waitForCLIAuthLink(session)
		}
		delete(cliAuthActive, id)
	}

	now := time.Now().UTC()
	ctx, cancel := context.WithTimeout(context.Background(), cliAuthWorkflowTimeout)
	runtimeSession := &cliAuthRuntimeSession{
		snapshot: CLIAuthSession{
			ID: id, SessionID: cliAuthSessionID(), Phase: "checking", State: CLIAuthSessionStarting,
			StartedAt: now, UpdatedAt: now,
		},
		cancel: cancel,
		ready:  make(chan struct{}),
	}
	cliAuthSessions.Store(runtimeSession.snapshot.SessionID, runtimeSession)
	cliAuthActive[id] = runtimeSession.snapshot.SessionID
	cliAuthActiveMu.Unlock()

	go runCLIAuthWorkflow(ctx, runtimeSession, spec, config, force)
	return waitForCLIAuthLink(runtimeSession)
}

// GetCLIAuthSession returns a live or recently-finished workflow snapshot.
func GetCLIAuthSession(sessionID string) (CLIAuthSession, bool) {
	raw, ok := cliAuthSessions.Load(strings.TrimSpace(sessionID))
	if !ok {
		return CLIAuthSession{}, false
	}
	return raw.(*cliAuthRuntimeSession).get(), true
}

// CancelCLIAuthSession stops an active setup/login subprocess.
func CancelCLIAuthSession(sessionID string) error {
	raw, ok := cliAuthSessions.Load(strings.TrimSpace(sessionID))
	if !ok {
		return errors.New("CLI login session was not found")
	}
	session := raw.(*cliAuthRuntimeSession)
	snapshot := session.get()
	if cliAuthSessionTerminal(snapshot.State) {
		return nil
	}
	session.finish(CLIAuthSessionCancelled, "CLI login was cancelled")
	return nil
}

func waitForCLIAuthLink(session *cliAuthRuntimeSession) (CLIAuthSession, error) {
	timer := time.NewTimer(cliAuthLinkTimeout)
	defer timer.Stop()
	select {
	case <-session.ready:
		snapshot := session.get()
		if snapshot.State == CLIAuthSessionFailed {
			return snapshot, errors.New(snapshot.Error)
		}
		if snapshot.State == CLIAuthSessionCancelled {
			return snapshot, errors.New("CLI login was cancelled")
		}
		return snapshot, nil
	case <-timer.C:
		session.cancel()
		return CLIAuthSession{}, fmt.Errorf("CLI %q login did not return an authorization link", session.get().ID)
	}
}

func runCLIAuthWorkflow(ctx context.Context, session *cliAuthRuntimeSession, spec CLISpec, config cliAuthConfig, force bool) {
	defer func() {
		snapshot := session.get()
		cliAuthActiveMu.Lock()
		if cliAuthActive[snapshot.ID] == snapshot.SessionID {
			delete(cliAuthActive, snapshot.ID)
		}
		cliAuthActiveMu.Unlock()
		time.AfterFunc(cliAuthSessionTTL, func() { cliAuthSessions.Delete(snapshot.SessionID) })
	}()

	status := CheckCLIAuth(ctx, spec.ID)
	if status.State == CLIAuthAuthenticated && !force {
		session.finish(CLIAuthSessionSucceeded, "")
		return
	}
	if status.State == CLIAuthSetupRequired {
		if len(config.setupArgs) == 0 {
			session.finish(CLIAuthSessionFailed, "CLI application setup is required but no setup flow is available")
			return
		}
		if err := runCLIAuthCommand(ctx, session, spec, config, "setup", config.setupArgs); err != nil {
			finishCLIAuthCommandError(ctx, session, spec.ID, err)
			return
		}
		status = CheckCLIAuth(ctx, spec.ID)
		if status.State == CLIAuthSetupRequired {
			session.finish(CLIAuthSessionFailed, "CLI application setup did not complete")
			return
		}
		if status.State == CLIAuthAuthenticated {
			session.finish(CLIAuthSessionSucceeded, "")
			return
		}
	}

	if err := runCLIAuthCommand(ctx, session, spec, config, "login", config.loginArgs); err != nil {
		if CheckCLIAuth(context.Background(), spec.ID).State == CLIAuthAuthenticated {
			session.finish(CLIAuthSessionSucceeded, "")
			return
		}
		finishCLIAuthCommandError(ctx, session, spec.ID, err)
		return
	}
	if CheckCLIAuth(context.Background(), spec.ID).State != CLIAuthAuthenticated {
		session.finish(CLIAuthSessionFailed, "CLI login command finished but authentication is not ready")
		return
	}
	session.finish(CLIAuthSessionSucceeded, "")
}

func runCLIAuthCommand(
	ctx context.Context,
	session *cliAuthRuntimeSession,
	spec CLISpec,
	config cliAuthConfig,
	phase string,
	args []string,
) error {
	session.beginPhase(phase)
	cmd := cliAuthCommand(ctx, spec, args, config.loginEnv)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		_ = stdout.Close()
		return err
	}

	scanErr := collectCLIAuthOutput(session, stdout)
	waitErr := cmd.Wait()
	if scanErr != nil && waitErr == nil {
		waitErr = scanErr
	}
	return waitErr
}

func collectCLIAuthOutput(session *cliAuthRuntimeSession, output io.ReadCloser) error {
	defer output.Close()
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 0, 4096), 512*1024)
	for scanner.Scan() {
		line := stripCLIAuthControls(scanner.Text())
		url := actionableCLIAuthURL(line)
		code := actionableCLIAuthCode(line)
		if url != "" || code != "" {
			session.actionable(url, code)
		}
	}
	return scanner.Err()
}

func finishCLIAuthCommandError(ctx context.Context, session *cliAuthRuntimeSession, id string, err error) {
	if ctx.Err() != nil {
		state := CLIAuthSessionCancelled
		message := "CLI login was cancelled"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			state = CLIAuthSessionFailed
			message = "CLI login timed out"
		}
		session.finish(state, message)
		return
	}
	session.finish(CLIAuthSessionFailed, fmt.Sprintf("%s login failed: %v", id, err))
}

func (s *cliAuthRuntimeSession) beginPhase(phase string) {
	s.mu.Lock()
	s.snapshot.Phase = phase
	s.snapshot.State = CLIAuthSessionStarting
	s.snapshot.LoginURL = ""
	s.snapshot.VerificationCode = ""
	s.snapshot.Error = ""
	s.snapshot.UpdatedAt = time.Now().UTC()
	s.mu.Unlock()
}

func (s *cliAuthRuntimeSession) actionable(url, code string) {
	s.mu.Lock()
	if url != "" {
		s.snapshot.LoginURL = url
	}
	if code != "" {
		s.snapshot.VerificationCode = code
	}
	s.snapshot.State = CLIAuthSessionWaiting
	s.snapshot.UpdatedAt = time.Now().UTC()
	s.mu.Unlock()
	s.readyOnce.Do(func() { close(s.ready) })
}

func (s *cliAuthRuntimeSession) finish(state CLIAuthSessionState, message string) {
	s.mu.Lock()
	s.snapshot.State = state
	s.snapshot.Error = message
	s.snapshot.UpdatedAt = time.Now().UTC()
	s.mu.Unlock()
	s.readyOnce.Do(func() { close(s.ready) })
	s.cancel()
}

func (s *cliAuthRuntimeSession) get() CLIAuthSession {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshot
}

func cliAuthCommand(ctx context.Context, spec CLISpec, args []string, overrides map[string]string) *exec.Cmd {
	executable := spec.Bin
	if resolved, err := resolveCLIExecutable(spec.Bin); err == nil {
		executable = resolved
	}
	cmd := exec.CommandContext(ctx, executable, args...)
	procutil.Prepare(cmd)
	cmd.Env = cliAuthEnvironment(cliEnv(spec), overrides)
	cmd.Dir = durableCLICommandDir()
	return cmd
}

func cliAuthEnvironment(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return base
	}
	out := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		key, _, ok := strings.Cut(entry, "=")
		if ok {
			if _, replaced := overrides[key]; replaced {
				continue
			}
		}
		out = append(out, entry)
	}
	for key, value := range overrides {
		out = append(out, key+"="+value)
	}
	return out
}

func durableCLICommandDir() string {
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		if info, statErr := os.Stat(home); statErr == nil && info.IsDir() {
			return filepath.Clean(home)
		}
	}
	return os.TempDir()
}

func classifyLarkCLIAuth(output []byte, commandErr error) CLIAuthState {
	var payload struct {
		AppID      string `json:"appId"`
		Identities struct {
			Bot struct {
				Status    string `json:"status"`
				Available bool   `json:"available"`
			} `json:"bot"`
			User struct {
				Status    string `json:"status"`
				Available bool   `json:"available"`
			} `json:"user"`
		} `json:"identities"`
		Error struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
		} `json:"error"`
	}
	if json.Unmarshal(output, &payload) == nil {
		if payload.Error.Subtype == "not_configured" || payload.Error.Type == "config" && payload.AppID == "" {
			return CLIAuthSetupRequired
		}
		if payload.Identities.User.Available && payload.Identities.User.Status != "not_configured" {
			return CLIAuthAuthenticated
		}
		if payload.AppID != "" || payload.Identities.Bot.Available {
			return CLIAuthUnauthenticated
		}
	}
	text := strings.ToLower(stripCLIAuthControls(string(output)))
	if strings.Contains(text, "not_configured") || strings.Contains(text, "not configured") {
		return CLIAuthSetupRequired
	}
	if strings.Contains(text, "needs refresh") || strings.Contains(text, "user identity: ready") {
		return CLIAuthAuthenticated
	}
	if commandErr != nil {
		return CLIAuthUnknown
	}
	return CLIAuthUnknown
}

func classifyGitHubCLIAuth(output []byte, commandErr error) CLIAuthState {
	text := strings.ToLower(stripCLIAuthControls(string(output)))
	if commandErr == nil && (strings.Contains(text, "logged in to") || strings.Contains(text, "active account: true")) {
		return CLIAuthAuthenticated
	}
	if strings.Contains(text, "not logged") || strings.Contains(text, "not authenticated") || commandErr != nil {
		return CLIAuthUnauthenticated
	}
	return CLIAuthUnknown
}

func actionableCLIAuthURL(line string) string {
	match := cliAuthURL.FindString(line)
	return strings.TrimRight(match, ".,;:!?)]}")
}

func actionableCLIAuthCode(line string) string {
	return strings.ReplaceAll(cliAuthCode.FindString(stripCLIAuthControls(line)), " ", "-")
}

func stripCLIAuthControls(value string) string {
	value = cliAuthANSISequence.ReplaceAllString(value, "")
	var builder strings.Builder
	for _, r := range value {
		if r == '\n' || r == '\t' || r >= 0x20 && r != 0x7f {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func cliAuthSessionTerminal(state CLIAuthSessionState) bool {
	return state == CLIAuthSessionSucceeded || state == CLIAuthSessionFailed || state == CLIAuthSessionCancelled
}

func cliAuthSessionID() string {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err == nil {
		return hex.EncodeToString(value)
	}
	return fmt.Sprintf("%d", time.Now().UnixNano())
}
