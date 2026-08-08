package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/platform/settingsui"
)

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
	elements := []map[string]any{}
	if state.Notice != "" {
		elements = append(elements, map[string]any{"tag": "markdown", "content": "<font color='red'>" + state.Notice + "</font>"})
	}
	if state.Hint != "" {
		elements = append(elements, map[string]any{"tag": "markdown", "content": "<font color='grey'>" + state.Hint + "</font>"})
	}
	for _, group := range settingsui.Groups(state) {
		elements = append(elements, runtimeSettingsSelector(msg, state, group)...)
	}
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

func runtimeSettingsSelector(msg *core.Message, state core.RuntimeSettingsPickerState, group settingsui.Group) []map[string]any {
	if len(group.Options) == 0 {
		return nil
	}

	selectOptions := make([]map[string]any, 0, len(group.Options))
	selected := ""
	for _, option := range group.Options {
		value := option.Action.Value
		if group.Setting == core.RuntimeSettingScope {
			value = string(option.Action.Scope)
		}
		selectOptions = append(selectOptions, map[string]any{
			"text":  map[string]any{"tag": "plain_text", "content": option.Label},
			"value": value,
		})
		if option.Selected {
			selected = value
		}
	}

	selector := map[string]any{
		"tag":         "select_static",
		"width":       "fill",
		"placeholder": map[string]any{"tag": "plain_text", "content": "选择" + group.Title},
		"options":     selectOptions,
		"behaviors": []map[string]any{
			{
				"type": "callback",
				"value": runtimeSettingsActionValue(msg, core.RuntimeSettingsAction{
					Scope: state.Scope, Setting: group.Setting,
				}),
			},
		},
	}
	if selected != "" {
		selector["initial_option"] = selected
	}
	return []map[string]any{{
		"tag": "column_set", "flex_mode": "stretch", "horizontal_spacing": "12px",
		"columns": []map[string]any{
			{
				"tag": "column", "width": "weighted", "weight": 1, "vertical_align": "center",
				"elements": []map[string]any{{"tag": "markdown", "content": "**" + group.Title + "：**"}},
			},
			{
				"tag": "column", "width": "weighted", "weight": 4, "vertical_align": "center",
				"elements": []map[string]any{selector},
			},
		},
	}}
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
