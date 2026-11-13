package hubha

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/wangning19940904/AgentMux/store"
)

// LeasePath is where the relay grants and renews the lease.
const LeasePath = "/hub/v1/lease"

const (
	minLeaseTTL   = time.Second
	maxLeaseTTL   = 2 * time.Minute
	pruneInterval = time.Hour
	// KeepChanges is how long the primary keeps its change log. A standby
	// away longer loads a snapshot.
	KeepChanges = 7 * 24 * time.Hour
)

// ErrNotPrimary is why a hub that is not the primary refuses devices.
var ErrNotPrimary = errors.New("this hub is not the primary right now; devices reach the primary through the relay")

// ControllerStore is the database half of the controller.
type ControllerStore interface {
	HubReplication(ctx context.Context) (store.HubReplicationState, error)
	SetHubRole(ctx context.Context, role store.HubRole, epoch int64) error
	HubLocalRows(ctx context.Context) (map[string]int, error)
	PruneHubChanges(ctx context.Context, cutoff time.Time) error
}

// Controller decides this hub's role from the relay's lease.
//
// The relay renews the lease of the hub it forwards devices to. That hub is
// the primary: it accepts devices and writes hub data. A primary that hears
// nothing for the lease's length fences itself, because the relay may already
// have moved to the other hub. A hub told it is not the primary follows the
// one that is.
type Controller struct {
	Store    ControllerStore
	Secret   string
	Follower *Follower
	// OnFence runs when this hub stops being the primary: drop the devices'
	// connections so they reconnect through the relay to the new primary.
	OnFence func()
	// OnStandby runs when this hub starts following, so it can open a link
	// to the primary if it has none.
	OnStandby func()
	// OnPromote runs when this hub becomes the primary.
	OnPromote func()
	Log       *slog.Logger

	mu         sync.Mutex
	ctx        context.Context
	role       store.HubRole
	epoch      int64
	leaseUntil time.Time
	follow     context.CancelFunc
}

// LeaseRequest is the relay's call.
type LeaseRequest struct {
	Epoch  int64 `json:"epoch"`
	Active bool  `json:"active"`
	TTLms  int64 `json:"ttl_ms"`
}

// LeaseResponse is the hub's answer.
type LeaseResponse struct {
	Role       store.HubRole `json:"role"`
	Epoch      int64         `json:"epoch"`
	AppliedSeq int64         `json:"applied_seq"`
	Ready      bool          `json:"ready"`
}

// Status is the controller's view, for the Console.
type Status struct {
	store.HubReplicationState
	LeaseUntil *time.Time      `json:"lease_until,omitempty"`
	Ready      bool            `json:"ready"`
	Follower   *FollowerStatus `json:"follower,omitempty"`
}

// Start reads the stored role and runs until ctx ends. A primary starts
// fenced: it cannot know whether its lease survived the restart until the
// relay renews it, which the relay does within seconds.
func (c *Controller) Start(ctx context.Context) error {
	if c.Log == nil {
		c.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	state, err := c.Store.HubReplication(ctx)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.ctx = ctx
	c.role, c.epoch = state.Role, state.Epoch
	c.mu.Unlock()
	if state.Role == store.HubRolePrimary {
		if err := c.setRole(ctx, store.HubRoleFenced, state.Epoch); err != nil {
			return err
		}
	}
	if state.Role == store.HubRoleStandby {
		c.startFollowing()
	}
	go c.watch(ctx)
	return nil
}

// Accepting says whether devices may connect and pair now.
func (c *Controller) Accepting() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case c.role == store.HubRoleNone:
		return nil
	case c.role == store.HubRolePrimary && time.Now().Before(c.leaseUntil):
		return nil
	default:
		return ErrNotPrimary
	}
}

// Status reports the role, lease and the follower's progress.
func (c *Controller) Status(ctx context.Context) (Status, error) {
	state, err := c.Store.HubReplication(ctx)
	if err != nil {
		return Status{}, err
	}
	out := Status{HubReplicationState: state, Ready: c.ready(ctx, state)}
	c.mu.Lock()
	if !c.leaseUntil.IsZero() && state.Role == store.HubRolePrimary {
		until := c.leaseUntil
		out.LeaseUntil = &until
	}
	c.mu.Unlock()
	if c.Follower != nil && state.Role == store.HubRoleStandby {
		follower := c.Follower.Status()
		out.Follower = &follower
	}
	return out, nil
}

