package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/config"
)

// A standby reaches the primary's machines, and the primary itself, through
// the primary, and sees which of them are connected, whoever owns them.
func TestAStandbyReachesThePrimarysMachinesThroughIt(t *testing.T) {
	const token = "node-bridge-token"
	withToken := func(cfg *config.Config) { cfg.Bridge.Enabled, cfg.Bridge.Token = true, token }
	yes, no := true, false
	hub := newLiveServer(t, nil)
	hub.primaryHubForTest = &yes
	standby := newLiveServer(t, withToken)
	standby.primaryHubForTest = &no
	node := newLiveServer(t, withToken)
	node.version = "node-version"
	standbyID := joinNode(t, hub, standby, token)
	nodeID := joinNode(t, hub, node, token)
	hubID := joinNode(t, hub, hub, "")
	ctx := context.Background()
	hub.standbyForTest = standbyID

	eventually(t, "the standby to see the node connected", func() bool {
		standby.fleet.mu.Lock()
		standby.fleet.presenceAt = standby.fleet.presenceAt.AddDate(-1, 0, 0)
		standby.fleet.mu.Unlock()
		return standby.deviceOnline(ctx, nodeID)
	})
	status, err := statusThroughHub(ctx, standby, nodeID)
	if err != nil || status["version"] != "node-version" {
		t.Fatalf("node status through the hub = %v, %v", status, err)
	}
	status, err = statusThroughHub(ctx, standby, hubID)
	if err != nil || status["version"] != hub.version {
		t.Fatalf("hub status through the hub = %v, %v", status, err)
	}
	// The standby reaches a machine of another owner; a plain node does not.
	if err := hub.st.SetDeviceOwner(ctx, nodeID, "acct_someone_else"); err != nil {
		t.Fatal(err)
	}
	if status, err := statusThroughHub(ctx, standby, nodeID); err != nil || status["version"] != "node-version" {
		t.Fatalf("another owner's node through the hub = %v, %v", status, err)
	}
	node.primaryHubForTest = &no
	if _, err := statusThroughHub(ctx, node, standbyID); err == nil {
		t.Fatal("a node opened a stream to another owner's machine through the hub")
	}
}

// A plain node lists the machines its hub lists for it and reaches them, and
// the hub itself, through the hub: its Console then shows the whole fleet.
// It cannot unpair them, and loses a machine that passes to another owner.
func TestANodeReachesItsOwnersMachinesThroughTheHub(t *testing.T) {
	const token = "node-bridge-token"
	withToken := func(cfg *config.Config) { cfg.Bridge.Enabled, cfg.Bridge.Token = true, token }
	yes, no := true, false
	hub := newLiveServer(t, nil)
	hub.primaryHubForTest = &yes
	first := newLiveServer(t, withToken)
	first.primaryHubForTest = &no
	second := newLiveServer(t, withToken)
	second.primaryHubForTest = &no
	second.version = "second-version"
	joinNode(t, hub, first, token)
	secondID := joinNode(t, hub, second, token)
	hubID := joinNode(t, hub, hub, "")
	ctx := context.Background()

	listed := func() map[string]bool {
		first.fleet.mu.Lock()
		first.fleet.presenceAt = time.Time{}
		first.fleet.mu.Unlock()
		online := map[string]bool{}
		for _, host := range first.remote.List() {
			online[host.ID] = host.Online != nil && *host.Online
		}
		return online
	}
	eventually(t, "the node to list the other node and the hub online", func() bool {
		online := listed()
		return online[secondID] && online[hubID]
	})

	if status, err := statusThroughHub(ctx, first, secondID); err != nil || status["version"] != "second-version" {
		t.Fatalf("other node through the hub = %v, %v", status, err)
	}
	if status, err := statusThroughHub(ctx, first, hubID); err != nil || status["version"] != hub.version {
		t.Fatalf("hub through the hub = %v, %v", status, err)
	}
	if err := first.remote.Delete(secondID); !errors.Is(err, errHubListedMachine) {
		t.Fatalf("unpairing a machine of the hub from a node = %v", err)
	}

	if err := hub.st.SetDeviceOwner(ctx, secondID, "acct_someone_else"); err != nil {
		t.Fatal(err)
	}
	if _, err := statusThroughHub(ctx, first, secondID); err == nil {
		t.Fatal("a node reached another owner's machine through the hub")
	}
	if _, still := listed()[secondID]; still {
		t.Fatal("a node still lists another owner's machine")
	}
}

// statusThroughHub asks a machine for its status over a stream that from
// opens through its hub.
func statusThroughHub(ctx context.Context, from liveServer, id string) (map[string]any, error) {
	conn, err := from.dialViaHub(ctx, id)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	request, _ := http.NewRequest(http.MethodGet, "http://machine/api/v1/status", nil)
	if err := request.Write(conn); err != nil {
		return nil, err
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status answered HTTP %d", response.StatusCode)
	}
	var status map[string]any
	err = json.NewDecoder(response.Body).Decode(&status)
	return status, err
}
