// Package discord implements the Discord platform adapter using the Gateway
// WebSocket via discordgo — no public IP required. The bot answers direct
// messages always and guild messages only when mentioned (unless
// reply_all=true). Requires the Message Content intent to be enabled in the
// Discord developer portal.
package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/wangning19940904/AgentMux/core"
)

// discordMaxLen keeps messages under Discord's 2000-char hard limit.
const discordMaxLen = 1900

func init() {
	core.RegisterPlatform("discord", func(cfg map[string]any) (core.Platform, error) {
		p := &Platform{}
		p.token, _ = cfg["token"].(string)
		if v, ok := cfg["reply_all"].(bool); ok {
			p.replyAll = v
		} else if v, ok := cfg["reply_all"].(string); ok {
			p.replyAll = v == "true" || v == "1"
		}
		if p.token == "" {
			return nil, fmt.Errorf("discord: token is required")
		}
		return p, nil
	})
}

// Platform is the Discord Gateway adapter.
type Platform struct {
	token    string
	replyAll bool

	mu      sync.Mutex
	session *discordgo.Session
}

// Name returns the registered name.
func (p *Platform) Name() string { return "discord" }

// Start opens the Gateway connection and forwards inbound messages until ctx
// is cancelled.
func (p *Platform) Start(ctx context.Context, inbound chan<- *core.Message) error {
	session, err := discordgo.New("Bot " + p.token)
	if err != nil {
		return fmt.Errorf("discord: %w", err)
	}
	session.Identify.Intents = discordgo.IntentsGuildMessages |
		discordgo.IntentsDirectMessages | discordgo.IntentMessageContent

	session.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		if m.Author == nil || m.Author.Bot || (s.State.User != nil && m.Author.ID == s.State.User.ID) {
			return
		}
		text := strings.TrimSpace(m.Content)
		isDM := m.GuildID == ""
		if !isDM && !p.replyAll {
			mentioned := false
			if s.State.User != nil {
				for _, u := range m.Mentions {
					if u.ID == s.State.User.ID {
						mentioned = true
						break
					}
				}
				text = stripBotMention(text, s.State.User.ID)
			}
			if !mentioned {
				return
			}
		}
		if text == "" {
			return
		}
		msg := &core.Message{
			ID:        m.ID,
			ChatID:    m.ChannelID,
			UserID:    m.Author.ID,
			UserName:  m.Author.Username,
			Text:      text,
			Timestamp: time.Now(),
			Platform:  "discord",
		}
		select {
		case inbound <- msg:
		case <-ctx.Done():
		}
	})

	session.AddHandler(func(s *discordgo.Session, interaction *discordgo.InteractionCreate) {
		if interaction == nil || interaction.Type != discordgo.InteractionMessageComponent {
			return
		}
		data := interaction.MessageComponentData()
		if !strings.HasPrefix(data.CustomID, "agentmux_settings_") || len(data.Values) == 0 {
			return
		}
		var action discordRuntimeSettingsAction
		if err := json.Unmarshal([]byte(data.Values[0]), &action); err != nil || action.Setting == "" || interaction.Message == nil {
			return
		}
		_ = s.InteractionRespond(interaction.Interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseDeferredMessageUpdate})
		userID := ""
		if interaction.Member != nil && interaction.Member.User != nil {
			userID = interaction.Member.User.ID
		} else if interaction.User != nil {
			userID = interaction.User.ID
		}
		msg := &core.Message{
			ID: interaction.ID, InteractionMessageID: interaction.Message.ID, ChatID: interaction.ChannelID, UserID: userID, Platform: "discord",
			RuntimeSettingsAction: &core.RuntimeSettingsAction{Scope: core.RuntimeSettingsScope(action.Scope), Setting: core.RuntimeSetting(action.Setting), Value: action.Value, Reset: action.Reset},
		}
		select {
		case inbound <- msg:
		case <-ctx.Done():
		}
	})

	if err := session.Open(); err != nil {
		return fmt.Errorf("discord open: %w", err)
	}
	p.mu.Lock()
	p.session = session
	p.mu.Unlock()

	<-ctx.Done()
	return session.Close()
}

// Reply sends text back to the originating channel.
func (p *Platform) Reply(ctx context.Context, msg *core.Message, text string) error {
	return p.Send(ctx, msg.ChatID, text)
}

// Send delivers text to a channel id, splitting to respect Discord's length
// limit.
func (p *Platform) Send(ctx context.Context, chatID, text string) error {
	if chatID == "" {
		return fmt.Errorf("discord: empty chat id")
	}
	p.mu.Lock()
	session := p.session
	p.mu.Unlock()
	if session == nil {
		return fmt.Errorf("discord: not connected")
	}
	for _, chunk := range splitMessage(text, discordMaxLen) {
		if _, err := session.ChannelMessageSend(chatID, chunk, discordgo.WithContext(ctx)); err != nil {
			return fmt.Errorf("discord send: %w", err)
		}
	}
	return nil
}

func (p *Platform) ReplyRuntimeSettingsPicker(ctx context.Context, msg *core.Message, state core.RuntimeSettingsPickerState) error {
	if msg == nil || msg.ChatID == "" {
		return fmt.Errorf("discord: empty chat id")
	}
	session := p.discordSession()
	_, err := session.ChannelMessageSendComplex(msg.ChatID, &discordgo.MessageSend{
		Content: discordRuntimeSettingsText(state), Components: discordRuntimeSettingsComponents(state),
	}, discordgo.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("discord settings picker: %w", err)
	}
	return nil
}

