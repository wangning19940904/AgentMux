// Package authflow provides the process-independent lifecycle shared by CLI
// setup and browser/device authorization flows.
package authflow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"
)

// State is the prompt-safe lifecycle of an authorization process.
type State string

const (
	StateStarting  State = "starting"
	StateWaiting   State = "waiting"
	StateSucceeded State = "succeeded"
	StateFailed    State = "failed"
	StateCancelled State = "cancelled"
)

func (s State) Active() bool { return s == StateStarting || s == StateWaiting }

func (s State) Terminal() bool {
	return s == StateSucceeded || s == StateFailed || s == StateCancelled
}

// Snapshot is a sanitized, immutable view of one authorization process.
type Snapshot struct {
	Subject          string
	SessionID        string
	Phase            string
	State            State
	LoginURL         string
	VerificationCode string
	InputRequired    bool
	Error            string
	StartedAt        time.Time
	UpdatedAt        time.Time
}

// Session owns lifecycle state while the caller owns the concrete child
// process. Terminal transitions are one-way and cancellation is idempotent.
type Session struct {
	mu        sync.RWMutex
	snapshot  Snapshot
	cancel    context.CancelFunc
	input     io.WriteCloser
	ready     chan struct{}
	readyOnce sync.Once
	done      chan struct{}
	doneOnce  sync.Once
	updates   chan struct{}
}

func newSession(subject, sessionID string, inputRequired bool, cancel context.CancelFunc) *Session {
	now := time.Now().UTC()
	return &Session{
		snapshot: Snapshot{
			Subject: subject, SessionID: sessionID, Phase: "checking",
			State: StateStarting, InputRequired: inputRequired,
			StartedAt: now, UpdatedAt: now,
		},
		cancel:  cancel,
		ready:   make(chan struct{}),
		done:    make(chan struct{}),
		updates: make(chan struct{}, 1),
	}
}

func (s *Session) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshot
}

func (s *Session) Ready() <-chan struct{}   { return s.ready }
func (s *Session) Done() <-chan struct{}    { return s.done }
func (s *Session) Updates() <-chan struct{} { return s.updates }

func (s *Session) BeginPhase(phase string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snapshot.State.Terminal() {
		return false
	}
	s.snapshot.Phase = strings.TrimSpace(phase)
	s.snapshot.State = StateStarting
	s.snapshot.LoginURL = ""
	s.snapshot.VerificationCode = ""
	s.snapshot.Error = ""
	s.snapshot.UpdatedAt = time.Now().UTC()
	s.signalUpdate()
	return true
}

func (s *Session) Actionable(url, code string) bool {
	s.mu.Lock()
	if s.snapshot.State.Terminal() {
		s.mu.Unlock()
		return false
	}
	if url = strings.TrimSpace(url); url != "" {
		s.snapshot.LoginURL = url
	}
	if code = strings.TrimSpace(code); code != "" {
		s.snapshot.VerificationCode = code
	}
	if s.snapshot.LoginURL == "" && s.snapshot.VerificationCode == "" {
		s.mu.Unlock()
		return false
	}
	s.snapshot.State = StateWaiting
	s.snapshot.UpdatedAt = time.Now().UTC()
	s.mu.Unlock()
	s.readyOnce.Do(func() { close(s.ready) })
	s.signalUpdate()
	return true
}

// Finish records a terminal state once. Later completion or cancellation
// signals cannot overwrite the first terminal outcome.
func (s *Session) Finish(state State, message string) bool {
	if !state.Terminal() {
		return false
	}
	s.mu.Lock()
	if s.snapshot.State.Terminal() {
		s.mu.Unlock()
		return false
	}
	s.snapshot.State = state
	s.snapshot.Error = strings.TrimSpace(message)
	s.snapshot.UpdatedAt = time.Now().UTC()
	s.mu.Unlock()
	s.readyOnce.Do(func() { close(s.ready) })
	s.doneOnce.Do(func() { close(s.done) })
	s.signalUpdate()
	if s.cancel != nil {
		s.cancel()
	}
	return true
}

func (s *Session) Cancel(message string) bool {
	return s.Finish(StateCancelled, message)
}

