package feishu

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	"github.com/wangning19940904/AgentMux/core"
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
		`"agentmux_action":"model_select"`,
		`"chat_id":"oc_1"`,
		`"chat_type":"group"`,
		`gpt-5-mini`,
	} {
		if !strings.Contains(card, want) {
			t.Fatalf("card missing %q: %s", want, card)
		}
	}
}

func TestStreamingTaskCardsExposeStopOnlyWhileRunning(t *testing.T) {
	control := &streamCardControl{
		taskID: "task-1", chatID: "oc_1", chatType: "group", conversationKey: "root:om_1",
	}
	for name, card := range map[string]string{
		"native": buildStreamCardJSON("working", false, false, control),
		"legacy": buildCard("working", false, false, control),
	} {
		if !json.Valid([]byte(card)) {
			t.Fatalf("%s card is not valid JSON: %s", name, card)
		}
		for _, want := range []string{
			"停止任务", `"agentmux_action":"codex_task_control"`, `"action":"stop"`,
			`"task_id":"task-1"`, `"conversation_key":"root:om_1"`,
		} {
			if !strings.Contains(card, want) {
				t.Fatalf("%s running card missing %q: %s", name, want, card)
			}
		}
	}
	for name, card := range map[string]string{
		"native": buildStreamCardJSON("done", true, false, control),
		"legacy": buildCard("done", true, false, control),
	} {
		if strings.Contains(card, "codex_task_control") || strings.Contains(card, "停止任务") {
			t.Fatalf("%s completed card is still actionable: %s", name, card)
		}
	}
}

func TestStreamingCardsLinkifyBareURLs(t *testing.T) {
	bareURL := "https://open.feishu.cn/page/cli?user_code=LX5C-4SAK&lpv=1.0.85&from=cli"
	input := "请打开：\n" + bareURL + "\n已有：[登录](https://example.com/login)\n代码：`https://example.com/code`"
	want := "[" + bareURL + "](" + bareURL + ")"
	wantJSON := strings.ReplaceAll(want, "&", `\u0026`)

	for name, card := range map[string]string{
		"native": buildStreamCardJSON(input, false, false, nil),
		"legacy": buildCard(input, false, false, nil),
	} {
		if !strings.Contains(card, wantJSON) {
			t.Fatalf("%s card did not linkify bare URL: %s", name, card)
		}
		if !strings.Contains(card, `[登录](https://example.com/login)`) ||
			!strings.Contains(card, "`https://example.com/code`") {
			t.Fatalf("%s card rewrote existing Markdown: %s", name, card)
		}
	}
}

func TestLinkifyFeishuMarkdownPreservesTrailingPunctuation(t *testing.T) {
	got := linkifyFeishuMarkdown("文档：https://example.com/docs。")
	want := "文档：[https://example.com/docs](https://example.com/docs)。"
	if got != want {
		t.Fatalf("linkified Markdown = %q, want %q", got, want)
	}
}

func TestTaskStopCardActionKeepsTaskCorrelation(t *testing.T) {
	client := &larkClient{platform: "feishu"}
	event := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Operator: &callback.Operator{OpenID: "ou_controller"},
		Context:  &callback.Context{OpenChatID: "fallback_chat", OpenMessageID: "om_task_card"},
		Action: &callback.CallBackAction{Value: map[string]interface{}{
			modelPickerActionKey: codexTaskControlAction,
			"action":             core.ChannelTaskActionStop,
			"task_id":            "task-1",
			"chat_id":            "oc_1",
			"chat_type":          "group",
			"conversation_key":   "root:om_1",
		}},
	}}
	msg, ok := client.messageFromCardAction("channel:c1", event)
	if !ok || msg == nil || msg.LogOnly || msg.ChannelTaskAction == nil {
		t.Fatalf("task stop action was not recognized: %+v", msg)
	}
	if msg.ChannelTaskAction.TaskID != "task-1" || msg.ChannelTaskAction.Action != core.ChannelTaskActionStop ||
		msg.ChatID != "oc_1" || msg.ConversationKey != "root:om_1" || msg.UserID != "ou_controller" {
		t.Fatalf("task stop action message = %+v", msg)
	}
}

