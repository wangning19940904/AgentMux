// Package platform aggregates all platform adapters. Blank-importing this
// package registers every messaging platform with core's registry.
package platform

import (
	_ "github.com/agentnexus/agentnexus/platform/feishu"
	_ "github.com/agentnexus/agentnexus/platform/telegram"
	_ "github.com/agentnexus/agentnexus/platform/webhook"
)
