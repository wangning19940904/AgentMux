// Package platform aggregates all platform adapters. Blank-importing this
// package registers every messaging platform with core's registry.
package platform

import (
	_ "github.com/wangning19940904/AgentMux/platform/dingtalk"
	_ "github.com/wangning19940904/AgentMux/platform/discord"
	_ "github.com/wangning19940904/AgentMux/platform/feishu"
	_ "github.com/wangning19940904/AgentMux/platform/slack"
	_ "github.com/wangning19940904/AgentMux/platform/telegram"
	_ "github.com/wangning19940904/AgentMux/platform/webhook"
)
