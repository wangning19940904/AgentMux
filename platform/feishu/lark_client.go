package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
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
