package core

import (
	"context"
	"fmt"
	"strings"
	"time"

	toolpkg "github.com/wangning19940904/AgentMux/tools"
)

const channelCLIAuthProgressTimeout = 5 * time.Second

var (
	checkChannelCLIAuth  = toolpkg.CheckCLIAuth
	startChannelCLIAuth  = toolpkg.StartCLIAuth
	getChannelCLIAuth    = toolpkg.GetCLIAuthSession
	cancelChannelCLIAuth = toolpkg.CancelCLIAuthSession
)

type channelCLIAuthPending struct {
	CLIID        string
	SessionID    string
	ControllerID string
	Phase        string
	LoginURL     string
}

// handleChannelCLIAuth takes known device/browser login workflows out of the
// model's shell loop. The daemon owns the bounded subprocess, while the
// channel turn ends as soon as an actionable URL or terminal state is known.
func (e *Engine) handleChannelCLIAuth(ctx context.Context, rt *channelRuntime, msg *Message, data map[string]string) bool {
	if rt == nil || msg == nil || !isFeishuLikeChannel(rt.channel.Type) {
		return false
	}
	key := ResolveConversationKey(msg)
	pending, hasPending := rt.channelCLIAuthPending(key)
	text := strings.TrimSpace(msg.Text)

	if hasPending && isChannelCLIAuthStop(text) {
		if pending.ControllerID != "" && pending.ControllerID != msg.UserID && !rt.isAdmin(msg.UserID) {
			_ = rt.platform.Reply(ctx, msg, "只有认证发起人或渠道管理员可以停止当前认证。")
		} else {
			_ = cancelChannelCLIAuth(pending.SessionID)
			rt.clearChannelCLIAuthPending(key, pending.SessionID)
			_ = rt.platform.Reply(ctx, msg, "已停止当前 CLI 认证流程。")
		}
		e.emit(ctx, HookMessageSent, data)
		return true
	}

	if hasPending && isChannelCLIAuthCompletion(text) {
		if pending.ControllerID != "" && pending.ControllerID != msg.UserID && !rt.isAdmin(msg.UserID) {
			_ = rt.platform.Reply(ctx, msg, "只有认证发起人或渠道管理员可以继续当前认证。")
			e.emit(ctx, HookMessageSent, data)
			return true
		}
		e.continueChannelCLIAuth(ctx, rt, msg, data, key, pending)
		return true
	}

	cliID, ok := channelCLIAuthRequest(text)
	if !ok {
		return false
	}
	if msg.ChatType != "" && msg.ChatType != "p2p" {
		_ = rt.platform.Reply(ctx, msg, "CLI 登录链接只能在私聊中获取，请私聊机器人后重试。")
		e.emit(ctx, HookMessageSent, data)
		return true
	}

	snapshot, err := startChannelCLIAuth(cliID, false)
	if err != nil {
		_ = rt.platform.Reply(ctx, msg, "启动 CLI 认证失败："+err.Error())
		e.emit(ctx, HookMessageSent, data)
		return true
	}
	e.replyChannelCLIAuthSnapshot(ctx, rt, msg, data, key, msg.UserID, snapshot)
	return true
}

func (e *Engine) continueChannelCLIAuth(
	ctx context.Context,
	rt *channelRuntime,
	msg *Message,
	data map[string]string,
	key string,
	pending channelCLIAuthPending,
) {
	status := checkChannelCLIAuth(ctx, pending.CLIID)
	if status.State == toolpkg.CLIAuthAuthenticated {
		rt.clearChannelCLIAuthPending(key, pending.SessionID)
		_ = rt.platform.Reply(ctx, msg, "CLI 认证已完成，可以继续使用。")
		e.emit(ctx, HookMessageSent, data)
		return
	}

	snapshot, ok := getChannelCLIAuth(pending.SessionID)
	if !ok {
		rt.clearChannelCLIAuthPending(key, pending.SessionID)
		_ = rt.platform.Reply(ctx, msg, "认证流程已过期。请重新发送初始化或登录请求，我会生成新的链接。")
		e.emit(ctx, HookMessageSent, data)
		return
	}
	if snapshot.State == toolpkg.CLIAuthSessionWaiting && snapshot.Phase == pending.Phase && snapshot.LoginURL == pending.LoginURL {
		snapshot = waitForChannelCLIAuthProgress(ctx, pending, snapshot)
	}
	e.replyChannelCLIAuthSnapshot(ctx, rt, msg, data, key, pending.ControllerID, snapshot)
}

