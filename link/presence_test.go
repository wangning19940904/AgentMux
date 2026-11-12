package link

import (
	"net"
	"testing"

	"github.com/hashicorp/yamux"
)

func testSession(t *testing.T) *hubSession {
	t.Helper()
	local, remote := net.Pipe()
	t.Cleanup(func() { _ = remote.Close() })
	mux, err := yamux.Client(local, muxConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mux.Close() })
	return &hubSession{mux: mux}
}

func TestHubReportsPresenceChanges(t *testing.T) {
	hub := NewHub(nil, nil)
	type change struct {
		id     string
		online bool
	}
	var changes []change
	hub.SetPresenceListener(func(id string, online bool) { changes = append(changes, change{id, online}) })

	first, second := testSession(t), testSession(t)
	hub.attach("d1", first)
	// A reconnect that replaces a live connection is no change, and the old
	// connection ending afterwards is not one either.
	hub.attach("d1", second)
	hub.detach("d1", first)
	hub.detach("d1", second)
	// A device dropped on purpose (unpaired) is not reported.
	hub.attach("d2", testSession(t))
	hub.Disconnect("d2")

	want := []change{{"d1", true}, {"d1", false}, {"d2", true}}
	if len(changes) != len(want) {
		t.Fatalf("changes = %+v, want %+v", changes, want)
	}
	for i := range want {
		if changes[i] != want[i] {
			t.Fatalf("changes = %+v, want %+v", changes, want)
		}
	}
}
