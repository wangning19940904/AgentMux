package hubha

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/wangning19940904/AgentMux/store"
)

// Sink is the standby's side: its own database.
type Sink interface {
	HubReplication(ctx context.Context) (store.HubReplicationState, error)
	LoadHubSnapshot(ctx context.Context, snapshot store.HubSnapshot) error
	ApplyHubChanges(ctx context.Context, schemaVersion int, changes []store.HubChange) error
}

// Follower keeps a standby's copy of hub data up to date. It talks to the
// primary through Uplink, which is the standby's own link to the hub.
type Follower struct {
	Sink Sink
	// Uplink returns the client for requests up the link, or nil while this
	// machine has no link to the hub.
	Uplink func() *http.Client
	// SaveIdentity stores the primary's certificate so this machine presents
	// the same one after it takes over. Nil skips it.
	SaveIdentity func(Identity) error
	Log          *slog.Logger

	mu     sync.Mutex
	status FollowerStatus
}

// FollowerStatus says how far behind the primary the standby is.
type FollowerStatus struct {
	Running     bool       `json:"running"`
	AppliedSeq  int64      `json:"applied_seq"`
	PrimaryHead int64      `json:"primary_head"`
	LastSync    *time.Time `json:"last_sync,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
}

func (f *Follower) Status() FollowerStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}

func (f *Follower) update(change func(*FollowerStatus)) {
	f.mu.Lock()
	change(&f.status)
	f.mu.Unlock()
}

var errNoLink = errors.New("this machine has no link to the primary hub")

// Run follows until ctx ends.
func (f *Follower) Run(ctx context.Context) {
	if f.Log == nil {
		f.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	f.update(func(s *FollowerStatus) { s.Running = true })
	defer f.update(func(s *FollowerStatus) { s.Running = false })
	backoff := time.Second
	identitySaved := false
	for ctx.Err() == nil {
		err := f.step(ctx, &identitySaved)
		if err == nil {
			backoff = time.Second
			now := time.Now()
			f.update(func(s *FollowerStatus) { s.LastSync, s.LastError = &now, "" })
			continue
		}
		if ctx.Err() != nil {
			return
		}
		f.update(func(s *FollowerStatus) { s.LastError = err.Error() })
		var mismatch *store.HubSchemaMismatchError
		if errors.As(err, &mismatch) {
			backoff = time.Minute
		}
		f.Log.Warn("hub standby sync failed", "err", err, "retry_in", backoff.String())
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, time.Minute)
	}
}

// step does one round: the identity once, a snapshot when there is no
// position yet, then one long-poll for changes.
func (f *Follower) step(ctx context.Context, identitySaved *bool) error {
	client := f.Uplink()
	if client == nil {
		return errNoLink
	}
	if !*identitySaved && f.SaveIdentity != nil {
		var identity Identity
		if err := getJSON(ctx, client, identityPath, &identity); err != nil {
			return fmt.Errorf("fetch the hub certificate: %w", err)
		}
		if err := f.SaveIdentity(identity); err != nil {
			return fmt.Errorf("save the hub certificate: %w", err)
		}
		*identitySaved = true
	}
	state, err := f.Sink.HubReplication(ctx)
	if err != nil {
		return err
	}
	if state.AppliedSeq == 0 {
		return f.snapshot(ctx, client)
	}
	var batch ChangesResponse
	err = getJSON(ctx, client, changesPath+"?after="+strconv.FormatInt(state.AppliedSeq, 10)+"&wait="+maxWait.String(), &batch)
	var status *statusError
	if errors.As(err, &status) && status.code == http.StatusGone {
		f.Log.Info("hub standby fell behind the primary's log; loading a snapshot")
		return f.snapshot(ctx, client)
	}
	if err != nil {
		return err
	}
	if err := f.Sink.ApplyHubChanges(ctx, batch.SchemaVersion, batch.Changes); err != nil {
		return err
	}
	applied := state.AppliedSeq
	if n := len(batch.Changes); n > 0 {
		applied = batch.Changes[n-1].Seq
	}
	f.update(func(s *FollowerStatus) { s.AppliedSeq, s.PrimaryHead = applied, batch.Head })
	return nil
}

func (f *Follower) snapshot(ctx context.Context, client *http.Client) error {
	var snapshot store.HubSnapshot
	if err := getJSON(ctx, client, snapshotPath, &snapshot); err != nil {
		return fmt.Errorf("fetch a snapshot: %w", err)
	}
	if err := f.Sink.LoadHubSnapshot(ctx, snapshot); err != nil {
		return fmt.Errorf("load the snapshot: %w", err)
	}
	f.Log.Info("hub standby loaded a snapshot", "seq", snapshot.Seq, "epoch", snapshot.Epoch)
	f.update(func(s *FollowerStatus) { s.AppliedSeq, s.PrimaryHead = snapshot.Seq, snapshot.Seq })
	return nil
}

type statusError struct {
	code    int
	message string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("the primary hub answered %d: %s", e.code, e.message)
}

func getJSON(ctx context.Context, client *http.Client, path string, out any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://hub"+path, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		var body struct {
			Error string `json:"error"`
		}
		message := string(bytes.TrimSpace(payload))
		if json.Unmarshal(payload, &body) == nil && body.Error != "" {
			message = body.Error
		}
		return &statusError{code: response.StatusCode, message: message}
	}
	return json.NewDecoder(response.Body).Decode(out)
}
