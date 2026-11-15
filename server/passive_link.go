package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	linkpkg "github.com/wangning19940904/AgentMux/link"
	"github.com/wangning19940904/AgentMux/store"
)

// Passive link (link/passive.go): a node that listens, and the hub that dials
// it. A machine plays either part independently, as with the outbound link:
// the hub half adds listening nodes and keeps a connection to each; the node
// half listens for the one hub that paired with it.

// passiveAddWait bounds adding a node: asking it, pairing, and waiting for
// the first connection.
const passiveAddWait = 45 * time.Second

// setupPassiveLink makes the hub's dialer. It dials nothing until a node is
// added, and only while this hub holds the lease (Hub.SetGate).
func (s *Server) setupPassiveLink() {
	s.passiveDialer = s.linkHub.NewPassiveDialer(s.st)
	s.devices.passiveRefresh = s.passiveDialer.Refresh
}

func (s *Server) startPassiveLink(ctx context.Context) {
	if s.passiveDialer != nil {
		go s.passiveDialer.Run(ctx)
	}
}

// refreshPassiveLink reads the listening devices again, as when this hub
// takes over from another and should dial them at once.
func (s *Server) refreshPassiveLink() {
	if s.passiveDialer != nil {
		s.passiveDialer.Refresh()
	}
}

func (s *Server) registerPassiveLinkRoutes() {
	s.mux.HandleFunc("POST /api/v1/devices/passive", s.handlePassiveDeviceAdd)
	s.mux.HandleFunc("POST /api/v1/link/passive", s.handlePassiveListen)
	s.mux.HandleFunc("DELETE /api/v1/link/passive", s.handlePassiveStop)
}

// passiveDeviceView is how the hub stands with a device it dials.
type passiveDeviceView struct {
	Addrs       []string `json:"addrs"`
	Fingerprint string   `json:"fingerprint"`
	linkpkg.PassiveDialStatus
}

// passiveDevices lists the devices this hub dials, with how each stands.
func (s *Server) passiveDevices(ctx context.Context) map[string]passiveDeviceView {
	out := map[string]passiveDeviceView{}
	if s.st == nil {
		return out
	}
	links, err := s.st.PassiveLinks(ctx)
	if err != nil {
		s.log.Warn("list listening devices failed", "err", err)
		return out
	}
	for _, link := range links {
		view := passiveDeviceView{Addrs: link.Addrs, Fingerprint: link.Fingerprint}
		if s.passiveDialer != nil {
			view.PassiveDialStatus, _ = s.passiveDialer.Status(link.DeviceID)
		}
		out[link.DeviceID] = view
	}
	return out
}

// passiveSet is the ids of the devices the hub dials.
func (b *deviceBackend) passiveSet(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	links, err := b.st.PassiveLinks(ctx)
	if err != nil {
		return out
	}
	for _, link := range links {
		out[link.DeviceID] = true
	}
	return out
}

// handlePassiveDeviceAdd adds a node that listens, from its invite. The hub
// asks the node who it is (pinning the invite's key), refuses a machine that
// already dials this hub, pairs, records the device and dials it. Adding the
// same machine again replaces its address, key and credential and keeps its
// device id. It is the administrator's alone: the hub opens a connection to
// an address the request names.
func (s *Server) handlePassiveDeviceAdd(w http.ResponseWriter, r *http.Request) {
	if s.linkHub == nil {
		serviceUnavailable(w, "device pairing")
		return
	}
	var req struct {
		Invite string   `json:"invite"`
		Name   string   `json:"name"`
		Addrs  []string `json:"addrs"`
	}
	if !decodeJSONInto(w, r, &req) {
		return
	}
	invite, err := linkpkg.ParseInvite(req.Invite)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(req.Addrs) > 0 {
		if invite.Addrs, err = linkpkg.NormalizePassiveAddrs(req.Addrs); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	req.Name = strings.TrimSpace(req.Name)
	if len(req.Name) > 120 {
		writeErr(w, http.StatusBadRequest, "a name of at most 120 characters is required")
		return
	}
	// A standby cannot write hub data, and would use the invite up for
	// nothing.
	if err := s.linkHub.Admits(); err != nil {
		writeErr(w, http.StatusServiceUnavailable, "this hub does not take devices now ("+err.Error()+"); add the node on the primary hub")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), passiveAddWait)
	defer cancel()
	info, addr, err := linkpkg.PassiveInfo(ctx, invite)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	// One mode at a time: checked here before the invite is used up, and
	// again when the device is saved.
	if conflict, err := s.outboundDeviceForMachine(ctx, info.MachineUID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	} else if conflict != "" {
		writeErr(w, http.StatusConflict, store.ErrMachinePairedOutbound.Error()+" (device "+conflict+")")
		return
	}
	credential := linkpkg.NewPassiveCredential()
	// The node gets the credential before the hub records it: a node that
	// cannot be paired leaves nothing behind on the hub. The node learns its
	// device id when the hub first connects.
	if _, err := linkpkg.PassivePair(ctx, invite, addr, credential, "", req.Name); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	// The address that answered is dialed first.
	addrs := append([]string{addr}, removeString(invite.Addrs, addr)...)
	device, replaced, err := s.st.SavePassiveDevice(ctx, req.Name, requestPrincipal(r).OwnerAccountID(), info,
		core.PassiveLink{Addrs: addrs, Fingerprint: invite.Fingerprint, Credential: credential})
	if errors.Is(err, store.ErrMachinePairedOutbound) {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "the node was paired but the hub could not record it; make a new invite on the node and add it again: "+err.Error())
		return
	}
	s.log.Info("listening device added", "device", device.ID, "name", device.Name, "addr", addr, "replaced", replaced)
	// A standby hub receives the device before it is reported added, as for
	// a device that pairs itself.
	s.linkHub.RunPairedHook(ctx)
	s.refreshPassiveLink()
	waitCtx, stop := context.WithTimeout(ctx, 10*time.Second)
	online := s.linkHub.WaitConnected(waitCtx, device.ID)
	stop()
	view := passiveDeviceView{Addrs: addrs, Fingerprint: invite.Fingerprint}
	if s.passiveDialer != nil {
		view.PassiveDialStatus, _ = s.passiveDialer.Status(device.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"device": device, "replaced": replaced, "online": online, "passive": view})
}

