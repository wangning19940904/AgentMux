package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/wangning19940904/AgentMux/config"
)

// A standby reaches the primary's machines, and the primary itself, through
// the primary, and sees which of them are connected. Other machines may not.
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

	statusVia := func(from liveServer, id string) (map[string]any, error) {
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
		var status map[string]any
		err = json.NewDecoder(response.Body).Decode(&status)
		return status, err
	}

	eventually(t, "the standby to see the node connected", func() bool {
		standby.fleet.mu.Lock()
		standby.fleet.presenceAt = standby.fleet.presenceAt.AddDate(-1, 0, 0)
		standby.fleet.mu.Unlock()
		return standby.deviceOnline(ctx, nodeID)
	})
	status, err := statusVia(standby, nodeID)
	if err != nil || status["version"] != "node-version" {
		t.Fatalf("node status through the hub = %v, %v", status, err)
	}
	status, err = statusVia(standby, hubID)
	if err != nil || status["version"] != hub.version {
		t.Fatalf("hub status through the hub = %v, %v", status, err)
	}
	node.primaryHubForTest = &no
	if _, err := statusVia(node, standbyID); err == nil {
		t.Fatal("a plain node opened a stream through the hub")
	}
}
