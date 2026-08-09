// Package feishu implements the Feishu/Lark platform adapter. It connects via
// Feishu's long-connection (WebSocket) so no public IP is required, receives
// message events, and replies through the IM API.
//
// This adapter is structured for the official larksuite/oapi-sdk-go but keeps
// the SDK wiring behind a small client interface so the core build does not
// require network credentials. The concrete SDK client is constructed lazily
// in Start when app_id/app_secret are present.
package feishu

import (
	"context"
	"fmt"
	"strings"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/wangning19940904/AgentMux/core"
)

func init() {
	core.RegisterPlatform("feishu", func(cfg map[string]any) (core.Platform, error) {
		return newPlatform("feishu", lark.FeishuBaseUrl, cfg)
	})
	core.RegisterPlatform("lark", func(cfg map[string]any) (core.Platform, error) {
		return newPlatform("lark", lark.LarkBaseUrl, cfg)
	})
}

// Platform is the Feishu adapter.
type Platform struct {
	name             string
	domain           string
	appID            string
	appSecret        string
	project          string
	voice            meetingVoiceConfig
	meetingGreeting  string
	meetingNotify    func(core.MeetingEvent)
	agentName        string
	channelName      string
	meetingUsers     []string
	meetingWakeWords []string

	client clientAPI
}

func newPlatform(name, domain string, cfg map[string]any) (*Platform, error) {
	p := &Platform{name: name, domain: domain}
	p.appID, _ = cfg["app_id"].(string)
	p.appSecret, _ = cfg["app_secret"].(string)
	p.project, _ = cfg["project"].(string)
	p.meetingGreeting, _ = cfg[core.ChannelConfigMeetingGreeting].(string)
	customMeetingGreeting := strings.TrimSpace(p.meetingGreeting) != ""
	p.meetingNotify, _ = cfg["meeting_event_notify"].(func(core.MeetingEvent))
	p.agentName, _ = cfg["agent_name"].(string)
	p.channelName, _ = cfg["channel_name"].(string)
	p.meetingUsers = meetingBootstrapUsers(cfg)
	wakeWords, err := core.ParseMeetingVoiceWakeWords(configString(cfg, core.ChannelConfigMeetingWakeWords))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	p.meetingWakeWords = wakeWords
	if strings.TrimSpace(p.meetingGreeting) == "" {
		p.meetingGreeting = "大家好，我是 {{agent_name}}，由 AgentMux 驱动的会议智能体。\n使用说明：@{{bot_name}} 可以跟机器人对话。"
	}
	voice, err := parseMeetingVoiceConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	p.voice = voice
	if voice.Enabled && !customMeetingGreeting {
		p.meetingGreeting += "\n语音提问：在会议中说“{{bot_name}}，……”即可唤醒我回答。"
	}
	if p.appID == "" || p.appSecret == "" {
		return nil, fmt.Errorf("%s: app_id and app_secret are required", name)
	}
	return p, nil
}

// Name returns the registered name.
func (p *Platform) Name() string { return p.name }

// ChannelHealth exposes the long-connection lifecycle and heartbeat snapshot
// to AgentMux's channel watchdog.
func (p *Platform) ChannelHealth() core.PlatformHealth {
	if p.client == nil {
		return core.PlatformHealth{State: core.ChannelStateStarting, CheckedAt: time.Now()}
	}
	if reporter, ok := p.client.(interface{ ChannelHealth() core.PlatformHealth }); ok {
		return reporter.ChannelHealth()
	}
	return core.PlatformHealth{State: core.ChannelStateRunning, Connected: true, CheckedAt: time.Now()}
}

// Start opens the long connection and forwards inbound messages.
func (p *Platform) Start(ctx context.Context, inbound chan<- *core.Message) error {
	if p.client == nil {
		c, err := newLarkClient(p.name, p.domain, p.appID, p.appSecret, p.voice, p.meetingGreeting, p.agentName, p.channelName, p.meetingUsers, p.meetingWakeWords, p.meetingNotify)
		if err != nil {
			return err
		}
		p.client = c
	}
	return p.client.Listen(ctx, p.project, inbound)
}

