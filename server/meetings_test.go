package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

type fakeMeetingPeer struct {
	snapshot core.MeetingSnapshot
	joined   meetingJoinRequest
	acted    meetingInvitationActionRequest
	mode     meetingResponseModeRequest
}

func (f *fakeMeetingPeer) SetMeetingResponseMode(
	_ context.Context,
	_ string,
	request meetingResponseModeRequest,
) (string, error) {
	f.mode = request
	return core.NormalizeMeetingResponseMode(request.Mode), nil
}

type meetingSSERecorder struct {
	mu      sync.Mutex
	header  http.Header
	body    bytes.Buffer
	status  int
	flushed chan struct{}
}

func newMeetingSSERecorder() *meetingSSERecorder {
	return &meetingSSERecorder{header: make(http.Header), flushed: make(chan struct{}, 8)}
}

func (r *meetingSSERecorder) Header() http.Header { return r.header }

func (r *meetingSSERecorder) WriteHeader(status int) {
	r.mu.Lock()
	if r.status == 0 {
		r.status = status
	}
	r.mu.Unlock()
}

func (r *meetingSSERecorder) Write(payload []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(payload)
}

func (r *meetingSSERecorder) Flush() {
	select {
	case r.flushed <- struct{}{}:
	default:
	}
}

func (r *meetingSSERecorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.String()
}

func (f *fakeMeetingPeer) Targets() []channelPeerTarget {
	return []channelPeerTarget{{ID: "ssh-1", Name: "Build host"}}
}

func (f *fakeMeetingPeer) MeetingSnapshot(context.Context, string) (core.MeetingSnapshot, error) {
	return f.snapshot, nil
}

func (f *fakeMeetingPeer) RespondMeetingInvitation(
	_ context.Context,
	_ string,
	request meetingInvitationActionRequest,
) (core.MeetingInvitation, error) {
	f.acted = request
	return core.MeetingInvitation{ID: request.InvitationID, ChannelID: request.ChannelID, State: "joined"}, nil
}

func (f *fakeMeetingPeer) JoinMeeting(
	_ context.Context,
	_ string,
	request meetingJoinRequest,
) (core.MeetingJoinResult, error) {
	f.joined = request
	return core.MeetingJoinResult{
		ChannelID: request.ChannelID, MeetingNumber: request.MeetingNumber,
		MeetingID: "meeting-long-id", GreetingSent: true,
	}, nil
}

func (f *fakeMeetingPeer) StreamMeetingEvents(ctx context.Context, _ string, notify func()) error {
	if notify != nil {
		notify()
	}
	<-ctx.Done()
	return ctx.Err()
}

