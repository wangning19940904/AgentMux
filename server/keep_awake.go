package server

import (
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
	"time"
)

const maxKeepAwakeMinutes = 24 * 60

// KeepAwakeStatus describes the temporary macOS power assertion owned by
// AgentMux. A zero duration disables the assertion.
type KeepAwakeStatus struct {
	Supported        bool   `json:"supported"`
	Enabled          bool   `json:"enabled"`
	DurationMinutes  int    `json:"duration_minutes"`
	RemainingSeconds int64  `json:"remaining_seconds"`
	EndsAt           string `json:"ends_at,omitempty"`
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

type keepAwakeManager struct {
	mu        sync.Mutex
	supported bool
	process   keepAwakeProcess
	runID     uint64
	duration  int
	endsAt    time.Time
	now       func() time.Time
	command   func(seconds int64) keepAwakeProcess
}

func newKeepAwakeManager() *keepAwakeManager {
	return &keepAwakeManager{
		supported: runtime.GOOS == "darwin",
		now:       time.Now,
		command:   newCaffeinateProcess,
	}
}

func newCaffeinateProcess(seconds int64) keepAwakeProcess {
	return &caffeinateProcess{command: exec.Command(
		"/usr/bin/caffeinate",
		"-d", // prevent display idle sleep
		"-i", // prevent system idle sleep
		"-u", // declare user activity for idle screen-lock behavior
		"-t", strconv.FormatInt(seconds, 10),
	)}
}

func (m *keepAwakeManager) Status() KeepAwakeStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statusLocked()
}

// Set replaces the current assertion. Passing zero stops it immediately.
func (m *keepAwakeManager) Set(durationMinutes int) (KeepAwakeStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.supported {
		return m.statusLocked(), nil
	}
	if durationMinutes < 0 || durationMinutes > maxKeepAwakeMinutes {
		return m.statusLocked(), fmt.Errorf("duration_minutes must be between 0 and %d", maxKeepAwakeMinutes)
	}

	m.stopLocked()
	if durationMinutes == 0 {
		return m.statusLocked(), nil
	}

	seconds := int64(durationMinutes) * int64(time.Minute/time.Second)
	process := m.command(seconds)
	if err := process.Start(); err != nil {
		return m.statusLocked(), fmt.Errorf("start caffeinate: %w", err)
	}
	m.runID++
	runID := m.runID
	m.process = process
	m.duration = durationMinutes
	m.endsAt = m.now().Add(time.Duration(durationMinutes) * time.Minute)
	go m.wait(process, runID)
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
	m.process = nil
	m.duration = 0
	m.endsAt = time.Time{}
}

func (m *keepAwakeManager) wait(process keepAwakeProcess, runID uint64) {
	_ = process.Wait()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.runID != runID {
		return
	}
	m.process = nil
	m.duration = 0
	m.endsAt = time.Time{}
}

func (m *keepAwakeManager) statusLocked() KeepAwakeStatus {
	status := KeepAwakeStatus{Supported: m.supported}
	if !m.supported || m.process == nil || m.endsAt.IsZero() {
		return status
	}
	remaining := m.endsAt.Sub(m.now())
	if remaining <= 0 {
		return status
	}
	status.Enabled = true
	status.DurationMinutes = m.duration
	status.RemainingSeconds = int64((remaining + time.Second - 1) / time.Second)
	status.EndsAt = m.endsAt.Format(time.RFC3339)
	return status
}

func (s *Server) handleKeepAwakeGet(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.keepAwake.Status())
}

func (s *Server) handleKeepAwakePut(w http.ResponseWriter, r *http.Request) {
	var request struct {
		DurationMinutes *int `json:"duration_minutes"`
	}
	if !decodeJSONInto(w, r, &request) {
		return
	}
	if request.DurationMinutes == nil {
		writeErr(w, http.StatusBadRequest, "duration_minutes is required")
		return
	}
	if *request.DurationMinutes < 0 || *request.DurationMinutes > maxKeepAwakeMinutes {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("duration_minutes must be between 0 and %d", maxKeepAwakeMinutes))
		return
	}
	status, err := s.keepAwake.Set(*request.DurationMinutes)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}
