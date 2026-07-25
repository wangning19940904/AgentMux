// Package telegram implements the Telegram platform adapter using the Bot API
// over HTTP long polling — no third-party SDK and no public IP required.
package telegram

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func init() {
	core.RegisterPlatform("telegram", func(cfg map[string]any) (core.Platform, error) {
		p := &Platform{client: &http.Client{Timeout: 65 * time.Second}}
		p.token, _ = cfg["token"].(string)
		p.project, _ = cfg["project"].(string)
		if p.token == "" {
			return nil, fmt.Errorf("telegram: token is required")
		}
		if allow, ok := cfg["allow_users"].([]any); ok {
			for _, a := range allow {
				switch v := a.(type) {
				case int64:
					p.allow = append(p.allow, v)
				case float64:
					p.allow = append(p.allow, int64(v))
				}
			}
		}
		return p, nil
	})
}

// Platform is the Telegram adapter.
type Platform struct {
	token         string
	project       string
	allow         []int64
	client        *http.Client
	offset        int64
	mu            sync.Mutex
	pickerActions map[string]core.RuntimeSettingsAction
}

// Name returns the registered name.
func (p *Platform) Name() string { return "telegram" }

func (p *Platform) api(method string) string {
	return "https://api.telegram.org/bot" + p.token + "/" + method
}

// Start long-polls getUpdates and forwards inbound text messages.
func (p *Platform) Start(ctx context.Context, inbound chan<- *core.Message) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		updates, err := p.getUpdates(ctx)
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(3 * time.Second):
			}
			continue
		}
		for _, u := range updates {
			p.offset = u.UpdateID + 1
			if u.CallbackQuery != nil {
				p.handleCallback(ctx, inbound, u.CallbackQuery)
				continue
			}
			if u.Message == nil || u.Message.Text == "" {
				continue
			}
			if !p.allowed(u.Message.From.ID) {
				continue
			}
			inbound <- &core.Message{
				ChatID:   strconv.FormatInt(u.Message.Chat.ID, 10),
				UserID:   strconv.FormatInt(u.Message.From.ID, 10),
				UserName: u.Message.From.Username,
				Text:     u.Message.Text,
				Platform: "telegram",
				Project:  p.project,
			}
		}
	}
}

func (p *Platform) handleCallback(ctx context.Context, inbound chan<- *core.Message, callback *tgCallbackQuery) {
	if callback == nil || callback.Message == nil || !p.allowed(callback.From.ID) {
		return
	}
	p.answerCallback(ctx, callback.ID)
	token := strings.TrimPrefix(callback.Data, "rs:")
	p.mu.Lock()
	action, ok := p.pickerActions[token]
	p.mu.Unlock()
	if !ok || token == callback.Data {
		return
	}
	msg := &core.Message{
		ID:                    callback.ID,
		InteractionMessageID:  strconv.FormatInt(callback.Message.MessageID, 10),
		ChatID:                strconv.FormatInt(callback.Message.Chat.ID, 10),
		UserID:                strconv.FormatInt(callback.From.ID, 10),
		UserName:              callback.From.Username,
		Platform:              "telegram",
		RuntimeSettingsAction: &action,
	}
	select {
	case inbound <- msg:
	case <-ctx.Done():
	}
}

func (p *Platform) allowed(userID int64) bool {
	if len(p.allow) == 0 {
		return true
	}
	for _, a := range p.allow {
		if a == userID {
			return true
		}
	}
	return false
}

// Reply sends text back to the originating chat.
func (p *Platform) Reply(ctx context.Context, msg *core.Message, text string) error {
	return p.Send(ctx, msg.ChatID, text)
}

// Send delivers text to a chat id.
func (p *Platform) Send(ctx context.Context, chatID, text string) error {
	if chatID == "" {
		return fmt.Errorf("telegram: empty chat id")
	}
	form := url.Values{}
	form.Set("chat_id", chatID)
	form.Set("text", text)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.api("sendMessage"),
		strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("telegram send: %s", resp.Status)
	}
	return nil
}

func (p *Platform) ReplyRuntimeSettingsPicker(ctx context.Context, msg *core.Message, state core.RuntimeSettingsPickerState) error {
	if msg == nil || msg.ChatID == "" {
		return fmt.Errorf("telegram: empty chat id")
	}
	return p.sendSettingsPicker(ctx, msg.ChatID, telegramRuntimeSettingsText(state), telegramRuntimeSettingsKeyboard(p, state))
}

func (p *Platform) UpdateRuntimeSettingsPicker(ctx context.Context, msg *core.Message, state core.RuntimeSettingsPickerState) error {
	if msg == nil || msg.ChatID == "" || (msg.ID == "" && msg.InteractionMessageID == "") {
		return fmt.Errorf("telegram: missing picker message reference")
	}
	messageRef := msg.InteractionMessageID
	if messageRef == "" {
		messageRef = msg.ID
	}
	messageID, err := strconv.ParseInt(messageRef, 10, 64)
	if err != nil {
		return fmt.Errorf("telegram: invalid picker message id: %w", err)
	}
	form := url.Values{}
	form.Set("chat_id", msg.ChatID)
	form.Set("message_id", strconv.FormatInt(messageID, 10))
	form.Set("text", telegramRuntimeSettingsText(state))
	markup, _ := json.Marshal(map[string]any{"inline_keyboard": telegramRuntimeSettingsKeyboard(p, state)})
	form.Set("reply_markup", string(markup))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.api("editMessageText"), strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("telegram update settings picker: %s", resp.Status)
	}
	return nil
}

