package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	remotepkg "github.com/wangning19940904/AgentMux/remote"
)

type channelPeerTarget struct {
	ID   string
	Name string
}

type channelPeerClient interface {
	Targets() []channelPeerTarget
	ListChannels(context.Context, string) ([]core.Channel, error)
	ValidateChannel(context.Context, string, core.Channel) (*core.Channel, error)
	UpsertChannel(context.Context, string, core.Channel) (*core.Channel, error)
}

type remoteChannelPeerClient struct {
	manager *remotepkg.Manager
}

func (c *remoteChannelPeerClient) Targets() []channelPeerTarget {
	if c == nil || c.manager == nil {
		return nil
	}
	views := c.manager.List()
	out := make([]channelPeerTarget, 0, len(views))
	for _, view := range views {
		if view.Trusted {
			out = append(out, channelPeerTarget{ID: view.ID, Name: view.Name})
		}
	}
	return out
}

func (c *remoteChannelPeerClient) ListChannels(ctx context.Context, id string) ([]core.Channel, error) {
	var channels []core.Channel
	if err := c.request(ctx, id, http.MethodGet, "/api/v1/channels", nil, &channels); err != nil {
		return nil, err
	}
	return channels, nil
}

func (c *remoteChannelPeerClient) ValidateChannel(ctx context.Context, id string, channel core.Channel) (*core.Channel, error) {
	var validated core.Channel
	if err := c.request(ctx, id, http.MethodPost, "/api/v1/channels/validate", channel, &validated); err != nil {
		return nil, err
	}
	return &validated, nil
}

func (c *remoteChannelPeerClient) UpsertChannel(ctx context.Context, id string, channel core.Channel) (*core.Channel, error) {
	var saved core.Channel
	if err := c.request(ctx, id, http.MethodPost, "/api/v1/channels", channel, &saved); err != nil {
		return nil, err
	}
	return &saved, nil
}

func (c *remoteChannelPeerClient) MeetingSnapshot(ctx context.Context, id string) (core.MeetingSnapshot, error) {
	var snapshot core.MeetingSnapshot
	if err := c.request(ctx, id, http.MethodGet, "/api/v1/meetings", nil, &snapshot); err != nil {
		return core.MeetingSnapshot{}, err
	}
	return snapshot, nil
}

func (c *remoteChannelPeerClient) RespondMeetingInvitation(
	ctx context.Context,
	id string,
	request meetingInvitationActionRequest,
) (core.MeetingInvitation, error) {
	request.TargetID = ""
	var invitation core.MeetingInvitation
	if err := c.request(ctx, id, http.MethodPost, "/api/v1/meetings/invitations/respond", request, &invitation); err != nil {
		return core.MeetingInvitation{}, err
	}
	return invitation, nil
}

func (c *remoteChannelPeerClient) JoinMeeting(
	ctx context.Context,
	id string,
	request meetingJoinRequest,
) (core.MeetingJoinResult, error) {
	request.TargetID = ""
	var result core.MeetingJoinResult
	if err := c.request(ctx, id, http.MethodPost, "/api/v1/meetings/join", request, &result); err != nil {
		return core.MeetingJoinResult{}, err
	}
	return result, nil
}

func (c *remoteChannelPeerClient) MeetingActivity(ctx context.Context, id, channelID, meetingID string) (core.MeetingDetail, error) {
	params := url.Values{"channel_id": {channelID}, "meeting_id": {meetingID}}
	var detail core.MeetingDetail
	if err := c.request(ctx, id, http.MethodGet, "/api/v1/meetings/activity?"+params.Encode(), nil, &detail); err != nil {
		return core.MeetingDetail{}, err
	}
	return detail, nil
}

func (c *remoteChannelPeerClient) SendMeetingMessage(ctx context.Context, id string, request meetingMessageRequest) error {
	request.TargetID = ""
	return c.request(ctx, id, http.MethodPost, "/api/v1/meetings/messages", request, nil)
}

func (c *remoteChannelPeerClient) AskMeeting(ctx context.Context, id string, request meetingQuestionRequest) (core.MeetingTurn, error) {
	request.TargetID = ""
	var turn core.MeetingTurn
	if err := c.request(ctx, id, http.MethodPost, "/api/v1/meetings/questions", request, &turn); err != nil {
		return core.MeetingTurn{}, err
	}
	return turn, nil
}

func (c *remoteChannelPeerClient) SetMeetingResponseMode(ctx context.Context, id string, request meetingResponseModeRequest) (string, error) {
	request.TargetID = ""
	var result struct {
		ResponseMode string `json:"response_mode"`
	}
	if err := c.request(ctx, id, http.MethodPost, "/api/v1/meetings/response-mode", request, &result); err != nil {
		return "", err
	}
	return result.ResponseMode, nil
}

