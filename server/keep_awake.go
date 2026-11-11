package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"sync"
	"time"
)

const maxKeepAwakeMinutes = 24 * 60

// KeepAwakeMode selects which macOS power assertions AgentMux holds.
type KeepAwakeMode string

const (
	// KeepAwakeModeDisplay keeps the display on, which also prevents the
	// idle-triggered screen saver / screen lock, and keeps the system awake.
	KeepAwakeModeDisplay KeepAwakeMode = "display"
	// KeepAwakeModeSystem only prevents idle system sleep. The display may
	// still turn off and the screen may still lock.
	KeepAwakeModeSystem KeepAwakeMode = "system"
)

// Activity simulation states reported in KeepAwakeStatus.ActivityState.
const (
	keepAwakeActivityWaiting = "waiting" // user active, no synthetic input needed yet
	keepAwakeActivityActive  = "active"  // synthetic input reset the HID idle timer
	keepAwakeActivityBlocked = "blocked" // synthetic input was posted but ignored
	keepAwakeActivityError   = "error"   // idle time could not be read or input could not be posted
)

// KeepAwakeOptions describes a keep-awake request. A zero duration keeps the
// assertion until it is stopped explicitly or AgentMux exits.
type KeepAwakeOptions struct {
	DurationMinutes  int
	Mode             KeepAwakeMode
	SimulateActivity bool
}

// KeepAwakeStatus describes the macOS power assertion owned by AgentMux.
type KeepAwakeStatus struct {
	Supported        bool          `json:"supported"`
	Enabled          bool          `json:"enabled"`
	Mode             KeepAwakeMode `json:"mode"`
	SimulateActivity bool          `json:"simulate_activity"`
	ActivityState    string        `json:"activity_state,omitempty"`
	ActivityError    string        `json:"activity_error,omitempty"`
	LastActivityAt   string        `json:"last_activity_at,omitempty"`
	DurationMinutes  int           `json:"duration_minutes"`
	RemainingSeconds int64         `json:"remaining_seconds"`
	EndsAt           string        `json:"ends_at,omitempty"`
}

type keepAwakeProcess interface {
	Start() error
	Wait() error
	Kill() error
}

type caffeinateProcess struct {
	command *exec.Cmd
}

func (p *caffeinateProcess) Start() error { return p.command.Start() }
func (p *caffeinateProcess) Wait() error  { return p.command.Wait() }
func (p *caffeinateProcess) Kill() error {
	if p.command.Process == nil {
		return nil
	}
	return p.command.Process.Kill()
}

// activitySimulator resets the HID idle timer. Power assertions do not touch
// that timer, so security agents and MDM policies that lock the screen after
// N seconds without keyboard or mouse input ignore caffeinate entirely.
type activitySimulator interface {
	IdleTime() (time.Duration, error)
	Nudge() error
}

type keepAwakeManager struct {
	mu        sync.Mutex
	supported bool
	process   keepAwakeProcess
	runID     uint64
	options   KeepAwakeOptions
	endsAt    time.Time
	active    bool
	now       func() time.Time
	command   func(options KeepAwakeOptions, ownerPID int) keepAwakeProcess

	simulator        activitySimulator
	activityInterval time.Duration // how often the HID idle time is checked
	activityIdle     time.Duration // idle time after which synthetic input is posted
	activityVerify   time.Duration // delay before re-reading idle time after a nudge
	activityStop     chan struct{}
	activityState    string
	activityError    string
	lastActivityAt   time.Time
}

func newKeepAwakeManager() *keepAwakeManager {
	return &keepAwakeManager{
		supported:        runtime.GOOS == "darwin",
		now:              time.Now,
		command:          newCaffeinateProcess,
		options:          KeepAwakeOptions{DurationMinutes: 60, Mode: KeepAwakeModeDisplay},
		simulator:        darwinActivitySimulator{},
		activityInterval: 15 * time.Second,
		activityIdle:     30 * time.Second,
		activityVerify:   400 * time.Millisecond,
	}
}

func caffeinateArgs(options KeepAwakeOptions, ownerPID int) []string {
	args := []string{"/usr/bin/caffeinate"}
	if options.Mode == KeepAwakeModeDisplay {
		args = append(args,
			"-d", // prevent display idle sleep (and with it the idle screen saver / lock)
			"-u", // declare user activity so a dimmed display wakes up
		)
	}
	args = append(args,
		"-i", // prevent system idle sleep
		"-s", // prevent system sleep while on AC power
	)
	if ownerPID > 0 {
		// Release the assertion if AgentMux exits without cleaning up.
		args = append(args, "-w", strconv.Itoa(ownerPID))
	}
	if options.DurationMinutes > 0 {
		args = append(args, "-t", strconv.FormatInt(int64(options.DurationMinutes)*60, 10))
	}
	return args
}

