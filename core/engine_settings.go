package core

import (
	"context"
	"fmt"
	"strings"
)

func (e *Engine) handleRuntimeSettingsCommand(sess AgentSession, text string, reply func(string), picker func(RuntimeSettingsPickerState) bool, legacyPicker func(ModelPickerState) bool) bool {
	cmd, ok := parseRuntimeSettingsCommand(text)
	if !ok {
		return false
	}
	if reply == nil {
		reply = func(string) {}
	}
	settings, ok := RuntimeSettingsForSession(sess)
	if !ok {
		reply("This runtime does not support runtime settings.")
		return true
	}
	if cmd.List {
		if picker != nil && picker(runtimeSettingsPickerState(settings, RuntimeSettingsScopeConversation, RuntimeSettings{}, false)) {
			return true
		}
		if cmd.Setting == RuntimeSettingModel && legacyPicker != nil {
			if models, ok := sess.(ModelSwitchingSession); ok && models.ModelSwitchingSupported() && legacyPicker(modelPickerState(models)) {
				return true
			}
		}
		reply(formatRuntimeSettingsStatus(settings))
		return true
	}
	var err error
	if cmd.Reset {
		err = settings.ResetRuntimeSetting(cmd.Setting)
	} else {
		err = settings.SetRuntimeSetting(cmd.Setting, cmd.Value)
	}
	if err != nil {
		if cmd.Setting == RuntimeSettingApprovalMode {
			reply(formatApprovalModeCommandError(err, settings))
		} else {
			reply(err.Error())
		}
		return true
	}
	// The command response itself is the refreshed menu/status; interactive
	// pickers use handleRuntimeSettingsAction and edit their existing message.
	if cmd.Setting == RuntimeSettingApprovalMode {
		reply(formatApprovalModeCommandResult(settings, cmd.Reset))
	} else {
		reply(formatRuntimeSettingsStatus(settings))
	}
	return true
}

func (e *Engine) handleRuntimeSettingsAction(ctx context.Context, sess AgentSession, msg *Message, agentDefaults *RuntimeSettings, agentID string, update func(RuntimeSettingsPickerState) bool, reply func(string)) bool {
	if msg == nil || msg.RuntimeSettingsAction == nil {
		return false
	}
	settings, ok := RuntimeSettingsForSession(sess)
	if !ok {
		if reply != nil {
			reply("This runtime does not support runtime settings.")
		}
		return true
	}
	action := *msg.RuntimeSettingsAction
	if action.Scope == "" {
		action.Scope = RuntimeSettingsScopeConversation
	}
	agentEditable := agentDefaults != nil && agentID != "" && !strings.HasPrefix(agentID, "config:") && e.runtimeSettingsDefaults != nil
	var err error
	if action.Scope == RuntimeSettingsScopeAgent {
		if !agentEditable {
			err = fmt.Errorf("Agent defaults are not editable for this channel")
		} else {
			candidate := *agentDefaults
			err = applyRuntimeSettingsAction(settings, action, &candidate)
			if err == nil {
				err = e.runtimeSettingsDefaults.UpdateAgentRuntimeSettings(ctx, agentID, candidate)
			}
			if err == nil {
				*agentDefaults = candidate
			}
		}
	} else {
		err = applyRuntimeSettingsAction(settings, action, nil)
	}
	defaults := RuntimeSettings{}
	if agentDefaults != nil {
		defaults = *agentDefaults
	}
	state := runtimeSettingsPickerState(settings, action.Scope, defaults, agentEditable)
	if err != nil {
		state.Notice = err.Error()
	} else if action.Setting == RuntimeSettingScope {
		if action.Scope == RuntimeSettingsScopeAgent {
			state.Hint = "正在编辑 Agent 默认；后续修改仅对新会话生效。"
		} else {
			state.Hint = "正在编辑当前会话；后续修改立即生效。"
		}
	} else if action.Scope == RuntimeSettingsScopeAgent {
		state.Hint = "已更新 Agent 默认，仅新会话生效；当前会话未改变。"
	} else if action.Setting == RuntimeSettingApprovalMode && action.Value == ApprovalModeYolo {
		state.Hint = "已对当前会话启用 YOLO；下一条消息将直接使用运行时最高权限。"
	} else if action.Setting == RuntimeSettingApprovalMode && action.Value == ApprovalModeManual {
		if _, interactive := settings.(InteractiveAgentSession); !interactive {
			state.Hint = "已切换为手动模式；当前运行时不能在渠道中逐次审批，工具请求会被拦截。"
		} else {
			state.Hint = "已切换为手动审批；运行时请求权限时会发送审批卡片。"
		}
	} else {
		state.Hint = "已应用到当前会话。"
	}
	if update != nil && update(state) {
		return true
	}
	if reply != nil {
		if err != nil {
			reply(err.Error())
		} else {
			reply(formatRuntimeSettingsStatus(settings))
		}
	}
	return true
}

func modelPickerState(models ModelSwitchingSession) ModelPickerState {
	current := models.CurrentModel()
	def := models.DefaultModel()
	supported := models.SupportedModels()
	options := make([]ModelPickerOption, 0, len(supported))
	for _, model := range supported {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		options = append(options, ModelPickerOption{
			Model:   model,
			Current: model == current,
			Default: model == def,
		})
	}
	return ModelPickerState{
		CurrentModel: current,
		DefaultModel: def,
		Options:      options,
	}
}

func (e *Engine) replyModelPicker(ctx context.Context, pr *projectRuntime, msg *Message, state ModelPickerState) bool {
	for _, p := range pr.platforms {
		if p.Name() != msg.Platform {
			continue
		}
		mp, ok := p.(ModelPickerReplier)
		if !ok {
			return false
		}
		if err := mp.ReplyModelPicker(ctx, msg, state); err != nil {
			e.log.Error("reply model picker", "platform", p.Name(), "err", err)
			return false
		}
		return true
	}
	return false
}

func (e *Engine) replyRuntimeSettingsPicker(ctx context.Context, pr *projectRuntime, msg *Message, state RuntimeSettingsPickerState) bool {
	for _, p := range pr.platforms {
		if p.Name() != msg.Platform {
			continue
		}
		picker, ok := p.(RuntimeSettingsPickerReplier)
		if !ok {
			return false
		}
		if err := picker.ReplyRuntimeSettingsPicker(ctx, msg, state); err != nil {
			e.log.Error("reply runtime settings picker", "platform", p.Name(), "err", err)
			return false
		}
		return true
	}
	return false
}

func (e *Engine) updateRuntimeSettingsPicker(ctx context.Context, pr *projectRuntime, msg *Message, state RuntimeSettingsPickerState) bool {
	for _, p := range pr.platforms {
		if p.Name() != msg.Platform {
			continue
		}
		picker, ok := p.(RuntimeSettingsPickerReplier)
		if !ok {
			return false
		}
		if err := picker.UpdateRuntimeSettingsPicker(ctx, msg, state); err != nil {
			e.log.Error("update runtime settings picker", "platform", p.Name(), "err", err)
			return false
		}
		return true
	}
	return false
}
