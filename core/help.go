package core

import (
	"context"
	"fmt"
	"strings"
)

// HelpCommand describes one command shown in the transport-neutral help card.
// Actionable commands can be rendered as buttons by interactive platforms.
type HelpCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
	Actionable  bool   `json:"actionable,omitempty"`
}

// HelpCardState contains the Agent introduction and commands rendered by a
// channel. Platforms without interactive-card support receive its text form.
type HelpCardState struct {
	AgentName    string        `json:"agent_name"`
	RuntimeName  string        `json:"runtime_name,omitempty"`
	Introduction string        `json:"introduction"`
	Commands     []HelpCommand `json:"commands"`
}

func isHelpCommand(text string) bool {
	return strings.EqualFold(strings.TrimSpace(text), "/help")
}

// IsHelpCommandAction validates commands accepted from help-card buttons.
// Keeping the allowlist in core prevents a forged callback from becoming an
// arbitrary prompt sent to an Agent.
func IsHelpCommandAction(command string) bool {
	switch strings.ToLower(strings.TrimSpace(command)) {
	case "/mode", "/queue", "/model", "/clear", "/effort", "/fast", "/approval", "/status", "/stop", "/sessions", "/open", "/takeover":
		return true
	default:
		return false
	}
}

func buildHelpCardState(agentName, runtimeName string, remoteControl bool) HelpCardState {
	agentName = strings.TrimSpace(agentName)
	runtimeName = strings.TrimSpace(runtimeName)
	if agentName == "" {
		agentName = runtimeName
	}
	if agentName == "" {
		agentName = "AgentMux Agent"
	}

	introduction := fmt.Sprintf("你好，我是 %s，一个通过 AgentMux 与你协作的 AI Agent。你可以直接发送任务，也可以使用下面的命令管理当前会话。", agentName)
	commands := []HelpCommand{
		{Command: "/mode", Description: "切换当前私聊或群聊的会话模式", Actionable: true},
		{Command: "/help", Description: "查看 Agent 介绍和命令帮助"},
		{Command: "/model", Description: "查看或切换当前会话的模型", Actionable: true},
		{Command: "/clear", Description: "清除上下文并开始新会话（同 /new、/reset）", Actionable: true},
		{Command: "/effort", Description: "查看或切换思考强度", Actionable: true},
		{Command: "/fast", Description: "查看或切换快速模式", Actionable: true},
		{Command: "/approval", Description: "查看或切换审批模式", Actionable: true},
	}
	if remoteControl {
		commands = append(commands,
			HelpCommand{Command: "/status", Description: "查看当前任务状态", Actionable: true},
			HelpCommand{Command: "/stop", Description: "停止当前任务", Actionable: true},
			HelpCommand{Command: "/queue", Description: "查看等待队列", Actionable: true},
			HelpCommand{Command: "/steer <内容>", Description: "立即调整当前任务方向"},
			HelpCommand{Command: "/queue cancel <任务ID>", Description: "取消一项等待任务"},
			HelpCommand{Command: "/queue <内容>", Description: "将任务加入执行队列"},
			HelpCommand{Command: "/queue clear", Description: "清空排队任务（需要确认）"},
			HelpCommand{Command: "/sessions", Description: "列出当前目录的 Codex threads", Actionable: true},
			HelpCommand{Command: "/bind <序号或 thread_id>", Description: "绑定一个 Codex thread"},
			HelpCommand{Command: "/open", Description: "在 Codex 中打开当前 thread", Actionable: true},
			HelpCommand{Command: "/takeover", Description: "接管当前任务", Actionable: true},
		)
	}
	return HelpCardState{
		AgentName: agentName, RuntimeName: runtimeName,
		Introduction: introduction, Commands: commands,
	}
}

func formatHelpText(state HelpCardState) string {
	lines := []string{state.Introduction}
	if state.RuntimeName != "" {
		lines = append(lines, "当前运行时："+state.RuntimeName)
	}
	lines = append(lines, "", "支持的命令：")
	for _, command := range state.Commands {
		lines = append(lines, command.Command+" — "+command.Description)
	}
	return strings.Join(lines, "\n")
}

func (e *Engine) handleChannelHelpCommand(ctx context.Context, rt *channelRuntime, msg *Message) bool {
	if rt == nil || msg == nil || !isHelpCommand(msg.Text) {
		return false
	}
	currentAgent, _, workspace := rt.agentSnapshot()
	agentName := workspace.AgentName
	runtimeName := workspace.RuntimeID
	if currentAgent != nil {
		if agentName == "" {
			agentName = currentAgent.Name()
		}
		if runtimeName == "" {
			runtimeName = currentAgent.Name()
		}
	}
	state := buildHelpCardState(agentName, runtimeName, rt.remoteControlEnabled())
	if replier, ok := rt.platform.(HelpCardReplier); ok {
		if err := replier.ReplyHelpCard(ctx, msg, state); err == nil {
			return true
		} else {
			e.log.Warn("channel help card reply", "channel", rt.channel.Name, "err", err)
		}
	}
	if err := rt.platform.Reply(ctx, msg, formatHelpText(state)); err != nil {
		e.log.Error("channel help reply", "channel", rt.channel.Name, "err", err)
	}
	return true
}

func (e *Engine) handleProjectHelpCommand(ctx context.Context, pr *projectRuntime, msg *Message) bool {
	if pr == nil || msg == nil || !isHelpCommand(msg.Text) {
		return false
	}
	agentName := pr.workspace.AgentName
	if agentName == "" {
		agentName = pr.name
	}
	runtimeName := pr.workspace.RuntimeID
	if runtimeName == "" && pr.agent != nil {
		runtimeName = pr.agent.Name()
	}
	state := buildHelpCardState(agentName, runtimeName, false)
	for _, platform := range pr.platforms {
		if platform.Name() != msg.Platform {
			continue
		}
		if replier, ok := platform.(HelpCardReplier); ok {
			if err := replier.ReplyHelpCard(ctx, msg, state); err == nil {
				return true
			} else {
				e.log.Warn("project help card reply", "project", pr.name, "err", err)
			}
		}
		if err := platform.Reply(ctx, msg, formatHelpText(state)); err != nil {
			e.log.Error("project help reply", "project", pr.name, "err", err)
		}
		return true
	}
	return true
}
