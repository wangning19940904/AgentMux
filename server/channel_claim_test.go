package server

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

type fakeChannelPeers struct {
	targets  []channelPeerTarget
	channels map[string][]core.Channel
	listErr  map[string]error
	validErr error
	validate []string
	onEnable func(string, core.Channel)
}

func (f *fakeChannelPeers) Targets() []channelPeerTarget { return f.targets }

func (f *fakeChannelPeers) ListChannels(_ context.Context, id string) ([]core.Channel, error) {
	if err := f.listErr[id]; err != nil {
		return nil, err
	}
	out := make([]core.Channel, len(f.channels[id]))
	copy(out, f.channels[id])
	return out, nil
}

func (f *fakeChannelPeers) ValidateChannel(_ context.Context, id string, channel core.Channel) (*core.Channel, error) {
	f.validate = append(f.validate, id)
	if f.validErr != nil {
		return nil, f.validErr
	}
	validated := channel
	return &validated, nil
}

func (f *fakeChannelPeers) UpsertChannel(_ context.Context, id string, channel core.Channel) (*core.Channel, error) {
	if channel.ID == "" {
		channel.ID = "channel-new-remote"
	}
	items := f.channels[id]
	updated := false
	for i := range items {
		if items[i].ID == channel.ID {
			items[i] = channel
			updated = true
			break
		}
	}
	if !updated {
		items = append(items, channel)
	}
	f.channels[id] = items
	if channel.Enabled && f.onEnable != nil {
		f.onEnable(id, channel)
	}
	return &channel, nil
}

func claimTestChannel(id, name, appID string, enabled bool) core.Channel {
	now := time.Now()
	return core.Channel{
		ID: id, Name: name, Type: "feishu", Enabled: enabled,
		Config:    map[string]string{"app_id": appID, "app_secret": "secret"},
		CreatedAt: now, UpdatedAt: now,
	}
}

func TestChannelClaimDisablesPreviousLocalConnection(t *testing.T) {
	s, st := newTestServer(t)
	ctx := context.Background()
	old := claimTestChannel("channel-old", "Old local", "cli_same", true)
	other := claimTestChannel("channel-other", "Other app", "cli_other", true)
	for _, channel := range []*core.Channel{&old, &other} {
		if err := st.UpsertChannel(ctx, channel); err != nil {
			t.Fatal(err)
		}
	}

	rec := doJSON(t, s, http.MethodPost, "/api/v1/remote/channels/claim", channelClaimRequest{
		Channel: claimTestChannel("", "New local", "cli_same", true),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("claim: code=%d body=%s", rec.Code, rec.Body.String())
	}
	storedOld, _ := st.GetChannel(ctx, old.ID)
	storedOther, _ := st.GetChannel(ctx, other.ID)
	if storedOld == nil || storedOld.Enabled {
		t.Fatalf("old channel was not disabled: %+v", storedOld)
	}
	if storedOther == nil || !storedOther.Enabled {
		t.Fatalf("unrelated channel was disabled: %+v", storedOther)
	}
	channels, err := st.ListChannels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	enabledSameApp := 0
	for _, channel := range channels {
		if channel.Enabled && channel.Config["app_id"] == "cli_same" {
			enabledSameApp++
		}
	}
	if enabledSameApp != 1 {
		t.Fatalf("enabled same-app channels=%d, want 1; channels=%+v", enabledSameApp, channels)
	}
}

func TestDirectChannelUpsertAlsoEnforcesLocalExclusivity(t *testing.T) {
	s, st := newTestServer(t)
	ctx := context.Background()
	old := claimTestChannel("channel-old", "Old local", "cli_same", true)
	if err := st.UpsertChannel(ctx, &old); err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, s, http.MethodPost, "/api/v1/channels", claimTestChannel("", "New local", "cli_same", true))
	if rec.Code != http.StatusOK {
		t.Fatalf("upsert: code=%d body=%s", rec.Code, rec.Body.String())
	}
	stored, err := st.GetChannel(ctx, old.ID)
	if err != nil || stored == nil || stored.Enabled {
		t.Fatalf("old direct-API channel was not disabled: channel=%+v err=%v", stored, err)
	}
}