func newCaffeinateProcess(options KeepAwakeOptions, ownerPID int) keepAwakeProcess {
	args := caffeinateArgs(options, ownerPID)
	return &caffeinateProcess{command: exec.Command(args[0], args[1:]...)}
}

func normalizeKeepAwakeOptions(options KeepAwakeOptions) (KeepAwakeOptions, error) {
	if options.DurationMinutes < 0 || options.DurationMinutes > maxKeepAwakeMinutes {
		return options, fmt.Errorf("duration_minutes must be between 0 and %d", maxKeepAwakeMinutes)
	}
	switch options.Mode {
	case "":
		options.Mode = KeepAwakeModeDisplay
	case KeepAwakeModeDisplay, KeepAwakeModeSystem:
	default:
		return options, fmt.Errorf("mode must be %q or %q", KeepAwakeModeDisplay, KeepAwakeModeSystem)
	}
	if options.Mode != KeepAwakeModeDisplay {
		// Simulated input would keep the screen unlocked, which contradicts
		// a request that only prevents system sleep.
		options.SimulateActivity = false
	}
	return options, nil
}

func (m *keepAwakeManager) Status() KeepAwakeStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statusLocked()
}

// Start replaces the current assertion with one described by options.
func (m *keepAwakeManager) Start(options KeepAwakeOptions) (KeepAwakeStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.supported {
		return m.statusLocked(), nil
	}
	options, err := normalizeKeepAwakeOptions(options)
	if err != nil {
		return m.statusLocked(), err
	}

	m.stopLocked()
	m.options = options
	process := m.command(options, os.Getpid())
	if err := process.Start(); err != nil {
		return m.statusLocked(), fmt.Errorf("start caffeinate: %w", err)
	}
	m.runID++
	runID := m.runID
	m.process = process
	m.active = true
	if options.DurationMinutes > 0 {
		m.endsAt = m.now().Add(time.Duration(options.DurationMinutes) * time.Minute)
	}
	go m.wait(process, runID)
	if options.SimulateActivity && m.simulator != nil {
		stop := make(chan struct{})
		m.activityStop = stop
		m.activityState = keepAwakeActivityWaiting
		go m.simulate(runID, stop)
	}
	return m.statusLocked(), nil
}

func (m *keepAwakeManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked()
}

func (m *keepAwakeManager) stopLocked() {
	m.runID++
	if m.process != nil {
		_ = m.process.Kill()
	}
	m.clearLocked()
}

func (m *keepAwakeManager) clearLocked() {
	if m.activityStop != nil {
		close(m.activityStop)
		m.activityStop = nil
	}
	m.process = nil
	m.active = false
	m.endsAt = time.Time{}
	m.activityState = ""
	m.activityError = ""
	m.lastActivityAt = time.Time{}
}

func (m *keepAwakeManager) wait(process keepAwakeProcess, runID uint64) {
	_ = process.Wait()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.runID != runID {
		return
	}
	m.clearLocked()
}

func (m *keepAwakeManager) simulate(runID uint64, stop <-chan struct{}) {
	ticker := time.NewTicker(m.activityInterval)
	defer ticker.Stop()
	for {
		m.nudgeIfIdle(runID, stop)
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
	}
}

// nudgeIfIdle posts synthetic input only once the user has been idle for a
// while, so it never interferes with real keyboard or mouse use.
func (m *keepAwakeManager) nudgeIfIdle(runID uint64, stop <-chan struct{}) {
	idle, err := m.simulator.IdleTime()
	if err != nil {
		m.recordActivity(runID, keepAwakeActivityError, err, false)
		return
	}
	if idle < m.activityIdle {
		m.recordActivity(runID, "", nil, false)
		return
	}
	if err := m.simulator.Nudge(); err != nil {
		m.recordActivity(runID, keepAwakeActivityError, err, false)
		return
	}
	if m.activityVerify > 0 {
		select {
		case <-stop:
			return
		case <-time.After(m.activityVerify):
		}
	}
	after, err := m.simulator.IdleTime()
	if err != nil {
		m.recordActivity(runID, keepAwakeActivityError, err, false)
		return
	}
	if after >= idle || after >= m.activityIdle {
		m.recordActivity(runID, keepAwakeActivityBlocked, nil, false)
		return
	}
	m.recordActivity(runID, keepAwakeActivityActive, nil, true)
}

// recordActivity stores the result of one simulation tick. An empty state
// keeps the last meaningful result while the user is active.
func (m *keepAwakeManager) recordActivity(runID uint64, state string, err error, nudged bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.runID != runID || !m.active {
		return
	}
	if state != "" {
		m.activityState = state
	}
	m.activityError = ""
	if err != nil {
		m.activityError = err.Error()
	}
	if nudged {
		m.lastActivityAt = m.now()
	}
}

