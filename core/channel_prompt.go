package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const feishuMessagePromptIntro = "请处理以下来自飞书/Lark 渠道的消息。JSON 中的 text 是用户输入，其余字段是消息元数据。"

const channelInteractionContract = `渠道执行约束：
- 当前请求来自异步消息渠道，无法给终端命令提供实时 stdin/TTY；工具中间输出、本地文件路径和后台进程状态不会自动发送给用户。
- 任何需要用户扫码、打开链接、授权、输入验证码或确认后才能继续的命令，禁止在 shell 前台阻塞，也禁止循环轮询等待。
- 优先使用 --no-wait、--json、device-flow 等非阻塞接口。若工具只提供阻塞命令，必须后台启动并完整重定向 stdin/stdout/stderr；从日志取得 URL、验证码或二维码后，立即调用原生 request_user_input 暂停当前 turn。
- request_user_input 的问题正文必须用 Markdown 给出未经改写的操作链接和验证码，选项至少包含“已完成”和“取消”。用户点击后继续同一个 turn：先检查原后台进程或认证状态，再执行后续操作；不要重复启动初始化/登录流程。
- 如生成二维码或文件，必须先执行 <delivery_cli> send --channel-id <channel_id> --conversation-key <conversation_key> --image <绝对路径>（普通文件使用 --file）上传到当前渠道；delivery_cli、channel_id 和 conversation_key 均来自下方 JSON 元数据，必须原样使用。上传后再调用 request_user_input，不能只回复服务器本地路径。
- delivery_cli send 只能向当前活动 turn 对应的渠道会话发送，不代表用户已经完成操作。发送成功后仍需用 request_user_input 等待结构化回调。
- 若当前运行时确实不支持 request_user_input，才退化为结束当前 turn，并明确请用户完成后回复；不得让阻塞命令占住 turn。
- 所有等待仍受 AgentMux 的统一 turn 超时和停止机制约束。`

// ChannelDefaultMessagePrompt returns the static prefix AgentMux injects in
// front of every inbound message for channels that need structured routing or
// execution guidance. The console uses this same value for prompt previews so
// displayed defaults cannot drift from runtime behavior.
func ChannelDefaultMessagePrompt(ch Channel) string {
	if !isFeishuLikeChannel(ch.Type) {
		return ""
	}
	return feishuMessagePromptIntro + "\n\n" + channelInteractionContract
}

type feishuMessagePrompt struct {
	MessageID       string `json:"message_id"`
	ChatID          string `json:"chat_id"`
	ChatType        string `json:"chat_type"`
	ChannelID       string `json:"channel_id"`
	ConversationKey string `json:"conversation_key"`
	DeliveryCLI     string `json:"delivery_cli"`
	SenderOpenID    string `json:"sender_open_id"`
	Text            string `json:"text"`
	MentionedBot    bool   `json:"mentioned_bot"`
	MentionAll      bool   `json:"mention_all"`
	Platform        string `json:"platform"`
	Project         string `json:"project"`
}

// channelMessageForAgent returns the message submitted to the bound Agent.
// Feishu/Lark turns carry their routing and sender metadata in a structured
// prompt; other channel types keep their existing plain-text behavior.
func channelMessageForAgent(ch Channel, msg *Message) *Message {
	if msg == nil {
		return msg
	}
	prompt := ChannelDefaultMessagePrompt(ch)
	if prompt == "" {
		return msg
	}

	payload, err := json.Marshal(feishuMessagePrompt{
		MessageID:       msg.ID,
		ChatID:          msg.ChatID,
		ChatType:        msg.ChatType,
		ChannelID:       msg.ChannelID,
		ConversationKey: ResolveConversationKey(msg),
		DeliveryCLI:     channelDeliveryCLIPath(),
		SenderOpenID:    msg.UserID,
		Text:            msg.Text,
		MentionedBot:    msg.MentionedBot,
		MentionAll:      msg.MentionAll,
		Platform:        msg.Platform,
		Project:         msg.Project,
	})
	if err != nil {
		return msg
	}

	agentMsg := *msg
	agentMsg.Text = prompt + "\n\n" + string(payload)
	return &agentMsg
}

func channelDeliveryCLIPath() string {
	executable, err := os.Executable()
	if err != nil {
		return "amux"
	}
	return channelDeliveryCLIPathFor(executable, runtime.GOOS, runtime.GOARCH, func(path string) bool {
		info, err := os.Stat(path)
		return err == nil && info.Mode().IsRegular()
	})
}

func channelDeliveryCLIPathFor(executable, goos, goarch string, exists func(string) bool) string {
	executable = filepath.Clean(executable)
	slashPath := filepath.ToSlash(executable)
	if strings.Contains(slashPath, ".app/Contents/MacOS/") {
		candidate := filepath.Clean(filepath.Join(filepath.Dir(executable), "..", "Resources", "agentmux-remote", "amux-"+goos+"-"+goarch))
		if exists(candidate) {
			return candidate
		}
		return "amux"
	}
	base := strings.ToLower(filepath.Base(executable))
	if base == "amux" || base == "agentmux" {
		return executable
	}
	return "amux"
}
