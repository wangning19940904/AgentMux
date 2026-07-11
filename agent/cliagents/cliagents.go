// Package cliagents registers all subprocess-based coding-agent adapters built
// on top of cliagent. Each agent's invocation mirrors the documented
// non-interactive mode (from cc-connect's INSTALL.md), e.g.:
//
//	codex   exec --json
//	cursor  agent --print --output-format stream-json
//	gemini  -p --output-format stream-json
//	qoder   -p -f stream-json
//	opencode run --format json
//	iflow   -i -r -o
package cliagents

import (
	"encoding/json"

	"github.com/agentnexus/agentnexus/agent/cliagent"
	"github.com/agentnexus/agentnexus/core"
)

func register(name, binary string, supportsModel bool, args func(prompt, sys, model string) []string, mapper cliagent.LineMapper, finalLast bool) {
	core.RegisterAgent(name, func(cfg map[string]any) (core.Agent, error) {
		return cliagent.New(cliagent.Spec{
			Name: name, Binary: binary, Args: args,
			Mapper: mapper, FinalFromLast: finalLast, SupportsModel: supportsModel,
		}, cfg), nil
	})
}

func init() {
	// Codex uses its native app-server protocol. Unlike `codex exec --json`,
	// that protocol provides agent-message deltas, reasoning summaries, tools,
	// and the signed-in account's live model catalog.
	registerCodex()

	// Cursor Agent: stream-json.
	register("cursor", "cursor-agent", true, cursorArgs, jsonTextMapper, true)

	// Gemini CLI: stream-json.
	register("gemini", "gemini", false, func(p, _, _ string) []string {
		return []string{"-p", p, "--output-format", "stream-json"}
	}, jsonTextMapper, true)

	// Qoder CLI.
	register("qoder", "qodercli", false, func(p, _, _ string) []string {
		return []string{"-p", p, "-f", "stream-json"}
	}, jsonTextMapper, true)

	// OpenCode.
	register("opencode", "opencode", false, func(p, _, _ string) []string {
		return []string{"run", "--format", "json", p}
	}, jsonTextMapper, true)

	// iFlow CLI.
	register("iflow", "iflow", false, func(p, _, _ string) []string {
		return []string{"-i", "-r", "-o", p}
	}, cliagent.PlainTextMapper, true)

	// Kimi CLI.
	register("kimi", "kimi", false, func(p, _, _ string) []string {
		return []string{"-p", p}
	}, cliagent.PlainTextMapper, true)
}

func cursorArgs(prompt, _, model string) []string {
	args := []string{"agent", "--print", "--output-format", "stream-json"}
	if model != "" {
		args = append(args, "--model", model)
	}
	return append(args, prompt)
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
			return &core.Event{Type: t, Text: v, Final: final}
		}
	}
	return nil
}