func (c *remoteChannelPeerClient) StreamMeetingEvents(ctx context.Context, id string, notify func()) error {
	return c.StreamMeetingEventPayloads(ctx, id, func(name string, _ json.RawMessage) {
		if notify != nil && (name == "ready" || strings.HasPrefix(name, "meeting.")) {
			notify()
		}
	})
}

func (c *remoteChannelPeerClient) StreamMeetingEventPayloads(ctx context.Context, id string, notify func(string, json.RawMessage)) error {
	if c == nil || c.manager == nil {
		return errors.New("remote SSH control unavailable")
	}
	host, ok := c.manager.Get(id)
	if !ok {
		return fmt.Errorf("remote host %q not found", id)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+host.RemoteAddr+"/api/v1/meetings/events", nil)
	if err != nil {
		return err
	}
	if host.APIToken != "" {
		req.Header.Set("Authorization", "Bearer "+host.APIToken)
	}
	req.Header.Set("Accept", "text/event-stream")
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return c.manager.DialContext(ctx, id, network)
		},
		DisableKeepAlives: true,
	}
	defer transport.CloseIdleConnections()
	resp, err := transport.RoundTrip(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("remote meeting event stream returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(payload)))
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	eventName := ""
	dataLines := []string{}
	emit := func() {
		if notify != nil && eventName != "" {
			notify(eventName, json.RawMessage(strings.Join(dataLines, "\n")))
		}
		eventName = ""
		dataLines = dataLines[:0]
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			emit()
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	emit()
	return io.EOF
}

func (c *remoteChannelPeerClient) request(
	ctx context.Context,
	id, method, path string,
	body any,
	out any,
) error {
	if c == nil || c.manager == nil {
		return errors.New("remote SSH control unavailable")
	}
	host, ok := c.manager.Get(id)
	if !ok {
		return fmt.Errorf("remote host %q not found", id)
	}
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+host.RemoteAddr+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if host.APIToken != "" {
		req.Header.Set("Authorization", "Bearer "+host.APIToken)
	}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return c.manager.DialContext(ctx, id, network)
		},
		DisableKeepAlives: true,
	}
	defer transport.CloseIdleConnections()
	resp, err := transport.RoundTrip(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		var envelope struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(payload, &envelope)
		message := strings.TrimSpace(envelope.Error)
		if message == "" {
			message = strings.TrimSpace(string(payload))
		}
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		return &channelPeerHTTPError{Status: resp.StatusCode, Message: message}
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out); err != nil {
		return fmt.Errorf("decode remote channel response: %w", err)
	}
	return nil
}

type channelPeerHTTPError struct {
	Status  int
	Message string
}

func (e *channelPeerHTTPError) Error() string { return e.Message }

type channelClaimRequest struct {
	TargetID string       `json:"target_id,omitempty"`
	Channel  core.Channel `json:"channel"`
}

type channelClaimConflict struct {
	TargetID   string
	TargetName string
	Channel    core.Channel
}

func (s *Server) handleChannelClaim(w http.ResponseWriter, r *http.Request) {
	if s.st == nil {
		writeErr(w, http.StatusServiceUnavailable, "store unavailable")
		return
	}
	var req channelClaimRequest
	if !decodeJSONInto(w, r, &req) {
		return
	}
	req.TargetID = strings.TrimSpace(req.TargetID)

	s.channelClaimMu.Lock()
	defer s.channelClaimMu.Unlock()

	if req.TargetID == "" {
		if err := s.normalizeChannel(r.Context(), &req.Channel); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	} else {
		if s.channelPeers == nil || !channelPeerExists(s.channelPeers.Targets(), req.TargetID) {
			writeErr(w, http.StatusNotFound, "remote host not found")
			return
		}
		if _, err := s.channelPeers.ValidateChannel(r.Context(), req.TargetID, req.Channel); err != nil {
			var peerErr *channelPeerHTTPError
			if !errors.As(err, &peerErr) ||
				(peerErr.Status != http.StatusNotFound && peerErr.Status != http.StatusMethodNotAllowed) {
				writeChannelClaimError(w, fmt.Errorf("validate target channel: %w", err))
				return
			}
			// Older remote AgentMux versions do not expose the read-only
			// validation endpoint. Keep the exclusive handoff compatible with
			// those peers; their normal upsert still performs final validation.
			if s.log != nil {
				s.log.Warn("remote channel preflight unavailable; using legacy handoff", "target_id", req.TargetID)
			}
		}
	}

	conflicts, err := s.findChannelClaimConflicts(r.Context(), req.TargetID, req.Channel)
	if err != nil {
		writeChannelClaimError(w, err)
		return
	}
	if err := s.disableChannelClaimConflicts(r.Context(), conflicts); err != nil {
		writeChannelClaimError(w, err)
		return
	}

	if req.TargetID != "" {
		saved, err := s.channelPeers.UpsertChannel(r.Context(), req.TargetID, req.Channel)
		if err != nil {
			writeChannelClaimError(w, fmt.Errorf("save target channel: %w", err))
			return
		}
		saved.Config = redactStringMap(saved.Config)
		writeJSON(w, http.StatusOK, saved)
		return
	}
	if err := s.st.UpsertChannel(r.Context(), &req.Channel); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.reloadChannels(r.Context())
	req.Channel.Config = redactStringMap(req.Channel.Config)
	writeJSON(w, http.StatusOK, &req.Channel)
}

