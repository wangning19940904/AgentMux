package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

const meetingPeerSnapshotTimeout = 5 * time.Second

const (
	meetingEventHeartbeatInterval = 15 * time.Second
	meetingPeerReconnectDelay     = 3 * time.Second
	meetingPeerReconnectMaxDelay  = 30 * time.Second
)

type meetingPeerClient interface {
	Targets() []channelPeerTarget
	MeetingSnapshot(context.Context, string) (core.MeetingSnapshot, error)
	RespondMeetingInvitation(context.Context, string, meetingInvitationActionRequest) (core.MeetingInvitation, error)
	JoinMeeting(context.Context, string, meetingJoinRequest) (core.MeetingJoinResult, error)
	StreamMeetingEvents(context.Context, string, func()) error
}

type meetingActivityPeerClient interface {
	MeetingActivity(context.Context, string, string, string) (core.MeetingDetail, error)
	SendMeetingMessage(context.Context, string, meetingMessageRequest) error
	AskMeeting(context.Context, string, meetingQuestionRequest) (core.MeetingTurn, error)
}

type meetingSettingsPeerClient interface {
	SetMeetingResponseMode(context.Context, string, meetingResponseModeRequest) (string, error)
}

type meetingPayloadPeerClient interface {
	StreamMeetingEventPayloads(context.Context, string, func(string, json.RawMessage)) error
}

type meetingChannelView struct {
	core.MeetingChannel
	TargetID   string `json:"target_id,omitempty"`
	TargetName string `json:"target_name,omitempty"`
}

type meetingInvitationView struct {
	core.MeetingInvitation
	TargetID   string `json:"target_id,omitempty"`
	TargetName string `json:"target_name,omitempty"`
}

type activeMeetingView struct {
	core.ActiveMeeting
	TargetID   string `json:"target_id,omitempty"`
	TargetName string `json:"target_name,omitempty"`
}

type meetingDetailView struct {
	core.MeetingDetail
	TargetID   string `json:"target_id,omitempty"`
	TargetName string `json:"target_name,omitempty"`
}

type meetingAggregate struct {
	Channels    []meetingChannelView    `json:"channels"`
	Invitations []meetingInvitationView `json:"invitations"`
	Meetings    []activeMeetingView     `json:"meetings"`
	Warnings    []string                `json:"warnings,omitempty"`
}

type meetingInvitationActionRequest struct {
	TargetID     string `json:"target_id,omitempty"`
	ChannelID    string `json:"channel_id"`
	InvitationID string `json:"invitation_id"`
	Nonce        string `json:"nonce"`
	Decision     string `json:"decision"`
}

type meetingJoinRequest struct {
	TargetID      string `json:"target_id,omitempty"`
	ChannelID     string `json:"channel_id"`
	MeetingNumber string `json:"meeting_number"`
}

type meetingMessageRequest struct {
	TargetID  string `json:"target_id,omitempty"`
	ChannelID string `json:"channel_id"`
	MeetingID string `json:"meeting_id"`
	Text      string `json:"text"`
	UUID      string `json:"uuid,omitempty"`
}

type meetingQuestionRequest struct {
	TargetID  string `json:"target_id,omitempty"`
	ChannelID string `json:"channel_id"`
	MeetingID string `json:"meeting_id"`
	Question  string `json:"question"`
	UserID    string `json:"user_id,omitempty"`
}

type meetingResponseModeRequest struct {
	TargetID  string `json:"target_id,omitempty"`
	ChannelID string `json:"channel_id"`
	Mode      string `json:"mode"`
}

func (s *Server) handleMeetingSnapshot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.connect == nil {
		writeJSON(w, http.StatusOK, core.MeetingSnapshot{
			Channels: []core.MeetingChannel{}, Invitations: []core.MeetingInvitation{}, Meetings: []core.ActiveMeeting{},
		})
		return
	}
	writeJSON(w, http.StatusOK, s.enrichMeetingSnapshot(r.Context(), s.connect.MeetingSnapshot()))
}

