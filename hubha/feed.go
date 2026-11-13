// Package hubha lets a second hub stand by for the primary.
//
// The standby copies hub data through the hub service, over the same link
// every device uses: it reads the primary's change log (store/hubsync.go) and
// applies it to its own database. A relay in front of both hubs decides which
// one is primary by granting it a short lease (hubrelay), and the hub that
// loses or lacks the lease stops writing hub data. See
// docs/architecture/hub-and-super-agents.md §4.2.
package hubha

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/wangning19940904/AgentMux/link"
	"github.com/wangning19940904/AgentMux/store"
)

const (
	changesPath  = "/uplink/v1/hub/changes"
	snapshotPath = "/uplink/v1/hub/snapshot"
	identityPath = "/uplink/v1/hub/identity"

	// FeedPrefix is where the feed's routes live on a hub's uplink.
	FeedPrefix = "/uplink/v1/hub/"

	changeBatch  = 500
	maxWait      = 25 * time.Second
	pollInterval = 300 * time.Millisecond
)

// Source is the primary's side of the feed.
type Source interface {
	HubReplication(ctx context.Context) (store.HubReplicationState, error)
	HubChangesAfter(ctx context.Context, after int64, limit int) ([]store.HubChange, error)
	WriteHubSnapshot(ctx context.Context, w io.Writer) error
	DeviceHubRole(ctx context.Context, id string) (string, error)
}

// Identity is the hub's TLS certificate and key, in PEM. Both hubs present the
// same one, so devices' pinned fingerprints hold after a failover.
type Identity struct {
	Cert []byte `json:"cert"`
	Key  []byte `json:"key"`
}

// ChangesResponse is one batch of the feed.
type ChangesResponse struct {
	Epoch         int64             `json:"epoch"`
	SchemaVersion int               `json:"schema_version"`
	Head          int64             `json:"head"`
	Changes       []store.HubChange `json:"changes"`
}

// NewFeedHandler serves the feed to devices the hub marked as its standby.
func NewFeedHandler(source Source, identity func() (Identity, error), tracker *Tracker, log *slog.Logger) http.Handler {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	mux := http.NewServeMux()
	authorized := func(w http.ResponseWriter, r *http.Request) (string, bool) {
		deviceID := link.DeviceID(r.Context())
		if deviceID == "" {
			writeError(w, http.StatusUnauthorized, "the hub feed is served only over a device link")
			return "", false
		}
		role, err := source.DeviceHubRole(r.Context(), deviceID)
		if err != nil {
			log.Warn("read device hub role failed", "device", deviceID, "err", err)
			writeError(w, http.StatusServiceUnavailable, "the hub could not check this device")
			return "", false
		}
		if role != "standby" {
			writeError(w, http.StatusForbidden, "this device is not a standby of this hub")
			return "", false
		}
		state, err := source.HubReplication(r.Context())
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "the hub could not read its replication state")
			return "", false
		}
		if state.Role != store.HubRolePrimary {
			writeError(w, http.StatusServiceUnavailable, "this hub is not the primary ("+string(state.Role)+")")
			return "", false
		}
		return deviceID, true
	}
	mux.HandleFunc("GET "+changesPath, func(w http.ResponseWriter, r *http.Request) {
		deviceID, ok := authorized(w, r)
		if !ok {
			return
		}
		after, err := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		if err != nil || after < 0 {
			writeError(w, http.StatusBadRequest, "after must be a change number")
			return
		}
		wait, _ := time.ParseDuration(r.URL.Query().Get("wait"))
		wait = min(max(wait, 0), maxWait)
		// Asking for changes after N says everything up to N is applied.
		tracker.Ack(deviceID, after)
		deadline := time.Now().Add(wait)
		for {
			state, err := source.HubReplication(r.Context())
			if err != nil {
				writeError(w, http.StatusServiceUnavailable, "the hub could not read its replication state")
				return
			}
			if state.Role != store.HubRolePrimary {
				writeError(w, http.StatusServiceUnavailable, "this hub is no longer the primary")
				return
			}
			changes, err := source.HubChangesAfter(r.Context(), after, changeBatch)
			if errors.Is(err, store.ErrHubChangesPruned) {
				writeError(w, http.StatusGone, err.Error())
				return
			}
			if err != nil {
				log.Warn("read hub changes failed", "err", err)
				writeError(w, http.StatusServiceUnavailable, "the hub could not read its change log")
				return
			}
			if len(changes) > 0 || !time.Now().Before(deadline) {
				if changes == nil {
					changes = []store.HubChange{}
				}
				writeJSON(w, ChangesResponse{Epoch: state.Epoch, SchemaVersion: state.SchemaVersion, Head: state.HeadSeq, Changes: changes})
				return
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(pollInterval):
			}
		}
	})
	mux.HandleFunc("GET "+snapshotPath, func(w http.ResponseWriter, r *http.Request) {
		if _, ok := authorized(w, r); !ok {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if err := source.WriteHubSnapshot(r.Context(), w); err != nil {
			log.Warn("write hub snapshot failed", "err", err)
		}
	})
	mux.HandleFunc("GET "+identityPath, func(w http.ResponseWriter, r *http.Request) {
		if _, ok := authorized(w, r); !ok {
			return
		}
		id, err := identity()
		if err != nil {
			log.Warn("read hub identity failed", "err", err)
			writeError(w, http.StatusServiceUnavailable, "the hub could not read its certificate")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, id)
	})
	return mux
}

// Tracker remembers how far each standby has applied the change log, so a
// write that must survive a failover can wait for it.
type Tracker struct {
	mu      sync.Mutex
	acked   map[string]trackerEntry
	changed chan struct{}
}

type trackerEntry struct {
	seq int64
	at  time.Time
}

// standbyActive is how recently a standby must have asked for changes to be
// waited for. One that is away is not waited on.
const standbyActive = time.Minute

func NewTracker() *Tracker {
	return &Tracker{acked: map[string]trackerEntry{}, changed: make(chan struct{})}
}

// Ack records that a standby applied everything up to seq.
func (t *Tracker) Ack(deviceID string, seq int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.acked[deviceID] = trackerEntry{seq: seq, at: time.Now()}
	close(t.changed)
	t.changed = make(chan struct{})
}

// Standbys lists the standbys seen recently and how far they applied.
func (t *Tracker) Standbys() map[string]int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := map[string]int64{}
	for id, entry := range t.acked {
		if time.Since(entry.at) < standbyActive {
			out[id] = entry.seq
		}
	}
	return out
}

// WaitFor waits until an active standby applied seq, or until timeout. It
// reports whether a standby has the change; with no active standby it
// returns false at once.
func (t *Tracker) WaitFor(ctx context.Context, seq int64, timeout time.Duration) bool {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		t.mu.Lock()
		active := false
		for _, entry := range t.acked {
			if time.Since(entry.at) >= standbyActive {
				continue
			}
			active = true
			if entry.seq >= seq {
				t.mu.Unlock()
				return true
			}
		}
		changed := t.changed
		t.mu.Unlock()
		if !active {
			return false
		}
		select {
		case <-changed:
		case <-deadline.C:
			return false
		case <-ctx.Done():
			return false
		}
	}
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
