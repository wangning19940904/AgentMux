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
	"github.com/wangning19940904/AgentMux/internal/traeauth"
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

	// ByteDance internal TRAE CLI.
	registerTrae()

	// iFlow CLI.
	register("iflow", "iflow", false, core.ApprovalModeManual, iflowArgs, iflowApprovalEnv, partialPlainTextMapper, true)

	// Kimi CLI.
	register("kimi", "kimi", false, core.ApprovalModeAuto, func(p, _, _, _ string) []string {
		return []string{"-p", p, "--output-format", "stream-json"}
	}, nil, partialPlainTextMapper, true)
}

func registerTrae() {
	core.RegisterAgent("traecli", func(cfg map[string]any) (core.Agent, error) {
		legacy := cliagent.New(cliagent.Spec{
			Name: "traecli", Binary: "traecli",
			EnsureAuth: traeauth.Ensure,
			Args:       traeArgs, ResumeArgs: traeResumeArgs,
			SessionIDFromLine: traeSessionID,
			EventMapper:       traeStreamEvents, FinalFromLast: true, SupportsModel: true,
			ApprovalModes: core.ApprovalModeValuesForRuntime("traecli"), DefaultApprovalMode: core.ApprovalModeManual,
			ModelCatalogArgs: []string{"models", "--json"}, ParseModelCatalog: parseTraeModelCatalog,
		}, cfg)
		return newTraeAppAgent(cfg, legacy), nil
	})
}

func traeArgs(prompt, systemPrompt, model, approvalMode string) []string {
	args := append([]string{"exec"}, traeCommonArgs(systemPrompt, model, approvalMode)...)
	return append(args, prompt)
}

func traeResumeArgs(sessionID, prompt, systemPrompt, model, approvalMode string) []string {
	args := append([]string{"exec", "resume"}, traeCommonArgs(systemPrompt, model, approvalMode)...)
	return append(args, sessionID, prompt)
}