func configString(cfg map[string]any, key string) string {
	value, _ := cfg[key].(string)
	return value
}

func meetingBootstrapUsers(cfg map[string]any) []string {
	seen := map[string]struct{}{}
	var users []string
	for _, key := range []string{core.ChannelConfigAllowedUserIDs, core.ChannelConfigAdminUserIDs} {
		value, _ := cfg[key].(string)
		for _, userID := range strings.FieldsFunc(value, func(r rune) bool {
			return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
		}) {
			userID = strings.TrimSpace(userID)
			if userID == "" {
				continue
			}
			if _, exists := seen[userID]; exists {
				continue
			}
			seen[userID] = struct{}{}
			users = append(users, userID)
		}
	}
	return users
}

func (p *Platform) ActiveMeetings() []core.ActiveMeeting {
	client, ok := p.client.(meetingActivityClient)
	if !ok {
		return []core.ActiveMeeting{}
	}
	return client.ActiveMeetings()
}

func (p *Platform) MeetingActivity(meetingID string) (core.MeetingDetail, error) {
	client, ok := p.client.(meetingActivityClient)
	if !ok {
		return core.MeetingDetail{}, fmt.Errorf("%s: meeting activity is unavailable", p.name)
	}
	return client.MeetingActivity(meetingID)
}

func (p *Platform) SendMeetingMessage(ctx context.Context, meetingID, text, uuid string) error {
	client, ok := p.client.(meetingActivityClient)
	if !ok {
		return fmt.Errorf("%s: meeting messages are unavailable", p.name)
	}
	return client.SendMeetingMessage(ctx, meetingID, text, uuid)
}

func (p *Platform) UserActiveMeetings(ctx context.Context, userID string) ([]core.ActiveMeeting, error) {
	client, ok := p.client.(meetingActivityClient)
	if !ok {
		return nil, fmt.Errorf("%s: active meeting lookup is unavailable", p.name)
	}
	return client.UserActiveMeetings(ctx, userID)
}

func (p *Platform) MeetingPromptContext(meetingID string) string {
	client, ok := p.client.(meetingActivityClient)
	if !ok {
		return ""
	}
	return client.MeetingPromptContext(meetingID)
}

func (p *Platform) UpsertMeetingTurn(turn core.MeetingTurn) {
	if client, ok := p.client.(meetingActivityClient); ok {
		client.UpsertMeetingTurn(turn)
	}
}

func (p *Platform) MeetingInvitations() []core.MeetingInvitation {
	client, ok := p.client.(meetingControlClient)
	if !ok {
		return []core.MeetingInvitation{}
	}
	return client.MeetingInvitations()
}

func (p *Platform) RespondMeetingInvitation(
	ctx context.Context,
	invitationID, nonce, decision string,
) (core.MeetingInvitation, error) {
	client, ok := p.client.(meetingControlClient)
	if !ok {
		return core.MeetingInvitation{}, fmt.Errorf("%s: meeting controls are unavailable", p.name)
	}
	return client.RespondMeetingInvitation(ctx, invitationID, nonce, decision)
}

func (p *Platform) JoinMeetingByNumber(ctx context.Context, meetingNumber string) (core.MeetingJoinResult, error) {
	client, ok := p.client.(meetingControlClient)
	if !ok {
		return core.MeetingJoinResult{}, fmt.Errorf("%s: meeting controls are unavailable", p.name)
	}
	return client.JoinMeetingDirect(ctx, meetingNumber)
}

// Reply responds to the chat that originated msg.
func (p *Platform) Reply(ctx context.Context, msg *core.Message, text string) error {
	if p.client == nil {
		return fmt.Errorf("%s: client not started", p.name)
	}
	if messageID := threadReplyMessageID(msg); messageID != "" {
		if client, ok := p.client.(threadReplyClient); ok {
			_, err := client.ReplyText(ctx, messageID, text)
			return err
		}
	}
	_, err := p.client.SendText(ctx, msg.ChatID, text)
	return err
}