func (s *Session) AttachInput(input io.WriteCloser) error {
	if input == nil {
		return errors.New("authorization input is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.snapshot.State.Terminal() {
		return errors.New("authorization session is no longer active")
	}
	s.input = input
	return nil
}

func (s *Session) WriteInput(value string, maxBytes int) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("authorization input is required")
	}
	if maxBytes > 0 && len(value) > maxBytes {
		return errors.New("authorization input is too long")
	}
	s.mu.RLock()
	if !s.snapshot.State.Active() {
		s.mu.RUnlock()
		return errors.New("authorization session is no longer active")
	}
	if !s.snapshot.InputRequired {
		s.mu.RUnlock()
		return errors.New("authorization session does not accept input")
	}
	input := s.input
	s.mu.RUnlock()
	if input == nil {
		return errors.New("authorization input is unavailable")
	}
	_, err := io.WriteString(input, value+"\n")
	return err
}

func (s *Session) CloseInput() error {
	s.mu.Lock()
	input := s.input
	s.input = nil
	s.mu.Unlock()
	if input == nil {
		return nil
	}
	return input.Close()
}

func (s *Session) signalUpdate() {
	select {
	case s.updates <- struct{}{}:
	default:
	}
}

// Registry stores live and recently completed sessions and prevents duplicate
// authorization processes for the same subject.
type Registry struct {
	mu       sync.Mutex
	sessions map[string]*Session
	active   map[string]string
	ttl      time.Duration
}

func NewRegistry(ttl time.Duration) *Registry {
	return &Registry{
		sessions: make(map[string]*Session),
		active:   make(map[string]string),
		ttl:      ttl,
	}
}

// Create reserves one active session for subject. When another active session
// already exists it is returned with created=false and the supplied cancel
// function is invoked because its context will not be used.
func (r *Registry) Create(subject string, inputRequired bool, cancel context.CancelFunc) (session *Session, created bool) {
	subject = strings.TrimSpace(subject)
	r.mu.Lock()
	if id := r.active[subject]; id != "" {
		if existing := r.sessions[id]; existing != nil && existing.Snapshot().State.Active() {
			r.mu.Unlock()
			if cancel != nil {
				cancel()
			}
			return existing, false
		}
		delete(r.active, subject)
	}
	session = newSession(subject, sessionID(), inputRequired, cancel)
	r.sessions[session.Snapshot().SessionID] = session
	r.active[subject] = session.Snapshot().SessionID
	r.mu.Unlock()
	return session, true
}

func (r *Registry) Get(sessionID string) (*Session, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, ok := r.sessions[strings.TrimSpace(sessionID)]
	return session, ok
}

// Release clears the subject's active slot and retains the terminal snapshot
// for the configured TTL so polling clients can observe the outcome.
func (r *Registry) Release(session *Session) {
	if session == nil {
		return
	}
	snapshot := session.Snapshot()
	r.mu.Lock()
	if r.active[snapshot.Subject] == snapshot.SessionID {
		delete(r.active, snapshot.Subject)
	}
	r.mu.Unlock()
	if r.ttl <= 0 {
		r.delete(snapshot.SessionID, session)
		return
	}
	time.AfterFunc(r.ttl, func() { r.delete(snapshot.SessionID, session) })
}

func (r *Registry) delete(sessionID string, expected *Session) {
	r.mu.Lock()
	if r.sessions[sessionID] == expected {
		delete(r.sessions, sessionID)
	}
	r.mu.Unlock()
}

var (
	ansiSequence = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	loginURL     = regexp.MustCompile(`https?://[^\s\x00-\x1f<>"']+`)
	loginCode    = regexp.MustCompile(`\b[A-Z0-9]{4}[- ][A-Z0-9]{4}\b`)
)

func ActionableURL(line string) string {
	return strings.TrimRight(loginURL.FindString(line), ".,;:!?)]}")
}

func ActionableCode(line string) string {
	return strings.ReplaceAll(loginCode.FindString(StripControls(line)), " ", "-")
}

func StripControls(value string) string {
	value = ansiSequence.ReplaceAllString(value, "")
	var builder strings.Builder
	for _, r := range value {
		if r == '\n' || r == '\t' || r >= 0x20 && r != 0x7f {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

// MergeEnvironment returns base with override keys replaced exactly once.
func MergeEnvironment(base []string, overrides map[string]string) []string {
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

func sessionID() string {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err == nil {
		return hex.EncodeToString(value)
	}
	return fmt.Sprintf("%d", time.Now().UnixNano())
}
