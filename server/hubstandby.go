package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/hubha"
	linkpkg "github.com/wangning19940904/AgentMux/link"
	"github.com/wangning19940904/AgentMux/store"
)

// A standby hub (hubha). It is on when [hub] lease_secret is set: a relay in
// front of two hubs then grants one of them the lease, and the other follows
// its data over a device link so it can take over.

const (
	hubPeerFile = "hub-peer.json"
	// pairReplicationWait bounds how long pairing waits for the standby to
	// hold the new device, so it can still connect after a failover.
	pairReplicationWait = 10 * time.Second
)

type hubStandby struct {
	controller *hubha.Controller
	tracker    *hubha.Tracker
	follower   *hubha.Follower
	// peer is true while this machine's link runs on its hub-peer
	// membership rather than one the administrator joined.
	peer bool
}

func (s *Server) hubLeaseSecret() (string, error) {
	if secret := strings.TrimSpace(s.cfg.Hub.LeaseSecret); secret != "" {
		return secret, nil
	}
	if s.cfg.Hub.LeaseSecretFile == "" {
		return "", nil
	}
	data, err := os.ReadFile(s.cfg.Hub.LeaseSecretFile)
	if err != nil {
		return "", fmt.Errorf("hub.lease_secret_file: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

// setupHubStandby builds the controller; uplink is what devices' requests
// reach today, and the result also serves the standby's feed.
func (s *Server) setupHubStandby(uplink http.Handler) http.Handler {
	secret, err := s.hubLeaseSecret()
	if err != nil {
		s.log.Error("standby hub disabled", "err", err)
		return uplink
	}
	if secret == "" {
		return uplink
	}
	if !s.st.IsPostgres() {
		s.log.Error("standby hub disabled: it needs PostgreSQL")
		return uplink
	}
	ha := &hubStandby{tracker: hubha.NewTracker()}
	ha.follower = &hubha.Follower{Sink: s.st, Uplink: s.node.uplink, SaveIdentity: s.saveHubIdentity, Log: s.log}
	ha.controller = &hubha.Controller{
		Store: s.st, Secret: secret, Follower: ha.follower, Log: s.log,
		OnFence:   s.linkHub.Close,
		OnStandby: s.ensureHubPeerLink,
		OnPromote: func() {
			// The devices now reconnect to this hub.
			s.linkHub.MarkStarted()
			s.stopHubPeerLink()
			// Only the primary writes hub data, so a record left from an
			// earlier version is renamed now.
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				s.tidyHubPeerName(ctx)
			}()
		},
	}
	s.hubHA = ha
	s.linkHub.SetGate(ha.controller.Accepting)
	s.linkHub.SetPairedHook(func(ctx context.Context) {
		state, err := s.st.HubReplication(ctx)
		if err != nil || state.Role != store.HubRolePrimary {
			return
		}
		if len(ha.tracker.Standbys()) > 0 && !ha.tracker.WaitFor(ctx, state.HeadSeq, pairReplicationWait) {
			s.log.Warn("the standby hub has not received a newly paired device yet; it cannot connect through the standby until it does")
		}
	})
	feed := hubha.NewFeedHandler(s.st, s.hubIdentity, ha.tracker, s.log)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, hubha.FeedPrefix) {
			feed.ServeHTTP(w, r)
			return
		}
		uplink.ServeHTTP(w, r)
	})
}

// startHubStandby takes up the stored role once the server runs.
func (s *Server) startHubStandby(ctx context.Context) error {
	if s.hubHA == nil {
		return nil
	}
	if err := s.hubHA.controller.Start(ctx); err != nil {
		return fmt.Errorf("standby hub: %w", err)
	}
	if state, err := s.st.HubReplication(ctx); err == nil && state.Role == store.HubRoleStandby {
		s.ensureHubPeerLink()
	}
	return nil
}

// ensureHubPeerLink gives a hub that must follow a link to the primary. A
// machine that joined the hub as a device already has one; a former primary
// uses the membership it made for itself when its standby was set up.
func (s *Server) ensureHubPeerLink() {
	if s.node.uplink() != nil {
		return
	}
	path, err := s.hubPeerPath()
	if err != nil {
		return
	}
	state, ok, err := linkpkg.LoadState(path)
	if err != nil || !ok {
		s.log.Warn("this hub must follow the primary but has no link to it; join it as a device", "err", err)
		return
	}
	s.node.start(state)
	s.hubHA.peer = true
}

