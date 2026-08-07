package core

import (
	"fmt"
	"strings"
)

// progressPreviewRunes keeps the live card useful without letting accumulated
// reasoning dominate the answer on narrow mobile screens.
const progressPreviewRunes = 120

const (
	toolInputPreviewRunes  = 96
	toolResultPreviewRunes = 120
)

// toolStep records one tool invocation and, once known, a short result summary.
type toolStep struct {
	id     string
	name   string
	input  string
	result string
	done   bool
	failed bool
}

// toolProgress accumulates the tool steps of a single agent turn and renders
// them, together with the user-visible reasoning summary and answer text, into
// one compact markdown blob that the streaming card / message updates in place.
type toolProgress struct {
	steps []toolStep
}

// addWithID records a new tool invocation and returns its index so a later
// result can be attached to it. The adapter's stable call id may be empty.
func (t *toolProgress) addWithID(id, name, input string) int {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "tool"
	}
	t.steps = append(t.steps, toolStep{id: strings.TrimSpace(id), name: name, input: strings.TrimSpace(input)})
	return len(t.steps) - 1
}

// attachResultForID uses the adapter's stable call id when available, falling
// back to newest-open ordering (Claude's tool_result frames arrive after the
// matching tool_use without a reliable id, so newest-open is the heuristic).
func (t *toolProgress) attachResultForID(id, result string, failed bool) {
	result = strings.TrimSpace(result)
	if id = strings.TrimSpace(id); id != "" {
		for i := range t.steps {
			if t.steps[i].id == id {
				t.steps[i].result = result
				t.steps[i].done = true
				t.steps[i].failed = failed
				return
			}
		}
	}
	for i := len(t.steps) - 1; i >= 0; i-- {
		if !t.steps[i].done {
			t.steps[i].result = result
			t.steps[i].done = true
			t.steps[i].failed = failed
			return
		}
	}
}

// empty reports whether any tool step has been recorded.
func (t *toolProgress) empty() bool { return len(t.steps) == 0 }

// settledSuccessfully reports whether the turn ran at least one tool and every
// recorded invocation reached a successful terminal state. It is deliberately
// stricter than "no failures": an open/backgrounded tool must not make a later
// transport error look harmless.
func (t *toolProgress) settledSuccessfully() bool {
	if len(t.steps) == 0 {
		return false
	}
	for _, step := range t.steps {
		if !step.done || step.failed {
			return false
		}
	}
	return true
}

// render returns the full card body with the answer first and execution context
// reduced to compact summaries below it. Raw commands, tool results, and the
// full reasoning stream remain available in logs/observability instead of
// crowding the chat card. When no progress is available it returns answer
// unchanged so plain conversations look exactly as before.
func (t *toolProgress) render(thinking, answer string, done bool) string {
	thinking = strings.TrimSpace(thinking)
	if len(t.steps) == 0 && thinking == "" {
		return answer
	}

	var b strings.Builder
	if strings.TrimSpace(answer) != "" {
		b.WriteString(answer)
		b.WriteString("\n\n---\n")
	}

	if thinking != "" {
		b.WriteString(renderThinkingSummary(thinking, done))
	}

	if len(t.steps) > 0 {
		if thinking != "" {
			b.WriteString("\n")
		}
		b.WriteString(renderToolSummary(t.steps, done))
	}
	return b.String()
}

// renderThinkingSummary keeps user-visible reasoning summaries visually
// secondary to the answer. Only the latest compact preview is rendered; the
// accumulated summary is intentionally kept out of the card.
func renderThinkingSummary(thinking string, done bool) string {
	header := "💭 思考摘要（已折叠）"
	if !done {
		header += " · 进行中…"
	}

	preview := compactProgressText(thinking, progressPreviewRunes, true)
	return fmt.Sprintf("> <font color=grey>%s</font>\n> <font color=grey>最新：%s</font>", header, preview)
}

// renderToolSummary replaces raw command lines and output with a compact
// overview. The latest tool keeps a bounded invocation and result preview so
// a folded card still explains what ran and what happened.
func renderToolSummary(steps []toolStep, done bool) string {
	succeeded, failed, running := 0, 0, 0
	for _, step := range steps {
		switch {
		case step.failed:
			failed++
		case step.done:
			succeeded++
		default:
			running++
		}
	}
	header := fmt.Sprintf("🔧 工具执行 (%d，详情已折叠)", len(steps))
	if !done {
		header += " · 进行中…"
	}
	step := steps[len(steps)-1]
	latest := compactProgressText(step.name, 40, false)
	counts := fmt.Sprintf("✓ %d · ✗ %d · ⏳ %d · 最近：%s", succeeded, failed, running, latest)
	input := "无参数"
	if step.input != "" {
		input = compactProgressText(step.input, toolInputPreviewRunes, false)
	}
	result := "执行中…"
	if step.done {
		result = "已完成（无返回内容）"
		if step.failed {
			result = "执行失败（无返回内容）"
		}
		if step.result != "" {
			result = compactProgressText(step.result, toolResultPreviewRunes, false)
		}
	}
	return fmt.Sprintf(
		"> <font color=grey>%s</font>\n> <font color=grey>%s</font>\n> <font color=grey>调用摘要：%s</font>\n> <font color=grey>结果摘要：%s</font>",
		header, counts, escapeProgressMarkup(input), escapeProgressMarkup(result),
	)
}

func escapeProgressMarkup(value string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(value)
}

func compactProgressText(value string, limit int, keepEnd bool) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	if limit <= 1 {
		return "…"
	}
	if keepEnd {
		return "…" + string(runes[len(runes)-limit+1:])
	}
	return string(runes[:limit-1]) + "…"
}