func (s *Server) handleMeetingEvents(w http.ResponseWriter, r *http.Request) {
	if s.connect == nil {
		writeErr(w, http.StatusServiceUnavailable, "connect runtime is not running")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming is unavailable")
		return
	}
	updates, unsubscribe := s.connect.SubscribeMeetingEvents()
	defer unsubscribe()
	prepareMeetingEventStream(w)
	if !writeMeetingEvent(w, flusher, "ready", map[string]bool{"ready": true}) {
		return
	}
	heartbeat := time.NewTicker(meetingEventHeartbeatInterval)
	defer heartbeat.Stop()
	for {
		select {
		case event, open := <-updates:
			eventName := event.Type
			if eventName == "" {
				eventName = "meeting.changed"
			}
			if !open || !writeMeetingEvent(w, flusher, eventName, event) {
				return
			}
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) handleMeetingActivity(w http.ResponseWriter, r *http.Request) {
	if s.connect == nil {
		writeErr(w, http.StatusServiceUnavailable, "connect runtime is not running")
		return
	}
	channelID, meetingID := strings.TrimSpace(r.URL.Query().Get("channel_id")), strings.TrimSpace(r.URL.Query().Get("meeting_id"))
	if channelID == "" || meetingID == "" {
		writeErr(w, http.StatusBadRequest, "channel_id and meeting_id are required")
		return
	}
	detail, err := s.connect.MeetingActivity(channelID, meetingID)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handleMeetingMessageSend(w http.ResponseWriter, r *http.Request) {
	if s.connect == nil {
		writeErr(w, http.StatusServiceUnavailable, "connect runtime is not running")
		return
	}
	var req meetingMessageRequest
	if !decodeJSONInto(w, r, &req) {
		return
	}
	if err := validateMeetingMessage(req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.connect.SendMeetingMessage(r.Context(), req.ChannelID, req.MeetingID, req.Text, req.UUID); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"sent": true})
}

func (s *Server) handleMeetingQuestion(w http.ResponseWriter, r *http.Request) {
	if s.connect == nil {
		writeErr(w, http.StatusServiceUnavailable, "connect runtime is not running")
		return
	}
	var req meetingQuestionRequest
	if !decodeJSONInto(w, r, &req) {
		return
	}
	if err := validateMeetingQuestion(req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	turn, err := s.connect.AskMeeting(req.ChannelID, req.MeetingID, req.Question, "app", req.UserID)
	if errors.Is(err, core.ErrMeetingBusy) {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, turn)
}

func (s *Server) handleMeetingResponseMode(w http.ResponseWriter, r *http.Request) {
	if s.connect == nil {
		writeErr(w, http.StatusServiceUnavailable, "connect runtime is not running")
		return
	}
	var req meetingResponseModeRequest
	if !decodeJSONInto(w, r, &req) {
		return
	}
	if err := validateMeetingResponseMode(req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	mode, err := s.connect.SetMeetingResponseMode(r.Context(), req.ChannelID, req.Mode)
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"channel_id": req.ChannelID, "response_mode": mode})
}

func (s *Server) handleMeetingInvitationRespond(w http.ResponseWriter, r *http.Request) {
	if s.connect == nil {
		writeErr(w, http.StatusServiceUnavailable, "connect runtime is not running")
		return
	}
	var req meetingInvitationActionRequest
	if !decodeJSONInto(w, r, &req) {
		return
	}
	if err := validateMeetingInvitationAction(req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	invitation, err := s.connect.RespondMeetingInvitation(
		r.Context(), req.ChannelID, req.InvitationID, req.Nonce, req.Decision,
	)
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, invitation)
}

func (s *Server) handleMeetingJoin(w http.ResponseWriter, r *http.Request) {
	if s.connect == nil {
		writeErr(w, http.StatusServiceUnavailable, "connect runtime is not running")
		return
	}
	var req meetingJoinRequest
	if !decodeJSONInto(w, r, &req) {
		return
	}
	if err := validateMeetingJoin(req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := s.connect.JoinMeetingByNumber(r.Context(), req.ChannelID, req.MeetingNumber)
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleMeetingAggregate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	aggregate := meetingAggregate{Channels: []meetingChannelView{}, Invitations: []meetingInvitationView{}, Meetings: []activeMeetingView{}}
	if s.connect != nil {
		appendMeetingSnapshot(&aggregate, "", "", s.enrichMeetingSnapshot(r.Context(), s.connect.MeetingSnapshot()))
	}
	if s.meetingPeers != nil {
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, target := range s.meetingPeers.Targets() {
			target := target
			wg.Add(1)
			go func() {
				defer wg.Done()
				peerCtx, cancel := context.WithTimeout(r.Context(), meetingPeerSnapshotTimeout)
				defer cancel()
				snapshot, err := s.meetingPeers.MeetingSnapshot(peerCtx, target.ID)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					aggregate.Warnings = append(aggregate.Warnings, fmt.Sprintf("%s: %v", target.Name, err))
					return
				}
				appendMeetingSnapshot(&aggregate, target.ID, target.Name, snapshot)
			}()
		}
		wg.Wait()
	}
	sort.Slice(aggregate.Channels, func(i, j int) bool {
		left := aggregate.Channels[i].TargetName + "\x00" + aggregate.Channels[i].ChannelName
		right := aggregate.Channels[j].TargetName + "\x00" + aggregate.Channels[j].ChannelName
		return strings.ToLower(left) < strings.ToLower(right)
	})
	sort.Slice(aggregate.Invitations, func(i, j int) bool {
		return aggregate.Invitations[i].CreatedAt.Before(aggregate.Invitations[j].CreatedAt)
	})
	sort.Slice(aggregate.Meetings, func(i, j int) bool {
		return aggregate.Meetings[i].LastActivityAt.After(aggregate.Meetings[j].LastActivityAt)
	})
	sort.Strings(aggregate.Warnings)
	writeJSON(w, http.StatusOK, aggregate)
}

// enrichMeetingSnapshot adds console-only display metadata to meeting-capable
// channels. Bot identity is intentionally resolved here rather than in core so
// credentials and Feishu/Lark OpenAPI access stay inside the management server.
func (s *Server) enrichMeetingSnapshot(ctx context.Context, snapshot core.MeetingSnapshot) core.MeetingSnapshot {
	if s.st == nil || len(snapshot.Channels) == 0 {
		return snapshot
	}
	storedChannels, err := s.st.ListChannels(ctx)
	if err != nil {
		return snapshot
	}
	byID := make(map[string]core.Channel, len(storedChannels))
	for _, channel := range storedChannels {
		byID[channel.ID] = channel
	}

	var wg sync.WaitGroup
	botNames := make(map[string]string, len(snapshot.Channels))
	var botMu sync.Mutex
	for i := range snapshot.Channels {
		channel, ok := byID[snapshot.Channels[i].ChannelID]
		if !ok {
			continue
		}
		wg.Add(1)
		go func(index int, stored core.Channel) {
			defer wg.Done()
			if info := s.lookupChannelBotInfo(ctx, stored); info != nil {
				snapshot.Channels[index].BotName = info.Name
				botMu.Lock()
				botNames[stored.ID] = info.Name
				botMu.Unlock()
			}
		}(i, channel)
	}
	wg.Wait()
	for i := range snapshot.Meetings {
		if snapshot.Meetings[i].BotName == "" {
			snapshot.Meetings[i].BotName = botNames[snapshot.Meetings[i].ChannelID]
		}
	}
	return snapshot
}

func (s *Server) handleMeetingAggregateEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming is unavailable")
		return
	}
	var localUpdates <-chan core.MeetingEvent
	unsubscribe := func() {}
	if s.connect != nil {
		localUpdates, unsubscribe = s.connect.SubscribeMeetingEvents()
	}
	defer unsubscribe()

	remoteUpdates := make(chan remoteMeetingStreamEvent, 64)
	streamCtx, cancelStreams := context.WithCancel(r.Context())
	defer cancelStreams()
	if s.meetingPeers != nil {
		for _, target := range s.meetingPeers.Targets() {
			target := target
			go s.streamRemoteMeetingEvents(streamCtx, target.ID, remoteUpdates)
		}
	}

	prepareMeetingEventStream(w)
	if !writeMeetingEvent(w, flusher, "ready", map[string]bool{"ready": true}) {
		return
	}
	heartbeat := time.NewTicker(meetingEventHeartbeatInterval)
	defer heartbeat.Stop()
	for {
		select {
		case event, open := <-localUpdates:
			if !open {
				localUpdates = nil
				continue
			}
			eventName := event.Type
			if eventName == "" {
				eventName = "meeting.changed"
			}
			if !writeMeetingEvent(w, flusher, eventName, event) {
				return
			}
		case remote := <-remoteUpdates:
			if !writeMeetingEvent(w, flusher, remote.Name, remote.Payload) {
				return
			}
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

type remoteMeetingStreamEvent struct {
	Name    string
	Payload map[string]any
}

func (s *Server) streamRemoteMeetingEvents(ctx context.Context, targetID string, updates chan<- remoteMeetingStreamEvent) {
	reconnectDelay := meetingPeerReconnectDelay
	for ctx.Err() == nil {
		connectedAt := time.Now()
		var err error
		if payloadPeer, ok := s.meetingPeers.(meetingPayloadPeerClient); ok {
			err = payloadPeer.StreamMeetingEventPayloads(ctx, targetID, func(name string, raw json.RawMessage) {
				if name == "ready" {
					return
				}
				payload := map[string]any{}
				_ = json.Unmarshal(raw, &payload)
				payload["target_id"] = targetID
				payload["target_name"] = meetingTargetName(s.meetingPeers.Targets(), targetID)
				select {
				case updates <- remoteMeetingStreamEvent{Name: name, Payload: payload}:
				default:
				}
			})
		} else {
			err = s.meetingPeers.StreamMeetingEvents(ctx, targetID, func() {
				select {
				case updates <- remoteMeetingStreamEvent{Name: "meeting.changed", Payload: map[string]any{"target_id": targetID, "target_name": meetingTargetName(s.meetingPeers.Targets(), targetID)}}:
				default:
				}
			})
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil && s.log != nil {
			s.log.Debug("remote meeting event stream disconnected", "target_id", targetID, "err", err)
		}
		if time.Since(connectedAt) >= meetingEventHeartbeatInterval {
			reconnectDelay = meetingPeerReconnectDelay
		}
		timer := time.NewTimer(reconnectDelay)
		select {
		case <-timer.C:
			if reconnectDelay < meetingPeerReconnectMaxDelay {
				reconnectDelay *= 2
				if reconnectDelay > meetingPeerReconnectMaxDelay {
					reconnectDelay = meetingPeerReconnectMaxDelay
				}
			}
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		}
	}
}

func prepareMeetingEventStream(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
}

func writeMeetingEvent(w http.ResponseWriter, flusher http.Flusher, eventName string, payload any) bool {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return false
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventName, encoded); err != nil {
		return false
	}
	flusher.Flush()
	return true
}

func (s *Server) handleRemoteMeetingInvitationRespond(w http.ResponseWriter, r *http.Request) {
	var req meetingInvitationActionRequest
	if !decodeJSONInto(w, r, &req) {
		return
	}
	if err := validateMeetingInvitationAction(req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	targetID := strings.TrimSpace(req.TargetID)
	if targetID == "" {
		if s.connect == nil {
			writeErr(w, http.StatusServiceUnavailable, "connect runtime is not running")
			return
		}
		invitation, err := s.connect.RespondMeetingInvitation(
			r.Context(), req.ChannelID, req.InvitationID, req.Nonce, req.Decision,
		)
		if err != nil {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, meetingInvitationView{MeetingInvitation: invitation})
		return
	}
	if s.meetingPeers == nil {
		writeErr(w, http.StatusServiceUnavailable, "remote SSH control unavailable")
		return
	}
	invitation, err := s.meetingPeers.RespondMeetingInvitation(r.Context(), targetID, req)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, meetingInvitationView{
		MeetingInvitation: invitation, TargetID: targetID, TargetName: meetingTargetName(s.meetingPeers.Targets(), targetID),
	})
}

func (s *Server) handleRemoteMeetingJoin(w http.ResponseWriter, r *http.Request) {
	var req meetingJoinRequest
	if !decodeJSONInto(w, r, &req) {
		return
	}
	if err := validateMeetingJoin(req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	targetID := strings.TrimSpace(req.TargetID)
	if targetID == "" {
		if s.connect == nil {
			writeErr(w, http.StatusServiceUnavailable, "connect runtime is not running")
			return
		}
		result, err := s.connect.JoinMeetingByNumber(r.Context(), req.ChannelID, req.MeetingNumber)
		if err != nil {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, result)
		return
	}
	if s.meetingPeers == nil {
		writeErr(w, http.StatusServiceUnavailable, "remote SSH control unavailable")
		return
	}
	result, err := s.meetingPeers.JoinMeeting(r.Context(), targetID, req)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func appendMeetingSnapshot(aggregate *meetingAggregate, targetID, targetName string, snapshot core.MeetingSnapshot) {
	for _, channel := range snapshot.Channels {
		aggregate.Channels = append(aggregate.Channels, meetingChannelView{
			MeetingChannel: channel, TargetID: targetID, TargetName: targetName,
		})
	}
	for _, invitation := range snapshot.Invitations {
		aggregate.Invitations = append(aggregate.Invitations, meetingInvitationView{
			MeetingInvitation: invitation, TargetID: targetID, TargetName: targetName,
		})
	}
	for _, meeting := range snapshot.Meetings {
		aggregate.Meetings = append(aggregate.Meetings, activeMeetingView{ActiveMeeting: meeting, TargetID: targetID, TargetName: targetName})
	}
}

func (s *Server) handleRemoteMeetingActivity(w http.ResponseWriter, r *http.Request) {
	targetID := strings.TrimSpace(r.URL.Query().Get("target_id"))
	channelID := strings.TrimSpace(r.URL.Query().Get("channel_id"))
	meetingID := strings.TrimSpace(r.URL.Query().Get("meeting_id"))
	if channelID == "" || meetingID == "" {
		writeErr(w, http.StatusBadRequest, "channel_id and meeting_id are required")
		return
	}
	if targetID == "" {
		if s.connect == nil {
			writeErr(w, http.StatusServiceUnavailable, "connect runtime is not running")
			return
		}
		detail, err := s.connect.MeetingActivity(channelID, meetingID)
		if err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, meetingDetailView{MeetingDetail: detail})
		return
	}
	peer, ok := s.meetingPeers.(meetingActivityPeerClient)
	if !ok {
		writeErr(w, http.StatusBadGateway, "remote host does not support meeting activity")
		return
	}
	detail, err := peer.MeetingActivity(r.Context(), targetID, channelID, meetingID)
	if err != nil {
		writePeerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, meetingDetailView{MeetingDetail: detail, TargetID: targetID, TargetName: meetingTargetName(s.meetingPeers.Targets(), targetID)})
}

func (s *Server) handleRemoteMeetingMessageSend(w http.ResponseWriter, r *http.Request) {
	var req meetingMessageRequest
	if !decodeJSONInto(w, r, &req) {
		return
	}
	if err := validateMeetingMessage(req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(req.TargetID) == "" {
		if s.connect == nil {
			writeErr(w, http.StatusServiceUnavailable, "connect runtime is not running")
			return
		}
		if err := s.connect.SendMeetingMessage(r.Context(), req.ChannelID, req.MeetingID, req.Text, req.UUID); err != nil {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
	} else {
		peer, ok := s.meetingPeers.(meetingActivityPeerClient)
		if !ok {
			writeErr(w, http.StatusBadGateway, "remote host does not support meeting messages")
			return
		}
		if err := peer.SendMeetingMessage(r.Context(), req.TargetID, req); err != nil {
			writePeerError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"sent": true})
}

func (s *Server) handleRemoteMeetingQuestion(w http.ResponseWriter, r *http.Request) {
	var req meetingQuestionRequest
	if !decodeJSONInto(w, r, &req) {
		return
	}
	if err := validateMeetingQuestion(req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var turn core.MeetingTurn
	var err error
	if strings.TrimSpace(req.TargetID) == "" {
		if s.connect == nil {
			writeErr(w, http.StatusServiceUnavailable, "connect runtime is not running")
			return
		}
		turn, err = s.connect.AskMeeting(req.ChannelID, req.MeetingID, req.Question, "app", req.UserID)
	} else {
		peer, ok := s.meetingPeers.(meetingActivityPeerClient)
		if !ok {
			writeErr(w, http.StatusBadGateway, "remote host does not support meeting questions")
			return
		}
		turn, err = peer.AskMeeting(r.Context(), req.TargetID, req)
	}
	if errors.Is(err, core.ErrMeetingBusy) {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writePeerError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, turn)
}

func (s *Server) handleRemoteMeetingResponseMode(w http.ResponseWriter, r *http.Request) {
	var req meetingResponseModeRequest
	if !decodeJSONInto(w, r, &req) {
		return
	}
	if err := validateMeetingResponseMode(req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var mode string
	var err error
	local := strings.TrimSpace(req.TargetID) == ""
	if local {
		if s.connect == nil {
			writeErr(w, http.StatusServiceUnavailable, "connect runtime is not running")
			return
		}
		mode, err = s.connect.SetMeetingResponseMode(r.Context(), req.ChannelID, req.Mode)
	} else {
		peer, ok := s.meetingPeers.(meetingSettingsPeerClient)
		if !ok {
			writeErr(w, http.StatusBadGateway, "remote host does not support meeting response settings")
			return
		}
		mode, err = peer.SetMeetingResponseMode(r.Context(), req.TargetID, req)
	}
	if err != nil {
		if local {
			writeErr(w, http.StatusConflict, err.Error())
		} else {
			writePeerError(w, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"channel_id": req.ChannelID, "response_mode": mode})
}

func writePeerError(w http.ResponseWriter, err error) {
	var peerErr *channelPeerHTTPError
	if errors.As(err, &peerErr) {
		writeErr(w, peerErr.Status, peerErr.Message)
		return
	}
	writeErr(w, http.StatusBadGateway, err.Error())
}

func validateMeetingInvitationAction(req meetingInvitationActionRequest) error {
	if strings.TrimSpace(req.ChannelID) == "" || strings.TrimSpace(req.InvitationID) == "" || strings.TrimSpace(req.Nonce) == "" {
		return fmt.Errorf("channel_id, invitation_id and nonce are required")
	}
	decision := strings.TrimSpace(req.Decision)
	if decision != "join" && decision != "reject" {
		return fmt.Errorf("decision must be join or reject")
	}
	return nil
}

func validateMeetingJoin(req meetingJoinRequest) error {
	if strings.TrimSpace(req.ChannelID) == "" {
		return fmt.Errorf("channel_id is required")
	}
	meetingNumber := strings.TrimSpace(req.MeetingNumber)
	if len(meetingNumber) != 9 {
		return fmt.Errorf("meeting_number must be exactly 9 digits")
	}
	for i := range meetingNumber {
		if meetingNumber[i] < '0' || meetingNumber[i] > '9' {
			return fmt.Errorf("meeting_number must be exactly 9 digits")
		}
	}
	return nil
}

func validateMeetingMessage(req meetingMessageRequest) error {
	if strings.TrimSpace(req.ChannelID) == "" || strings.TrimSpace(req.MeetingID) == "" || strings.TrimSpace(req.Text) == "" {
		return fmt.Errorf("channel_id, meeting_id and text are required")
	}
	return nil
}

func validateMeetingQuestion(req meetingQuestionRequest) error {
	if strings.TrimSpace(req.ChannelID) == "" || strings.TrimSpace(req.MeetingID) == "" || strings.TrimSpace(req.Question) == "" {
		return fmt.Errorf("channel_id, meeting_id and question are required")
	}
	return nil
}

func validateMeetingResponseMode(req meetingResponseModeRequest) error {
	if strings.TrimSpace(req.ChannelID) == "" {
		return fmt.Errorf("channel_id is required")
	}
	if core.NormalizeMeetingResponseMode(req.Mode) == "" {
		return fmt.Errorf("mode must be stream_text, final_text, text_voice or voice")
	}
	return nil
}

func meetingTargetName(targets []channelPeerTarget, id string) string {
	for _, target := range targets {
		if target.ID == id {
			return target.Name
		}
	}
	return ""
}