func TestCardActionReplyTargetsTheCardMessage(t *testing.T) {
	msg := &core.Message{
		ID: "event-id", InteractionMessageID: "om_task_card", ChatID: "oc_1", ChatType: "group",
	}
	if got := threadReplyMessageID(msg); got != "om_task_card" {
		t.Fatalf("card action reply target = %q, want card message", got)
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
		Settings:              core.RuntimeSettings{Model: "gpt-5", ReasoningEffort: "high", ServiceTier: "priority", ApprovalMode: core.ApprovalModeYolo},
		AgentDefaultsEditable: true,
		Capabilities: core.RuntimeSettingsCapabilities{
			Models:           []core.RuntimeOption{{Value: "gpt-5", Label: "gpt-5"}, {Value: "gpt-5-mini", Label: "gpt-5-mini"}},
			ReasoningEfforts: []core.RuntimeOption{{Value: "low"}, {Value: "high"}},
			ServiceTiers:     []core.RuntimeOption{{Value: "default"}, {Value: "priority"}},
			ApprovalModes:    []core.RuntimeOption{{Value: core.ApprovalModeManual}, {Value: core.ApprovalModeYolo}},
		},
	})
	if !json.Valid([]byte(card)) {
		t.Fatalf("card is not valid JSON: %s", card)
	}
	for _, want := range []string{
		`"agentmux_action":"runtime_settings"`, `"setting":"model"`, `"setting":"reasoning_effort"`,
		`"setting":"service_tier"`, `"setting":"approval_mode"`, `"setting":"scope"`, `Agent 默认`, `gpt-5-mini`,
		`"tag":"select_static"`, `"initial_option":"gpt-5"`, `"width":"fill"`,
	} {
		if !strings.Contains(card, want) {
			t.Fatalf("card missing %q: %s", want, card)
		}
	}
	if got := strings.Count(card, `"tag":"select_static"`); got != 5 {
		t.Fatalf("select count = %d, want 5: %s", got, card)
	}
	if strings.Contains(card, `"tag":"button"`) {
		t.Fatalf("runtime settings card still contains buttons: %s", card)
	}
	if strings.Contains(card, `**设置范围**：`) || strings.Contains(card, `**模型**：`) {
		t.Fatalf("runtime settings card still contains the duplicate summary: %s", card)
	}
	if got := strings.Count(card, `"tag":"column_set"`); got != 5 {
		t.Fatalf("inline selector row count = %d, want 5: %s", got, card)
	}
	for _, selected := range []string{"conversation", "gpt-5", "high", "priority", core.ApprovalModeYolo} {
		if !strings.Contains(card, `"initial_option":"`+selected+`"`) {
			t.Fatalf("card missing selected option %q: %s", selected, card)
		}
	}
}

func TestRuntimeSettingsPickerHidesUnsupportedControls(t *testing.T) {
	card := buildRuntimeSettingsPickerCard(&core.Message{ChatID: "oc_1"}, core.RuntimeSettingsPickerState{
		Scope:    core.RuntimeSettingsScopeConversation,
		Settings: core.RuntimeSettings{Model: "cursor-grok-4.5-medium-fast"},
		Capabilities: core.RuntimeSettingsCapabilities{
			Models: []core.RuntimeOption{{Value: "cursor-grok-4.5-medium-fast"}},
		},
		Unsupported: map[core.RuntimeSetting]string{
			core.RuntimeSettingReasoningEffort: "not supported",
			core.RuntimeSettingServiceTier:     "not supported",
			core.RuntimeSettingApprovalMode:    "not supported",
		},
	})
	for _, hidden := range []string{"思考强度", "速度", "审批模式", "not supported"} {
		if strings.Contains(card, hidden) {
			t.Fatalf("unsupported control %q rendered: %s", hidden, card)
		}
	}
	if got := strings.Count(card, `"tag":"select_static"`); got != 1 {
		t.Fatalf("select count = %d, want model only: %s", got, card)
	}
}

