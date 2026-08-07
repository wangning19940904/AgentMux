// Package cliagents registers all subprocess-based coding-agent adapters built
// on top of cliagent. Each agent's invocation mirrors the documented
// non-interactive mode (from cc-connect's INSTALL.md), e.g.:
//
//	codex   exec --json
//	cursor  agent --print --output-format stream-json --trust
//	gemini  -p --output-format stream-json
//	qoder   -p -f stream-json
//	opencode run --format json
//	iflow   -i -r -o
package cliagents

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/wangning19940904/AgentMux/agent/cliagent"
	"github.com/wangning19940904/AgentMux/core"
)

func register(name, binary string, supportsModel bool, defaultApproval string,
	args func(prompt, sys, model, approvalMode string) []string, approvalEnv func(string) map[string]string,
	mapper cliagent.LineMapper, finalLast bool) {
	core.RegisterAgent(name, func(cfg map[string]any) (core.Agent, error) {
		return cliagent.New(cliagent.Spec{
			Name: name, Binary: binary, Args: args,
			Mapper: mapper, FinalFromLast: finalLast, SupportsModel: supportsModel,
			ApprovalModes: core.ApprovalModeValuesForRuntime(name), DefaultApprovalMode: defaultApproval,
			ApprovalEnv: approvalEnv,
		}, cfg), nil
	})
}

func init() {
	// Codex uses its native app-server protocol. Unlike `codex exec --json`,
	// that protocol provides agent-message deltas, reasoning summaries, tools,
	// and the signed-in account's live model catalog.
	registerCodex()

	// Cursor Agent: stream-json.
	registerCursor()

	// Gemini CLI: stream-json.
	register("gemini", "gemini", false, core.ApprovalModeManual, geminiArgs, nil, jsonTextMapper, true)

	// Qoder CLI.
	register("qoder", "qodercli", false, core.ApprovalModeManual, qoderArgs, nil, jsonTextMapper, true)

	// OpenCode.
	register("opencode", "opencode", false, core.ApprovalModeManual, func(p, _, _, _ string) []string {
		return []string{"run", "--format", "json", p}
	}, opencodeApprovalEnv, jsonTextMapper, true)

	// iFlow CLI.
	register("iflow", "iflow", false, core.ApprovalModeManual, iflowArgs, iflowApprovalEnv, partialPlainTextMapper, true)

	// Kimi CLI.
	register("kimi", "kimi", false, core.ApprovalModeAuto, func(p, _, _, _ string) []string {
		return []string{"-p", p, "--output-format", "stream-json"}
	}, nil, partialPlainTextMapper, true)
}

func registerCursor() {
	core.RegisterAgent("cursor", func(cfg map[string]any) (core.Agent, error) {
		return cliagent.New(cliagent.Spec{
			Name: "cursor", Binary: "cursor-agent", Args: cursorArgs,
			EventMapper: cursorStreamEvents, NewStderrMapper: newCursorStderrMapper,
			FinalFromLast: true, SupportsModel: true,
			ApprovalModes: core.ApprovalModeValuesForRuntime("cursor"), DefaultApprovalMode: core.ApprovalModeManual,
			ModelCatalogArgs: []string{"--list-models"}, ParseModelCatalog: parseCursorModelCatalog,
			ModelForSettings: cursorModelForSettings,
			ReasoningEfforts: []string{"low", "medium", "high", "xhigh"},
			ServiceTiers:     []string{"default", "priority"},
		}, cfg), nil
	})
}

func cursorArgs(prompt, _, model, approvalMode string) []string {
	// AgentMux runs Cursor non-interactively, so it cannot answer Cursor's
	// workspace trust prompt. Trust the isolated per-conversation workspace
	// explicitly; this only skips that prompt and does not auto-approve tools.
	args := []string{"agent", "--print", "--output-format", "stream-json", "--trust"}
	if model != "" {
		args = append(args, "--model", model)
	}
	switch approvalMode {
	case core.ApprovalModeAuto:
		args = append(args, "--auto-review")
	case core.ApprovalModePlan:
		args = append(args, "--plan")
	case core.ApprovalModeYolo:
		args = append(args, "--yolo")
	}
	return append(args, prompt)
}

var cursorANSISequence = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)

func parseCursorModelCatalog(output []byte) (cliagent.ModelCatalog, error) {
	text := cursorANSISequence.ReplaceAllString(string(output), "")
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	catalog := cliagent.ModelCatalog{}
	inModels := false
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "Available models" {
			inModels = true
			continue
		}
		if strings.Contains(line, "No models available for this account") {
			return cliagent.ModelCatalog{}, nil
		}
		if !inModels || line == "" {
			continue
		}
		if strings.HasPrefix(line, "Tip:") {
			break
		}

		isDefault := false
		if statusStart := strings.LastIndex(line, " ("); statusStart >= 0 && strings.HasSuffix(line, ")") {
			status := strings.ToLower(line[statusStart+2 : len(line)-1])
			for _, value := range strings.Split(status, ",") {
				if strings.TrimSpace(value) == "default" {
					isDefault = true
				}
			}
			line = strings.TrimSpace(line[:statusStart])
		}
		if separator := strings.Index(line, " - "); separator >= 0 {
			line = strings.TrimSpace(line[:separator])
		}
		if line == "" {
			continue
		}
		catalog.Models = append(catalog.Models, line)
		if isDefault {
			catalog.DefaultModel = line
		}
	}
	if !inModels {
		return cliagent.ModelCatalog{}, fmt.Errorf("Cursor model list did not contain an Available models section")
	}
	if len(catalog.Models) == 0 {
		return cliagent.ModelCatalog{}, fmt.Errorf("Cursor model list did not contain any model IDs")
	}
	return catalog, nil
}