// Send delivers an unsolicited message to a chat.
func (p *Platform) Send(ctx context.Context, chatID, text string) error {
	if p.client == nil {
		return fmt.Errorf("%s: client not started", p.name)
	}
	_, err := p.client.SendText(ctx, chatID, text)
	return err
}

// ReplyImage uploads an image and delivers it to the originating conversation.
func (p *Platform) ReplyImage(ctx context.Context, msg *core.Message, fileName string, data []byte) error {
	client, ok := p.client.(attachmentClient)
	if !ok {
		return fmt.Errorf("%s: image delivery is unavailable", p.name)
	}
	if messageID := threadReplyMessageID(msg); messageID != "" {
		_, err := client.ReplyImage(ctx, messageID, fileName, data)
		return err
	}
	_, err := client.SendImage(ctx, msg.ChatID, fileName, data)
	return err
}

// ReplyFile uploads a file and delivers it to the originating conversation.
func (p *Platform) ReplyFile(ctx context.Context, msg *core.Message, fileName string, data []byte) error {
	client, ok := p.client.(attachmentClient)
	if !ok {
		return fmt.Errorf("%s: file delivery is unavailable", p.name)
	}
	if messageID := threadReplyMessageID(msg); messageID != "" {
		_, err := client.ReplyFile(ctx, messageID, fileName, data)
		return err
	}
	_, err := client.SendFile(ctx, msg.ChatID, fileName, data)
	return err
}

// BeginMessageReply opens a streaming plain-text reply for the chat that
// originated msg. The first Update posts a text message; later Updates edit it.
func (p *Platform) BeginMessageReply(ctx context.Context, msg *core.Message) (core.ReplyStream, error) {
	if p.client == nil {
		return nil, fmt.Errorf("%s: client not started", p.name)
	}
	return &textStream{client: p.client, chatID: msg.ChatID, replyMessageID: threadReplyMessageID(msg)}, nil
}

// BeginReply opens a streaming interactive-card reply for the chat that
// originated msg. It implements core.StreamReplier so the engine renders a
// whole agent turn as one in-place updating card.
func (p *Platform) BeginReply(ctx context.Context, msg *core.Message) (core.ReplyStream, error) {
	if p.client == nil {
		return nil, fmt.Errorf("%s: client not started", p.name)
	}
	return &cardStream{client: p.client, chatID: msg.ChatID, replyMessageID: threadReplyMessageID(msg)}, nil
}

// BeginTaskReply opens a task-scoped streaming card. While the turn is active
// the card exposes a stop button whose callback is bound to taskID.
func (p *Platform) BeginTaskReply(ctx context.Context, msg *core.Message, taskID string) (core.ReplyStream, error) {
	if p.client == nil {
		return nil, fmt.Errorf("%s: client not started", p.name)
	}
	control := newStreamCardControl(msg, taskID)
	return &cardStream{
		client: p.client, chatID: msg.ChatID, replyMessageID: threadReplyMessageID(msg), control: control,
	}, nil
}

// BeginSpeechReply mirrors the assistant answer to the active meeting joined
// through the invite approval flow. A nil reply means voice is disabled or the
// bot is not currently in a meeting.
func (p *Platform) BeginSpeechReply(ctx context.Context, msg *core.Message) (core.SpeechReply, error) {
	if p.client == nil {
		return nil, fmt.Errorf("%s: client not started", p.name)
	}
	client, ok := p.client.(meetingVoiceClient)
	if !ok {
		return nil, nil
	}
	return client.BeginMeetingSpeech(ctx, msg)
}

// ConfigureMeetingResponseMode hot-swaps the TTS worker while preserving the
// live Lark connection and the meeting activation correlation.
func (p *Platform) ConfigureMeetingResponseMode(config map[string]string) error {
	values := make(map[string]any, len(config))
	for key, value := range config {
		values[key] = value
	}
	voice, err := parseMeetingVoiceConfig(values)
	if err != nil {
		return err
	}
	p.voice = voice
	if p.client == nil {
		return nil
	}
	client, ok := p.client.(meetingVoiceConfigurableClient)
	if !ok {
		if voice.Enabled {
			return fmt.Errorf("%s: live meeting voice configuration is unavailable", p.name)
		}
		return nil
	}
	client.ConfigureMeetingVoice(voice)
	return nil
}