func (p *Platform) UpdateRuntimeSettingsPicker(ctx context.Context, msg *core.Message, state core.RuntimeSettingsPickerState) error {
	if msg == nil || msg.ChatID == "" || (msg.ID == "" && msg.InteractionMessageID == "") {
		return fmt.Errorf("discord: missing picker message reference")
	}
	session := p.discordSession()
	text := discordRuntimeSettingsText(state)
	components := discordRuntimeSettingsComponents(state)
	messageID := msg.InteractionMessageID
	if messageID == "" {
		messageID = msg.ID
	}
	_, err := session.ChannelMessageEditComplex(&discordgo.MessageEdit{
		ID: messageID, Channel: msg.ChatID, Content: &text, Components: &components,
	}, discordgo.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("discord update settings picker: %w", err)
	}
	return nil
}

func (p *Platform) discordSession() *discordgo.Session {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.session
}

type discordRuntimeSettingsAction struct {
	Scope   string `json:"s"`
	Setting string `json:"k"`
	Value   string `json:"v,omitempty"`
	Reset   bool   `json:"r,omitempty"`
}

func discordRuntimeSettingsText(state core.RuntimeSettingsPickerState) string {
	scope := "当前会话"
	if state.Scope == core.RuntimeSettingsScopeAgent {
		scope = "Agent 默认（仅后续会话）"
	}
	text := fmt.Sprintf("**运行时设置**\n范围：%s\n模型：`%s`\n思考：`%s`\n速度：`%s`", scope, discordDisplay(state.Settings.Model), discordDisplay(state.Settings.ReasoningEffort), discordDisplay(state.Settings.ServiceTier))
	if state.Notice != "" {
		text += "\n> " + state.Notice
	}
	return text
}

func discordRuntimeSettingsComponents(state core.RuntimeSettingsPickerState) []discordgo.MessageComponent {
	rows := make([]discordgo.MessageComponent, 0, 5)
	if state.AgentDefaultsEditable {
		rows = append(rows, discordSettingsSelect("scope", "设置范围", []discordSettingOption{
			{label: "当前会话", action: discordRuntimeSettingsAction{Scope: string(core.RuntimeSettingsScopeConversation), Setting: string(core.RuntimeSettingScope)}},
			{label: "Agent 默认", action: discordRuntimeSettingsAction{Scope: string(core.RuntimeSettingsScopeAgent), Setting: string(core.RuntimeSettingScope)}},
		}, string(state.Scope)))
	}
	rows = append(rows, discordSettingRow("model", "选择模型", state, core.RuntimeSettingModel, state.Capabilities.Models)...)
	rows = append(rows, discordSettingRow("effort", "选择思考强度", state, core.RuntimeSettingReasoningEffort, state.Capabilities.ReasoningEfforts)...)
	rows = append(rows, discordSettingRow("tier", "选择速度", state, core.RuntimeSettingServiceTier, state.Capabilities.ServiceTiers)...)
	if len(rows) > 5 {
		return rows[:5]
	}
	return rows
}

type discordSettingOption struct {
	label  string
	action discordRuntimeSettingsAction
}

func discordSettingRow(id, placeholder string, state core.RuntimeSettingsPickerState, setting core.RuntimeSetting, options []core.RuntimeOption) []discordgo.MessageComponent {
	if len(options) == 0 {
		return nil
	}
	entries := make([]discordSettingOption, 0, len(options))
	for _, option := range options {
		label := option.Label
		if label == "" {
			label = option.Value
		}
		entries = append(entries, discordSettingOption{label: label, action: discordRuntimeSettingsAction{Scope: string(state.Scope), Setting: string(setting), Value: option.Value}})
	}
	// Discord limits a single select menu to 25 options. Keep the interactive
	// card valid and leave the universal /model command as the fallback for
	// unusually large provider catalogs.
	if len(entries) > 25 {
		entries = entries[:25]
	}
	return []discordgo.MessageComponent{discordSettingsSelect(id, placeholder, entries, state.Settings.Value(setting))}
}

func discordSettingsSelect(id, placeholder string, entries []discordSettingOption, selected string) discordgo.MessageComponent {
	options := make([]discordgo.SelectMenuOption, 0, len(entries))
	for _, entry := range entries {
		encoded, _ := json.Marshal(entry.action)
		options = append(options, discordgo.SelectMenuOption{Label: entry.label, Value: string(encoded), Default: entry.action.Value == selected || (entry.action.Setting == string(core.RuntimeSettingScope) && entry.action.Scope == selected)})
	}
	return discordgo.ActionsRow{Components: []discordgo.MessageComponent{discordgo.SelectMenu{
		CustomID: "agentmux_settings_" + id, Placeholder: placeholder, Options: options,
	}}}
}

func discordDisplay(value string) string {
	if strings.TrimSpace(value) == "" {
		return "runtime default"
	}
	return value
}

// Stop is a no-op; the Start goroutine closes the session on ctx cancellation.
func (p *Platform) Stop(ctx context.Context) error { return nil }

func stripBotMention(text, botID string) string {
	for _, tag := range []string{"<@" + botID + ">", "<@!" + botID + ">"} {
		text = strings.ReplaceAll(text, tag, "")
	}
	return strings.TrimSpace(text)
}

// splitMessage cuts text into chunks of at most limit runes, preferring
// newline boundaries.
func splitMessage(text string, limit int) []string {
	if text == "" {
		return nil
	}
	var out []string
	runes := []rune(text)
	for len(runes) > limit {
		cut := limit
		for i := limit; i > limit/2; i-- {
			if runes[i-1] == '\n' {
				cut = i
				break
			}
		}
		out = append(out, strings.TrimRight(string(runes[:cut]), "\n"))
		runes = runes[cut:]
	}
	if len(runes) > 0 {
		out = append(out, string(runes))
	}
	return out
}
