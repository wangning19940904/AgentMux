package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wangning19940904/AgentMux/framework"
)

// Framework operations run installs, updates and uninstalls in the
// background. The request that starts one returns at once with an ID, and
// the Console polls the operation, including through the fleet relay, which
// cannot stream. An operation keeps running when the page that started it
// is closed, and its progress and output are still there when it comes back.

const (
	frameworkOperationLogLimit  = 256 << 10
	frameworkOperationRetention = time.Hour
	frameworkOperationMaxKept   = 50
)

type frameworkOperation struct {
	ID         string                   `json:"id"`
	Kind       string                   `json:"kind"`
	Action     string                   `json:"action"`
	State      string                   `json:"state"`
	Progress   installProgress          `json:"progress"`
	Log        string                   `json:"log,omitempty"`
	Result     *framework.InstallResult `json:"result,omitempty"`
	StartedAt  time.Time                `json:"started_at"`
	UpdatedAt  time.Time                `json:"updated_at"`
	FinishedAt *time.Time               `json:"finished_at,omitempty"`
}

const (
	frameworkOperationRunning   = "running"
	frameworkOperationSucceeded = "succeeded"
	frameworkOperationFailed    = "failed"
)

type frameworkOperationStore struct {
	mu         sync.Mutex
	operations map[string]*frameworkOperation
	now        func() time.Time
}

var frameworkOperations = &frameworkOperationStore{operations: map[string]*frameworkOperation{}, now: time.Now}

// start registers an operation for kind, or returns the one already running
// for it. conflict is true when that running operation is a different action.
func (store *frameworkOperationStore) start(kind, action string) (op frameworkOperation, created, conflict bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.pruneLocked()
	for _, existing := range store.operations {
		if existing.Kind == kind && existing.State == frameworkOperationRunning {
			return existing.snapshot(true), false, existing.Action != action
		}
	}
	now := store.now()
	phase := "preparing"
	if action == "update" {
		phase = "checking"
	}
	fresh := &frameworkOperation{
		ID: newFrameworkOperationID(), Kind: kind, Action: action, State: frameworkOperationRunning,
		Progress: installProgress{Phase: phase, Percent: 4}, StartedAt: now, UpdatedAt: now,
	}
	store.operations[fresh.ID] = fresh
	return fresh.snapshot(true), true, false
}

func (store *frameworkOperationStore) progress(id, phase, detail string, percent int) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if op := store.operations[id]; op != nil && op.State == frameworkOperationRunning {
		op.Progress = installProgress{Phase: phase, Detail: detail, Percent: percent}
		op.UpdatedAt = store.now()
	}
}

// output appends command output and shows its latest line as the detail.
func (store *frameworkOperationStore) output(id string, chunk []byte) {
	store.mu.Lock()
	defer store.mu.Unlock()
	op := store.operations[id]
	if op == nil || op.State != frameworkOperationRunning {
		return
	}
	op.Log = tailString(op.Log+string(chunk), frameworkOperationLogLimit)
	if line := lastOutputLine(op.Log); line != "" {
		op.Progress.Detail = line
	}
	op.UpdatedAt = store.now()
}

func (store *frameworkOperationStore) finish(id string, result framework.InstallResult) {
	store.mu.Lock()
	defer store.mu.Unlock()
	op := store.operations[id]
	if op == nil {
		return
	}
	now := store.now()
	result.Log = tailString(result.Log, frameworkOperationLogLimit)
	op.Result = &result
	op.Log = result.Log
	op.State = frameworkOperationFailed
	if result.OK {
		op.State = frameworkOperationSucceeded
	}
	op.Progress = installProgress{Phase: "complete", Percent: 100}
	op.UpdatedAt = now
	op.FinishedAt = &now
}

func (store *frameworkOperationStore) get(id string) (frameworkOperation, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	op := store.operations[id]
	if op == nil {
		return frameworkOperation{}, false
	}
	return op.snapshot(true), true
}

