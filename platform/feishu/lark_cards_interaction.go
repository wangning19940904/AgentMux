package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/wangning19940904/AgentMux/core"
)

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

func buildAgentInteractionCard(msg *core.Message, task core.ChannelTask, interaction core.ChannelInteraction, outcome string) string {
	request := interaction.Request
	title := request.Title
	if title == "" {
		title = "Agent 需要确认"
	}
	template := "orange"
	elements := []map[string]any{}
	if outcome != "" {
		template = "green"
		if outcome == "decline" || outcome == "cancel" || outcome == "expired" {
			template = "grey"
		}
		elements = append(elements, map[string]any{
			"tag": "markdown", "content": "**已处理**：" + interactionOutcomeLabel(outcome),
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

func interactionOutcomeLabel(outcome string) string {
	switch outcome {
	case "accept":
		return "已允许一次"
	case "acceptForSession":
		return "本会话已允许"
	case "decline", "cancel":
		return "已拒绝"
	case "expired":
		return "已过期"
	case "answered":
		return "已提交"
	default:
		return outcome
	}
}

func interactionApprovalElements(msg *core.Message, task core.ChannelTask, interaction core.ChannelInteraction) []map[string]any {
	once := modelPickerButton("允许一次", "primary", interactionActionValue(msg, task, interaction, "accept", "", ""))
	decline := modelPickerButton("拒绝", "danger", interactionActionValue(msg, task, interaction, "decline", "", ""))
	buttons := []map[string]any{once}
	if !interaction.Request.HighRisk {
		session := modelPickerButton("本会话允许", "default", interactionActionValue(msg, task, interaction, "acceptForSession", "", ""))
		session["confirm"] = map[string]any{
			"title": map[string]any{"tag": "plain_text", "content": "确认本会话允许"},
			"text":  map[string]any{"tag": "plain_text", "content": "仅当前 AgentMux 会话有效，重启后失效。"},
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
