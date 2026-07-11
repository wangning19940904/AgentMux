package feishu

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/agentnexus/agentnexus/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func TestBuildModelPickerCardUsesV2CallbackButtons(t *testing.T) {
	card := buildModelPickerCard(&core.Message{ChatID: "oc_1", ChatType: "group"}, core.ModelPickerState{
		CurrentModel: "gpt-5",
		DefaultModel: "gpt-5",
		Options: []core.ModelPickerOption{
			{Model: "gpt-5", Current: true, Default: true},
			{Model: "gpt-5-mini"},
		},
	})
	if !json.Valid([]byte(card)) {
		t.Fatalf("card is not valid JSON: %s", card)
	}
	for _, want := range []string{
		`"schema":"2.0"`,
		`"tag":"column_set"`,
		`"behaviors"`,
		`"type":"callback"`,
		`"agentnexus_action":"model_select"`,
		`"chat_id":"oc_1"`,
		`"chat_type":"group"`,
		`gpt-5-mini`,
	} {
		if !strings.Contains(card, want) {
			t.Fatalf("card missing %q: %s", want, card)
		}
	}
}

func TestModelCommandFromCardActionSelectsModel(t *testing.T) {
	client := &larkClient{platform: "feishu"}
	event := &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_actor"},
			Context:  &callback.Context{OpenChatID: "fallback_chat"},
			Action: &callback.CallBackAction{Value: map[string]interface{}{
				modelPickerActionKey: modelPickerActionSelect,
				"model":              "gpt-5-mini",
				"chat_id":            "oc_1",
				"chat_type":          "group",
			}},
		},
	}
	msg, ok := client.modelCommandFromCardAction("channel:c1", event)
	if !ok {
		t.Fatal("card action was not recognized")
	}
	if msg.Text != "/model gpt-5-mini" || msg.ChatID != "oc_1" || msg.ChatType != "group" {
		t.Fatalf("message = %+v", msg)
	}
	if !msg.MentionedBot || msg.Platform != "feishu" || msg.Project != "channel:c1" || msg.UserID != "ou_actor" {
		t.Fatalf("routing metadata = %+v", msg)
	}
}

func TestModelCommandFromCardActionResetsModelWithContextChat(t *testing.T) {
	client := &larkClient{platform: "lark"}
	event := &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Context: &callback.Context{OpenChatID: "oc_context"},
			Action: &callback.CallBackAction{Value: map[string]interface{}{
				modelPickerActionKey: modelPickerActionReset,
			}},
		},
	}
	msg, ok := client.modelCommandFromCardAction("project:demo", event)
	if !ok {
		t.Fatal("card action was not recognized")
	}
	if msg.Text != "/model reset" || msg.ChatID != "oc_context" || msg.Platform != "lark" || msg.Project != "project:demo" {
		t.Fatalf("message = %+v", msg)
	}
}

func TestRuntimeSettingsPickerCardCarriesScopeAndControls(t *testing.T) {
	card := buildRuntimeSettingsPickerCard(&core.Message{ChatID: "oc_1", ChatType: "group"}, core.RuntimeSettingsPickerState{
		Scope:                 core.RuntimeSettingsScopeConversation,
		Settings:              core.RuntimeSettings{Model: "gpt-5", ReasoningEffort: "high", ServiceTier: "priority"},
		AgentDefaultsEditable: true,
		Capabilities: core.RuntimeSettingsCapabilities{
			Models:           []core.RuntimeOption{{Value: "gpt-5", Label: "gpt-5"}, {Value: "gpt-5-mini", Label: "gpt-5-mini"}},
			ReasoningEfforts: []core.RuntimeOption{{Value: "low"}, {Value: "high"}},
			ServiceTiers:     []core.RuntimeOption{{Value: "default"}, {Value: "priority"}},
		},
	})
	if !json.Valid([]byte(card)) {
		t.Fatalf("card is not valid JSON: %s", card)
	}
	for _, want := range []string{
		`"agentnexus_action":"runtime_settings"`, `"setting":"model"`, `"setting":"reasoning_effort"`,
		`"setting":"service_tier"`, `"setting":"scope"`, `Agent 默认`, `gpt-5-mini`,
	} {
		if !strings.Contains(card, want) {
			t.Fatalf("card missing %q: %s", want, card)
		}
	}
}

func TestRuntimeSettingsActionKeepsOriginalCardMessageID(t *testing.T) {
	client := &larkClient{platform: "feishu"}
	event := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Operator: &callback.Operator{OpenID: "ou_actor"},
		Context:  &callback.Context{OpenChatID: "oc_context", OpenMessageID: "om_picker"},
		Action: &callback.CallBackAction{Value: map[string]interface{}{
			modelPickerActionKey: runtimeSettingsAction,
			"scope":              "conversation",
			"setting":            "reasoning_effort",
			"value":              "high",
			"chat_id":            "oc_1",
			"chat_type":          "group",
		}},
	}}
	msg, ok := client.modelCommandFromCardAction("channel:c1", event)
	if !ok || msg.RuntimeSettingsAction == nil {
		t.Fatalf("runtime action was not recognized: %+v", msg)
	}
	if msg.ID != "om_picker" || msg.InteractionMessageID != "om_picker" || msg.Text != "" || msg.RuntimeSettingsAction.Setting != core.RuntimeSettingReasoningEffort || msg.RuntimeSettingsAction.Value != "high" {
		t.Fatalf("runtime action message = %+v", msg)
	}
}
