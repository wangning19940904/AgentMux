// Package slack implements the Slack platform adapter using Socket Mode: a
// WebSocket connection receives Events API payloads (no public IP required)
// and replies go through chat.postMessage. Requires a bot token (xoxb-) with
// chat:write plus an app-level token (xapp-) with connections:write, and the
// app must subscribe to app_mention and message.im events.
package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/platform/settingsui"
)

func init() {
	core.RegisterPlatform("slack", func(cfg map[string]any) (core.Platform, error) {
		p := &Platform{}
		p.botToken, _ = cfg["bot_token"].(string)
		p.appToken, _ = cfg["app_token"].(string)
		if p.botToken == "" || p.appToken == "" {
			return nil, fmt.Errorf("slack: bot_token and app_token are required")
		}
		return p, nil
	})
}

// Platform is the Slack Socket Mode adapter.
type Platform struct {
	botToken string
	appToken string

	mu     sync.Mutex
	client *slackapi.Client
	seen   map[string]bool // message ts dedup (app_mention + message overlap)
}

// Name returns the registered name.
func (p *Platform) Name() string { return "slack" }

// Start opens the Socket Mode connection and forwards inbound messages until
// ctx is cancelled.
func (p *Platform) Start(ctx context.Context, inbound chan<- *core.Message) error {
	client := slackapi.New(p.botToken, slackapi.OptionAppLevelToken(p.appToken))
	socket := socketmode.New(client)
	p.mu.Lock()
	p.client = client
	p.seen = map[string]bool{}
	p.mu.Unlock()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case evt, ok := <-socket.Events:
				if !ok {
					return
				}
				p.handleEvent(ctx, socket, evt, inbound)
			}
		}
	}()

	err := socket.RunContext(ctx)
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func (p *Platform) handleEvent(ctx context.Context, socket *socketmode.Client, evt socketmode.Event, inbound chan<- *core.Message) {
	if evt.Type == socketmode.EventTypeInteractive {
		p.handleInteractive(ctx, socket, evt, inbound)
		return
	}
	if evt.Type != socketmode.EventTypeEventsAPI {
		return
	}
	data, ok := evt.Data.(slackevents.EventsAPIEvent)
	if !ok {
		return
	}
	if evt.Request != nil {
		socket.Ack(*evt.Request)
	}
	if data.Type != slackevents.CallbackEvent {
		return
	}

	var msg *core.Message
	switch ev := data.InnerEvent.Data.(type) {
	case *slackevents.AppMentionEvent:
		if ev.BotID != "" || ev.User == "" || p.duplicate(ev.TimeStamp) {
			return
		}
		text := stripMention(ev.Text)
		if text == "" {
			return
		}
		msg = &core.Message{
			ID: ev.TimeStamp, ChatID: ev.Channel, UserID: ev.User,
			Text: text, Platform: "slack",
		}
	case *slackevents.MessageEvent:
		// Direct messages only; channel messages are handled via app_mention
		// so the bot does not answer every channel conversation.
		if ev.ChannelType != "im" || ev.BotID != "" || ev.User == "" || ev.Text == "" {
			return
		}
		if p.duplicate(ev.TimeStamp) {
			return
		}
		msg = &core.Message{
			ID: ev.TimeStamp, ChatID: ev.Channel, UserID: ev.User,
			Text: ev.Text, Platform: "slack",
		}
	default:
		return
	}

	select {
	case inbound <- msg:
	case <-ctx.Done():
	}
}