func cursorModelForSettings(settings core.RuntimeSettings) string {
	base, parameters := splitCursorModelParameters(settings.Model)
	if base == "" {
		return ""
	}
	if effort := strings.TrimSpace(settings.ReasoningEffort); effort != "" {
		parameters = setCursorModelParameter(parameters, []string{"effort", "reasoning"}, "effort", effort)
	}
	if fast, ok := cursorFastParameter(settings.ServiceTier); ok {
		parameters = setCursorModelParameter(parameters, []string{"fast"}, "fast", fast)
	}
	if len(parameters) == 0 {
		return base
	}
	return base + "[" + strings.Join(parameters, ",") + "]"
}

func splitCursorModelParameters(model string) (string, []string) {
	model = strings.TrimSpace(model)
	if !strings.HasSuffix(model, "]") {
		return model, nil
	}
	open := strings.LastIndex(model, "[")
	if open <= 0 {
		return model, nil
	}
	base := strings.TrimSpace(model[:open])
	if base == "" {
		return model, nil
	}
	var parameters []string
	for _, parameter := range strings.Split(model[open+1:len(model)-1], ",") {
		if parameter = strings.TrimSpace(parameter); parameter != "" {
			parameters = append(parameters, parameter)
		}
	}
	return base, parameters
}

func setCursorModelParameter(parameters, aliases []string, key, value string) []string {
	aliasSet := map[string]bool{}
	for _, alias := range aliases {
		aliasSet[strings.ToLower(alias)] = true
	}
	replacement := key + "=" + strings.TrimSpace(value)
	replaced := false
	out := make([]string, 0, len(parameters)+1)
	for _, parameter := range parameters {
		parameterKey, _, ok := strings.Cut(parameter, "=")
		if !ok || !aliasSet[strings.ToLower(strings.TrimSpace(parameterKey))] {
			out = append(out, parameter)
			continue
		}
		if !replaced {
			out = append(out, replacement)
			replaced = true
		}
	}
	if !replaced {
		out = append(out, replacement)
	}
	return out
}

func cursorFastParameter(serviceTier string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(serviceTier)) {
	case "priority", "fast":
		return "true", true
	case "default", "normal", "standard", "flex":
		return "false", true
	default:
		return "", false
	}
}

func geminiArgs(prompt, _, _, approvalMode string) []string {
	mode := map[string]string{
		core.ApprovalModeManual: "default", core.ApprovalModeAutoEdit: "auto_edit",
		core.ApprovalModePlan: "plan", core.ApprovalModeYolo: "yolo",
	}[approvalMode]
	args := []string{"-p", prompt, "--output-format", "stream-json"}
	if mode != "" {
		args = append(args, "--approval-mode", mode)
	}
	return args
}

func qoderArgs(prompt, _, _, approvalMode string) []string {
	mode := map[string]string{
		core.ApprovalModeManual: "default", core.ApprovalModeAutoEdit: "accept_edits",
		core.ApprovalModeAuto: "auto", core.ApprovalModePlan: "plan", core.ApprovalModeYolo: "bypass_permissions",
	}[approvalMode]
	args := []string{"-p", prompt, "-f", "stream-json"}
	if mode != "" {
		args = append(args, "--permission-mode", mode)
	}
	return args
}

func iflowArgs(prompt, _, _, _ string) []string {
	return []string{"-i", "-r", "-o", prompt}
}

func iflowApprovalEnv(approvalMode string) map[string]string {
	mode := map[string]string{
		core.ApprovalModeManual: "default", core.ApprovalModeAutoEdit: "autoEdit",
		core.ApprovalModePlan: "plan", core.ApprovalModeYolo: "yolo",
	}[approvalMode]
	if mode == "" {
		return nil
	}
	// iFlow documents approvalMode as a settings key and supports every
	// settings key through the IFLOW_ prefixed environment form.
	return map[string]string{"IFLOW_approvalMode": mode}
}

func opencodeApprovalEnv(approvalMode string) map[string]string {
	var permission any
	switch approvalMode {
	case core.ApprovalModeManual:
		permission = "ask"
	case core.ApprovalModeAutoEdit:
		permission = map[string]string{"*": "ask", "read": "allow", "glob": "allow", "grep": "allow", "list": "allow", "edit": "allow"}
	case core.ApprovalModePlan:
		permission = map[string]string{"*": "deny", "read": "allow", "glob": "allow", "grep": "allow", "list": "allow"}
	case core.ApprovalModeYolo:
		permission = "allow"
	default:
		return nil
	}
	payload, _ := json.Marshal(map[string]any{"permission": permission})
	return map[string]string{"OPENCODE_CONFIG_CONTENT": string(payload)}
}

// jsonTextMapper extracts a "text"/"message"/"content"/"result" field from a
// JSON line; falls back to nil for control frames.
func jsonTextMapper(line []byte) *core.Event {
	var m map[string]any
	if err := json.Unmarshal(line, &m); err != nil {
		return nil
	}
	for _, k := range []string{"result", "text", "message", "content", "delta"} {
		if v, ok := m[k].(string); ok && v != "" {
			final := k == "result"
			t := core.EventOutput
			if final {
				t = core.EventFinal
			}
			return &core.Event{Type: t, Text: v, Final: final, Metadata: partialCLIMetadata()}
		}
	}
	return nil
}

func partialPlainTextMapper(line []byte) *core.Event {
	event := cliagent.PlainTextMapper(line)
	if event != nil {
		event.Metadata = partialCLIMetadata()
	}
	return event
}

func partialCLIMetadata() map[string]string {
	return map[string]string{
		"transport": "generic-cli",
		"coverage":  "partial",
	}
}
