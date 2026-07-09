package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/agentnexus/agentnexus/core"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkcardkit "github.com/larksuite/oapi-sdk-go/v3/service/cardkit/v1"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

// streamCardElementID is the fixed element_id of the markdown component we
// stream text into. It must match the element_id embedded in the card JSON
// created by BeginStreamCard.
const streamCardElementID = "answer"

const (
	modelPickerActionKey    = "agentnexus_action"
	modelPickerActionSelect = "model_select"
	modelPickerActionReset  = "model_reset"
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
}

func newLarkClient(platform, domain, appID, appSecret string) (clientAPI, error) {
	return &larkClient{
		platform:  platform,
		domain:    domain,
		appID:     appID,
		appSecret: appSecret,
		api:       lark.NewClient(appID, appSecret, lark.WithOpenBaseUrl(domain)),
	}, nil
}

func (c *larkClient) Listen(ctx context.Context, project string, inbound chan<- *core.Message) error {
	botOpenID := c.loadBotOpenID(ctx)
	handler := dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(func(_ context.Context, event *larkim.P2MessageReceiveV1) error {
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
			userID := ""
			if event.Event.Sender != nil && event.Event.Sender.SenderId != nil &&
				event.Event.Sender.SenderId.OpenId != nil {
				userID = *event.Event.Sender.SenderId.OpenId
			}
			mentionedBot, mentionAll := mentionState(msg, botOpenID, text)
			inbound <- &core.Message{
				ID:           messageID,
				ChatID:       chatID,
				ChatType:     chatType,
				UserID:       userID,
				Text:         text,
				MentionedBot: mentionedBot,
				MentionAll:   mentionAll,
				Platform:     c.platform,
				Project:      project,
			}
			return nil
		}).
		OnP2CardActionTrigger(func(eventCtx context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
			msg, ok := c.modelCommandFromCardAction(project, event)
			if !ok {
				return nil, nil
			}
			select {
			case inbound <- msg:
			case <-ctx.Done():
			case <-eventCtx.Done():
			}
			return nil, nil
		})

	c.ws = larkws.NewClient(c.appID, c.appSecret, larkws.WithDomain(c.domain), larkws.WithEventHandler(handler))
	wsCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	// Start blocks; run until context cancelled.
	errCh := make(chan error, 1)
	go func() { errCh <- c.ws.Start(wsCtx) }()
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

func (c *larkClient) SendModelPickerCard(ctx context.Context, msg *core.Message, state core.ModelPickerState) (string, error) {
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
	if c.cancel != nil {
		c.cancel()
	}
	return nil
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
	title := "AgentNexus"
	if done {
		template = "green"
	}
	if failed {
		template = "red"
		title = "AgentNexus · 出错"
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
	title := "AgentNexus"
	if done {
		template = "green"
	}
	if failed {
		template = "red"
		title = "AgentNexus · 出错"
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

// extractText pulls plain text out of a Feishu message content payload.
func extractText(msgType, content string) string {
	if msgType != "text" {
		return ""
	}
	var c struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(content), &c); err != nil {
		return ""
	}
	return strings.TrimSpace(c.Text)
}

func (c *larkClient) modelCommandFromCardAction(project string, event *callback.CardActionTriggerEvent) (*core.Message, bool) {
	if event == nil || event.Event == nil || event.Event.Action == nil {
		return nil, false
	}
	value := event.Event.Action.Value
	action := stringValue(value[modelPickerActionKey])
	if action == "" {
		return nil, false
	}
	chatID := stringValue(value["chat_id"])
	chatType := stringValue(value["chat_type"])
	if chatID == "" && event.Event.Context != nil {
		chatID = event.Event.Context.OpenChatID
	}
	if chatID == "" {
		return nil, false
	}
	text := ""
	switch action {
	case modelPickerActionSelect:
		model := stringValue(value["model"])
		if model == "" {
			return nil, false
		}
		text = "/model " + model
	case modelPickerActionReset:
		text = "/model reset"
	default:
		return nil, false
	}
	userID := ""
	if event.Event.Operator != nil {
		userID = event.Event.Operator.OpenID
	}
	return &core.Message{
		ChatID:       chatID,
		ChatType:     chatType,
		UserID:       userID,
		Text:         text,
		MentionedBot: true,
		Platform:     c.platform,
		Project:      project,
		Timestamp:    time.Now(),
	}, true
}

func stringValue(raw any) string {
	if s, ok := raw.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
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
