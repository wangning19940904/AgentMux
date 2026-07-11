package core

import (
	"fmt"
	"strings"
)

// toolProgressCollapseThreshold is the number of tool steps beyond which the
// progress section is collapsed behind a summary line to keep the card compact.
const toolProgressCollapseThreshold = 3

// toolStep records one tool invocation and, once known, a short result summary.
type toolStep struct {
	name   string
	input  string
	result string
	failed bool
}

// toolProgress accumulates the tool steps of a single agent turn and renders
// them, together with the user-visible reasoning summary and answer text, into one markdown blob that the
// streaming card / message updates in place. When there are many steps the
// rendering collapses older ones behind a summary so the card stays readable.
type toolProgress struct {
	steps []toolStep
}

// add records a new tool invocation and returns its index so a later result can
// be attached to it.
func (t *toolProgress) add(name, input string) int {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "tool"
	}
	t.steps = append(t.steps, toolStep{name: name, input: strings.TrimSpace(input)})
	return len(t.steps) - 1
}

// attachResult records a result summary for the most recent step lacking one
// (Claude's tool_result frames arrive after the matching tool_use and carry no
// reliable id we can map, so newest-open-step ordering is the best heuristic).
func (t *toolProgress) attachResult(result string, failed bool) {
	result = strings.TrimSpace(result)
	for i := len(t.steps) - 1; i >= 0; i-- {
		if t.steps[i].result == "" && !t.steps[i].failed {
			t.steps[i].result = result
			t.steps[i].failed = failed
			return
		}
	}
}

// empty reports whether any tool step has been recorded.
func (t *toolProgress) empty() bool { return len(t.steps) == 0 }

// render returns the full card body: a reasoning summary, tool-progress
// section (collapsed when long), and answer text. done controls whether the
// section headers read as in-progress or finished. When no progress is
// available it returns answer unchanged so plain conversations look exactly as
// before.
func (t *toolProgress) render(thinking, answer string, done bool) string {
	thinking = strings.TrimSpace(thinking)
	if len(t.steps) == 0 && thinking == "" {
		return answer
	}

	var b strings.Builder
	if thinking != "" {
		header := "💭 思考摘要"
		if !done {
			header += " · 进行中…"
		}
		b.WriteString("**")
		b.WriteString(header)
		b.WriteString("**\n")
		b.WriteString(thinking)
		b.WriteString("\n\n")
	}

	if len(t.steps) > 0 {
		header := fmt.Sprintf("🔧 工具执行 (%d)", len(t.steps))
		if !done {
			header += " · 进行中…"
		}

		// Collapse older steps once the list grows long: show a summary line plus
		// only the most recent steps in full.
		visibleFrom := 0
		if len(t.steps) > toolProgressCollapseThreshold {
			visibleFrom = len(t.steps) - toolProgressCollapseThreshold
		}

		b.WriteString("**")
		b.WriteString(header)
		b.WriteString("**\n")
		if visibleFrom > 0 {
			b.WriteString(fmt.Sprintf("<font color=grey>…已折叠前 %d 个步骤</font>\n", visibleFrom))
		}
		for i := visibleFrom; i < len(t.steps); i++ {
			b.WriteString(renderToolStep(i+1, t.steps[i]))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n---\n")

	if strings.TrimSpace(answer) != "" {
		b.WriteString(answer)
	}
	return b.String()
}

// renderToolStep renders one numbered step line with an optional result.
func renderToolStep(n int, s toolStep) string {
	icon := "▹"
	if s.result != "" || s.failed {
		icon = "✓"
	}
	if s.failed {
		icon = "✗"
	}
	line := fmt.Sprintf("%s `%s`", icon, s.name)
	if s.input != "" {
		line += " " + s.input
	}
	out := line + "\n"
	if s.result != "" {
		out += fmt.Sprintf("<font color=grey>  ↳ %s</font>\n", s.result)
	}
	return out
}