// list returns recent operations, newest first, without their logs.
func (store *frameworkOperationStore) list() []frameworkOperation {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.pruneLocked()
	out := make([]frameworkOperation, 0, len(store.operations))
	for _, op := range store.operations {
		out = append(out, op.snapshot(false))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out
}

func (store *frameworkOperationStore) pruneLocked() {
	now := store.now()
	finished := make([]*frameworkOperation, 0, len(store.operations))
	for id, op := range store.operations {
		if op.FinishedAt == nil {
			continue
		}
		if now.Sub(*op.FinishedAt) > frameworkOperationRetention {
			delete(store.operations, id)
			continue
		}
		finished = append(finished, op)
	}
	if len(finished) <= frameworkOperationMaxKept {
		return
	}
	sort.Slice(finished, func(i, j int) bool { return finished[i].FinishedAt.After(*finished[j].FinishedAt) })
	for _, op := range finished[frameworkOperationMaxKept:] {
		delete(store.operations, op.ID)
	}
}

func (op *frameworkOperation) snapshot(withLog bool) frameworkOperation {
	out := *op
	if op.Result != nil {
		result := *op.Result
		if !withLog {
			result.Log = ""
		}
		out.Result = &result
	}
	if !withLog {
		out.Log = ""
	}
	return out
}

func newFrameworkOperationID() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("fwop-%d", time.Now().UnixNano())
	}
	return "fwop-" + hex.EncodeToString(raw[:])
}

func tailString(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[len(value)-limit:]
}

// lastOutputLine returns the last non-blank line of output. Package managers
// redraw progress with carriage returns, so only the text after the last one
// on a line is what the terminal would show.
func lastOutputLine(output string) string {
	lines := strings.Split(output, "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		line := lines[index]
		if cut := strings.LastIndex(strings.TrimRight(line, "\r"), "\r"); cut >= 0 {
			line = line[cut+1:]
		}
		line = strings.TrimSpace(line)
		if line != "" {
			if len(line) > 240 {
				line = line[:240] + "…"
			}
			return line
		}
	}
	return ""
}

func (s *Server) handleFrameworkOperationStart(w http.ResponseWriter, r *http.Request) {
	var req frameworkInstallRequest
	if !decodeJSONInto(w, r, &req) {
		return
	}
	kind := strings.TrimSpace(req.Kind)
	if kind == "" {
		writeErr(w, http.StatusBadRequest, "framework kind is required")
		return
	}
	action := strings.TrimSpace(req.Action)
	if action == "" {
		action = "install"
	}
	if action != "install" && action != "update" && action != "uninstall" {
		writeErr(w, http.StatusBadRequest, "action must be install, update, or uninstall")
		return
	}
	op, created, conflict := frameworkOperations.start(kind, action)
	if conflict {
		writeErr(w, http.StatusConflict, fmt.Sprintf("%s already has an %s running", kind, op.Action))
		return
	}
	if created {
		options := s.frameworkInstallOptions(r, req)
		id := op.ID
		options.Output = func(chunk []byte) { frameworkOperations.output(id, chunk) }
		// The operation outlives this request: the page that started it may
		// close, and the fleet relay only carries the start and the polls.
		go func() {
			result := s.runFrameworkInstall(context.WithoutCancel(r.Context()), kind, action, options, func(phase, detail string, percent int) {
				frameworkOperations.progress(id, phase, detail, percent)
			})
			frameworkOperations.finish(id, result)
		}()
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, op)
}

func (s *Server) handleFrameworkOperations(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if id := strings.TrimSpace(r.URL.Query().Get("id")); id != "" {
		op, ok := frameworkOperations.get(id)
		if !ok {
			writeErr(w, http.StatusNotFound, "framework operation not found")
			return
		}
		writeJSON(w, http.StatusOK, op)
		return
	}
	writeJSON(w, http.StatusOK, frameworkOperations.list())
}