// outboundDeviceForMachine returns a device that is this machine and dials
// the hub, or "".
func (s *Server) outboundDeviceForMachine(ctx context.Context, machineUID string) (string, error) {
	if machineUID == "" {
		return "", nil
	}
	devices, err := s.st.ListDevices(ctx)
	if err != nil {
		return "", err
	}
	passive := s.devices.passiveSet(ctx)
	for _, device := range devices {
		if device.MachineUID == machineUID && !passive[device.ID] {
			return device.ID, nil
		}
	}
	return "", nil
}

func removeString(values []string, drop string) []string {
	var out []string
	for _, value := range values {
		if value != drop {
			out = append(out, value)
		}
	}
	return out
}

// Node side.

// openPassive reads this machine's passive link and listens when it was told
// to: by [hub] passive_listen, or by an earlier `amux join --listen`.
func (n *nodeLink) openPassive() {
	s := n.server
	state, ok, err := n.loadPassiveState()
	if err != nil {
		s.log.Warn("passive link state could not be read", "err", err)
		return
	}
	listen := s.cfg.Hub.PassiveListen
	if listen == "" && ok {
		listen = state.Listen
	}
	if listen == "" {
		return
	}
	node, err := n.passiveNode()
	if err != nil {
		s.log.Warn("passive link unavailable", "err", err)
		return
	}
	if err := node.Serve(listen); err != nil {
		s.log.Error("passive link could not listen", "err", err)
	}
}

func (n *nodeLink) loadPassiveState() (linkpkg.PassiveState, bool, error) {
	path, err := linkpkg.PassiveStatePath(n.server.cfg.Hub.StateDir)
	if err != nil {
		return linkpkg.PassiveState{}, false, err
	}
	return linkpkg.LoadPassiveState(path)
}

// passiveNode returns this machine's listener, opening it on first use.
// Its connections end with the server.
func (n *nodeLink) passiveNode() (*linkpkg.PassiveNode, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.passive != nil {
		return n.passive, nil
	}
	ctx := n.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	s := n.server
	token := ""
	if s.cfg.Bridge.Enabled {
		token = s.cfg.Bridge.Token
	}
	info := linkpkg.LocalDeviceInfo("", s.version)
	info.MachineUID = n.uid
	node := &linkpkg.PassiveNode{
		Dir: s.cfg.Hub.StateDir, Info: info,
		Handler: linkpkg.LocalAPIHandler(linkpkg.LoopbackAddr(s.cfg.Server.Addr), token), Log: s.log,
		ReportInterval: n.reportInterval,
	}
	if err := node.Open(ctx); err != nil {
		return nil, err
	}
	n.passive = node
	return node, nil
}

// passiveActive reports whether this machine listens for a hub or is paired
// with one that way.
func (n *nodeLink) passiveActive() bool {
	n.mu.Lock()
	node := n.passive
	n.mu.Unlock()
	if node != nil {
		status := node.Status()
		return status.Listening || status.Paired
	}
	state, ok, _ := n.loadPassiveState()
	return ok && (state.Listen != "" || state.Paired())
}

func (n *nodeLink) passiveStatus() *linkpkg.PassiveStatus {
	n.mu.Lock()
	node := n.passive
	n.mu.Unlock()
	if node == nil {
		return nil
	}
	status := node.Status()
	return &status
}

// handlePassiveListen makes this machine listen for its hub and returns a new
// invite, which the hub's administrator adds it with. A machine that dials a
// hub has to leave it first: a machine is in one mode at a time.
func (s *Server) handlePassiveListen(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Listen string   `json:"listen"`
		Addrs  []string `json:"addrs"`
	}
	if r.ContentLength != 0 && !decodeJSONInto(w, r, &req) {
		return
	}
	path, err := s.node.statePath()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if existing, ok, _ := linkpkg.LoadState(path); ok {
		writeErr(w, http.StatusConflict, "this machine dials the hub at "+existing.HubURL+"; a machine is linked one way at a time, so leave that hub first (amux leave)")
		return
	}
	node, err := s.node.passiveNode()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	listen := strings.TrimSpace(req.Listen)
	if listen == "" {
		listen = firstNonEmptyString(s.cfg.Hub.PassiveListen, node.State().Listen, linkpkg.DefaultPassiveListen)
	}
	if err := node.Serve(listen); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	addrs := req.Addrs
	if len(addrs) == 0 {
		addrs = s.cfg.Hub.PassiveAddrs
	}
	invite, err := node.Invite(addrs, "")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"invite": invite.String(), "addrs": invite.Addrs, "fingerprint": invite.Fingerprint,
		"listen": node.ListenAddr(), "expires_at": time.Unix(invite.Expires, 0).UTC(),
		"command": "amux hub add " + invite.String(),
	})
}

// handlePassiveStop stops listening and forgets the hub that paired this way.
// The hub keeps the device in its list, offline, until it is removed there.
func (s *Server) handlePassiveStop(w http.ResponseWriter, _ *http.Request) {
	node, err := s.node.passiveNode()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := node.Forget(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w)
}
