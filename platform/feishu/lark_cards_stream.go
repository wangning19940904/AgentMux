package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	larkcardkit "github.com/larksuite/oapi-sdk-go/v3/service/cardkit/v1"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/wangning19940904/AgentMux/core"
)

var feishuBareURLPattern = regexp.MustCompile(`https?://[^\s<>"'\[\]]+`)

// BeginStreamCard creates a streaming card entity via CardKit and sends it to
// the chat, returning the card entity ID for subsequent streaming updates.
func (c *larkClient) BeginStreamCard(ctx context.Context, chatID string, control *streamCardControl) (string, error) {
	return c.beginStreamCard(ctx, chatID, "", control)
}

func (c *larkClient) BeginStreamCardReply(ctx context.Context, messageID string, control *streamCardControl) (string, error) {
	return c.beginStreamCard(ctx, "", messageID, control)
}

func (c *larkClient) beginStreamCard(ctx context.Context, chatID, replyMessageID string, control *streamCardControl) (string, error) {
	req := larkcardkit.NewCreateCardReqBuilder().
		Body(larkcardkit.NewCreateCardReqBodyBuilder().
			Type("card_json").
			Data(buildStreamCardJSON("", false, false, control)).
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
	} else {
		text = linkifyFeishuMarkdown(text)
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
func (c *larkClient) FinishStreamCard(ctx context.Context, cardID, text string, sequence int, failed bool, control *streamCardControl) error {
	req := larkcardkit.NewUpdateCardReqBuilder().
		CardId(cardID).
		Body(larkcardkit.NewUpdateCardReqBodyBuilder().
			Card(larkcardkit.NewCardBuilder().
				Type("card_json").
				Data(buildStreamCardJSON(text, true, failed, control)).
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

// buildCard renders text into a Feishu interactive card JSON payload. While a
// turn is streaming (done=false) a subtle "typing" note is appended; the final
// update drops it, and failures switch the header to a red error style.
func buildCard(text string, done, failed bool, control *streamCardControl) string {
	if text == "" {
		text = " "
	} else {
		text = linkifyFeishuMarkdown(text)
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
		if control != nil {
			elements = append(elements, legacyStreamStopButton(control))
		}
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

// buildStreamCardJSON renders a JSON 2.0 card with a single markdown element
// (element_id = streamCardElementID) that native streaming updates write into.
// While streaming (done=false) streaming_mode is on so text-content updates
// render with a typewriter effect and bypass card update rate limits; the final
// update turns streaming_mode off and restyles the header for done/failed.
func buildStreamCardJSON(text string, done, failed bool, control *streamCardControl) string {
	if text == "" {
		text = " "
	} else {
		text = linkifyFeishuMarkdown(text)
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
	elements := []map[string]any{
		{
			"tag":        "markdown",
			"element_id": streamCardElementID,
			"content":    text,
		},
	}
	if !done && control != nil {
		elements = append(elements, modelPickerButton("停止任务", "danger", streamStopActionValue(control)))
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
		"body": map[string]any{"elements": elements},
	}
	b, err := json.Marshal(card)
	if err != nil {
		return `{"schema":"2.0","body":{"elements":[{"tag":"markdown","element_id":"answer","content":" "}]}}`
	}
	return string(b)
}

// linkifyFeishuMarkdown makes bare URLs explicit Markdown links. Feishu card
// Markdown does not consistently auto-link plain URL text, especially while a
// CardKit element is streaming. Existing Markdown links, autolinks, HTML tags,
// and code spans/blocks are left untouched.
func linkifyFeishuMarkdown(text string) string {
	matches := feishuBareURLPattern.FindAllStringIndex(text, -1)
	if len(matches) == 0 {
		return text
	}

	var b strings.Builder
	last := 0
	changed := false
	for _, match := range matches {
		start, end := match[0], match[1]
		candidate := text[start:end]
		url := strings.TrimRight(candidate, ".,;:!?)]}，。；：！？、）】")
		urlEnd := start + len(url)
		if url == "" || feishuURLAlreadyFormatted(text, start, urlEnd) {
			continue
		}

		b.WriteString(text[last:start])
		b.WriteByte('[')
		b.WriteString(url)
		b.WriteString("](")
		b.WriteString(url)
		b.WriteByte(')')
		b.WriteString(text[urlEnd:end])
		last = end
		changed = true
	}
	if !changed {
		return text
	}
	b.WriteString(text[last:])
	return b.String()
}

func feishuURLAlreadyFormatted(text string, start, end int) bool {
	before := text[:start]
	after := text[end:]
	if strings.HasSuffix(before, "](") || strings.HasSuffix(before, "<") ||
		(strings.HasSuffix(before, "[") && strings.HasPrefix(after, "](")) {
		return true
	}
	// Do not rewrite URLs inside raw HTML tags such as href attributes.
	if strings.LastIndex(before, "<") > strings.LastIndex(before, ">") {
		return true
	}
	// Preserve literal examples in fenced and inline code.
	if strings.Count(before, "```")%2 == 1 {
		return true
	}
	lineStart := strings.LastIndex(before, "\n") + 1
	return strings.Count(before[lineStart:], "`")%2 == 1
}

func legacyStreamStopButton(control *streamCardControl) map[string]any {
	return map[string]any{
		"tag": "action",
		"actions": []map[string]any{
			{
				"tag": "button", "type": "danger",
				"text":  map[string]any{"tag": "plain_text", "content": "停止任务"},
				"value": streamStopActionValue(control),
			},
		},
	}
}

func streamStopActionValue(control *streamCardControl) map[string]any {
	if control == nil {
		return nil
	}
	return map[string]any{
		modelPickerActionKey: codexTaskControlAction,
		"action":             core.ChannelTaskActionStop,
		"task_id":            control.taskID,
		"chat_id":            control.chatID,
		"chat_type":          control.chatType,
		"conversation_key":   control.conversationKey,
	}
}
