package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"strings"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func (c *larkClient) SendImage(ctx context.Context, chatID, fileName string, data []byte) (string, error) {
	imageKey, err := c.uploadImage(ctx, fileName, data)
	if err != nil {
		return "", err
	}
	content, _ := json.Marshal(map[string]string{"image_key": imageKey})
	return c.sendMessage(ctx, chatID, larkim.MsgTypeImage, string(content))
}

func (c *larkClient) ReplyImage(ctx context.Context, messageID, fileName string, data []byte) (string, error) {
	imageKey, err := c.uploadImage(ctx, fileName, data)
	if err != nil {
		return "", err
	}
	content, _ := json.Marshal(map[string]string{"image_key": imageKey})
	return c.replyMessage(ctx, messageID, larkim.MsgTypeImage, string(content))
}

func (c *larkClient) SendFile(ctx context.Context, chatID, fileName string, data []byte) (string, error) {
	fileKey, err := c.uploadFile(ctx, fileName, data)
	if err != nil {
		return "", err
	}
	content, _ := json.Marshal(map[string]string{"file_key": fileKey})
	return c.sendMessage(ctx, chatID, larkim.MsgTypeFile, string(content))
}

func (c *larkClient) ReplyFile(ctx context.Context, messageID, fileName string, data []byte) (string, error) {
	fileKey, err := c.uploadFile(ctx, fileName, data)
	if err != nil {
		return "", err
	}
	content, _ := json.Marshal(map[string]string{"file_key": fileKey})
	return c.replyMessage(ctx, messageID, larkim.MsgTypeFile, string(content))
}

func (c *larkClient) uploadImage(ctx context.Context, fileName string, data []byte) (string, error) {
	req := larkim.NewCreateImageReqBuilder().
		Body(larkim.NewCreateImageReqBodyBuilder().
			ImageType("message").
			Image(bytes.NewReader(data)).
			Build()).
		Build()
	resp, err := c.api.Im.Image.Create(ctx, req)
	if err != nil {
		return "", err
	}
	if !resp.Success() {
		return "", fmt.Errorf("%s upload image failed: %s", c.platform, resp.Msg)
	}
	if resp.Data == nil || resp.Data.ImageKey == nil {
		return "", fmt.Errorf("%s upload image: missing image key", c.platform)
	}
	return *resp.Data.ImageKey, nil
}

func (c *larkClient) uploadFile(ctx context.Context, fileName string, data []byte) (string, error) {
	req := larkim.NewCreateFileReqBuilder().
		Body(larkim.NewCreateFileReqBodyBuilder().
			FileType("stream").
			FileName(fileName).
			File(bytes.NewReader(data)).
			Build()).
		Build()
	resp, err := c.api.Im.File.Create(ctx, req)
	if err != nil {
		return "", err
	}
	if !resp.Success() {
		return "", fmt.Errorf("%s upload file failed: %s", c.platform, resp.Msg)
	}
	if resp.Data == nil || resp.Data.FileKey == nil {
		return "", fmt.Errorf("%s upload file: missing file key", c.platform)
	}
	return *resp.Data.FileKey, nil
}

func (c *larkClient) sendMessage(ctx context.Context, chatID, msgType, content string) (string, error) {
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("chat_id").
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).
			MsgType(msgType).
			Content(content).
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
		return "", fmt.Errorf("%s send: missing message id", c.platform)
	}
	return *resp.Data.MessageId, nil
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

func (c *larkClient) SendCard(ctx context.Context, chatID, text string, done, failed bool, images []streamCardImage, control *streamCardControl) (string, error) {
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("chat_id").
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).
			MsgType(larkim.MsgTypeInteractive).
			Content(buildCardWithImages(text, done, failed, images, control)).
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

func (c *larkClient) ReplyCard(ctx context.Context, messageID, text string, done, failed bool, images []streamCardImage, control *streamCardControl) (string, error) {
	return c.replyMessage(ctx, messageID, larkim.MsgTypeInteractive, buildCardWithImages(text, done, failed, images, control))
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

func (c *larkClient) UpdateCard(ctx context.Context, messageID, text string, done, failed bool, images []streamCardImage, control *streamCardControl) error {
	req := larkim.NewPatchMessageReqBuilder().
		MessageId(messageID).
		Body(larkim.NewPatchMessageReqBodyBuilder().
			Content(buildCardWithImages(text, done, failed, images, control)).
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