// ReplyModelPicker renders /model status as an interactive selector.
func (p *Platform) ReplyModelPicker(ctx context.Context, msg *core.Message, state core.ModelPickerState) error {
	if p.client == nil {
		return fmt.Errorf("%s: client not started", p.name)
	}
	_, err := p.client.SendModelPickerCard(ctx, msg, state)
	return err
}

func (p *Platform) ReplyHelpCard(ctx context.Context, msg *core.Message, state core.HelpCardState) error {
	client, ok := p.client.(helpCardClient)
	if !ok {
		return fmt.Errorf("%s: help cards are unavailable", p.name)
	}
	_, err := client.SendHelpCard(ctx, msg, state)
	return err
}

func (p *Platform) ReplyRuntimeSettingsPicker(ctx context.Context, msg *core.Message, state core.RuntimeSettingsPickerState) error {
	if p.client == nil {
		return fmt.Errorf("%s: client not started", p.name)
	}
	_, err := p.client.SendRuntimeSettingsPickerCard(ctx, msg, state)
	return err
}

func (p *Platform) UpdateRuntimeSettingsPicker(ctx context.Context, msg *core.Message, state core.RuntimeSettingsPickerState) error {
	if p.client == nil {
		return fmt.Errorf("%s: client not started", p.name)
	}
	if msg == nil || (msg.ID == "" && msg.InteractionMessageID == "") {
		return fmt.Errorf("%s: missing settings picker message id", p.name)
	}
	messageID := msg.InteractionMessageID
	if messageID == "" {
		messageID = msg.ID
	}
	return p.client.UpdateRuntimeSettingsPickerCard(ctx, messageID, msg, state)
}

func (p *Platform) ReplyAgentInteraction(ctx context.Context, msg *core.Message, task core.ChannelTask, interaction core.ChannelInteraction) (string, error) {
	client, ok := p.client.(interactionCardClient)
	if !ok {
		return "", fmt.Errorf("%s: interaction cards are unavailable", p.name)
	}
	return client.SendAgentInteractionCard(ctx, msg, task, interaction)
}

func (p *Platform) UpdateAgentInteraction(ctx context.Context, msg *core.Message, interaction core.ChannelInteraction, outcome string) error {
	client, ok := p.client.(interactionCardClient)
	if !ok {
		return fmt.Errorf("%s: interaction cards are unavailable", p.name)
	}
	messageID := msg.InteractionMessageID
	if messageID == "" {
		messageID = msg.ID
	}
	return client.UpdateAgentInteractionCard(ctx, messageID, interaction, outcome)
}

// AddReaction marks the inbound message while the agent is working.
func (p *Platform) AddReaction(ctx context.Context, msg *core.Message, emojiType string) (string, error) {
	if p.client == nil {
		return "", fmt.Errorf("%s: client not started", p.name)
	}
	return p.client.AddReaction(ctx, msg.ID, emojiType)
}

// DeleteReaction removes a mark previously added by AddReaction.
func (p *Platform) DeleteReaction(ctx context.Context, msg *core.Message, reactionID string) error {
	if p.client == nil {
		return fmt.Errorf("%s: client not started", p.name)
	}
	return p.client.DeleteReaction(ctx, msg.ID, reactionID)
}

// textStream is a live Feishu text message: the first Update posts the
// message, later Updates edit it in place.
type textStream struct {
	client         clientAPI
	chatID         string
	replyMessageID string
	messageID      string
}

