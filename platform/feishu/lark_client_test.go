package feishu

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/agentnexus/agentnexus/core"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
)

func TestChannelHealthDetectsStartupTimeout(t *testing.T) {
	client := &larkClient{}
	client.beginHealth()
	client.mu.Lock()
	client.healthStartedAt = time.Now().Add(-larkWSStartupTimeout - time.Second)
	client.mu.Unlock()

	health := client.ChannelHealth()
	if health.State != core.ChannelStateDegraded || health.Connected || !strings.Contains(health.Error, "did not become ready") {
		t.Fatalf("health = %+v", health)
	}
}

func TestChannelHealthDefaultsToStartingBeforeListen(t *testing.T) {
	health := (&larkClient{}).ChannelHealth()
	if health.State != core.ChannelStateStarting || health.Connected {
		t.Fatalf("health = %+v", health)
	}
}

func TestChannelHealthDetectsStaleHeartbeatAndRecoversOnPong(t *testing.T) {
	client := &larkClient{}
	client.beginHealth()
	client.markReady()
	client.mu.Lock()
	client.lastHeartbeatAt = time.Now().Add(-larkWSHeartbeatTimeout - time.Second)
	client.lastEventAt = time.Time{}
	client.mu.Unlock()

	health := client.ChannelHealth()
	if health.State != core.ChannelStateDegraded || health.Connected || !strings.Contains(health.Error, "heartbeat is stale") {
		t.Fatalf("stale health = %+v", health)
	}

	beforePong := time.Now()
	logger := &larkWSHealthLogger{client: client}
	logger.Debug(context.Background(), "receive pong")
	health = client.ChannelHealth()
	if health.State != core.ChannelStateRunning || !health.Connected || health.LastHeartbeatAt.Before(beforePong) {
		t.Fatalf("recovered health = %+v", health)
	}
}

func TestCloseCancelsClientAndMarksItStopped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := &larkClient{cancel: cancel}
	client.beginHealth()
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("Close did not cancel the WebSocket context")
	}
	health := client.ChannelHealth()
	if health.State != core.ChannelStateStopped || health.Connected {
		t.Fatalf("health = %+v", health)
	}
}

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
	msg, ok := client.messageFromCardAction("channel:c1", event)
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
	msg, ok := client.messageFromCardAction("project:demo", event)
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
	msg, ok := client.messageFromCardAction("channel:c1", event)
	if !ok || msg.RuntimeSettingsAction == nil {
		t.Fatalf("runtime action was not recognized: %+v", msg)
	}
	if msg.ID != "om_picker" || msg.InteractionMessageID != "om_picker" || msg.Text != "" || msg.RuntimeSettingsAction.Setting != core.RuntimeSettingReasoningEffort || msg.RuntimeSettingsAction.Value != "high" {
		t.Fatalf("runtime action message = %+v", msg)
	}
}

func TestAgentInteractionCardAndCallbackCorrelation(t *testing.T) {
	msg := &core.Message{ID: "om_root", ChatID: "oc_1", ChatType: "group", ConversationKey: "root:om_root"}
	task := core.ChannelTask{ID: "task-1", ConversationKey: "root:om_root"}
	interaction := core.ChannelInteraction{
		ID: "interaction-1", TaskID: task.ID, Nonce: "nonce-1",
		Request: core.AgentInteraction{
			ID: "interaction-1", Kind: core.AgentInteractionCommandApproval,
			Title: "命令执行审批", Command: "git status",
		},
	}
	card := buildAgentInteractionCard(msg, task, interaction, "")
	for _, want := range []string{`"schema":"2.0"`, `"interaction_id":"interaction-1"`, `"nonce":"nonce-1"`, `"decision":"acceptForSession"`} {
		if !strings.Contains(card, want) {
			t.Fatalf("interaction card missing %q: %s", want, card)
		}
	}

	client := &larkClient{platform: "feishu"}
	event := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Operator: &callback.Operator{OpenID: "ou_controller"},
		Context:  &callback.Context{OpenChatID: "oc_1", OpenMessageID: "om_card"},
		Action: &callback.CallBackAction{Value: map[string]interface{}{
			modelPickerActionKey: codexInteractionAction,
			"interaction_id":     "interaction-1",
			"task_id":            "task-1",
			"nonce":              "nonce-1",
			"decision":           "accept",
			"chat_id":            "oc_1",
			"chat_type":          "group",
			"conversation_key":   "root:om_root",
		}},
	}}
	actionMsg, ok := client.messageFromCardAction("channel:c1", event)
	if !ok || actionMsg.AgentInteractionAction == nil {
		t.Fatalf("interaction callback not recognized: %+v", actionMsg)
	}
	if actionMsg.AgentInteractionAction.InteractionID != "interaction-1" ||
		actionMsg.AgentInteractionAction.Decision != "accept" ||
		actionMsg.ConversationKey != "root:om_root" ||
		actionMsg.UserID != "ou_controller" ||
		actionMsg.InteractionMessageID != "om_card" {
		t.Fatalf("interaction callback = %+v", actionMsg)
	}
	if strings.Contains(actionMsg.Callback.ActionValue, "nonce-1") || strings.Contains(actionMsg.Callback.ActionValue, `"decision"`) {
		t.Fatalf("logged callback value retained approval data: %s", actionMsg.Callback.ActionValue)
	}
}