func waitForChannelCLIAuthProgress(ctx context.Context, pending channelCLIAuthPending, current toolpkg.CLIAuthSession) toolpkg.CLIAuthSession {
	timer := time.NewTimer(channelCLIAuthProgressTimeout)
	defer timer.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return current
		case <-timer.C:
			return current
		case <-ticker.C:
			next, ok := getChannelCLIAuth(pending.SessionID)
			if !ok {
				return current
			}
			current = next
			if next.State != toolpkg.CLIAuthSessionWaiting || next.Phase != pending.Phase || next.LoginURL != pending.LoginURL {
				return next
			}
		}
	}
}

func (e *Engine) replyChannelCLIAuthSnapshot(
	ctx context.Context,
	rt *channelRuntime,
	msg *Message,
	data map[string]string,
	key string,
	controllerID string,
	snapshot toolpkg.CLIAuthSession,
) {
	text, terminal := channelCLIAuthReply(snapshot)
	if terminal {
		rt.clearChannelCLIAuthPending(key, snapshot.SessionID)
	} else {
		rt.setChannelCLIAuthPending(key, channelCLIAuthPending{
			CLIID: snapshot.ID, SessionID: snapshot.SessionID, ControllerID: controllerID,
			Phase: snapshot.Phase, LoginURL: snapshot.LoginURL,
		})
	}
	_ = rt.platform.Reply(ctx, msg, text)
	e.emit(ctx, HookMessageSent, data)
}

func channelCLIAuthReply(snapshot toolpkg.CLIAuthSession) (string, bool) {
	switch snapshot.State {
	case toolpkg.CLIAuthSessionSucceeded:
		return "CLI 认证已完成，可以继续使用。", true
	case toolpkg.CLIAuthSessionFailed:
		message := strings.TrimSpace(snapshot.Error)
		if message == "" {
			message = "认证流程失败"
		}
		return "CLI 认证失败：" + message + "。请重新发送初始化或登录请求后重试。", true
	case toolpkg.CLIAuthSessionCancelled:
		return "CLI 认证已停止。", true
	}

	if strings.TrimSpace(snapshot.LoginURL) == "" {
		return "认证流程正在切换阶段，当前没有新的链接。请稍后回复“已完成”再次检查；本轮不会阻塞等待。", false
	}
	phase := "登录"
	if snapshot.Phase == "setup" {
		phase = "初始化配置"
	}
	text := fmt.Sprintf("请打开以下链接完成 CLI %s：\n\n%s", phase, snapshot.LoginURL)
	if code := strings.TrimSpace(snapshot.VerificationCode); code != "" {
		text += "\n\n验证码：" + code
	}
	text += "\n\n完成后回复“已完成”。本轮已经结束，AgentMux 不会在后台占用当前对话。"
	return text, false
}

func channelCLIAuthRequest(text string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(text))
	compact := strings.NewReplacer(" ", "", "-", "", "_", "").Replace(normalized)
	if !strings.Contains(compact, "larkcli") && !strings.Contains(compact, "飞书cli") {
		return "", false
	}
	for _, marker := range []string{"初始化", "登录", "登陆", "认证", "授权", "配置", "init", "login", "auth", "setup", "二维码"} {
		if strings.Contains(normalized, marker) {
			return "lark-cli", true
		}
	}
	return "", false
}

func isChannelCLIAuthCompletion(text string) bool {
	normalized := strings.ToLower(strings.TrimSpace(text))
	switch normalized {
	case "已完成", "完成", "完成了", "我已完成", "好了", "搞定", "done", "completed":
		return true
	default:
		return false
	}
}

func isChannelCLIAuthStop(text string) bool {
	normalized := strings.ToLower(strings.TrimSpace(text))
	return normalized == "/stop" || normalized == "停止"
}

func (rt *channelRuntime) channelCLIAuthPending(key string) (channelCLIAuthPending, bool) {
	rt.controlMu.Lock()
	defer rt.controlMu.Unlock()
	pending, ok := rt.cliAuth[key]
	return pending, ok
}

func (rt *channelRuntime) setChannelCLIAuthPending(key string, pending channelCLIAuthPending) {
	rt.controlMu.Lock()
	defer rt.controlMu.Unlock()
	if rt.cliAuth == nil {
		rt.cliAuth = map[string]channelCLIAuthPending{}
	}
	rt.cliAuth[key] = pending
}

func (rt *channelRuntime) clearChannelCLIAuthPending(key, sessionID string) {
	rt.controlMu.Lock()
	defer rt.controlMu.Unlock()
	if pending, ok := rt.cliAuth[key]; ok && (sessionID == "" || pending.SessionID == sessionID) {
		delete(rt.cliAuth, key)
	}
}
