package core

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// MeetingInvitation is the transport-neutral invitation state exposed to the
// local AgentMux console. Secret callback material stays inside the platform
// adapter; Nonce is a single-use correlation token for console decisions.
type MeetingInvitation struct {
	ID              string    `json:"id"`
	Nonce           string    `json:"nonce"`
	ChannelID       string    `json:"channel_id"`
	ChannelName     string    `json:"channel_name"`
	Platform        string    `json:"platform"`
	MeetingID       string    `json:"meeting_id,omitempty"`
	MeetingNumber   string    `json:"meeting_number"`
	Topic           string    `json:"topic"`
	InviterName     string    `json:"inviter_name"`
	State           string    `json:"state"`
	LastError       string    `json:"last_error,omitempty"`
	GreetingSent    bool      `json:"greeting_sent,omitempty"`
	GreetingWarning string    `json:"greeting_warning,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	ExpiresAt       time.Time `json:"expires_at"`
}

// MeetingChannel is one live channel capable of joining a Feishu/Lark video
// meeting. Remote-host metadata is added by the server aggregation layer.
type MeetingChannel struct {
	ChannelID    string `json:"channel_id"`
	ChannelName  string `json:"channel_name"`
	Platform     string `json:"platform"`
	BotName      string `json:"bot_name,omitempty"`
	AgentName    string `json:"agent_name,omitempty"`
	ResponseMode string `json:"response_mode"`
	State        string `json:"state"`
	Connected    bool   `json:"connected"`
	Error        string `json:"error,omitempty"`
}

type MeetingJoinResult struct {
	ChannelID       string `json:"channel_id"`
	ChannelName     string `json:"channel_name"`
	Platform        string `json:"platform"`
	MeetingID       string `json:"meeting_id"`
	MeetingNumber   string `json:"meeting_number"`
	GreetingSent    bool   `json:"greeting_sent"`
	GreetingWarning string `json:"greeting_warning,omitempty"`
}

type MeetingSnapshot struct {
	Channels    []MeetingChannel    `json:"channels"`
	Invitations []MeetingInvitation `json:"invitations"`
	Meetings    []ActiveMeeting     `json:"meetings"`
}

type MeetingActor struct {
	ID              string `json:"id,omitempty"`
	Name            string `json:"name,omitempty"`
	ParticipantType string `json:"participant_type,omitempty"`
	Role            string `json:"role,omitempty"`
}

type MeetingTimelineItem struct {
	ID          string        `json:"id"`
	MeetingID   string        `json:"meeting_id"`
	Kind        string        `json:"kind"`
	EventTime   time.Time     `json:"event_time"`
	Actor       *MeetingActor `json:"actor,omitempty"`
	Text        string        `json:"text,omitempty"`
	MessageType int           `json:"message_type,omitempty"`
	ShareTitle  string        `json:"share_title,omitempty"`
	ShareURL    string        `json:"share_url,omitempty"`
	Visibility  string        `json:"visibility,omitempty"`
	TurnID      string        `json:"turn_id,omitempty"`
}

type ActiveMeeting struct {
	ID               string    `json:"id"`
	MeetingNumber    string    `json:"meeting_number,omitempty"`
	Topic            string    `json:"topic,omitempty"`
	Status           string    `json:"status"`
	ChannelID        string    `json:"channel_id"`
	ChannelName      string    `json:"channel_name"`
	Platform         string    `json:"platform"`
	BotName          string    `json:"bot_name,omitempty"`
	AgentName        string    `json:"agent_name,omitempty"`
	ResponseMode     string    `json:"response_mode"`
	JoinedAt         time.Time `json:"joined_at,omitempty"`
	StartedAt        time.Time `json:"started_at,omitempty"`
	EndedAt          time.Time `json:"ended_at,omitempty"`
	LastActivityAt   time.Time `json:"last_activity_at,omitempty"`
	ParticipantCount int       `json:"participant_count,omitempty"`
}

type MeetingTurn struct {
	ID        string    `json:"id"`
	MeetingID string    `json:"meeting_id"`
	Question  string    `json:"question"`
	Source    string    `json:"source"`
	Status    string    `json:"status"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	StartedAt time.Time `json:"started_at,omitempty"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
}

type MeetingDetail struct {
	Meeting ActiveMeeting         `json:"meeting"`
	Items   []MeetingTimelineItem `json:"items"`
	Turns   []MeetingTurn         `json:"turns"`
}

// MeetingEvent is the payload delivered to SSE consumers. Invitation and
// lifecycle updates use meeting.changed; activity and Agent turn updates carry
// the normalized data directly, so remote clients do not have to poll again.
type MeetingEvent struct {
	Type      string                `json:"type,omitempty"`
	ChannelID string                `json:"channel_id"`
	MeetingID string                `json:"meeting_id,omitempty"`
	Meeting   *ActiveMeeting        `json:"meeting,omitempty"`
	Items     []MeetingTimelineItem `json:"items,omitempty"`
	Turn      *MeetingTurn          `json:"turn,omitempty"`
	CreatedAt time.Time             `json:"created_at"`
}

// MeetingPlatform is an optional capability implemented by platforms that
// can receive invitations and join meetings as an application bot.
type MeetingPlatform interface {
	MeetingInvitations() []MeetingInvitation
	RespondMeetingInvitation(ctx context.Context, invitationID, nonce, decision string) (MeetingInvitation, error)
	JoinMeetingByNumber(ctx context.Context, meetingNumber string) (MeetingJoinResult, error)
}

// MeetingActivityPlatform is the live-meeting extension implemented by VC
// adapters. Keeping it optional preserves lightweight/non-VC platforms.
type MeetingActivityPlatform interface {
	ActiveMeetings() []ActiveMeeting
	MeetingActivity(meetingID string) (MeetingDetail, error)
	SendMeetingMessage(ctx context.Context, meetingID, text, uuid string) error
	UserActiveMeetings(ctx context.Context, userID string) ([]ActiveMeeting, error)
	MeetingPromptContext(meetingID string) string
	UpsertMeetingTurn(turn MeetingTurn)
}

// MeetingResponseModeConfigurer updates a live platform's TTS path without
// reconnecting its long-lived event stream or dropping active meeting state.
type MeetingResponseModeConfigurer interface {
	ConfigureMeetingResponseMode(config map[string]string) error
}

// MeetingResponseModeStore persists channel-level meeting output choices.
// ConnectService implements it for console-managed channels.
type MeetingResponseModeStore interface {
	SetMeetingResponseMode(ctx context.Context, channelID, mode string) (string, error)
}

func (e *Engine) MeetingSnapshot() MeetingSnapshot {
	runtimes := e.meetingRuntimes()
	snapshot := MeetingSnapshot{
		Channels:    make([]MeetingChannel, 0, len(runtimes)),
		Invitations: []MeetingInvitation{},
		Meetings:    []ActiveMeeting{},
	}
	for _, rt := range runtimes {
		platform, ok := rt.platform.(MeetingPlatform)
		if !ok {
			continue
		}
		status := rt.status()
		channel := MeetingChannel{
			ChannelID: rt.channel.ID, ChannelName: rt.channel.Name, Platform: rt.channel.Type,
			AgentName: rt.workspace.AgentName, ResponseMode: rt.currentMeetingResponseMode(),
			State: status.State, Connected: status.Connected, Error: status.Error,
		}
		snapshot.Channels = append(snapshot.Channels, channel)
		for _, invitation := range platform.MeetingInvitations() {
			invitation.ChannelID = rt.channel.ID
			invitation.ChannelName = rt.channel.Name
			invitation.Platform = rt.channel.Type
			snapshot.Invitations = append(snapshot.Invitations, invitation)
		}
		if activity, ok := rt.platform.(MeetingActivityPlatform); ok {
			for _, meeting := range activity.ActiveMeetings() {
				meeting.ChannelID = rt.channel.ID
				meeting.ChannelName = rt.channel.Name
				meeting.Platform = rt.channel.Type
				meeting.AgentName = rt.workspace.AgentName
				meeting.ResponseMode = rt.currentMeetingResponseMode()
				snapshot.Meetings = append(snapshot.Meetings, meeting)
			}
		}
	}
	sort.Slice(snapshot.Channels, func(i, j int) bool {
		return strings.ToLower(snapshot.Channels[i].ChannelName) < strings.ToLower(snapshot.Channels[j].ChannelName)
	})
	sort.Slice(snapshot.Invitations, func(i, j int) bool {
		return snapshot.Invitations[i].CreatedAt.Before(snapshot.Invitations[j].CreatedAt)
	})
	sort.Slice(snapshot.Meetings, func(i, j int) bool {
		return snapshot.Meetings[i].LastActivityAt.After(snapshot.Meetings[j].LastActivityAt)
	})
	return snapshot
}

// SubscribeMeetingEvents registers a non-blocking meeting-state listener.
// The bounded buffer retains payload and ordering for the live timeline while
// keeping publishers independent from slow or disconnected consoles.
func (e *Engine) SubscribeMeetingEvents() (<-chan MeetingEvent, func()) {
	if e == nil {
		closed := make(chan MeetingEvent)
		close(closed)
		return closed, func() {}
	}
	e.meetingEventMu.Lock()
	if e.meetingEventSubscribers == nil {
		e.meetingEventSubscribers = make(map[uint64]chan MeetingEvent)
	}
	e.nextMeetingEventID++
	id := e.nextMeetingEventID
	updates := make(chan MeetingEvent, 256)
	e.meetingEventSubscribers[id] = updates
	e.meetingEventMu.Unlock()
	var once sync.Once
	return updates, func() {
		once.Do(func() {
			e.meetingEventMu.Lock()
			delete(e.meetingEventSubscribers, id)
			e.meetingEventMu.Unlock()
		})
	}
}

func (e *Engine) publishMeetingEvent(channelID string, supplied ...MeetingEvent) {
	if e == nil {
		return
	}
	event := MeetingEvent{Type: "meeting.changed", ChannelID: strings.TrimSpace(channelID), CreatedAt: time.Now().UTC()}
	if len(supplied) > 0 {
		event = supplied[0]
		event.ChannelID = strings.TrimSpace(channelID)
		if event.Type == "" {
			event.Type = "meeting.changed"
		}
		if event.CreatedAt.IsZero() {
			event.CreatedAt = time.Now().UTC()
		}
	}
	e.meetingEventMu.Lock()
	defer e.meetingEventMu.Unlock()
	for _, updates := range e.meetingEventSubscribers {
		select {
		case updates <- event:
		default:
		}
	}
}

func (e *Engine) MeetingActivity(channelID, meetingID string) (MeetingDetail, error) {
	rt, _, err := e.meetingPlatform(channelID)
	if err != nil {
		return MeetingDetail{}, err
	}
	activity, ok := rt.platform.(MeetingActivityPlatform)
	if !ok {
		return MeetingDetail{}, fmt.Errorf("channel %q does not expose meeting activity", channelID)
	}
	detail, err := activity.MeetingActivity(strings.TrimSpace(meetingID))
	if err != nil {
		return MeetingDetail{}, err
	}
	detail.Meeting.ChannelID = rt.channel.ID
	detail.Meeting.ChannelName = rt.channel.Name
	detail.Meeting.Platform = rt.channel.Type
	detail.Meeting.AgentName = rt.workspace.AgentName
	detail.Meeting.ResponseMode = rt.currentMeetingResponseMode()
	return detail, nil
}

func (e *Engine) SetMeetingResponseMode(ctx context.Context, channelID, mode string) (string, error) {
	if e == nil || e.meetingResponseModes == nil {
		return "", fmt.Errorf("meeting response mode store is unavailable")
	}
	return e.meetingResponseModes.SetMeetingResponseMode(ctx, channelID, mode)
}

func (e *Engine) applyMeetingResponseMode(channelID string, channel Channel, updatedAt time.Time) error {
	rt, _, err := e.meetingPlatform(channelID)
	if err != nil {
		return err
	}
	mode := ChannelMeetingResponseMode(channel)
	configurer, configurable := rt.platform.(MeetingResponseModeConfigurer)
	if MeetingResponseModeUsesVoice(mode) && !configurable {
		return fmt.Errorf("channel %q does not support meeting voice replies", channelID)
	}
	if configurable {
		if err := configurer.ConfigureMeetingResponseMode(channel.Config); err != nil {
			return err
		}
	}
	rt.setMeetingResponseMode(mode)
	rt.setDefinitionUpdatedAt(updatedAt)
	return nil
}

func (e *Engine) SendMeetingMessage(ctx context.Context, channelID, meetingID, text, uuid string) error {
	rt, _, err := e.meetingPlatform(channelID)
	if err != nil {
		return err
	}
	activity, ok := rt.platform.(MeetingActivityPlatform)
	if !ok {
		return fmt.Errorf("channel %q does not support meeting messages", channelID)
	}
	text = strings.TrimSpace(text)
	if strings.TrimSpace(meetingID) == "" || text == "" {
		return fmt.Errorf("meeting_id and text are required")
	}
	return activity.SendMeetingMessage(ctx, strings.TrimSpace(meetingID), text, strings.TrimSpace(uuid))
}

func (e *Engine) RespondMeetingInvitation(
	ctx context.Context,
	channelID, invitationID, nonce, decision string,
) (MeetingInvitation, error) {
	rt, platform, err := e.meetingPlatform(channelID)
	if err != nil {
		return MeetingInvitation{}, err
	}
	invitation, err := platform.RespondMeetingInvitation(ctx, invitationID, nonce, decision)
	if err != nil {
		return MeetingInvitation{}, err
	}
	invitation.ChannelID = rt.channel.ID
	invitation.ChannelName = rt.channel.Name
	invitation.Platform = rt.channel.Type
	return invitation, nil
}

func (e *Engine) JoinMeetingByNumber(ctx context.Context, channelID, meetingNumber string) (MeetingJoinResult, error) {
	rt, platform, err := e.meetingPlatform(channelID)
	if err != nil {
		return MeetingJoinResult{}, err
	}
	meetingNumber = strings.TrimSpace(meetingNumber)
	if !isNineDigitMeetingNumber(meetingNumber) {
		return MeetingJoinResult{}, fmt.Errorf("meeting number must be exactly 9 digits")
	}
	result, err := platform.JoinMeetingByNumber(ctx, meetingNumber)
	if err != nil {
		return MeetingJoinResult{}, err
	}
	result.ChannelID = rt.channel.ID
	result.ChannelName = rt.channel.Name
	result.Platform = rt.channel.Type
	result.MeetingNumber = meetingNumber
	return result, nil
}

func (e *Engine) meetingPlatform(channelID string) (*channelRuntime, MeetingPlatform, error) {
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return nil, nil, fmt.Errorf("channel_id is required")
	}
	e.mu.RLock()
	rt := e.channels[channelID]
	e.mu.RUnlock()
	if rt == nil {
		return nil, nil, fmt.Errorf("channel %q is not attached", channelID)
	}
	platform, ok := rt.platform.(MeetingPlatform)
	if !ok {
		return nil, nil, fmt.Errorf("channel %q does not support meeting controls", channelID)
	}
	status := rt.status()
	if !status.Connected {
		return nil, nil, fmt.Errorf("channel %q is not connected", channelID)
	}
	return rt, platform, nil
}

func (e *Engine) meetingRuntimes() []*channelRuntime {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]*channelRuntime, 0, len(e.channels))
	for _, rt := range e.channels {
		if rt != nil {
			out = append(out, rt)
		}
	}
	return out
}

func isNineDigitMeetingNumber(value string) bool {
	if len(value) != 9 {
		return false
	}
	for i := range value {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}