func (s *textStream) Update(ctx context.Context, text string, done, failed bool) error {
	if s.messageID == "" {
		var id string
		var err error
		if s.replyMessageID != "" {
			if client, ok := s.client.(threadReplyClient); ok {
				id, err = client.ReplyText(ctx, s.replyMessageID, text)
			} else {
				id, err = s.client.SendText(ctx, s.chatID, text)
			}
		} else {
			id, err = s.client.SendText(ctx, s.chatID, text)
		}
		if err != nil {
			return err
		}
		s.messageID = id
		return nil
	}
	return s.client.UpdateText(ctx, s.messageID, text)
}

func (s *textStream) Close(ctx context.Context) error { return nil }

// cardStream is a live Feishu card. It prefers the native CardKit streaming
// path (a card entity whose text element is updated with a real typewriter
// effect); if creating that entity fails (e.g. missing cardkit:card:write
// permission) it degrades to the legacy path of posting a card message and
// patching it in place.
type cardStream struct {
	client         clientAPI
	chatID         string
	replyMessageID string

	// native streaming path state
	cardID   string
	sequence int
	fellBack bool

	// legacy patch path state
	messageID string
	control   *streamCardControl
}

type streamCardControl struct {
	taskID          string
	chatID          string
	chatType        string
	conversationKey string
}

func newStreamCardControl(msg *core.Message, taskID string) *streamCardControl {
	if msg == nil || taskID == "" {
		return nil
	}
	return &streamCardControl{
		taskID: taskID, chatID: msg.ChatID, chatType: msg.ChatType,
		conversationKey: core.ResolveConversationKey(msg),
	}
}

func (s *cardStream) Update(ctx context.Context, text string, done, failed bool) error {
	if !s.fellBack && s.cardID == "" {
		// First update: try to open a native streaming card.
		var id string
		var err error
		if s.replyMessageID != "" {
			if client, ok := s.client.(threadReplyClient); ok {
				id, err = client.BeginStreamCardReply(ctx, s.replyMessageID, s.control)
			} else {
				id, err = s.client.BeginStreamCard(ctx, s.chatID, s.control)
			}
		} else {
			id, err = s.client.BeginStreamCard(ctx, s.chatID, s.control)
		}
		if err != nil {
			s.fellBack = true
		} else {
			s.cardID = id
		}
	}

	if s.cardID != "" {
		return s.updateNative(ctx, text, done, failed)
	}
	return s.updateLegacy(ctx, text, done, failed)
}

func (s *cardStream) updateNative(ctx context.Context, text string, done, failed bool) error {
	s.sequence++
	if done {
		return s.client.FinishStreamCard(ctx, s.cardID, text, s.sequence, failed, s.control)
	}
	return s.client.StreamCardText(ctx, s.cardID, text, s.sequence)
}

func (s *cardStream) updateLegacy(ctx context.Context, text string, done, failed bool) error {
	if s.messageID == "" {
		var id string
		var err error
		if s.replyMessageID != "" {
			if client, ok := s.client.(threadReplyClient); ok {
				id, err = client.ReplyCard(ctx, s.replyMessageID, text, done, failed, s.control)
			} else {
				id, err = s.client.SendCard(ctx, s.chatID, text, done, failed, s.control)
			}
		} else {
			id, err = s.client.SendCard(ctx, s.chatID, text, done, failed, s.control)
		}
		if err != nil {
			return err
		}
		s.messageID = id
		return nil
	}
	return s.client.UpdateCard(ctx, s.messageID, text, done, failed, s.control)
}

func (s *cardStream) Close(ctx context.Context) error { return nil }

func shouldReplyInThread(msg *core.Message) bool {
	if msg == nil || msg.ID == "" {
		return false
	}
	return msg.ThreadID != "" || msg.RootID != "" || (msg.ChatType != "" && msg.ChatType != "p2p")
}

func threadReplyMessageID(msg *core.Message) string {
	if shouldReplyInThread(msg) {
		if msg.InteractionMessageID != "" {
			return msg.InteractionMessageID
		}
		return msg.ID
	}
	return ""
}

// Stop closes the connection.
func (p *Platform) Stop(ctx context.Context) error {
	if p.client == nil {
		return nil
	}
	return p.client.Close()
}