func TestChannelClaimDisablesLocalAndRemoteBeforeEnablingTarget(t *testing.T) {
	s, st := newTestServer(t)
	ctx := context.Background()
	localOld := claimTestChannel("channel-local-old", "Local old", "cli_same", true)
	if err := st.UpsertChannel(ctx, &localOld); err != nil {
		t.Fatal(err)
	}
	peers := &fakeChannelPeers{
		targets: []channelPeerTarget{{ID: "target", Name: "Target"}, {ID: "peer", Name: "Peer"}},
		channels: map[string][]core.Channel{
			"target": {claimTestChannel("channel-target-old", "Target old", "cli_same", true)},
			"peer":   {claimTestChannel("channel-peer-old", "Peer old", "cli_same", true)},
		},
		listErr: map[string]error{},
	}
	peers.onEnable = func(id string, _ core.Channel) {
		if id != "target" {
			t.Fatalf("new channel enabled on %q", id)
		}
		stored, err := st.GetChannel(ctx, localOld.ID)
		if err != nil || stored == nil || stored.Enabled {
			t.Fatalf("local old channel still enabled when target started: channel=%+v err=%v", stored, err)
		}
		for peerID, channels := range peers.channels {
			for _, channel := range channels {
				if channel.ID != "channel-new-remote" && channel.Config["app_id"] == "cli_same" && channel.Enabled {
					t.Fatalf("old channel %s on %s still enabled when target started", channel.ID, peerID)
				}
			}
		}
	}
	s.channelPeers = peers

	rec := doJSON(t, s, http.MethodPost, "/api/v1/remote/channels/claim", channelClaimRequest{
		TargetID: "target",
		Channel:  claimTestChannel("", "New remote", "cli_same", true),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("claim: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(peers.validate) != 1 || peers.validate[0] != "target" {
		t.Fatalf("validated targets=%v", peers.validate)
	}
}

func TestChannelClaimScanFailureLeavesExistingConnectionRunning(t *testing.T) {
	s, st := newTestServer(t)
	ctx := context.Background()
	localOld := claimTestChannel("channel-local-old", "Local old", "cli_same", true)
	if err := st.UpsertChannel(ctx, &localOld); err != nil {
		t.Fatal(err)
	}
	s.channelPeers = &fakeChannelPeers{
		targets:  []channelPeerTarget{{ID: "target", Name: "Target"}, {ID: "offline", Name: "Offline"}},
		channels: map[string][]core.Channel{"target": nil},
		listErr:  map[string]error{"offline": errors.New("unreachable")},
	}

	rec := doJSON(t, s, http.MethodPost, "/api/v1/remote/channels/claim", channelClaimRequest{
		TargetID: "target",
		Channel:  claimTestChannel("", "New remote", "cli_same", true),
	})
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("claim: code=%d body=%s", rec.Code, rec.Body.String())
	}
	stored, err := st.GetChannel(ctx, localOld.ID)
	if err != nil || stored == nil || !stored.Enabled {
		t.Fatalf("existing channel changed after failed scan: channel=%+v err=%v", stored, err)
	}
}

func TestChannelClaimSupportsLegacyRemoteWithoutValidationEndpoint(t *testing.T) {
	s, _ := newTestServer(t)
	peers := &fakeChannelPeers{
		targets:  []channelPeerTarget{{ID: "legacy", Name: "Legacy"}},
		channels: map[string][]core.Channel{"legacy": nil},
		listErr:  map[string]error{},
		validErr: &channelPeerHTTPError{Status: http.StatusMethodNotAllowed, Message: "method not allowed"},
	}
	s.channelPeers = peers

	rec := doJSON(t, s, http.MethodPost, "/api/v1/remote/channels/claim", channelClaimRequest{
		TargetID: "legacy",
		Channel:  claimTestChannel("", "New remote", "cli_same", true),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("claim: code=%d body=%s", rec.Code, rec.Body.String())
	}
}