func TestMessageFromCardActionPreservesGenericCallbackForLogging(t *testing.T) {
	client := &larkClient{platform: "feishu"}
	event := &callback.CardActionTriggerEvent{
		EventV2Base: &larkevent.EventV2Base{Header: &larkevent.EventHeader{
			EventID: "evt-choice", EventType: "card.action.trigger",
		}},
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_actor"},
			Host:     "im_message",
			Context:  &callback.Context{OpenChatID: "oc_1", OpenMessageID: "om_card"},
			Action: &callback.CallBackAction{
				Tag:  "button",
				Name: "choiceA",
				Value: map[string]interface{}{
					"choice": "option_a",
					"label":  "方案 A",
				},
			},
		},
	}
	msg, ok := client.messageFromCardAction("channel:c1", event)
	if !ok || msg == nil || msg.Callback == nil {
		t.Fatalf("generic callback was not converted: %+v", msg)
	}
	if !msg.LogOnly || msg.ID != "evt-choice" || msg.ChatID != "oc_1" || msg.UserID != "ou_actor" {
		t.Fatalf("generic callback routing = %+v", msg)
	}
	if msg.Callback.Type != "card.action.trigger" || msg.Callback.MessageID != "om_card" || msg.Callback.ActionTag != "button" || msg.Callback.ActionName != "choiceA" {
		t.Fatalf("generic callback metadata = %+v", msg.Callback)
	}
	if msg.Callback.ActionValue != `{"choice":"option_a","label":"方案 A"}` {
		t.Fatalf("action value = %s", msg.Callback.ActionValue)
	}
}

func TestExtractTextPreservesPlainTextMessages(t *testing.T) {
	got := extractText("text", `{"text":"  hello 飞书  "}`)
	if got != "hello 飞书" {
		t.Fatalf("text = %q", got)
	}
}

func TestExtractTextRendersPostContent(t *testing.T) {
	content := `{
		"title":"MR 更新",
		"content":[
			[
				{"tag":"at","user_id":"ou_bot","user_name":"WangNing Bot"},
				{"tag":"text","text":" 请 Review："},
				{"tag":"a","text":"MR #7","href":"https://example.com/mr/7"}
			],
			[
				{"tag":"text","text":"参数已修复 "},
				{"tag":"img","image_key":"img_1"},
				{"tag":"emotion","emoji_type":"SMILE"}
			]
		]
	}`
	want := "MR 更新\n@WangNing Bot 请 Review：MR #7 (https://example.com/mr/7)\n参数已修复 [图片]:SMILE:"
	if got := extractText("post", content); got != want {
		t.Fatalf("post text = %q, want %q", got, want)
	}
}

func TestExtractTextRendersLocalizedPostContent(t *testing.T) {
	content := `{
		"en_us":{"title":"English","content":[[{"tag":"text","text":"fallback"}]]},
		"zh_cn":{"title":"中文标题","content":[[{"tag":"text","text":"第一行"}],[{"tag":"text","text":"第二行"}]]}
	}`
	want := "中文标题\n第一行\n第二行"
	if got := extractText("post", content); got != want {
		t.Fatalf("localized post text = %q, want %q", got, want)
	}
}

func TestExtractTextRejectsMalformedOrUnsupportedContent(t *testing.T) {
	for _, tc := range []struct {
		msgType string
		content string
	}{
		{msgType: "post", content: `{not-json`},
		{msgType: "image", content: `{"image_key":"img_1"}`},
	} {
		if got := extractText(tc.msgType, tc.content); got != "" {
			t.Fatalf("extractText(%q) = %q", tc.msgType, got)
		}
	}
}