func (p *Platform) handleInteractive(ctx context.Context, socket *socketmode.Client, evt socketmode.Event, inbound chan<- *core.Message) {
	callback, ok := evt.Data.(slackapi.InteractionCallback)
	if evt.Request != nil {
		socket.Ack(*evt.Request)
	}
	if !ok || callback.Type != slackapi.InteractionTypeBlockActions || len(callback.ActionCallback.BlockActions) == 0 {
		return
	}
	action := callback.ActionCallback.BlockActions[0]
	value := action.Value
	if action.SelectedOption.Value != "" {
		value = action.SelectedOption.Value
	}
	if strings.HasPrefix(action.ActionID, "agentmux_help_") {
		command := strings.ToLower(strings.TrimSpace(value))
		if !core.IsHelpCommandAction(command) {
			return
		}
		messageID := callback.Container.MessageTs
		if messageID == "" {
			messageID = callback.MessageTs
		}
		msg := &core.Message{
			ID: callback.ActionTs, InteractionMessageID: messageID,
			ChatID: callback.Channel.ID, UserID: callback.User.ID,
			Platform: "slack", Text: command,
		}
		select {
		case inbound <- msg:
		case <-ctx.Done():
		}
		return
	}
	var decoded slackRuntimeSettingsAction
	if err := json.Unmarshal([]byte(value), &decoded); err != nil || decoded.Setting == "" {
		return
	}
	messageID := callback.Container.MessageTs
	if messageID == "" {
		messageID = callback.MessageTs
	}
	msg := &core.Message{
		ID:                   callback.ActionTs,
		InteractionMessageID: messageID,
		ChatID:               callback.Channel.ID,
		UserID:               callback.User.ID,
		Platform:             "slack",
		RuntimeSettingsAction: &core.RuntimeSettingsAction{
			Scope: core.RuntimeSettingsScope(decoded.Scope), Setting: core.RuntimeSetting(decoded.Setting), Value: decoded.Value, Reset: decoded.Reset,
		},
	}
	select {
	case inbound <- msg:
	case <-ctx.Done():
	}
}