// stopHubPeerLink drops the peer membership once this hub is the primary
// again: it would only connect to itself.
func (s *Server) stopHubPeerLink() {
	if s.hubHA != nil && s.hubHA.peer {
		s.node.stop()
		s.hubHA.peer = false
	}
}

// hubPeerDeviceID is the device this hub registered as itself, or "".
func (s *Server) hubPeerDeviceID() string {
	path, err := s.hubPeerPath()
	if err != nil {
		return ""
	}
	state, ok, err := linkpkg.LoadState(path)
	if err != nil || !ok {
		return ""
	}
	return state.DeviceID
}

// deviceHubLabels says which paired machines are hubs, for the machine list.
// A device allowed to follow this hub is the standby while this hub is the
// primary; while this hub follows, it is the one serving as the hub.
func (s *Server) deviceHubLabels(ctx context.Context) map[string]string {
	if s.hubHA == nil {
		return nil
	}
	roles, err := s.st.DeviceHubRoles(ctx)
	if err != nil || len(roles) == 0 {
		return nil
	}
	label := "standby"
	if state, err := s.st.HubReplication(ctx); err == nil && state.Role == store.HubRoleStandby {
		label = "hub"
	}
	out := make(map[string]string, len(roles))
	for id, role := range roles {
		if role == "standby" {
			out[id] = label
		}
	}
	return out
}

// hubPeerSuffix marked this hub's own record in earlier versions, which
// listed it as a machine of its own.
const hubPeerSuffix = " (hub)"

// tidyHubPeerName names this hub's own record after the machine, as every
// other machine is named, so the other hub lists it as this machine.
func (s *Server) tidyHubPeerName(ctx context.Context) {
	path, err := s.hubPeerPath()
	if err != nil {
		return
	}
	state, ok, err := linkpkg.LoadState(path)
	if err != nil || !ok {
		return
	}
	device, err := s.st.GetDevice(ctx, state.DeviceID)
	if err != nil || device == nil || !strings.HasSuffix(device.Name, hubPeerSuffix) {
		return
	}
	name := strings.TrimSuffix(device.Name, hubPeerSuffix)
	if err := s.st.RenameDevice(ctx, device.ID, name); err != nil {
		s.log.Warn("rename this hub's own record", "err", err)
		return
	}
	state.Name = name
	_ = linkpkg.SaveState(path, state)
}

func (s *Server) hubPeerPath() (string, error) {
	dir, err := s.linkStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, hubPeerFile), nil
}

// hubIdentity is the certificate devices pin, for the standby to copy.
func (s *Server) hubIdentity() (hubha.Identity, error) {
	certPath, keyPath := s.cfg.Hub.CertFile, s.cfg.Hub.KeyFile
	if s.cfg.Hub.TLS != "files" {
		if _, _, err := s.hubCertificate(); err != nil {
			return hubha.Identity{}, err
		}
		dir, err := s.linkStateDir()
		if err != nil {
			return hubha.Identity{}, err
		}
		certPath, keyPath = linkpkg.CertificateFiles(dir)
	}
	cert, err := os.ReadFile(certPath)
	if err != nil {
		return hubha.Identity{}, err
	}
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return hubha.Identity{}, err
	}
	return hubha.Identity{Cert: cert, Key: key}, nil
}