// ServeLease answers the relay.
func (c *Controller) ServeLease(w http.ResponseWriter, r *http.Request) {
	token, _ := strings.CutPrefix(strings.TrimSpace(r.Header.Get("Authorization")), "Bearer ")
	if c.Secret == "" || subtle.ConstantTimeCompare([]byte(token), []byte(c.Secret)) != 1 {
		writeError(w, http.StatusUnauthorized, "wrong lease secret")
		return
	}
	var request LeaseRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid lease request")
		return
	}
	ttl := min(max(time.Duration(request.TTLms)*time.Millisecond, minLeaseTTL), maxLeaseTTL)
	received := time.Now()
	var err error
	if request.Active {
		err = c.grant(r.Context(), request.Epoch, received.Add(ttl))
	} else {
		err = c.release(r.Context(), request.Epoch)
	}
	if err != nil {
		c.Log.Warn("hub lease call refused", "active", request.Active, "epoch", request.Epoch, "err", err)
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	state, err := c.Store.HubReplication(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "the hub could not read its replication state")
		return
	}
	writeJSON(w, LeaseResponse{Role: state.Role, Epoch: state.Epoch, AppliedSeq: state.AppliedSeq, Ready: c.ready(r.Context(), state)})
}

// grant makes or keeps this hub the primary for epoch until until.
func (c *Controller) grant(ctx context.Context, epoch int64, until time.Time) error {
	c.mu.Lock()
	role, current := c.role, c.epoch
	c.mu.Unlock()
	if epoch < current {
		return errors.New("the lease is for an older epoch than this hub has seen")
	}
	if role != store.HubRolePrimary || epoch != current {
		state, err := c.Store.HubReplication(ctx)
		if err != nil {
			return err
		}
		if !c.ready(ctx, state) {
			return errors.New("this hub has no copy of hub data to serve yet")
		}
		c.stopFollowing()
		if err := c.setRole(ctx, store.HubRolePrimary, epoch); err != nil {
			return err
		}
		c.Log.Info("this hub is now the primary", "epoch", epoch, "was", string(role))
		if c.OnPromote != nil {
			c.OnPromote()
		}
	}
	c.mu.Lock()
	c.leaseUntil = until
	c.mu.Unlock()
	return nil
}

// release follows the relay's word that another hub is the primary.
func (c *Controller) release(ctx context.Context, epoch int64) error {
	c.mu.Lock()
	role, current := c.role, c.epoch
	c.mu.Unlock()
	// The relay before it chose anyone, or a relay that has not caught up,
	// says nothing about this hub.
	if epoch == 0 || epoch < current {
		return nil
	}
	if role == store.HubRoleStandby {
		if epoch != current {
			if err := c.setRole(ctx, store.HubRoleStandby, epoch); err != nil {
				return err
			}
		}
		c.startFollowing()
		return nil
	}
	if err := c.setRole(ctx, store.HubRoleStandby, epoch); err != nil {
		return err
	}
	c.mu.Lock()
	c.leaseUntil = time.Time{}
	c.mu.Unlock()
	c.Log.Info("this hub now follows the primary", "epoch", epoch, "was", string(role))
	if c.OnFence != nil {
		c.OnFence()
	}
	if c.OnStandby != nil {
		c.OnStandby()
	}
	c.startFollowing()
	return nil
}

// ready says whether this hub may be made the primary: it holds hub data.
func (c *Controller) ready(ctx context.Context, state store.HubReplicationState) bool {
	switch state.Role {
	case store.HubRolePrimary, store.HubRoleFenced:
		return true
	case store.HubRoleStandby:
		return state.AppliedSeq > 0
	default:
		rows, err := c.Store.HubLocalRows(ctx)
		return err == nil && len(rows) > 0
	}
}

func (c *Controller) setRole(ctx context.Context, role store.HubRole, epoch int64) error {
	if err := c.Store.SetHubRole(ctx, role, epoch); err != nil {
		return err
	}
	c.mu.Lock()
	c.role, c.epoch = role, epoch
	c.mu.Unlock()
	return nil
}

// watch fences a primary whose lease ran out and prunes its change log.
func (c *Controller) watch(ctx context.Context) {
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	lastPrune := time.Time{}
	for {
		select {
		case <-ctx.Done():
			c.stopFollowing()
			return
		case <-tick.C:
		}
		c.mu.Lock()
		role, epoch, until := c.role, c.epoch, c.leaseUntil
		c.mu.Unlock()
		if role != store.HubRolePrimary {
			continue
		}
		if time.Now().After(until) {
			if err := c.setRole(ctx, store.HubRoleFenced, epoch); err != nil {
				c.Log.Warn("fence the hub failed", "err", err)
				continue
			}
			c.Log.Warn("the relay stopped renewing this hub's lease; it no longer accepts devices", "epoch", epoch)
			if c.OnFence != nil {
				c.OnFence()
			}
			continue
		}
		if time.Since(lastPrune) >= pruneInterval {
			lastPrune = time.Now()
			if err := c.Store.PruneHubChanges(ctx, time.Now().Add(-KeepChanges)); err != nil {
				c.Log.Warn("prune hub changes failed", "err", err)
			}
		}
	}
}

func (c *Controller) startFollowing() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.follow != nil || c.Follower == nil || c.ctx == nil {
		return
	}
	ctx, cancel := context.WithCancel(c.ctx)
	c.follow = cancel
	go c.Follower.Run(ctx)
}

func (c *Controller) stopFollowing() {
	c.mu.Lock()
	cancel := c.follow
	c.follow = nil
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