func TestMeetingAggregateIncludesSSHInvitations(t *testing.T) {
	createdAt := time.Now().UTC()
	peer := &fakeMeetingPeer{snapshot: core.MeetingSnapshot{
		Channels: []core.MeetingChannel{{ChannelID: "remote-channel", ChannelName: "Remote Feishu", Connected: true}},
		Invitations: []core.MeetingInvitation{{
			ID: "invite-1", ChannelID: "remote-channel", MeetingNumber: "123456789",
			Topic: "Remote meeting", State: "pending", CreatedAt: createdAt,
		}},
	}}
	srv := &Server{meetingPeers: peer}
	recorder := httptest.NewRecorder()
	srv.handleMeetingAggregate(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/remote/meetings", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var aggregate meetingAggregate
	if err := json.Unmarshal(recorder.Body.Bytes(), &aggregate); err != nil {
		t.Fatal(err)
	}
	if len(aggregate.Invitations) != 1 || aggregate.Invitations[0].TargetID != "ssh-1" ||
		aggregate.Invitations[0].TargetName != "Build host" || aggregate.Invitations[0].ChannelID != "remote-channel" {
		t.Fatalf("aggregate = %+v", aggregate)
	}
}

func TestMeetingSnapshotEnrichmentIncludesFeishuBotName(t *testing.T) {
	srv, st := newTestServer(t)
	oldBase := channelBotOpenAPIBase["feishu"]
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open-apis/auth/v3/app_access_token/internal":
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","app_access_token":"app-token"}`))
		case "/open-apis/bot/v3/info":
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","bot":{"app_name":"Meeting Helper"}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer upstream.Close()
	channelBotOpenAPIBase["feishu"] = upstream.URL
	t.Cleanup(func() { channelBotOpenAPIBase["feishu"] = oldBase })

	channel := &core.Channel{
		ID: "channel-1", Name: "Production bot", Type: "feishu",
		Config: map[string]string{"app_id": "cli_test", "app_secret": "secret"}, Enabled: true,
	}
	if err := st.UpsertChannel(context.Background(), channel); err != nil {
		t.Fatal(err)
	}
	snapshot := srv.enrichMeetingSnapshot(context.Background(), core.MeetingSnapshot{
		Channels: []core.MeetingChannel{{ChannelID: channel.ID, ChannelName: channel.Name, Platform: channel.Type}},
	})
	if len(snapshot.Channels) != 1 || snapshot.Channels[0].BotName != "Meeting Helper" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestRemoteMeetingJoinRoutesBackToSSHChannel(t *testing.T) {
	peer := &fakeMeetingPeer{}
	srv := &Server{meetingPeers: peer}
	body := bytes.NewBufferString(`{"target_id":"ssh-1","channel_id":"remote-channel","meeting_number":"123456789"}`)
	recorder := httptest.NewRecorder()
	srv.handleRemoteMeetingJoin(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/remote/meetings/join", body))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if peer.joined.TargetID != "ssh-1" || peer.joined.ChannelID != "remote-channel" || peer.joined.MeetingNumber != "123456789" {
		t.Fatalf("join request = %+v", peer.joined)
	}
}

func TestRemoteMeetingInvitationResponseRoutesBackToSSHChannel(t *testing.T) {
	peer := &fakeMeetingPeer{}
	srv := &Server{meetingPeers: peer}
	body := bytes.NewBufferString(`{"target_id":"ssh-1","channel_id":"remote-channel","invitation_id":"invite-1","nonce":"nonce-1","decision":"join"}`)
	recorder := httptest.NewRecorder()
	srv.handleRemoteMeetingInvitationRespond(
		recorder,
		httptest.NewRequest(http.MethodPost, "/api/v1/remote/meetings/invitations/respond", body),
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if peer.acted.TargetID != "ssh-1" || peer.acted.ChannelID != "remote-channel" ||
		peer.acted.InvitationID != "invite-1" || peer.acted.Nonce != "nonce-1" || peer.acted.Decision != "join" {
		t.Fatalf("invitation action = %+v", peer.acted)
	}
}

func TestRemoteMeetingResponseModeRoutesBackToSSHChannel(t *testing.T) {
	peer := &fakeMeetingPeer{}
	srv := &Server{meetingPeers: peer}
	body := bytes.NewBufferString(`{"target_id":"ssh-1","channel_id":"remote-channel","mode":"text_voice"}`)
	recorder := httptest.NewRecorder()
	srv.handleRemoteMeetingResponseMode(
		recorder,
		httptest.NewRequest(http.MethodPost, "/api/v1/remote/meetings/response-mode", body),
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if peer.mode.TargetID != "ssh-1" || peer.mode.ChannelID != "remote-channel" || peer.mode.Mode != "text_voice" {
		t.Fatalf("mode request = %+v", peer.mode)
	}
	if !strings.Contains(recorder.Body.String(), `"response_mode":"text_voice"`) {
		t.Fatalf("response = %s", recorder.Body.String())
	}
}

func TestMeetingAggregateEventStreamRelaysSSHNotifications(t *testing.T) {
	peer := &fakeMeetingPeer{}
	srv := &Server{meetingPeers: peer}
	ctx, cancel := context.WithCancel(context.Background())
	recorder := newMeetingSSERecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.handleMeetingAggregateEvents(
			recorder,
			httptest.NewRequest(http.MethodGet, "/api/v1/remote/meetings/events", nil).WithContext(ctx),
		)
	}()

	for range 2 {
		select {
		case <-recorder.flushed:
		case <-time.After(2 * time.Second):
			cancel()
			t.Fatal("timed out waiting for meeting event stream")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("meeting event stream did not stop after cancellation")
	}
	body := recorder.String()
	if !strings.Contains(body, "event: ready") || !strings.Contains(body, "event: meeting.changed") {
		t.Fatalf("event stream body = %q", body)
	}
}
