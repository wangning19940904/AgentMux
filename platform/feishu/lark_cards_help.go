package feishu

import (
	"context"
	"encoding/json"
	"fmt"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/wangning19940904/AgentMux/core"
)

func (c *larkClient) SendHelpCard(ctx context.Context, msg *core.Message, state core.HelpCardState) (string, error) {
	content := buildHelpCard(msg, state)
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
		return "", fmt.Errorf("%s send help card failed: %s", c.platform, resp.Msg)
	}
	if resp.Data == nil || resp.Data.MessageId == nil {
		return "", fmt.Errorf("%s send help card: missing message id", c.platform)
	}
	return *resp.Data.MessageId, nil
}

func buildHelpCard(msg *core.Message, state core.HelpCardState) string {
	elements := []map[string]any{{"tag": "markdown", "content": state.Introduction}}
	if state.RuntimeName != "" {
		elements = append(elements, map[string]any{
			"tag": "markdown", "content": "**当前运行时**：`" + state.RuntimeName + "`",
		})
	}

	commandText := ""
	for _, command := range state.Commands {
		if commandText != "" {
			commandText += "\n"
		}
		commandText += "`" + command.Command + "`  " + command.Description
	}
	elements = append(elements, map[string]any{"tag": "markdown", "content": "**支持的命令**\n" + commandText})

	buttons := make([]map[string]any, 0)
	for _, command := range state.Commands {
		if !command.Actionable || !core.IsHelpCommandAction(command.Command) {
			continue
		}
		buttonType := "default"
		if command.Command == "/model" {
			buttonType = "primary"
		} else if command.Command == "/clear" || command.Command == "/stop" {
			buttonType = "danger"
		}
		buttons = append(buttons, modelPickerButton(command.Command, buttonType, helpCommandActionValue(msg, command.Command)))
	}
	for i := 0; i < len(buttons); i += 3 {
		end := min(i+3, len(buttons))
		elements = append(elements, map[string]any{
			"tag": "column_set", "flex_mode": "stretch", "columns": interactionButtonColumns(buttons[i:end]),
		})
	}

	card := map[string]any{
		"schema": "2.0",
		"config": map[string]any{"wide_screen_mode": true},
		"header": map[string]any{
			"template": "blue",
			"title":    map[string]any{"tag": "plain_text", "content": state.AgentName + " · 帮助"},
		},
		"body": map[string]any{"elements": elements},
	}
	encoded, err := json.Marshal(card)
	if err != nil {
		return `{"schema":"2.0","body":{"elements":[{"tag":"markdown","content":"help unavailable"}]}}`
	}
	return string(encoded)
}

func helpCommandActionValue(msg *core.Message, command string) map[string]any {
	return map[string]any{
		modelPickerActionKey: helpCommandAction,
		"command":            command,
		"chat_id":            msg.ChatID,
		"chat_type":          msg.ChatType,
		"conversation_key":   core.ResolveConversationKey(msg),
	}
}
