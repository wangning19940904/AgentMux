package server

import (
	"net/http"
	"testing"

	"github.com/wangning19940904/AgentMux/config"
)

// Every machine sees which machines are connected to the hub: a node asks
// the hub over its link, and the hub answers from the connections it holds.
func TestEveryMachineSeesWhichMachinesAreConnectedToTheHub(t *testing.T) {
	const token = "node-bridge-token"
	withToken := func(cfg *config.Config) { cfg.Bridge.Enabled, cfg.Bridge.Token = true, token }
	hub := newLiveServer(t, nil)
	first := newLiveServer(t, withToken)
	second := newLiveServer(t, withToken)
	firstID := joinNode(t, hub, first, token)
	secondID := joinNode(t, hub, second, token)

	fleetOf := func(machine liveServer, auth string) fleetView {
		var view fleetView
		if code := machine.call(t, http.MethodGet, "/api/v1/fleet", auth, nil, &view); code != http.StatusOK {
			t.Fatalf("fleet status = %d", code)
		}
		return view
	}
	find := func(view fleetView, id string) (fleetMachine, bool) {
		for _, machine := range view.Machines {
			if machine.ID == id {
				return machine, true
			}
		}
		return fleetMachine{}, false
	}

	eventually(t, "the first node to see the second online through the hub", func() bool {
		view := fleetOf(first, token)
		other, ok := find(view, secondID)
		return view.Source == fleetSourceHub && ok && other.Online
	})
	view := fleetOf(first, token)
	if _, ok := find(view, firstID); ok {
		t.Fatalf("a node lists itself: %+v", view.Machines)
	}
	if hubMachine, ok := find(view, "hub"); !ok || !hubMachine.Online || hubMachine.HubRole != "hub" {
		t.Fatalf("the hub is missing from %+v", view.Machines)
	}

	local := fleetOf(hub, "")
	if local.Source != fleetSourceLocal || len(local.Machines) != 2 {
		t.Fatalf("hub fleet = %+v", local)
	}

	second.node.stop()
	eventually(t, "the first node to see the second go offline", func() bool {
		other, ok := find(fleetOf(first, token), secondID)
		return ok && !other.Online && other.LastSeenAt != nil
	})
}

// A hub joined to itself, as a primary is after taking over, lists itself
// once, under its own record.
func TestAHubJoinedToItselfIsListedOnceAsTheHub(t *testing.T) {
	const token = "node-bridge-token"
	hub := newLiveServer(t, nil)
	node := newLiveServer(t, func(cfg *config.Config) { cfg.Bridge.Enabled, cfg.Bridge.Token = true, token })
	joinNode(t, hub, node, token)
	hubID := joinNode(t, hub, hub, "")

	var hubs []fleetMachine
	eventually(t, "the node to see the hub through its own record", func() bool {
		var view fleetView
		node.call(t, http.MethodGet, "/api/v1/fleet", token, nil, &view)
		hubs = hubs[:0]
		for _, machine := range view.Machines {
			if machine.HubRole == "hub" || machine.ID == hubID {
				hubs = append(hubs, machine)
			}
		}
		return len(hubs) == 1 && hubs[0].ID == hubID && hubs[0].HubRole == "hub" && hubs[0].Online
	})
}