func TestRuntimeSettingsSelectCallbackUsesSelectedOption(t *testing.T) {
	tests := []struct {
		name      string
		setting   core.RuntimeSetting
		option    string
		wantScope core.RuntimeSettingsScope
		wantValue string
	}{
		{name: "model", setting: core.RuntimeSettingModel, option: "gpt-5-mini", wantScope: core.RuntimeSettingsScopeConversation, wantValue: "gpt-5-mini"},
		{name: "effort", setting: core.RuntimeSettingReasoningEffort, option: "high", wantScope: core.RuntimeSettingsScopeConversation, wantValue: "high"},
		{name: "scope", setting: core.RuntimeSettingScope, option: string(core.RuntimeSettingsScopeAgent), wantScope: core.RuntimeSettingsScopeAgent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &larkClient{platform: "feishu"}
			event := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
				Context: &callback.Context{OpenChatID: "oc_context", OpenMessageID: "om_picker"},
				Action: &callback.CallBackAction{
					Value: map[string]interface{}{
						modelPickerActionKey: runtimeSettingsAction,
						"scope":              "conversation",
						"setting":            string(tt.setting),
						"chat_id":            "oc_1",
						"chat_type":          "group",
					},
					Option: tt.option,
				},
			}}
			msg, ok := client.messageFromCardAction("channel:c1", event)
			if !ok || msg.RuntimeSettingsAction == nil {
				t.Fatalf("runtime select action was not recognized: %+v", msg)
			}
			if msg.RuntimeSettingsAction.Setting != tt.setting || msg.RuntimeSettingsAction.Scope != tt.wantScope || msg.RuntimeSettingsAction.Value != tt.wantValue {
				t.Fatalf("runtime select action = %+v", msg.RuntimeSettingsAction)
			}
		})
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

func TestResolvedAgentInteractionCardUsesReadableOutcome(t *testing.T) {
	card := buildAgentInteractionCard(&core.Message{}, core.ChannelTask{}, core.ChannelInteraction{
		Request: core.AgentInteraction{Title: "命令执行审批"},
	}, "acceptForSession")
	if !strings.Contains(card, "本会话已允许") || strings.Contains(card, ">acceptForSession<") {
		t.Fatalf("resolved interaction outcome is not readable: %s", card)
	}
}

func TestUserInputInteractionCardRendersLinkCodeAndActions(t *testing.T) {
	msg := &core.Message{ID: "om_root", ChatID: "oc_1", ChatType: "group"}
	task := core.ChannelTask{ID: "task-1", ConversationKey: "root:om_root"}
	interaction := core.ChannelInteraction{
		ID: "interaction-1", TaskID: task.ID, Nonce: "nonce-1",
		Request: core.AgentInteraction{
			ID: "interaction-1", Kind: core.AgentInteractionUserInput, Title: "完成认证",
			Questions: []core.InteractionQuestion{{
				ID: "auth", Header: "飞书认证",
				Question: "[打开认证页面](https://open.feishu.cn/page/cli?user_code=ABCD)\n\n验证码：`ABCD`",
				Options:  []core.InteractionOption{{Label: "已完成"}, {Label: "取消"}},
			}},
		},
	}
	card := buildAgentInteractionCard(msg, task, interaction, "")
	for _, want := range []string{
		"https://open.feishu.cn/page/cli?user_code=ABCD", "验证码：`ABCD`", "已完成", "取消", `"decision":"answer"`,
	} {
		if !strings.Contains(card, want) {
			t.Fatalf("user-input card missing %q: %s", want, card)
		}
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