func (p *Platform) duplicate(ts string) bool {
	if ts == "" {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.seen[ts] {
		return true
	}
	if len(p.seen) > 2048 {
		p.seen = map[string]bool{}
	}
	p.seen[ts] = true
	return false
}

// Reply sends text back to the originating channel.
func (p *Platform) Reply(ctx context.Context, msg *core.Message, text string) error {
	return p.Send(ctx, msg.ChatID, text)
}

// Send delivers text to a channel or DM id.
func (p *Platform) Send(ctx context.Context, chatID, text string) error {
	if chatID == "" {
		return fmt.Errorf("slack: empty chat id")
	}
	p.mu.Lock()
	client := p.client
	p.mu.Unlock()
	if client == nil {
		client = slackapi.New(p.botToken, slackapi.OptionAppLevelToken(p.appToken))
	}
	_, _, err := client.PostMessageContext(ctx, chatID, slackapi.MsgOptionText(text, false))
	if err != nil {
		return fmt.Errorf("slack send: %w", err)
	}
	return nil
}

func (p *Platform) ReplyRuntimeSettingsPicker(ctx context.Context, msg *core.Message, state core.RuntimeSettingsPickerState) error {
	if msg == nil || msg.ChatID == "" {
		return fmt.Errorf("slack: empty chat id")
	}
	client := p.slackClient()
	_, _, err := client.PostMessageContext(ctx, msg.ChatID,
		slackapi.MsgOptionText("Runtime settings", false),
		slackapi.MsgOptionBlocks(slackRuntimeSettingsBlocks(state)...),
	)
	if err != nil {
		return fmt.Errorf("slack settings picker: %w", err)
	}
	return nil
}

func (p *Platform) ReplyHelpCard(ctx context.Context, msg *core.Message, state core.HelpCardState) error {
	if msg == nil || msg.ChatID == "" {
		return fmt.Errorf("slack: empty chat id")
	}
	_, _, err := p.slackClient().PostMessageContext(ctx, msg.ChatID,
		slackapi.MsgOptionText(state.Introduction, false),
		slackapi.MsgOptionBlocks(slackHelpBlocks(state)...),
	)
	if err != nil {
		return fmt.Errorf("slack help card: %w", err)
	}
	return nil
}

func (p *Platform) UpdateRuntimeSettingsPicker(ctx context.Context, msg *core.Message, state core.RuntimeSettingsPickerState) error {
	if msg == nil || msg.ChatID == "" || msg.ID == "" {
		return fmt.Errorf("slack: missing picker message reference")
	}
	client := p.slackClient()
	_, _, _, err := client.UpdateMessageContext(ctx, msg.ChatID, msg.ID,
		slackapi.MsgOptionText("Runtime settings", false),
		slackapi.MsgOptionBlocks(slackRuntimeSettingsBlocks(state)...),
	)
	if err != nil {
		return fmt.Errorf("slack update settings picker: %w", err)
	}
	return nil
}

func (p *Platform) slackClient() *slackapi.Client {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client == nil {
		p.client = slackapi.New(p.botToken, slackapi.OptionAppLevelToken(p.appToken))
	}
	return p.client
}

type slackRuntimeSettingsAction struct {
	Scope   string `json:"s"`
	Setting string `json:"k"`
	Value   string `json:"v,omitempty"`
	Reset   bool   `json:"r,omitempty"`
}

func slackRuntimeSettingsBlocks(state core.RuntimeSettingsPickerState) []slackapi.Block {
	summary := settingsui.SummaryText(state, settingsui.Format{
		Bold: func(s string) string { return "*" + s + "*" },
		Code: func(s string) string { return "`" + s + "`" },
	})
	blocks := []slackapi.Block{slackapi.NewSectionBlock(
		slackapi.NewTextBlockObject("mrkdwn", summary, false, false), nil, nil,
	)}
	if state.Notice != "" {
		blocks = append(blocks, slackapi.NewSectionBlock(slackapi.NewTextBlockObject("mrkdwn", ":warning: "+state.Notice, false, false), nil, nil))
	}
	if state.Hint != "" {
		blocks = append(blocks, slackapi.NewContextBlock("agentmux_settings_hint", slackapi.NewTextBlockObject("mrkdwn", state.Hint, false, false)))
	}
	for _, group := range settingsui.Groups(state) {
		blocks = append(blocks, slackSettingsSelect(group))
	}
	return blocks
}

func slackHelpBlocks(state core.HelpCardState) []slackapi.Block {
	intro := "*" + state.AgentName + " · 帮助*\n" + state.Introduction
	if state.RuntimeName != "" {
		intro += "\n*当前运行时*：`" + state.RuntimeName + "`"
	}
	blocks := []slackapi.Block{slackapi.NewSectionBlock(
		slackapi.NewTextBlockObject("mrkdwn", intro, false, false), nil, nil,
	)}
	commands := "*支持的命令*"
	buttons := make([]slackapi.BlockElement, 0)
	for index, command := range state.Commands {
		commands += "\n`" + command.Command + "`  " + command.Description
		if !command.Actionable || !core.IsHelpCommandAction(command.Command) {
			continue
		}
		button := slackapi.NewButtonBlockElement(
			fmt.Sprintf("agentmux_help_%d", index), command.Command,
			slackapi.NewTextBlockObject("plain_text", command.Command, false, false),
		)
		if command.Command == "/model" {
			button.WithStyle(slackapi.StylePrimary)
		} else if command.Command == "/clear" || command.Command == "/stop" {
			button.WithStyle(slackapi.StyleDanger)
		}
		buttons = append(buttons, button)
	}
	blocks = append(blocks, slackapi.NewSectionBlock(slackapi.NewTextBlockObject("mrkdwn", commands, false, false), nil, nil))
	for i := 0; i < len(buttons); i += 5 {
		end := min(i+5, len(buttons))
		blocks = append(blocks, slackapi.NewActionBlock(fmt.Sprintf("agentmux_help_actions_%d", i/5), buttons[i:end]...))
	}
	return blocks
}

func slackSettingsSelect(group settingsui.Group) slackapi.Block {
	options := make([]*slackapi.OptionBlockObject, 0, len(group.Options))
	var initial *slackapi.OptionBlockObject
	for _, entry := range group.Options {
		action := slackRuntimeSettingsAction{
			Scope: string(entry.Action.Scope), Setting: string(entry.Action.Setting),
			Value: entry.Action.Value, Reset: entry.Action.Reset,
		}
		data, _ := json.Marshal(action)
		option := slackapi.NewOptionBlockObject(string(data), slackapi.NewTextBlockObject("plain_text", entry.Label, false, false), nil)
		options = append(options, option)
		if entry.Selected {
			initial = option
		}
	}
	selectElement := slackapi.NewOptionsSelectBlockElement("static_select", slackapi.NewTextBlockObject("plain_text", group.Title, false, false), "agentmux_settings_"+group.ID, options...)
	if initial != nil {
		selectElement.WithInitialOption(initial)
	}
	return slackapi.NewActionBlock("agentmux_settings_"+group.ID, selectElement)
}

// Stop is a no-op; RunContext exits on ctx cancellation.
func (p *Platform) Stop(ctx context.Context) error { return nil }

// stripMention removes the leading <@BOTID> from an app_mention text.
func stripMention(text string) string {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "<@") {
		if idx := strings.Index(text, ">"); idx != -1 {
			return strings.TrimSpace(text[idx+1:])
		}
	}
	return text
}
