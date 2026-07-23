package core

import (
	"strings"
	"testing"
)

func TestToolProgressRenderEmptyPassthrough(t *testing.T) {
	var tp toolProgress
	if got := tp.render("", "hello", true); got != "hello" {
		t.Fatalf("empty progress should pass answer through, got %q", got)
	}
	if !tp.empty() {
		t.Fatal("expected empty")
	}
}

func TestToolProgressRendersCompactSummaryAfterAnswer(t *testing.T) {
	var tp toolProgress
	tp.add("执行命令", "lark-cli im send --very-long-sensitive-command")
	tp.attachResult("very long raw result that should stay out of the card", false)

	out := tp.render("", "这是笑话", true)
	for _, want := range []string{"这是笑话", "工具执行 (1，详情已折叠)", "✓ 1 · ✗ 0 · ⏳ 0", "最近：执行命令"} {
		if !strings.Contains(out, want) {
			t.Fatalf("render missing %q:\n%s", want, out)
		}
	}
	if strings.Index(out, "这是笑话") > strings.Index(out, "工具执行") {
		t.Fatalf("answer should render before tools:\n%s", out)
	}
	for _, hidden := range []string{"lark-cli im send", "sensitive-command", "very long raw result"} {
		if strings.Contains(out, hidden) {
			t.Fatalf("raw tool detail %q should be folded:\n%s", hidden, out)
		}
	}
	if strings.Contains(out, "进行中") {
		t.Fatalf("done render should not say in-progress:\n%s", out)
	}
}

func TestToolProgressInProgressMarker(t *testing.T) {
	var tp toolProgress
	tp.add("Read", "a.go")
	out := tp.render("", "", false)
	if !strings.Contains(out, "进行中") {
		t.Fatalf("streaming render should mark in-progress:\n%s", out)
	}
}

func TestToolProgressSummarizesManyStepsAtFixedHeight(t *testing.T) {
	var tp toolProgress
	for i := 0; i < 5; i++ {
		tp.add("Tool", "arg")
	}
	tp.attachResult("ok", false)
	tp.attachResult("failed", true)
	out := tp.render("", "answer", true)
	if !strings.Contains(out, "工具执行 (5，详情已折叠)") || !strings.Contains(out, "✓ 1 · ✗ 1 · ⏳ 3") {
		t.Fatalf("expected total count 5, got:\n%s", out)
	}
	if strings.Count(out, "最近：") != 1 {
		t.Fatalf("tool summary should stay fixed-height:\n%s", out)
	}
}

func TestToolProgressRendersThinkingSummary(t *testing.T) {
	var tp toolProgress
	out := tp.render("我先检查配置。\n然后读取设置。", "结果", false)
	for _, want := range []string{"结果\n\n---\n", "> <font color=grey>💭 思考摘要（已折叠） · 进行中…</font>", "> <font color=grey>最新：我先检查配置。 然后读取设置。</font>"} {
		if !strings.Contains(out, want) {
			t.Fatalf("render missing %q:\n%s", want, out)
		}
	}
}

func TestToolProgressFinishedThinkingSummaryIsQuotedAndSecondary(t *testing.T) {
	var tp toolProgress
	out := tp.render("检查完成。", "最终答复", true)
	if strings.Contains(out, "进行中") {
		t.Fatalf("finished summary should not say in-progress:\n%s", out)
	}
	for _, want := range []string{"最终答复\n\n---\n", "> <font color=grey>💭 思考摘要（已折叠）</font>", "> <font color=grey>最新：检查完成。</font>"} {
		if !strings.Contains(out, want) {
			t.Fatalf("render missing %q:\n%s", want, out)
		}
	}
}

func TestToolProgressAttachResultTargetsNewestOpen(t *testing.T) {
	var tp toolProgress
	tp.add("A", "")
	tp.add("B", "")
	tp.attachResult("resB", false)
	tp.attachResult("resA", true)
	if tp.steps[1].result != "resB" || tp.steps[1].failed {
		t.Fatalf("step B = %#v", tp.steps[1])
	}
	if tp.steps[0].result != "resA" || !tp.steps[0].failed {
		t.Fatalf("step A = %#v", tp.steps[0])
	}
}

func TestToolProgressAttachesOutOfOrderResultsByCallID(t *testing.T) {
	var tp toolProgress
	tp.addWithID("call-a", "A", "")
	tp.addWithID("call-b", "B", "")
	tp.attachResultForID("call-a", "resA", false)
	tp.attachResultForID("call-b", "resB", true)
	out := tp.render("", "done", true)
	if !tp.steps[0].done || tp.steps[0].failed || tp.steps[0].result != "resA" {
		t.Fatalf("call-a result not attached to A: %#v", tp.steps[0])
	}
	if !tp.steps[1].done || !tp.steps[1].failed || tp.steps[1].result != "resB" {
		t.Fatalf("call-b failure not attached to B: %#v", tp.steps[1])
	}
	if !strings.Contains(out, "✓ 1 · ✗ 1 · ⏳ 0") {
		t.Fatalf("out-of-order results not reflected in summary:\n%s", out)
	}
}

func TestToolProgressThinkingPreviewKeepsLatestText(t *testing.T) {
	thinking := strings.Repeat("旧", progressPreviewRunes) + "最新进展"
	out := renderThinkingSummary(thinking, false)
	if !strings.Contains(out, "…") || !strings.Contains(out, "最新进展") || strings.Contains(out, strings.Repeat("旧", progressPreviewRunes)) {
		t.Fatalf("thinking preview was not compacted from the end:\n%s", out)
	}
}
