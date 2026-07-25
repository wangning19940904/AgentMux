package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkcardkit "github.com/larksuite/oapi-sdk-go/v3/service/cardkit/v1"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
	"github.com/wangning19940904/AgentMux/core"
)

// streamCardElementID is the fixed element_id of the markdown component we
// stream text into. It must match the element_id embedded in the card JSON
// created by BeginStreamCard.
const streamCardElementID = "answer"

const (
	modelPickerActionKey    = "agentmux_action"
	modelPickerActionSelect = "model_select"
	modelPickerActionReset  = "model_reset"
	runtimeSettingsAction   = "runtime_settings"
	codexInteractionAction  = "codex_interaction"
	larkWSStartupTimeout    = 45 * time.Second
	larkWSHeartbeatTimeout  = 6 * time.Minute
)

// larkClient wraps the official Lark SDK: a WebSocket client for inbound events
// and an API client for outbound messages.
type larkClient struct {
	platform  string
	domain    string
	appID     string
	appSecret string
	api       *lark.Client
	ws        *larkws.Client
	cancel    context.CancelFunc
	botOpenID string

	mu              sync.Mutex
	closing         bool
	healthState     string
	healthError     string
	healthStartedAt time.Time
	connectedAt     time.Time
	lastHeartbeatAt time.Time
	lastEventAt     time.Time
	lastInboundAt   time.Time

	meetingInvites *meetingInviteController
	meetingVoice   *meetingVoiceManager
}

func newLarkClient(platform, domain, appID, appSecret string, voiceConfig meetingVoiceConfig) (clientAPI, error) {
	client := &larkClient{
		platform:  platform,
		domain:    domain,
		appID:     appID,
		appSecret: appSecret,
		api:       lark.NewClient(appID, appSecret, lark.WithOpenBaseUrl(domain)),
	}
	client.meetingInvites = newMeetingInviteController(client)
	client.meetingVoice = newMeetingVoiceManager(client, voiceConfig)
	return client, nil
}

func (c *larkClient) Listen(ctx context.Context, project string, inbound chan<- *core.Message) error {
	c.beginHealth()
	botOpenID := c.loadBotOpenID(ctx)
	handler := dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(func(_ context.Context, event *larkim.P2MessageReceiveV1) error {
			c.markEvent()
			if event == nil || event.Event == nil || event.Event.Message == nil {
				return nil
			}
			msg := event.Event.Message
			if msg.MessageType == nil || msg.Content == nil {
				return nil
			}
			text := extractText(*msg.MessageType, *msg.Content)
			if text == "" {
				return nil
			}
			messageID := ""
			if msg.MessageId != nil {
				messageID = *msg.MessageId
			}
			chatID := ""
			if msg.ChatId != nil {
				chatID = *msg.ChatId
			}
			chatType := ""
			if msg.ChatType != nil {
				chatType = *msg.ChatType
			}
			rootID := ""
			if msg.RootId != nil {
				rootID = *msg.RootId
			}
			parentID := ""
			if msg.ParentId != nil {
				parentID = *msg.ParentId
			}
			threadID := ""
			if msg.ThreadId != nil {
				threadID = *msg.ThreadId
			}
			userID := ""
			if event.Event.Sender != nil && event.Event.Sender.SenderId != nil &&
				event.Event.Sender.SenderId.OpenId != nil {
				userID = *event.Event.Sender.SenderId.OpenId
			}
			mentionedBot, mentionAll := mentionState(msg, botOpenID, text)
			c.markInbound()
			inbound <- &core.Message{
				ID:           messageID,
				ChatID:       chatID,
				ChatType:     chatType,
				RootID:       rootID,
				ParentID:     parentID,
				ThreadID:     threadID,
				UserID:       userID,
				Text:         text,
				MentionedBot: mentionedBot,
				MentionAll:   mentionAll,
				Platform:     c.platform,
				Project:      project,
			}
			return nil
		}).
		OnCustomizedEvent(meetingInvitedEventType, func(eventCtx context.Context, event *larkevent.EventReq) error {
			c.markEvent()
			if c.meetingInvites == nil || event == nil {
				return nil
			}
			return c.meetingInvites.HandleInvitation(eventCtx, event.Body)
		}).
		OnCustomizedEvent(meetingEndedEventType, func(_ context.Context, event *larkevent.EventReq) error {
			c.markEvent()
			if c.meetingVoice == nil || event == nil {
				return nil
			}
			c.meetingVoice.HandleMeetingEnded(event.Body)
			return nil
		}).
		OnP2CardActionTrigger(func(eventCtx context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
			c.markEvent()
			if c.meetingInvites != nil {
				if handled, response := c.meetingInvites.HandleAction(ctx, event); handled {
					c.markInbound()
					return response, nil
				}
			}
			msg, ok := c.messageFromCardAction(project, event)
			if !ok {
				return nil, nil
			}
			c.markInbound()
			select {
			case inbound <- msg:
			case <-ctx.Done():
			case <-eventCtx.Done():
			}
			return nil, nil
		})

	logger := &larkWSHealthLogger{
		client:   c,
		delegate: larkcore.NewDefaultLogger(larkcore.LogLevelInfo),
	}
	ws := larkws.NewClient(
		c.appID,
		c.appSecret,
		larkws.WithDomain(c.domain),
		larkws.WithEventHandler(handler),
		larkws.WithLogger(logger),
		larkws.WithOnReady(c.markReady),
		larkws.WithOnReconnecting(c.markReconnecting),
		larkws.WithOnReconnected(c.markReady),
		larkws.WithOnDisconnected(c.markDisconnected),
		larkws.WithOnError(c.markError),
	)
	wsCtx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.ws = ws
	c.cancel = cancel
	c.mu.Unlock()
	// Start blocks; run until context cancelled.
	errCh := make(chan error, 1)
	go func() { errCh <- ws.Start(wsCtx) }()
	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return err
	}
}

func (c *larkClient) SendText(ctx context.Context, chatID, text string) (string, error) {
	content, _ := json.Marshal(map[string]string{"text": text})
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("chat_id").
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).
			MsgType(larkim.MsgTypeText).
			Content(string(content)).
			Build()).
		Build()
	resp, err := c.api.Im.Message.Create(ctx, req)
	if err != nil {
		return "", err
	}
	if !resp.Success() {
		return "", fmt.Errorf("%s send failed: %s", c.platform, resp.Msg)
	}
	if resp.Data == nil || resp.Data.MessageId == nil {
		return "", fmt.Errorf("%s send text: missing message id", c.platform)
	}
	return *resp.Data.MessageId, nil
}

