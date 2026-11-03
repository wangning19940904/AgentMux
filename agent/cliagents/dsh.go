package cliagents

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/wangning19940904/AgentMux/agent/cliagent"
	"github.com/wangning19940904/AgentMux/core"
)

// registerDSH drives DeepSeek Harness through its one-shot headless profile.
// The `--json` stream is projected from durable Session events, so every text
// frame is committed output and the terminal `final` frame is lossless.
func registerDSH() {
	core.RegisterAgent("dsh", func(cfg map[string]any) (core.Agent, error) {
		return cliagent.New(cliagent.Spec{
			Name: "dsh", Binary: "dsh",
			Args: dshArgs, ResumeArgs: dshResumeArgs,
			SessionIDFromLine: dshSessionID,
			EventMapper:       dshStreamEvents,
			ApprovalModes:     core.ApprovalModeValuesForRuntime("dsh"), DefaultApprovalMode: core.ApprovalModeAutoEdit,
		}, cfg), nil
	})
}

func dshArgs(prompt, systemPrompt, _, approvalMode string) []string {
	// Headless has no system prompt flag, and a resumed Session already carries
	// the instructions from its first turn.
	if systemPrompt = strings.TrimSpace(systemPrompt); systemPrompt != "" {
		prompt = systemPrompt + "\n\n" + prompt
	}
	return append(dshCommonArgs(approvalMode), "--", prompt)
}

func dshResumeArgs(sessionID, prompt, _, _, approvalMode string) []string {
	return append(dshCommonArgs(approvalMode), "--session-id", sessionID, "--", prompt)
}

// dshCommonArgs maps AgentMux approval modes onto DSH permission presets.
// Headless has nobody to answer a prompt, so approvals are always "never": an
// action outside the preset fails instead of blocking the turn.
func dshCommonArgs(approvalMode string) []string {
	preset := "workspace-write"
	switch approvalMode {
	case core.ApprovalModePlan:
		preset = "read-only"
	case core.ApprovalModeYolo:
		preset = "danger-full-access"
	}
	return []string{"--profile", "headless", "--json", "--permission-mode", preset, "--approval-policy", "never"}
}

func dshSessionID(line []byte) string {
	var frame struct {
		Type      string `json:"type"`
		SessionID string `json:"sessionId"`
	}
	if json.Unmarshal(line, &frame) != nil || frame.Type != "session" {
		return ""
	}
	return frame.SessionID
}

func dshStreamEvents(line []byte) []*core.Event {
	var frame struct {
		Type    string          `json:"type"`
		Phase   string          `json:"phase"`
		Text    string          `json:"text"`
		Message string          `json:"message"`
		CallID  string          `json:"callId"`
		Tool    string          `json:"tool"`
		Input   json.RawMessage `json:"input"`
		Status  string          `json:"status"`
		Result  string          `json:"result"`
		Usage   *dshUsage       `json:"usage"`
		Reason  *dshTurnReason  `json:"reason"`
	}
	if json.Unmarshal(line, &frame) != nil {
		return nil
	}
	switch frame.Type {
	case "status":
		switch frame.Phase {
		case "step_start":
			return []*core.Event{{Type: core.EventModelRequest, Status: "in_progress", Metadata: dshMetadata("started")}}
		case "step_end":
			return []*core.Event{{Type: core.EventModelResponse, Status: "completed", Usage: frame.Usage.turnUsage(), Metadata: dshMetadata("completed")}}
		}
		return nil
	case "text":
		if frame.Text == "" {
			return nil
		}
		return []*core.Event{{Type: core.EventOutput, Text: frame.Text, Status: "completed", Metadata: dshMetadata("completed")}}
	case "tool_call":
		input := string(frame.Input)
		return []*core.Event{{
			Type: core.EventToolUse, ToolCallID: frame.CallID, ToolName: frame.Tool, ToolUse: frame.Tool,
			ToolInput: input, ToolInputRaw: input, Status: "in_progress", Metadata: dshMetadata("started"),
		}}
	case "tool_result":
		event := &core.Event{
			Type: core.EventToolUse, ToolCallID: frame.CallID,
			ToolResult: frame.Result, ToolResultRaw: frame.Result, Status: "completed", Metadata: dshMetadata("completed"),
		}
		if frame.Status == "error" {
			event.Err = errors.New("tool failed")
		}
		return []*core.Event{event}
	case "final":
		if message := frame.Reason.failure(); message != "" {
			return []*core.Event{{Type: core.EventError, Err: errors.New(message), Metadata: dshMetadata("failed")}}
		}
		return []*core.Event{{Type: core.EventFinal, Text: frame.Text, Final: true, Metadata: dshMetadata("completed")}}
	case "error":
		message := frame.Message
		if message == "" {
			message = "DeepSeek Harness run failed"
		}
		return []*core.Event{{Type: core.EventError, Err: errors.New(message), Metadata: dshMetadata("failed")}}
	default:
		return nil
	}
}

type dshUsage struct {
	InputTokens      int64 `json:"inputTokens"`
	OutputTokens     int64 `json:"outputTokens"`
	TotalTokens      int64 `json:"totalTokens"`
	CacheReadTokens  int64 `json:"cacheReadTokens"`
	CacheWriteTokens int64 `json:"cacheWriteTokens"`
	ReasoningTokens  int64 `json:"reasoningTokens"`
}

func (u *dshUsage) turnUsage() *core.TurnUsage {
	if u == nil {
		return nil
	}
	usage := &core.TurnUsage{
		InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, TotalTokens: u.TotalTokens,
		CacheReadTokens: u.CacheReadTokens, CacheWriteTokens: u.CacheWriteTokens, ReasoningTokens: u.ReasoningTokens,
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	}
	return usage
}

type dshTurnReason struct {
	Kind  string `json:"kind"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// failure describes a terminal turn that did not complete. A missing reason
// means the Session recorded no terminal turn, which is also not a success.
func (r *dshTurnReason) failure() string {
	switch {
	case r == nil:
		return "DeepSeek Harness run ended without a terminal turn"
	case r.Kind == "completed":
		return ""
	case r.Error != nil && r.Error.Message != "":
		return r.Error.Message
	default:
		return "DeepSeek Harness turn ended: " + r.Kind
	}
}

func dshMetadata(lifecycle string) map[string]string {
	return map[string]string{
		"runtime": "dsh", "transport": "dsh-headless-json",
		"coverage": "native", "lifecycle": lifecycle,
	}
}