func traeCommonArgs(systemPrompt, model, approvalMode string) []string {
	args := []string{"--json", "--skip-git-repo-check"}
	if systemPrompt != "" {
		encoded, _ := json.Marshal(systemPrompt)
		args = append(args, "-c", "developer_instructions="+string(encoded))
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	switch approvalMode {
	case core.ApprovalModeAutoEdit:
		args = append(args, "-c", `approval_policy="on-request"`, "-c", `sandbox_mode="workspace-write"`)
	case core.ApprovalModeAuto:
		args = append(args, "--permission-mode", "auto")
	case core.ApprovalModePlan:
		args = append(args, "-c", `approval_policy="never"`, "-c", `sandbox_mode="read-only"`)
	case core.ApprovalModeYolo:
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	default:
		args = append(args, "-c", `approval_policy="on-request"`, "-c", `sandbox_mode="read-only"`)
	}
	return args
}

func parseTraeModelCatalog(output []byte) (cliagent.ModelCatalog, error) {
	var items []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(output, &items); err != nil {
		return cliagent.ModelCatalog{}, fmt.Errorf("parse TRAE model catalog: %w", err)
	}
	catalog := cliagent.ModelCatalog{}
	for _, item := range items {
		if name := strings.TrimSpace(item.Name); name != "" {
			catalog.Models = append(catalog.Models, name)
		}
	}
	if len(catalog.Models) == 0 {
		return cliagent.ModelCatalog{}, fmt.Errorf("TRAE model catalog contained no models")
	}
	return catalog, nil
}

func registerCursor() {
	core.RegisterAgent("cursor", func(cfg map[string]any) (core.Agent, error) {
		return cliagent.New(cliagent.Spec{
			Name: "cursor", Binary: "cursor-agent", Args: cursorArgs,
			EventMapper:   cursorStreamEvents,
			FinalFromLast: true, SupportsModel: true,
			ApprovalModes: core.ApprovalModeValuesForRuntime("cursor"), DefaultApprovalMode: core.ApprovalModeManual,
			ModelCatalogArgs: []string{"--list-models"}, ParseModelCatalog: parseCursorModelCatalog,
			ModelForSettings: cursorModelForSettings, EmbeddedModelSettings: cursorEmbeddedModelSettings,
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
	args = append(args, cursorApprovalArgs[approvalMode]...)
	return append(args, prompt)
}

// cursorApprovalArgs maps each approval mode to its cursor-agent CLI flags.
var cursorApprovalArgs = map[string][]string{
	core.ApprovalModeAuto: {"--auto-review"},
	core.ApprovalModePlan: {"--plan"},
	core.ApprovalModeYolo: {"--yolo"},
}

var cursorANSISequence = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)

func parseCursorModelCatalog(output []byte) (cliagent.ModelCatalog, error) {
	text := cursorANSISequence.ReplaceAllString(string(output), "")
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	type catalogEntry struct {
		model     string
		isDefault bool
	}
	var entries []catalogEntry
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
		entries = append(entries, catalogEntry{model: line, isDefault: isDefault})
	}
	if !inModels {
		return cliagent.ModelCatalog{}, fmt.Errorf("Cursor model list did not contain an Available models section")
	}
	if len(entries) == 0 {
		return cliagent.ModelCatalog{}, fmt.Errorf("Cursor model list did not contain any model IDs")
	}

	// Cursor's live catalog expands one base model into concrete effort/speed
	// slugs (for example gpt-5.6-sol-high-fast). Keep one model option and move
	// those axes into the shared effort and speed controls. Besides fitting IM
	// picker limits, this mirrors Cursor's documented parameterized --model
	// syntax instead of presenting hundreds of near-duplicate models.
	catalog := cliagent.ModelCatalog{ModelCapabilities: map[string]cliagent.ModelRuntimeCapabilities{}}
	seenModels := map[string]bool{}
	for _, entry := range entries {
		model, effort, tier, parameterized := cursorCatalogModel(entry.model)
		if !seenModels[model] {
			seenModels[model] = true
			catalog.Models = append(catalog.Models, model)
		}
		capabilities := catalog.ModelCapabilities[model]
		capabilities.Variants = append(capabilities.Variants, cliagent.ModelVariant{
			ID: entry.model, ReasoningEffort: effort, ServiceTier: tier,
		})
		if parameterized {
			if effort != "" {
				capabilities.ReasoningEfforts = appendRuntimeOption(capabilities.ReasoningEfforts, effort)
			}
			if tier != "" {
				capabilities.ServiceTiers = appendRuntimeOption(capabilities.ServiceTiers, tier)
			}
		}
		catalog.ModelCapabilities[model] = capabilities
		if entry.isDefault {
			catalog.DefaultModel = model
			catalog.DefaultReasoningEffort = effort
			catalog.DefaultServiceTier = tier
		}
	}
	for model, capabilities := range catalog.ModelCapabilities {
		// A speed selector is useful only when Cursor advertised a Fast variant.
		// The unsuffixed/normal form is the other side of that switch.
		if runtimeOptionValueExists(capabilities.ServiceTiers, "priority") {
			capabilities.ServiceTiers = prependRuntimeOption(capabilities.ServiceTiers, "default")
		} else {
			capabilities.ServiceTiers = nil
		}
		catalog.ModelCapabilities[model] = capabilities
	}
	return catalog, nil
}

// cursorCatalogModel folds the effort and Fast dimensions of one catalog ID.
// The returned base remains an exact Cursor parameterized-model base; semantic
// names such as "thinking" remain part of it.
func cursorCatalogModel(model string) (base, effort, tier string, parameterized bool) {
	base, parameters := splitCursorModelParameters(model)
	remaining := make([]string, 0, len(parameters))
	for _, parameter := range parameters {
		key, value, ok := strings.Cut(parameter, "=")
		if !ok {
			remaining = append(remaining, parameter)
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "effort", "reasoning":
			if normalized := normalizeCursorEffort(value); normalized != "" {
				effort = normalized
				parameterized = true
				continue
			}
		case "fast":
			switch strings.ToLower(strings.TrimSpace(value)) {
			case "true", "1", "yes", "on":
				tier = "priority"
				parameterized = true
				continue
			case "false", "0", "no", "off":
				tier = "default"
				parameterized = true
				continue
			}
		}
		remaining = append(remaining, parameter)
	}
	if len(parameters) > 0 {
		if len(remaining) > 0 {
			base += "[" + strings.Join(remaining, ",") + "]"
		}
		return base, effort, tier, parameterized
	}

	tokens := strings.Split(strings.TrimSpace(base), "-")
	end := len(tokens)
	if end > 0 && strings.EqualFold(tokens[end-1], "fast") {
		tier = "priority"
		parameterized = true
		end--
	} else {
		tier = "default"
	}
	if end > 1 && strings.EqualFold(tokens[end-2], "extra") && strings.EqualFold(tokens[end-1], "high") {
		effort = "xhigh"
		parameterized = true
		end -= 2
	} else if end > 0 {
		if normalized := normalizeCursorEffort(tokens[end-1]); normalized != "" {
			effort = normalized
			parameterized = true
			end--
		}
	}
	if !parameterized || end == 0 {
		return strings.TrimSpace(model), "", "", false
	}
	return strings.Join(tokens[:end], "-"), effort, tier, true
}

func appendRuntimeOption(options []core.RuntimeOption, value string) []core.RuntimeOption {
	if value == "" || runtimeOptionValueExists(options, value) {
		return options
	}
	return append(options, core.RuntimeOption{Value: value, Label: value})
}

func prependRuntimeOption(options []core.RuntimeOption, value string) []core.RuntimeOption {
	if value == "" || runtimeOptionValueExists(options, value) {
		return options
	}
	return append([]core.RuntimeOption{{Value: value, Label: value}}, options...)
}

func runtimeOptionValueExists(options []core.RuntimeOption, value string) bool {
	for _, option := range options {
		if option.Value == value {
			return true
		}
	}
	return false
}

func cursorModelForSettings(settings core.RuntimeSettings) string {
	base, parameters := splitCursorModelParameters(settings.Model)
	if base == "" {
		return ""
	}
	embedded := cursorEmbeddedModelSettings(settings.Model)
	if effort := strings.TrimSpace(settings.ReasoningEffort); effort != "" && embedded.ReasoningEffort == "" {
		parameters = setCursorModelParameter(parameters, []string{"effort", "reasoning"}, "effort", effort)
	}
	if fast, ok := cursorFastParameter(settings.ServiceTier); ok && embedded.ServiceTier == "" {
		parameters = setCursorModelParameter(parameters, []string{"fast"}, "fast", fast)
	}
	if len(parameters) == 0 {
		return base
	}
	return base + "[" + strings.Join(parameters, ",") + "]"
}

// cursorEmbeddedModelSettings recognizes the parameterized model variants
// returned by Cursor's live catalog. Cursor currently exposes both bracket
// parameters and expanded slugs such as `cursor-grok-4.5-medium-fast`. In both
// forms the selected model already owns effort/speed, so sending another
// independently selected value would create a contradictory model request.
func cursorEmbeddedModelSettings(model string) core.RuntimeSettings {
	base, parameters := splitCursorModelParameters(model)
	settings := core.RuntimeSettings{}
	for _, parameter := range parameters {
		key, value, ok := strings.Cut(parameter, "=")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "effort", "reasoning":
			settings.ReasoningEffort = normalizeCursorEffort(value)
		case "fast":
			switch strings.ToLower(strings.TrimSpace(value)) {
			case "true", "1", "yes", "on":
				settings.ServiceTier = "priority"
			case "false", "0", "no", "off":
				settings.ServiceTier = "default"
			}
		}
	}

	tokens := strings.Split(strings.ToLower(strings.TrimSpace(base)), "-")
	if len(tokens) == 0 {
		return settings
	}
	end := len(tokens)
	if tokens[end-1] == "fast" {
		if settings.ServiceTier == "" {
			settings.ServiceTier = "priority"
		}
		end--
	}
	if settings.ReasoningEffort == "" && end > 0 {
		effort := normalizeCursorEffort(tokens[end-1])
		if effort == "high" && end > 1 && tokens[end-2] == "extra" {
			effort = "xhigh"
		}
		if effort != "" {
			settings.ReasoningEffort = effort
			// Expanded catalog slugs without `-fast` are the normal-speed variant.
			if settings.ServiceTier == "" {
				settings.ServiceTier = "default"
			}
		}
	}
	return settings
}

func normalizeCursorEffort(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "none", "minimal", "low", "medium", "high", "xhigh", "max":
		return strings.ToLower(strings.TrimSpace(value))
	case "extra-high", "extra_high":
		return "xhigh"
	default:
		return ""
	}
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