func (c *larkClient) ReplyText(ctx context.Context, messageID, text string) (string, error) {
	content, _ := json.Marshal(map[string]string{"text": text})
	return c.replyMessage(ctx, messageID, larkim.MsgTypeText, string(content))
}

func (c *larkClient) UpdateText(ctx context.Context, messageID, text string) error {
	content, _ := json.Marshal(map[string]string{"text": text})
	req := larkim.NewUpdateMessageReqBuilder().
		MessageId(messageID).
		Body(larkim.NewUpdateMessageReqBodyBuilder().
			MsgType(larkim.MsgTypeText).
			Content(string(content)).
			Build()).
		Build()
	resp, err := c.api.Im.Message.Update(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success() {
		return fmt.Errorf("%s update text failed: %s", c.platform, resp.Msg)
	}
	return nil
}

func (c *larkClient) SendCard(ctx context.Context, chatID, text string, done, failed bool) (string, error) {
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("chat_id").
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).
			MsgType(larkim.MsgTypeInteractive).
			Content(buildCard(text, done, failed)).
			Build()).
		Build()
	resp, err := c.api.Im.Message.Create(ctx, req)
	if err != nil {
		return "", err
	}
	if !resp.Success() {
		return "", fmt.Errorf("%s send card failed: %s", c.platform, resp.Msg)
	}
	if resp.Data == nil || resp.Data.MessageId == nil {
		return "", fmt.Errorf("%s send card: missing message id", c.platform)
	}
	return *resp.Data.MessageId, nil
}

func (c *larkClient) ReplyCard(ctx context.Context, messageID, text string, done, failed bool) (string, error) {
	return c.replyMessage(ctx, messageID, larkim.MsgTypeInteractive, buildCard(text, done, failed))
}

func (c *larkClient) replyMessage(ctx context.Context, messageID, msgType, content string) (string, error) {
	replyInThread := true
	req := larkim.NewReplyMessageReqBuilder().
		MessageId(messageID).
		Body(larkim.NewReplyMessageReqBodyBuilder().
			MsgType(msgType).
			Content(content).
			ReplyInThread(replyInThread).
			Build()).
		Build()
	resp, err := c.api.Im.Message.Reply(ctx, req)
	if err != nil {
		return "", err
	}
	if !resp.Success() {
		return "", fmt.Errorf("%s reply failed: %s", c.platform, resp.Msg)
	}
	if resp.Data == nil || resp.Data.MessageId == nil {
		return "", fmt.Errorf("%s reply: missing message id", c.platform)
	}
	return *resp.Data.MessageId, nil
}

func (c *larkClient) SendModelPickerCard(ctx context.Context, msg *core.Message, state core.ModelPickerState) (string, error) {
	if shouldReplyInThread(msg) {
		return c.replyMessage(ctx, msg.ID, larkim.MsgTypeInteractive, buildModelPickerCard(msg, state))
	}
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("chat_id").
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(msg.ChatID).
			MsgType(larkim.MsgTypeInteractive).
			Content(buildModelPickerCard(msg, state)).
			Build()).
		Build()
	resp, err := c.api.Im.Message.Create(ctx, req)
	if err != nil {
		return "", err
	}
	if !resp.Success() {
		return "", fmt.Errorf("%s send model picker card failed: %s", c.platform, resp.Msg)
	}
	if resp.Data == nil || resp.Data.MessageId == nil {
		return "", fmt.Errorf("%s send model picker card: missing message id", c.platform)
	}
	return *resp.Data.MessageId, nil
}

func (c *larkClient) SendRuntimeSettingsPickerCard(ctx context.Context, msg *core.Message, state core.RuntimeSettingsPickerState) (string, error) {
	if shouldReplyInThread(msg) {
		return c.replyMessage(ctx, msg.ID, larkim.MsgTypeInteractive, buildRuntimeSettingsPickerCard(msg, state))
	}
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("chat_id").
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(msg.ChatID).
			MsgType(larkim.MsgTypeInteractive).
			Content(buildRuntimeSettingsPickerCard(msg, state)).
			Build()).
		Build()
	resp, err := c.api.Im.Message.Create(ctx, req)
	if err != nil {
		return "", err
	}
	if !resp.Success() {
		return "", fmt.Errorf("%s send runtime settings picker failed: %s", c.platform, resp.Msg)
	}
	if resp.Data == nil || resp.Data.MessageId == nil {
		return "", fmt.Errorf("%s send runtime settings picker: missing message id", c.platform)
	}
	return *resp.Data.MessageId, nil
}

func (c *larkClient) UpdateRuntimeSettingsPickerCard(ctx context.Context, messageID string, msg *core.Message, state core.RuntimeSettingsPickerState) error {
	req := larkim.NewPatchMessageReqBuilder().
		MessageId(messageID).
		Body(larkim.NewPatchMessageReqBodyBuilder().
			Content(buildRuntimeSettingsPickerCard(msg, state)).
			Build()).
		Build()
	resp, err := c.api.Im.Message.Patch(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success() {
		return fmt.Errorf("%s update runtime settings picker failed: %s", c.platform, resp.Msg)
	}
	return nil
}

func (c *larkClient) SendAgentInteractionCard(ctx context.Context, msg *core.Message, task core.ChannelTask, interaction core.ChannelInteraction) (string, error) {
	content := buildAgentInteractionCard(msg, task, interaction, "")
	if shouldReplyInThread(msg) {
		return c.replyMessage(ctx, msg.ID, larkim.MsgTypeInteractive, content)
	}
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("chat_id").
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(msg.ChatID).
			MsgType(larkim.MsgTypeInteractive).
			Content(content).
			Build()).
		Build()
	resp, err := c.api.Im.Message.Create(ctx, req)
	if err != nil {
		return "", err
	}
	if !resp.Success() {
		return "", fmt.Errorf("%s send Codex interaction failed: %s", c.platform, resp.Msg)
	}
	if resp.Data == nil || resp.Data.MessageId == nil {
		return "", fmt.Errorf("%s send Codex interaction: missing message id", c.platform)
	}
	return *resp.Data.MessageId, nil
}

