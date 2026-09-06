package feishu

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	"github.com/wangning19940904/AgentMux/core"
)

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
	if action == codexInteractionAction || action == channelFeedbackAction || action == queueControlAction {
		// Approval nonces and user answers are used only for the in-memory
		// control action. Channel JSONL/audit records retain correlation but
		// never the replay token or submitted answer values.
		if action == codexInteractionAction {
			actionValue = jsonValue(map[string]any{
				modelPickerActionKey: codexInteractionAction,
				"interaction_id":     stringValue(value["interaction_id"]),
			})
		} else {
			actionValue = jsonValue(map[string]any{
				modelPickerActionKey: action,
				"task_id":            stringValue(value["task_id"]),
				"semantic":           stringValue(value["semantic"]),
			})
		}
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

	if action == conversationModeAction {
		msg.LogOnly = false
		msg.ConversationModeAction = &core.ConversationModeAction{Mode: stringValue(value["mode"]), UserID: stringValue(value["user_id"])}
		return msg, true
	}
	if action == queueControlAction {
		kind := stringValue(value["action"])
		if kind != core.ChannelTaskActionSteer && kind != core.ChannelTaskActionCancel {
			return msg, true
		}
		msg.LogOnly = false
		msg.ChannelTaskAction = &core.ChannelTaskAction{TaskID: stringValue(value["task_id"]), Action: kind, Nonce: stringValue(value["nonce"])}
		return msg, true
	}
	if action == codexTaskControlAction {
		taskID := stringValue(value["task_id"])
		taskAction := stringValue(value["action"])
		if taskID == "" || taskAction != core.ChannelTaskActionStop {
			return msg, true
		}
		msg.LogOnly = false
		msg.ChannelTaskAction = &core.ChannelTaskAction{TaskID: taskID, Action: taskAction}
		return msg, true
	}
	if action == channelFeedbackAction {
		taskID := stringValue(value["task_id"])
		nonce := stringValue(value["nonce"])
		semantic := stringValue(value["semantic"])
		if taskID == "" || nonce == "" || !core.ValidFeedbackSemantic(semantic) {
			return msg, true
		}
		msg.LogOnly = false
		msg.ChannelFeedbackAction = &core.ChannelFeedbackAction{TaskID: taskID, Nonce: nonce, Semantic: semantic}
		return msg, true
	}
	if action == channelSessionControlAction {
		taskID := stringValue(value["task_id"])
		sessionAction := stringValue(value["action"])
		if taskID == "" || (sessionAction != core.ChannelSessionActionNew && sessionAction != core.ChannelSessionActionStatus) {
			return msg, true
		}
		msg.LogOnly = false
		msg.ChannelSessionAction = &core.ChannelSessionAction{TaskID: taskID, Action: sessionAction}
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
		selectedValue := stringValue(value["value"])
		if selectedValue == "" {
			selectedValue = event.Event.Action.Option
		}
		if setting == core.RuntimeSettingScope && selectedValue != "" {
			scope = core.RuntimeSettingsScope(selectedValue)
			selectedValue = ""
		}
		if scope == "" {
			scope = core.RuntimeSettingsScopeConversation
		}
		if setting == "" {
			return msg, true
		}
		msg.LogOnly = false
		msg.RuntimeSettingsAction = &core.RuntimeSettingsAction{
			Scope: scope, Setting: setting, Value: selectedValue, Reset: boolValue(value["reset"]),
		}
		return msg, true
	}
	if action == helpCommandAction {
		command := strings.ToLower(strings.TrimSpace(stringValue(value["command"])))
		if !core.IsHelpCommandAction(command) {
			return msg, true
		}
		msg.LogOnly = false
		msg.Text = command
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