func channelPeerExists(targets []channelPeerTarget, id string) bool {
	for _, target := range targets {
		if target.ID == id {
			return true
		}
	}
	return false
}

func (s *Server) findChannelClaimConflicts(
	ctx context.Context,
	targetID string,
	claimed core.Channel,
) ([]channelClaimConflict, error) {
	if !isExclusiveLongConnection(claimed) {
		return nil, nil
	}
	local, err := s.st.ListChannels(ctx)
	if err != nil {
		return nil, err
	}
	conflicts := collectChannelClaimConflicts(nil, "", "local machine", targetID, claimed, local)
	if s.channelPeers == nil {
		return conflicts, nil
	}
	for _, target := range s.channelPeers.Targets() {
		channels, err := s.channelPeers.ListChannels(ctx, target.ID)
		if err != nil {
			return nil, fmt.Errorf("check remote %s before opening the new connection: %w", target.Name, err)
		}
		conflicts = collectChannelClaimConflicts(conflicts, target.ID, target.Name, targetID, claimed, channels)
	}
	return conflicts, nil
}

func collectChannelClaimConflicts(
	conflicts []channelClaimConflict,
	locationID, locationName, targetID string,
	claimed core.Channel,
	channels []core.Channel,
) []channelClaimConflict {
	for _, existing := range channels {
		if !sameLongConnectionIdentity(existing, claimed) {
			continue
		}
		if locationID == targetID && claimed.ID != "" && existing.ID == claimed.ID {
			continue
		}
		conflicts = append(conflicts, channelClaimConflict{
			TargetID: locationID, TargetName: locationName, Channel: existing,
		})
	}
	return conflicts
}

func isExclusiveLongConnection(channel core.Channel) bool {
	if !channel.Enabled {
		return false
	}
	typ := strings.ToLower(strings.TrimSpace(channel.Type))
	return (typ == "feishu" || typ == "lark") && strings.TrimSpace(channel.Config["app_id"]) != ""
}

func sameLongConnectionIdentity(existing, claimed core.Channel) bool {
	if !existing.Enabled {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(existing.Type), strings.TrimSpace(claimed.Type)) &&
		strings.TrimSpace(existing.Config["app_id"]) == strings.TrimSpace(claimed.Config["app_id"])
}

func (s *Server) disableChannelClaimConflicts(ctx context.Context, conflicts []channelClaimConflict) error {
	localChanged := false
	for _, conflict := range conflicts {
		channel := conflict.Channel
		channel.Enabled = false
		channel.UpdatedAt = time.Now()
		if conflict.TargetID == "" {
			if err := s.st.UpsertChannel(ctx, &channel); err != nil {
				return fmt.Errorf("disable previous local channel %s: %w", channel.Name, err)
			}
			localChanged = true
			continue
		}
		if _, err := s.channelPeers.UpsertChannel(ctx, conflict.TargetID, channel); err != nil {
			return fmt.Errorf("disable previous channel %s on %s: %w", channel.Name, conflict.TargetName, err)
		}
	}
	if localChanged {
		// Reload is synchronous: DetachChannel closes the old WebSocket before
		// the claimed channel is persisted and started.
		s.reloadChannels(ctx)
	}
	if len(conflicts) > 0 && s.log != nil {
		s.log.Info("disabled conflicting long connections", "count", len(conflicts))
	}
	return nil
}

func writeChannelClaimError(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	var peerErr *channelPeerHTTPError
	if errors.As(err, &peerErr) && peerErr.Status >= 400 && peerErr.Status < 500 {
		status = peerErr.Status
	}
	writeErr(w, status, err.Error())
}