func (c *larkClient) UpdateAgentInteractionCard(ctx context.Context, messageID string, interaction core.ChannelInteraction, outcome string) error {
	if messageID == "" {
		return fmt.Errorf("%s update Codex interaction: missing message id", c.platform)
	}
	req := larkim.NewPatchMessageReqBuilder().
		MessageId(messageID).
		Body(larkim.NewPatchMessageReqBodyBuilder().
			Content(buildAgentInteractionCard(&core.Message{}, core.ChannelTask{}, interaction, outcome)).
			Build()).
		Build()
	resp, err := c.api.Im.Message.Patch(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success() {
		return fmt.Errorf("%s update Codex interaction failed: %s", c.platform, resp.Msg)
	}
	return nil
}

func (c *larkClient) UpdateCard(ctx context.Context, messageID, text string, done, failed bool) error {
	req := larkim.NewPatchMessageReqBuilder().
		MessageId(messageID).
		Body(larkim.NewPatchMessageReqBodyBuilder().
			Content(buildCard(text, done, failed)).
			Build()).
		Build()
	resp, err := c.api.Im.Message.Patch(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success() {
		return fmt.Errorf("%s update card failed: %s", c.platform, resp.Msg)
	}
	return nil
}

// BeginStreamCard creates a streaming card entity via CardKit and sends it to
// the chat, returning the card entity ID for subsequent streaming updates.
func (c *larkClient) BeginStreamCard(ctx context.Context, chatID string) (string, error) {
	return c.beginStreamCard(ctx, chatID, "")
}

func (c *larkClient) BeginStreamCardReply(ctx context.Context, messageID string) (string, error) {
	return c.beginStreamCard(ctx, "", messageID)
}

func (c *larkClient) beginStreamCard(ctx context.Context, chatID, replyMessageID string) (string, error) {
	req := larkcardkit.NewCreateCardReqBuilder().
		Body(larkcardkit.NewCreateCardReqBodyBuilder().
			Type("card_json").
			Data(buildStreamCardJSON("", false, false)).
			Build()).
		Build()
	resp, err := c.api.Cardkit.V1.Card.Create(ctx, req)
	if err != nil {
		return "", err
	}
	if !resp.Success() {
		return "", fmt.Errorf("%s create stream card failed: %s", c.platform, resp.Msg)
	}
	if resp.Data == nil || resp.Data.CardId == nil {
		return "", fmt.Errorf("%s create stream card: missing card id", c.platform)
	}
	cardID := *resp.Data.CardId

	content, _ := json.Marshal(map[string]any{
		"type": "card",
		"data": map[string]string{"card_id": cardID},
	})
	if replyMessageID != "" {
		if _, err := c.replyMessage(ctx, replyMessageID, larkim.MsgTypeInteractive, string(content)); err != nil {
			return "", err
		}
	} else {
		sendReq := larkim.NewCreateMessageReqBuilder().
			ReceiveIdType("chat_id").
			Body(larkim.NewCreateMessageReqBodyBuilder().
				ReceiveId(chatID).
				MsgType(larkim.MsgTypeInteractive).
				Content(string(content)).
				Build()).
			Build()
		sendResp, err := c.api.Im.Message.Create(ctx, sendReq)
		if err != nil {
			return "", err
		}
		if !sendResp.Success() {
			return "", fmt.Errorf("%s send stream card failed: %s", c.platform, sendResp.Msg)
		}
	}
	return cardID, nil
}

// StreamCardText pushes the full accumulated text to the streaming element.
func (c *larkClient) StreamCardText(ctx context.Context, cardID, text string, sequence int) error {
	if text == "" {
		text = " "
	}
	req := larkcardkit.NewContentCardElementReqBuilder().
		CardId(cardID).
		ElementId(streamCardElementID).
		Body(larkcardkit.NewContentCardElementReqBodyBuilder().
			Content(text).
			Sequence(sequence).
			Build()).
		Build()
	resp, err := c.api.Cardkit.V1.CardElement.Content(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success() {
		return fmt.Errorf("%s stream card text failed: %s", c.platform, resp.Msg)
	}
	return nil
}

// FinishStreamCard writes the terminal text, restyles the header for the final
// state, and turns streaming mode off with a full card update.
func (c *larkClient) FinishStreamCard(ctx context.Context, cardID, text string, sequence int, failed bool) error {
	req := larkcardkit.NewUpdateCardReqBuilder().
		CardId(cardID).
		Body(larkcardkit.NewUpdateCardReqBodyBuilder().
			Card(larkcardkit.NewCardBuilder().
				Type("card_json").
				Data(buildStreamCardJSON(text, true, failed)).
				Build()).
			Sequence(sequence).
			Build()).
		Build()
	resp, err := c.api.Cardkit.V1.Card.Update(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success() {
		return fmt.Errorf("%s finish stream card failed: %s", c.platform, resp.Msg)
	}
	return nil
}

func (c *larkClient) AddReaction(ctx context.Context, messageID, emojiType string) (string, error) {
	req := larkim.NewCreateMessageReactionReqBuilder().
		MessageId(messageID).
		Body(&larkim.CreateMessageReactionReqBody{
			ReactionType: larkim.NewEmojiBuilder().EmojiType(emojiType).Build(),
		}).
		Build()
	resp, err := c.api.Im.MessageReaction.Create(ctx, req)
	if err != nil {
		return "", err
	}
	if !resp.Success() {
		return "", fmt.Errorf("%s add reaction failed: %s", c.platform, resp.Msg)
	}
	if resp.Data == nil || resp.Data.ReactionId == nil {
		return "", fmt.Errorf("%s add reaction: missing reaction id", c.platform)
	}
	return *resp.Data.ReactionId, nil
}

func (c *larkClient) DeleteReaction(ctx context.Context, messageID, reactionID string) error {
	req := larkim.NewDeleteMessageReactionReqBuilder().
		MessageId(messageID).
		ReactionId(reactionID).
		Build()
	resp, err := c.api.Im.MessageReaction.Delete(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success() {
		return fmt.Errorf("%s delete reaction failed: %s", c.platform, resp.Msg)
	}
	return nil
}

func (c *larkClient) Close() error {
	c.mu.Lock()
	cancel, ws := c.cancel, c.ws
	c.closing = true
	c.healthState = core.ChannelStateStopped
	c.healthError = ""
	c.cancel, c.ws, c.connectedAt = nil, nil, time.Time{}
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if ws != nil {
		ws.Close()
	}
	if c.meetingVoice != nil {
		c.meetingVoice.Close()
	}
	return nil
}

func (c *larkClient) beginHealth() {
	now := time.Now()
	c.mu.Lock()
	c.closing = false
	c.healthState = core.ChannelStateStarting
	c.healthError = ""
	c.healthStartedAt = now
	c.connectedAt = time.Time{}
	c.lastHeartbeatAt = time.Time{}
	c.lastEventAt = time.Time{}
	c.lastInboundAt = time.Time{}
	c.mu.Unlock()
}

func (c *larkClient) markReady() {
	now := time.Now()
	c.mu.Lock()
	if !c.closing {
		c.healthState = core.ChannelStateRunning
		c.healthError = ""
		c.connectedAt = now
		// Treat readiness as the initial heartbeat. The server's first pong will
		// replace it before the heartbeat watchdog window expires.
		c.lastHeartbeatAt = now
	}
	c.mu.Unlock()
}

func (c *larkClient) markReconnecting() {
	c.mu.Lock()
	if !c.closing {
		c.healthState = core.ChannelStateReconnecting
		c.healthError = "Feishu WebSocket is reconnecting"
	}
	c.mu.Unlock()
}

func (c *larkClient) markDisconnected() {
	c.mu.Lock()
	if !c.closing {
		c.healthState = core.ChannelStateReconnecting
		c.healthError = "Feishu WebSocket disconnected; waiting for reconnect"
	}
	c.mu.Unlock()
}

func (c *larkClient) markError(err error) {
	c.mu.Lock()
	if !c.closing {
		c.healthState = core.ChannelStateError
		if err != nil {
			c.healthError = err.Error()
		} else {
			c.healthError = "Feishu WebSocket connection failed"
		}
	}
	c.mu.Unlock()
}

func (c *larkClient) markHeartbeat() {
	c.mu.Lock()
	if !c.closing {
		c.lastHeartbeatAt = time.Now()
		if c.healthState == core.ChannelStateDegraded {
			c.healthState = core.ChannelStateRunning
			c.healthError = ""
		}
	}
	c.mu.Unlock()
}

func (c *larkClient) markEvent() {
	c.mu.Lock()
	if !c.closing {
		c.lastEventAt = time.Now()
	}
	c.mu.Unlock()
}

func (c *larkClient) markInbound() {
	c.mu.Lock()
	if !c.closing {
		c.lastInboundAt = time.Now()
	}
	c.mu.Unlock()
}

func (c *larkClient) ChannelHealth() core.PlatformHealth {
	now := time.Now()
	c.mu.Lock()
	health := core.PlatformHealth{
		State:           c.healthState,
		Connected:       c.healthState == core.ChannelStateRunning,
		Error:           c.healthError,
		CheckedAt:       now,
		ConnectedAt:     c.connectedAt,
		LastHeartbeatAt: c.lastHeartbeatAt,
		LastEventAt:     c.lastEventAt,
		LastInboundAt:   c.lastInboundAt,
	}
	startedAt := c.healthStartedAt
	c.mu.Unlock()

	if health.State == "" {
		health.State = core.ChannelStateStarting
	}
	if health.State == core.ChannelStateStarting && !startedAt.IsZero() && now.Sub(startedAt) > larkWSStartupTimeout {
		health.State = core.ChannelStateDegraded
		health.Error = "Feishu WebSocket did not become ready within 45 seconds"
	}
	if health.State == core.ChannelStateRunning {
		lastActivity := health.LastHeartbeatAt
		if health.LastEventAt.After(lastActivity) {
			lastActivity = health.LastEventAt
		}
		if !lastActivity.IsZero() && now.Sub(lastActivity) > larkWSHeartbeatTimeout {
			health.State = core.ChannelStateDegraded
			health.Connected = false
			health.Error = fmt.Sprintf("Feishu WebSocket heartbeat is stale (no pong or event for %s); restart the channel", now.Sub(lastActivity).Round(time.Second))
		}
	}
	return health
}

type larkWSHealthLogger struct {
	client   *larkClient
	delegate larkcore.Logger
}

func (l *larkWSHealthLogger) Debug(ctx context.Context, args ...interface{}) {
	if strings.Contains(fmt.Sprint(args...), "receive pong") {
		l.client.markHeartbeat()
	}
	if l.delegate != nil {
		l.delegate.Debug(ctx, args...)
	}
}

func (l *larkWSHealthLogger) Info(ctx context.Context, args ...interface{}) {
	if l.delegate != nil {
		l.delegate.Info(ctx, args...)
	}
}

func (l *larkWSHealthLogger) Warn(ctx context.Context, args ...interface{}) {
	if l.delegate != nil {
		l.delegate.Warn(ctx, args...)
	}
}

func (l *larkWSHealthLogger) Error(ctx context.Context, args ...interface{}) {
	if l.delegate != nil {
		l.delegate.Error(ctx, args...)
	}
}

// buildCard renders text into a Feishu interactive card JSON payload. While a
// turn is streaming (done=false) a subtle "typing" note is appended; the final
// update drops it, and failures switch the header to a red error style.
func buildCard(text string, done, failed bool) string {
	if text == "" {
		text = " "
	}
	elements := []map[string]any{
		{
			"tag":     "markdown",
			"content": text,
		},
	}
	if !done {
		elements = append(elements, map[string]any{
			"tag": "note",
			"elements": []map[string]any{
				{"tag": "plain_text", "content": "正在输入…"},
			},
		})
	}

	template := "blue"
	title := "AgentMux"
	if done {
		template = "green"
	}
	if failed {
		template = "red"
		title = "AgentMux · 出错"
	}

	card := map[string]any{
		"config": map[string]any{"wide_screen_mode": true},
		"header": map[string]any{
			"template": template,
			"title": map[string]any{
				"tag":     "plain_text",
				"content": title,
			},
		},
		"elements": elements,
	}
	b, err := json.Marshal(card)
	if err != nil {
		return `{"config":{"wide_screen_mode":true},"elements":[{"tag":"markdown","content":" "}]}`
	}
	return string(b)
}

func buildModelPickerCard(msg *core.Message, state core.ModelPickerState) string {
	current := modelPickerDisplay(state.CurrentModel)
	def := modelPickerDisplay(state.DefaultModel)
	elements := []map[string]any{
		{
			"tag":     "markdown",
			"content": fmt.Sprintf("**当前模型**: `%s`\n**默认模型**: `%s`", current, def),
		},
	}
	if len(state.Options) == 0 {
		elements = append(elements, map[string]any{
			"tag":     "markdown",
			"content": "当前 Provider 没有配置可选模型。",
		})
	} else {
		for i := 0; i < len(state.Options); i += 2 {
			end := i + 2
			if end > len(state.Options) {
				end = len(state.Options)
			}
			elements = append(elements, modelPickerButtonRow(msg, state.Options[i:end]))
		}
		if state.CurrentModel != state.DefaultModel {
			elements = append(elements, modelPickerResetRow(msg))
		}
	}
	card := map[string]any{
		"schema": "2.0",
		"config": map[string]any{"wide_screen_mode": true},
		"header": map[string]any{
			"template": "blue",
			"title": map[string]any{
				"tag":     "plain_text",
				"content": "选择模型",
			},
		},
		"body": map[string]any{
			"elements": elements,
		},
	}
	b, err := json.Marshal(card)
	if err != nil {
		return `{"schema":"2.0","body":{"elements":[{"tag":"markdown","content":"model picker unavailable"}]}}`
	}
	return string(b)
}

func buildRuntimeSettingsPickerCard(msg *core.Message, state core.RuntimeSettingsPickerState) string {
	scopeLabel := "当前会话"
	if state.Scope == core.RuntimeSettingsScopeAgent {
		scopeLabel = "Agent 默认（仅后续会话）"
	}
	elements := []map[string]any{
		{
			"tag": "markdown",
			"content": fmt.Sprintf("**设置范围**：%s\n**模型**：`%s`\n**思考强度**：`%s`\n**速度**：`%s`", scopeLabel,
				modelPickerDisplay(state.Settings.Model), modelPickerDisplay(state.Settings.ReasoningEffort), modelPickerDisplay(state.Settings.ServiceTier)),
		},
	}
	if state.Notice != "" {
		elements = append(elements, map[string]any{"tag": "markdown", "content": "<font color='red'>" + state.Notice + "</font>"})
	}
	if state.AgentDefaultsEditable {
		elements = append(elements, runtimeSettingsScopeRow(msg, state.Scope))
	}
	elements = append(elements, runtimeSettingsRows(msg, state, core.RuntimeSettingModel, "模型", state.Capabilities.Models)...)
	elements = append(elements, runtimeSettingsRows(msg, state, core.RuntimeSettingReasoningEffort, "思考强度", state.Capabilities.ReasoningEfforts)...)
	elements = append(elements, runtimeSettingsRows(msg, state, core.RuntimeSettingServiceTier, "速度", state.Capabilities.ServiceTiers)...)
	card := map[string]any{
		"schema": "2.0",
		"config": map[string]any{"wide_screen_mode": true},
		"header": map[string]any{
			"template": "blue",
			"title":    map[string]any{"tag": "plain_text", "content": "运行时设置"},
		},
		"body": map[string]any{"elements": elements},
	}
	b, err := json.Marshal(card)
	if err != nil {
		return `{"schema":"2.0","body":{"elements":[{"tag":"markdown","content":"settings picker unavailable"}]}}`
	}
	return string(b)
}

func buildAgentInteractionCard(msg *core.Message, task core.ChannelTask, interaction core.ChannelInteraction, outcome string) string {
	request := interaction.Request
	title := request.Title
	if title == "" {
		title = "Codex 需要确认"
	}
	template := "orange"
	elements := []map[string]any{}
	if outcome != "" {
		template = "green"
		if outcome == "decline" || outcome == "cancel" || outcome == "expired" {
			template = "grey"
		}
		elements = append(elements, map[string]any{
			"tag": "markdown", "content": "**已处理**：" + outcome,
		})
	} else {
		detail := strings.TrimSpace(request.Description)
		if request.Command != "" {
			command := request.Command
			if len(command) > 3000 {
				command = command[:3000] + "…"
			}
			detail = "```text\n" + command + "\n```"
		}
		if request.Reason != "" {
			if detail != "" {
				detail += "\n"
			}
			detail += request.Reason
		}
		if request.Cwd != "" {
			detail += "\n\n工作目录：`" + request.Cwd + "`"
		}
		if request.HighRisk {
			detail += "\n\n<font color='red'>高风险操作：只能逐次允许。</font>"
		}
		if detail != "" {
			elements = append(elements, map[string]any{"tag": "markdown", "content": detail})
		}
		if request.Kind == core.AgentInteractionUserInput {
			elements = append(elements, interactionQuestionElements(msg, task, interaction)...)
		} else {
			elements = append(elements, interactionApprovalElements(msg, task, interaction)...)
		}
	}
	card := map[string]any{
		"schema": "2.0",
		"config": map[string]any{"wide_screen_mode": true},
		"header": map[string]any{
			"template": template,
			"title":    map[string]any{"tag": "plain_text", "content": title},
		},
		"body": map[string]any{"elements": elements},
	}
	data, err := json.Marshal(card)
	if err != nil {
		return `{"schema":"2.0","body":{"elements":[{"tag":"markdown","content":"Codex interaction unavailable"}]}}`
	}
	return string(data)
}

func interactionApprovalElements(msg *core.Message, task core.ChannelTask, interaction core.ChannelInteraction) []map[string]any {
	once := modelPickerButton("允许一次", "primary", interactionActionValue(msg, task, interaction, "accept", "", ""))
	decline := modelPickerButton("拒绝", "danger", interactionActionValue(msg, task, interaction, "decline", "", ""))
	buttons := []map[string]any{once}
	if !interaction.Request.HighRisk {
		session := modelPickerButton("本会话允许", "default", interactionActionValue(msg, task, interaction, "acceptForSession", "", ""))
		session["confirm"] = map[string]any{
			"title": map[string]any{"tag": "plain_text", "content": "确认本会话允许"},
			"text":  map[string]any{"tag": "plain_text", "content": "仅当前 AgentMux/Codex 会话有效，重启后失效。"},
		}
		buttons = append(buttons, session)
	}
	buttons = append(buttons, decline)
	return []map[string]any{{"tag": "column_set", "flex_mode": "stretch", "columns": interactionButtonColumns(buttons)}}
}

func interactionQuestionElements(msg *core.Message, task core.ChannelTask, interaction core.ChannelInteraction) []map[string]any {
	request := interaction.Request
	for _, question := range request.Questions {
		if question.Secret {
			return []map[string]any{
				{"tag": "markdown", "content": "🔒 此问题包含敏感输入，只能在本机 AgentMux 控制台处理。"},
			}
		}
	}
	elements := []map[string]any{}
	if len(request.Questions) == 1 && len(request.Questions[0].Options) > 0 {
		question := request.Questions[0]
		elements = append(elements, map[string]any{"tag": "markdown", "content": "**" + question.Header + "**\n" + question.Question})
		buttons := make([]map[string]any, 0, len(question.Options))
		for _, option := range question.Options {
			buttons = append(buttons, modelPickerButton(option.Label, "default",
				interactionActionValue(msg, task, interaction, "answer", question.ID, option.Label)))
		}
		elements = append(elements, map[string]any{"tag": "column_set", "flex_mode": "stretch", "columns": interactionButtonColumns(buttons)})
		return elements
	}
	for _, question := range request.Questions {
		elements = append(elements,
			map[string]any{"tag": "markdown", "content": "**" + question.Header + "**\n" + question.Question},
			map[string]any{
				"tag": "input", "name": "answer_" + question.ID,
				"placeholder": map[string]any{"tag": "plain_text", "content": "请输入答案"},
			},
		)
	}
	elements = append(elements, modelPickerButton("提交", "primary",
		interactionActionValue(msg, task, interaction, "answer", "", "")))
	return elements
}

func interactionButtonColumns(buttons []map[string]any) []map[string]any {
	columns := make([]map[string]any, 0, len(buttons))
	for _, button := range buttons {
		columns = append(columns, map[string]any{
			"tag": "column", "width": "weighted", "weight": 1,
			"elements": []map[string]any{button},
		})
	}
	return columns
}

func interactionActionValue(msg *core.Message, task core.ChannelTask, interaction core.ChannelInteraction, decision, questionID, answer string) map[string]any {
	return map[string]any{
		modelPickerActionKey: codexInteractionAction,
		"interaction_id":     interaction.ID,
		"task_id":            task.ID,
		"nonce":              interaction.Nonce,
		"decision":           decision,
		"question_id":        questionID,
		"answer":             answer,
		"chat_id":            msg.ChatID,
		"chat_type":          msg.ChatType,
		"conversation_key":   task.ConversationKey,
	}
}

func runtimeSettingsScopeRow(msg *core.Message, current core.RuntimeSettingsScope) map[string]any {
	return runtimeSettingsButtonRow(msg, []runtimeSettingsButton{
		{label: "当前会话", primary: current == core.RuntimeSettingsScopeConversation, action: core.RuntimeSettingsAction{Scope: core.RuntimeSettingsScopeConversation, Setting: core.RuntimeSettingScope}},
		{label: "Agent 默认", primary: current == core.RuntimeSettingsScopeAgent, action: core.RuntimeSettingsAction{Scope: core.RuntimeSettingsScopeAgent, Setting: core.RuntimeSettingScope}},
	})
}

type runtimeSettingsButton struct {
	label   string
	primary bool
	action  core.RuntimeSettingsAction
}

func runtimeSettingsRows(msg *core.Message, state core.RuntimeSettingsPickerState, setting core.RuntimeSetting, title string, options []core.RuntimeOption) []map[string]any {
	if len(options) == 0 {
		if reason := state.Unsupported[setting]; reason != "" {
			return []map[string]any{{"tag": "markdown", "content": "**" + title + "**：" + reason}}
		}
		return nil
	}
	rows := []map[string]any{{"tag": "markdown", "content": "**" + title + "**"}}
	selected := state.Settings.Value(setting)
	for i := 0; i < len(options); i += 2 {
		end := i + 2
		if end > len(options) {
			end = len(options)
		}
		buttons := make([]runtimeSettingsButton, 0, end-i)
		for _, option := range options[i:end] {
			label := option.Label
			if label == "" {
				label = option.Value
			}
			if option.Value == selected {
				label += " 当前"
			}
			buttons = append(buttons, runtimeSettingsButton{
				label: label, primary: option.Value == selected,
				action: core.RuntimeSettingsAction{Scope: state.Scope, Setting: setting, Value: option.Value},
			})
		}
		rows = append(rows, runtimeSettingsButtonRow(msg, buttons))
	}
	return rows
}

func runtimeSettingsButtonRow(msg *core.Message, buttons []runtimeSettingsButton) map[string]any {
	columns := make([]map[string]any, 0, len(buttons))
	for _, button := range buttons {
		buttonType := "default"
		if button.primary {
			buttonType = "primary"
		}
		columns = append(columns, map[string]any{
			"tag": "column", "width": "weighted", "weight": 1, "vertical_align": "top",
			"elements": []map[string]any{modelPickerButton(button.label, buttonType, runtimeSettingsActionValue(msg, button.action))},
		})
	}
	return map[string]any{"tag": "column_set", "flex_mode": "stretch", "background_style": "default", "columns": columns}
}

func runtimeSettingsActionValue(msg *core.Message, action core.RuntimeSettingsAction) map[string]any {
	return map[string]any{
		modelPickerActionKey: runtimeSettingsAction,
		"scope":              string(action.Scope),
		"setting":            string(action.Setting),
		"value":              action.Value,
		"reset":              action.Reset,
		"chat_id":            msg.ChatID,
		"chat_type":          msg.ChatType,
		"conversation_key":   core.ResolveConversationKey(msg),
	}
}

func modelPickerButtonRow(msg *core.Message, options []core.ModelPickerOption) map[string]any {
	columns := make([]map[string]any, 0, len(options))
	for _, option := range options {
		label := option.Model
		if option.Current {
			label += " 当前"
		} else if option.Default {
			label += " 默认"
		}
		buttonType := "default"
		if option.Current {
			buttonType = "primary"
		}
		columns = append(columns, map[string]any{
			"tag":            "column",
			"width":          "weighted",
			"weight":         1,
			"vertical_align": "top",
			"elements": []map[string]any{
				modelPickerButton(label, buttonType, map[string]any{
					modelPickerActionKey: modelPickerActionSelect,
					"model":              option.Model,
					"chat_id":            msg.ChatID,
					"chat_type":          msg.ChatType,
					"conversation_key":   core.ResolveConversationKey(msg),
				}),
			},
		})
	}
	return map[string]any{
		"tag":              "column_set",
		"flex_mode":        "stretch",
		"background_style": "default",
		"columns":          columns,
	}
}

func modelPickerResetRow(msg *core.Message) map[string]any {
	return map[string]any{
		"tag":              "column_set",
		"flex_mode":        "stretch",
		"background_style": "default",
		"columns": []map[string]any{
			{
				"tag":            "column",
				"width":          "weighted",
				"weight":         1,
				"vertical_align": "top",
				"elements": []map[string]any{
					modelPickerButton("恢复默认", "default", map[string]any{
						modelPickerActionKey: modelPickerActionReset,
						"chat_id":            msg.ChatID,
						"chat_type":          msg.ChatType,
						"conversation_key":   core.ResolveConversationKey(msg),
					}),
				},
			},
		},
	}
}

func modelPickerButton(label, buttonType string, value map[string]any) map[string]any {
	return map[string]any{
		"tag":   "button",
		"type":  buttonType,
		"width": "fill",
		"text": map[string]any{
			"tag":     "plain_text",
			"content": label,
		},
		"behaviors": []map[string]any{
			{
				"type":  "callback",
				"value": value,
			},
		},
	}
}

func modelPickerDisplay(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return "runtime default"
	}
	return model
}

// buildStreamCardJSON renders a JSON 2.0 card with a single markdown element
// (element_id = streamCardElementID) that native streaming updates write into.
// While streaming (done=false) streaming_mode is on so text-content updates
// render with a typewriter effect and bypass card update rate limits; the final
// update turns streaming_mode off and restyles the header for done/failed.
func buildStreamCardJSON(text string, done, failed bool) string {
	if text == "" {
		text = " "
	}
	template := "blue"
	title := "AgentMux"
	if done {
		template = "green"
	}
	if failed {
		template = "red"
		title = "AgentMux · 出错"
	}
	card := map[string]any{
		"schema": "2.0",
		"config": map[string]any{
			"streaming_mode": !done,
			"streaming_config": map[string]any{
				"print_strategy": "fast",
			},
		},
		"header": map[string]any{
			"template": template,
			"title": map[string]any{
				"tag":     "plain_text",
				"content": title,
			},
		},
		"body": map[string]any{
			"elements": []map[string]any{
				{
					"tag":        "markdown",
					"element_id": streamCardElementID,
					"content":    text,
				},
			},
		},
	}
	b, err := json.Marshal(card)
	if err != nil {
		return `{"schema":"2.0","body":{"elements":[{"tag":"markdown","element_id":"answer","content":" "}]}}`
	}
	return string(b)
}

type larkPostContent struct {
	Title   string              `json:"title"`
	Content [][]larkPostElement `json:"content"`
}

type larkPostElement struct {
	Tag       string `json:"tag"`
	Text      string `json:"text"`
	Href      string `json:"href"`
	UserID    string `json:"user_id"`
	UserName  string `json:"user_name"`
	EmojiType string `json:"emoji_type"`
}

// extractText pulls readable text out of a Feishu message content payload.
func extractText(msgType, content string) string {
	switch msgType {
	case "text":
		var c struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(content), &c); err != nil {
			return ""
		}
		return strings.TrimSpace(c.Text)
	case "post":
		return extractPostText(content)
	default:
		return ""
	}
}

func extractPostText(content string) string {
	var post larkPostContent
	if err := json.Unmarshal([]byte(content), &post); err != nil {
		return ""
	}
	if post.Title != "" || post.Content != nil {
		return renderPostText(post)
	}

	// Some Feishu APIs wrap post content by locale, for example
	// {"zh_cn":{"title":"...","content":[...]}}. Prefer the common
	// locales, then accept any other locale deterministically.
	var localized map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &localized); err != nil {
		return ""
	}
	preferred := []string{"zh_cn", "en_us", "ja_jp"}
	keys := make([]string, 0, len(localized))
	seen := make(map[string]bool, len(preferred))
	for _, key := range preferred {
		if _, ok := localized[key]; ok {
			keys = append(keys, key)
			seen[key] = true
		}
	}
	remaining := make([]string, 0, len(localized))
	for key := range localized {
		if !seen[key] {
			remaining = append(remaining, key)
		}
	}
	slices.Sort(remaining)
	keys = append(keys, remaining...)
	for _, key := range keys {
		var candidate larkPostContent
		if err := json.Unmarshal(localized[key], &candidate); err != nil {
			continue
		}
		if candidate.Title != "" || candidate.Content != nil {
			return renderPostText(candidate)
		}
	}
	return ""
}

func renderPostText(post larkPostContent) string {
	lines := make([]string, 0, len(post.Content)+1)
	if title := strings.TrimSpace(post.Title); title != "" {
		lines = append(lines, title)
	}
	for _, paragraph := range post.Content {
		var line strings.Builder
		for _, element := range paragraph {
			switch element.Tag {
			case "a":
				label := strings.TrimSpace(element.Text)
				href := strings.TrimSpace(element.Href)
				switch {
				case label == "":
					line.WriteString(href)
				case href == "" || label == href:
					line.WriteString(element.Text)
				default:
					line.WriteString(element.Text)
					line.WriteString(" (")
					line.WriteString(href)
					line.WriteByte(')')
				}
			case "at":
				name := strings.TrimSpace(element.UserName)
				if name == "" {
					name = strings.TrimSpace(element.UserID)
				}
				if name != "" {
					line.WriteByte('@')
					line.WriteString(name)
				}
			case "img":
				line.WriteString("[图片]")
			case "media":
				line.WriteString("[媒体]")
			case "emotion":
				if emoji := strings.TrimSpace(element.EmojiType); emoji != "" {
					line.WriteByte(':')
					line.WriteString(emoji)
					line.WriteByte(':')
				}
			case "br":
				line.WriteByte('\n')
			default:
				// Text and future text-bearing element types can degrade to their
				// textual representation instead of dropping the whole message.
				line.WriteString(element.Text)
			}
		}
		if text := strings.TrimSpace(line.String()); text != "" {
			lines = append(lines, text)
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func (c *larkClient) messageFromCardAction(project string, event *callback.CardActionTriggerEvent) (*core.Message, bool) {
	if event == nil || event.Event == nil || event.Event.Action == nil {
		return nil, false
	}
	messageID := ""
	chatID := ""
	if event.Event.Context != nil {
		messageID = event.Event.Context.OpenMessageID
		chatID = event.Event.Context.OpenChatID
	}
	userID := ""
	if event.Event.Operator != nil {
		userID = event.Event.Operator.OpenID
	}
	value := event.Event.Action.Value
	action := stringValue(value[modelPickerActionKey])
	actionValue := jsonValue(event.Event.Action.Value)
	formValue := jsonValue(event.Event.Action.FormValue)
	inputValue := event.Event.Action.InputValue
	option := event.Event.Action.Option
	options := strings.Join(event.Event.Action.Options, ",")
	if action == codexInteractionAction {
		// Approval nonces and user answers are used only for the in-memory
		// control action. Channel JSONL/audit records retain correlation but
		// never the replay token or submitted answer values.
		actionValue = jsonValue(map[string]any{
			modelPickerActionKey: codexInteractionAction,
			"interaction_id":     stringValue(value["interaction_id"]),
		})
		formValue = ""
		inputValue = ""
		option = ""
		options = ""
	}
	msg := &core.Message{
		ID:                   larkCardActionEventID(event, messageID),
		InteractionMessageID: messageID,
		ChatID:               chatID,
		UserID:               userID,
		MentionedBot:         true,
		Platform:             c.platform,
		Project:              project,
		Timestamp:            time.Now(),
		LogOnly:              true,
		Callback: &core.CallbackEvent{
			Type:        larkCardActionEventType(event),
			MessageID:   messageID,
			Host:        event.Event.Host,
			ActionTag:   event.Event.Action.Tag,
			ActionName:  event.Event.Action.Name,
			ActionValue: actionValue,
			FormValue:   formValue,
			InputValue:  inputValue,
			Option:      option,
			Options:     options,
			Checked:     event.Event.Action.Checked,
			Timezone:    event.Event.Action.Timezone,
		},
	}
	if action == "" {
		return msg, true
	}
	if valueChatID := stringValue(value["chat_id"]); valueChatID != "" {
		msg.ChatID = valueChatID
	}
	msg.ChatType = stringValue(value["chat_type"])
	msg.ConversationKey = stringValue(value["conversation_key"])
	if msg.ChatID == "" {
		return msg, true
	}
	if action == codexInteractionAction {
		interactionID := stringValue(value["interaction_id"])
		nonce := stringValue(value["nonce"])
		if interactionID == "" || nonce == "" {
			return msg, true
		}
		decision := stringValue(value["decision"])
		answers := map[string][]string{}
		if questionID := stringValue(value["question_id"]); questionID != "" {
			answers[questionID] = []string{stringValue(value["answer"])}
		}
		for key, raw := range event.Event.Action.FormValue {
			if !strings.HasPrefix(key, "answer_") {
				continue
			}
			questionID := strings.TrimPrefix(key, "answer_")
			answer := strings.TrimSpace(fmt.Sprint(raw))
			if questionID != "" {
				answers[questionID] = []string{answer}
			}
		}
		if decision == "answer" {
			decision = ""
		}
		msg.LogOnly = false
		msg.AgentInteractionAction = &core.AgentInteractionAction{
			InteractionID: interactionID,
			TaskID:        stringValue(value["task_id"]),
			Nonce:         nonce,
			Decision:      decision,
			Answers:       answers,
		}
		return msg, true
	}
	if action == runtimeSettingsAction {
		scope := core.RuntimeSettingsScope(stringValue(value["scope"]))
		setting := core.RuntimeSetting(stringValue(value["setting"]))
		if scope == "" {
			scope = core.RuntimeSettingsScopeConversation
		}
		if setting == "" {
			return msg, true
		}
		msg.LogOnly = false
		msg.RuntimeSettingsAction = &core.RuntimeSettingsAction{
			Scope: scope, Setting: setting, Value: stringValue(value["value"]), Reset: boolValue(value["reset"]),
		}
		return msg, true
	}
	text := ""
	switch action {
	case modelPickerActionSelect:
		model := stringValue(value["model"])
		if model == "" {
			return msg, true
		}
		text = "/model " + model
	case modelPickerActionReset:
		text = "/model reset"
	default:
		return msg, true
	}
	msg.LogOnly = false
	msg.Text = text
	return msg, true
}

func larkCardActionEventType(event *callback.CardActionTriggerEvent) string {
	if event != nil && event.EventV2Base != nil && event.EventV2Base.Header != nil && event.EventV2Base.Header.EventType != "" {
		return event.EventV2Base.Header.EventType
	}
	return "card.action.trigger"
}

func jsonValue(value any) string {
	b, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(b)
}

func larkCardActionEventID(event *callback.CardActionTriggerEvent, fallback string) string {
	if event != nil && event.EventV2Base != nil && event.EventV2Base.Header != nil && event.EventV2Base.Header.EventID != "" {
		return event.EventV2Base.Header.EventID
	}
	return fallback
}

func stringValue(raw any) string {
	if s, ok := raw.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func boolValue(raw any) bool {
	value, ok := raw.(bool)
	return ok && value
}

func (c *larkClient) loadBotOpenID(ctx context.Context) string {
	if c.botOpenID != "" {
		return c.botOpenID
	}
	reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	openID, err := fetchBotOpenID(reqCtx, c.domain, c.appID, c.appSecret)
	if err == nil {
		c.botOpenID = openID
	}
	return c.botOpenID
}

func fetchBotOpenID(ctx context.Context, domain, appID, appSecret string) (string, error) {
	base := strings.TrimRight(domain, "/")
	payload, _ := json.Marshal(map[string]string{
		"app_id":     appID,
		"app_secret": appSecret,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/open-apis/auth/v3/app_access_token/internal", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("token request failed: HTTP %d", resp.StatusCode)
	}
	var tokenResp struct {
		Code           int    `json:"code"`
		Msg            string `json:"msg"`
		AppAccessToken string `json:"app_access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tokenResp); err != nil {
		return "", err
	}
	if tokenResp.Code != 0 {
		return "", fmt.Errorf("token request failed: %s", tokenResp.Msg)
	}
	if tokenResp.AppAccessToken == "" {
		return "", fmt.Errorf("token request returned empty token")
	}

	req, err = http.NewRequestWithContext(ctx, http.MethodGet, base+"/open-apis/bot/v3/info", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+tokenResp.AppAccessToken)
	resp, err = client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("bot info request failed: HTTP %d", resp.StatusCode)
	}
	var infoResp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Bot  struct {
			OpenID string `json:"open_id"`
		} `json:"bot"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&infoResp); err != nil {
		return "", err
	}
	if infoResp.Code != 0 {
		return "", fmt.Errorf("bot info request failed: %s", infoResp.Msg)
	}
	if infoResp.Bot.OpenID == "" {
		return "", fmt.Errorf("bot info returned empty open_id")
	}
	return infoResp.Bot.OpenID, nil
}

func mentionState(msg *larkim.EventMessage, botOpenID, text string) (bool, bool) {
	var mentionedBot bool
	var mentionAll bool
	if strings.Contains(text, "@_all") || strings.Contains(text, "@all") {
		mentionAll = true
	}
	for _, mention := range msg.Mentions {
		if mention == nil {
			continue
		}
		if mention.Key != nil && (*mention.Key == "@_all" || *mention.Key == "@all") {
			mentionAll = true
		}
		if mention.Id != nil && botOpenID != "" {
			if (mention.Id.OpenId != nil && *mention.Id.OpenId == botOpenID) ||
				(mention.Id.UserId != nil && *mention.Id.UserId == botOpenID) ||
				(mention.Id.UnionId != nil && *mention.Id.UnionId == botOpenID) {
				mentionedBot = true
			}
		}
		if botOpenID == "" && mention.MentionedType != nil && strings.EqualFold(*mention.MentionedType, "app") {
			mentionedBot = true
		}
	}
	return mentionedBot, mentionAll
}