// saveHubIdentity makes this hub present the primary's certificate. The hub
// listener reads it on every handshake, so no restart is needed.
func (s *Server) saveHubIdentity(identity hubha.Identity) error {
	if _, err := tls.X509KeyPair(identity.Cert, identity.Key); err != nil {
		return fmt.Errorf("the primary sent an unusable certificate: %w", err)
	}
	if s.cfg.Hub.TLS == "files" {
		s.log.Warn("hub.tls is \"files\": install the primary's certificate there yourself, or devices will not accept this hub after a failover")
		return nil
	}
	dir, err := s.linkStateDir()
	if err != nil {
		return err
	}
	certPath, keyPath := linkpkg.CertificateFiles(dir)
	if current, err := os.ReadFile(certPath); err == nil && string(current) == string(identity.Cert) {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, file := range []struct {
		path string
		data []byte
	}{{keyPath, identity.Key}, {certPath, identity.Cert}} {
		tmp := file.path + ".tmp"
		if err := os.WriteFile(tmp, file.data, 0o600); err != nil {
			return err
		}
		if err := os.Rename(tmp, file.path); err != nil {
			return err
		}
	}
	s.log.Info("this hub now presents the primary hub's certificate")
	return nil
}

func (s *Server) registerHubStandbyRoutes() {
	s.mux.HandleFunc("GET /api/v1/hub/replication", s.handleHubReplication)
	s.mux.HandleFunc("POST /api/v1/hub/standby", s.handleHubStandby)
}

// handleHubReplication reports this machine's part as a hub: whether it
// serves as one at all (hub), its name for the machine list, and, with a
// standby hub set up, the replication status.
func (s *Server) handleHubReplication(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	hostname, _ := os.Hostname()
	out := map[string]any{"enabled": false, "hub": s.linkHub != nil, "name": strings.TrimSuffix(hostname, ".local")}
	if s.hubHA == nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	status, err := s.hubHA.controller.Status(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out["enabled"], out["status"], out["standbys"] = true, status, s.hubHA.tracker.Standbys()
	writeJSON(w, http.StatusOK, out)
}

// handleHubStandby lets a paired device follow this hub's data, or stops it.
// Allowing one also gives this hub a membership of its own, which it uses to
// follow the other hub if it ever loses the lease.
func (s *Server) handleHubStandby(w http.ResponseWriter, r *http.Request) {
	if s.hubHA == nil {
		writeErr(w, http.StatusServiceUnavailable, "set [hub] lease_secret on both hubs and the relay first")
		return
	}
	var req struct {
		DeviceID string `json:"device_id"`
		Enabled  *bool  `json:"enabled"`
	}
	if !decodeJSONInto(w, r, &req) {
		return
	}
	enabled := req.Enabled == nil || *req.Enabled
	role := ""
	if enabled {
		role = "standby"
	}
	if err := s.st.SetDeviceHubRole(r.Context(), req.DeviceID, role); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !enabled {
		writeOK(w)
		return
	}
	peer, err := s.ensureHubPeerMembership(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "the standby is allowed, but this hub could not register itself for a later failover: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "device_id": req.DeviceID, "peer_device_id": peer.DeviceID})
}

// ensureHubPeerMembership registers this hub as a standby-capable device of
// itself. The record is hub data, so the other hub holds it too and accepts
// this machine when it has to follow. It is this machine's own entry: this hub
// leaves it out of its machine list, and the other hub lists it under the
// machine's name.
func (s *Server) ensureHubPeerMembership(ctx context.Context) (linkpkg.State, error) {
	path, err := s.hubPeerPath()
	if err != nil {
		return linkpkg.State{}, err
	}
	if state, ok, err := linkpkg.LoadState(path); err != nil || ok {
		if ok {
			if device, _ := s.st.GetDevice(ctx, state.DeviceID); device != nil {
				s.tidyHubPeerName(ctx)
				return state, nil
			}
		} else {
			return linkpkg.State{}, err
		}
	}
	address, err := s.hubJoinAddress("", nil)
	if err != nil {
		return linkpkg.State{}, err
	}
	if address.warning != "" {
		return linkpkg.State{}, errors.New(address.warning)
	}
	hostname, _ := os.Hostname()
	name := strings.TrimSuffix(hostname, ".local")
	code, err := s.st.CreateDevicePairingCode(ctx, name)
	if err != nil {
		return linkpkg.State{}, err
	}
	info := linkpkg.LocalDeviceInfo(name, s.version)
	device, token, err := s.st.RedeemDevicePairingCode(ctx, code.Code, core.DeviceInfo(info))
	if err != nil || device == nil {
		return linkpkg.State{}, fmt.Errorf("register this hub: %v", err)
	}
	if err := s.st.SetDeviceHubRole(ctx, device.ID, "standby"); err != nil {
		return linkpkg.State{}, err
	}
	state := linkpkg.State{HubURL: address.url, Fingerprint: address.fingerprint, DeviceID: device.ID, Token: token, Name: device.Name, JoinedAt: time.Now().UTC()}
	if err := linkpkg.SaveState(path, state); err != nil {
		return linkpkg.State{}, err
	}
	return state, nil
}