func (m *keepAwakeManager) statusLocked() KeepAwakeStatus {
	status := KeepAwakeStatus{
		Supported:        m.supported,
		Mode:             m.options.Mode,
		SimulateActivity: m.options.SimulateActivity,
		DurationMinutes:  m.options.DurationMinutes,
	}
	if status.Mode == "" {
		status.Mode = KeepAwakeModeDisplay
	}
	if !m.supported || m.process == nil || !m.active {
		return status
	}
	if !m.endsAt.IsZero() {
		remaining := m.endsAt.Sub(m.now())
		if remaining <= 0 {
			return status
		}
		status.RemainingSeconds = int64((remaining + time.Second - 1) / time.Second)
		status.EndsAt = m.endsAt.Format(time.RFC3339)
	}
	status.Enabled = true
	if m.options.SimulateActivity {
		status.ActivityState = m.activityState
		status.ActivityError = m.activityError
		if !m.lastActivityAt.IsZero() {
			status.LastActivityAt = m.lastActivityAt.Format(time.RFC3339)
		}
	}
	return status
}

// darwinActivitySimulator reads HIDIdleTime from IOHIDSystem and posts a
// zero-distance mouse-moved event through CoreGraphics. The event resets the
// HID idle timer like real input without moving the cursor. The helpers are
// spawned from AgentMux, so macOS attributes them to the app for any
// Accessibility permission check.
type darwinActivitySimulator struct{}

var hidIdleTimePattern = regexp.MustCompile(`"HIDIdleTime" = (\d+)`)

func (darwinActivitySimulator) IdleTime() (time.Duration, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/sbin/ioreg", "-c", "IOHIDSystem", "-d", "4", "-r", "-k", "HIDIdleTime").Output()
	if err != nil {
		return 0, fmt.Errorf("read HID idle time: %w", err)
	}
	match := hidIdleTimePattern.FindSubmatch(out)
	if match == nil {
		return 0, errors.New("read HID idle time: HIDIdleTime not reported")
	}
	nanos, err := strconv.ParseInt(string(match[1]), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse HID idle time: %w", err)
	}
	return time.Duration(nanos), nil
}

const nudgeMouseScript = `ObjC.import("CoreGraphics");
var current = $.CGEventCreate(null);
var point = $.CGEventGetLocation(current);
var move = $.CGEventCreateMouseEvent(null, $.kCGEventMouseMoved, point, 0);
$.CGEventPost($.kCGHIDEventTap, move);
"ok"`

func (darwinActivitySimulator) Nudge() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/bin/osascript", "-l", "JavaScript", "-e", nudgeMouseScript).CombinedOutput()
	if err != nil {
		return fmt.Errorf("post synthetic input: %w: %s", err, string(out))
	}
	return nil
}

const accessibilityPromptScript = `ObjC.import("ApplicationServices");
var options = $.NSDictionary.dictionaryWithObjectForKey(true, "AXTrustedCheckOptionPrompt");
String($.AXIsProcessTrustedWithOptions(options))`

const accessibilitySettingsURL = "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility"

// requestAccessibilityPermission asks macOS to list AgentMux under
// Privacy & Security > Accessibility and opens that settings pane.
func requestAccessibilityPermission(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "/usr/bin/osascript", "-l", "JavaScript", "-e", accessibilityPromptScript).Run()
	if out, err := exec.CommandContext(ctx, "/usr/bin/open", accessibilitySettingsURL).CombinedOutput(); err != nil {
		return fmt.Errorf("open Accessibility settings: %w: %s", err, string(out))
	}
	return nil
}

func (s *Server) handleKeepAwakeGet(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.keepAwake.Status())
}

func (s *Server) handleKeepAwakePut(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Enabled          *bool         `json:"enabled"`
		DurationMinutes  *int          `json:"duration_minutes"`
		Mode             KeepAwakeMode `json:"mode"`
		SimulateActivity bool          `json:"simulate_activity"`
	}
	if !decodeJSONInto(w, r, &request) {
		return
	}
	if request.Enabled == nil {
		// Legacy request shape: a positive duration starts, zero stops.
		if request.DurationMinutes == nil {
			writeErr(w, http.StatusBadRequest, "enabled or duration_minutes is required")
			return
		}
		enabled := *request.DurationMinutes > 0
		request.Enabled = &enabled
	}
	if !*request.Enabled {
		s.keepAwake.Stop()
		writeJSON(w, http.StatusOK, s.keepAwake.Status())
		return
	}
	options := KeepAwakeOptions{Mode: request.Mode, SimulateActivity: request.SimulateActivity}
	if request.DurationMinutes != nil {
		options.DurationMinutes = *request.DurationMinutes
	}
	if _, err := normalizeKeepAwakeOptions(options); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	status, err := s.keepAwake.Start(options)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleKeepAwakeAccessibility(w http.ResponseWriter, r *http.Request) {
	if !s.keepAwake.Status().Supported {
		writeErr(w, http.StatusNotImplemented, "keep awake is only supported on macOS")
		return
	}
	if err := requestAccessibilityPermission(r.Context()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"opened": true})
}