func (p *Platform) sendSettingsPicker(ctx context.Context, chatID, text string, keyboard [][]tgInlineButton) error {
	form := url.Values{}
	form.Set("chat_id", chatID)
	form.Set("text", text)
	markup, _ := json.Marshal(map[string]any{"inline_keyboard": keyboard})
	form.Set("reply_markup", string(markup))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.api("sendMessage"), strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("telegram settings picker: %s", resp.Status)
	}
	return nil
}

func telegramRuntimeSettingsText(state core.RuntimeSettingsPickerState) string {
	scope := "当前会话"
	if state.Scope == core.RuntimeSettingsScopeAgent {
		scope = "Agent 默认（仅后续会话）"
	}
	text := fmt.Sprintf("运行时设置\n范围：%s\n模型：%s\n思考：%s\n速度：%s", scope, telegramDisplay(state.Settings.Model), telegramDisplay(state.Settings.ReasoningEffort), telegramDisplay(state.Settings.ServiceTier))
	if state.Notice != "" {
		text += "\n提示：" + state.Notice
	}
	return text
}

type tgInlineButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

func telegramRuntimeSettingsKeyboard(p *Platform, state core.RuntimeSettingsPickerState) [][]tgInlineButton {
	rows := make([][]tgInlineButton, 0)
	if state.AgentDefaultsEditable {
		rows = append(rows, []tgInlineButton{
			telegramSettingButton(p, "当前会话", core.RuntimeSettingsAction{Scope: core.RuntimeSettingsScopeConversation, Setting: core.RuntimeSettingScope}),
			telegramSettingButton(p, "Agent 默认", core.RuntimeSettingsAction{Scope: core.RuntimeSettingsScopeAgent, Setting: core.RuntimeSettingScope}),
		})
	}
	rows = append(rows, telegramSettingRows(p, state, core.RuntimeSettingModel, state.Capabilities.Models)...)
	rows = append(rows, telegramSettingRows(p, state, core.RuntimeSettingReasoningEffort, state.Capabilities.ReasoningEfforts)...)
	rows = append(rows, telegramSettingRows(p, state, core.RuntimeSettingServiceTier, state.Capabilities.ServiceTiers)...)
	return rows
}

func telegramSettingRows(p *Platform, state core.RuntimeSettingsPickerState, setting core.RuntimeSetting, options []core.RuntimeOption) [][]tgInlineButton {
	if len(options) == 0 {
		return nil
	}
	rows := make([][]tgInlineButton, 0, (len(options)+1)/2)
	selected := state.Settings.Value(setting)
	for i := 0; i < len(options); i += 2 {
		end := i + 2
		if end > len(options) {
			end = len(options)
		}
		row := make([]tgInlineButton, 0, end-i)
		for _, option := range options[i:end] {
			label := option.Label
			if label == "" {
				label = option.Value
			}
			if option.Value == selected {
				label += " 当前"
			}
			row = append(row, telegramSettingButton(p, label, core.RuntimeSettingsAction{Scope: state.Scope, Setting: setting, Value: option.Value}))
		}
		rows = append(rows, row)
	}
	return rows
}

func telegramSettingButton(p *Platform, label string, action core.RuntimeSettingsAction) tgInlineButton {
	return tgInlineButton{Text: label, CallbackData: "rs:" + p.storePickerAction(action)}
}

func (p *Platform) storePickerAction(action core.RuntimeSettingsAction) string {
	var random [6]byte
	_, _ = rand.Read(random[:])
	token := fmt.Sprintf("%x", random[:])
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.pickerActions) > 2048 {
		p.pickerActions = map[string]core.RuntimeSettingsAction{}
	}
	if p.pickerActions == nil {
		p.pickerActions = map[string]core.RuntimeSettingsAction{}
	}
	p.pickerActions[token] = action
	return token
}

func (p *Platform) answerCallback(ctx context.Context, id string) {
	if id == "" {
		return
	}
	form := url.Values{}
	form.Set("callback_query_id", id)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.api("answerCallbackQuery"), strings.NewReader(form.Encode()))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := p.client.Do(req)
	if err == nil && resp != nil {
		_ = resp.Body.Close()
	}
}

func telegramDisplay(value string) string {
	if strings.TrimSpace(value) == "" {
		return "runtime default"
	}
	return value
}

// Stop is a no-op; the poll loop exits on ctx cancellation.
func (p *Platform) Stop(ctx context.Context) error { return nil }

type tgUpdate struct {
	UpdateID      int64            `json:"update_id"`
	CallbackQuery *tgCallbackQuery `json:"callback_query"`
	Message       *struct {
		Text string `json:"text"`
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		From struct {
			ID       int64  `json:"id"`
			Username string `json:"username"`
		} `json:"from"`
	} `json:"message"`
}

type tgCallbackQuery struct {
	ID   string `json:"id"`
	Data string `json:"data"`
	From struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	} `json:"from"`
	Message *struct {
		MessageID int64 `json:"message_id"`
		Chat      struct {
			ID int64 `json:"id"`
		} `json:"chat"`
	} `json:"message"`
}

func (p *Platform) getUpdates(ctx context.Context) ([]tgUpdate, error) {
	form := url.Values{}
	form.Set("timeout", "60")
	form.Set("offset", strconv.FormatInt(p.offset, 10))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		p.api("getUpdates")+"?"+form.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		OK     bool       `json:"ok"`
		Result []tgUpdate `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if !out.OK {
		return nil, fmt.Errorf("telegram getUpdates not ok")
	}
	return out.Result, nil
}
