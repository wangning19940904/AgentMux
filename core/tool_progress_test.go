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
	tp.add("执行命令", "bytedcli codebase list --format json")
	tp.attachResult("found 12 codebases", false)

	out := tp.render("", "这是笑话", true)
	for _, want := range []string{
		"这是笑话", "工具执行 (1，详情已折叠)", "✓ 1 · ✗ 0 · ⏳ 0", "最近：执行命令",
		"调用摘要：bytedcli codebase list --format json", "结果摘要：found 12 codebases",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("render missing %q:\n%s", want, out)
		}
	}
	if strings.Index(out, "这是笑话") > strings.Index(out, "工具执行") {
		t.Fatalf("answer should render before tools:\n%s", out)
	}
	if strings.Contains(out, "进行中") {
		t.Fatalf("done render should not say in-progress:\n%s", out)
	}
}

func TestToolProgressInProgressMarker(t *testing.T) {
	var tp toolProgress
	tp.add("Read", "a.go")
	out := tp.render("", "", false)
	if !strings.Contains(out, "进行中") || !strings.Contains(out, "调用摘要：a.go") || !strings.Contains(out, "结果摘要：执行中…") {
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
	if strings.Count(out, "调用摘要：") != 1 || strings.Count(out, "结果摘要：") != 1 {
		t.Fatalf("tool summary should show one latest invocation/result pair:\n%s", out)
	}
}

func TestToolProgressSummaryIsBoundedAndEscapesCardMarkup(t *testing.T) {
	var tp toolProgress
	tp.add("Bash", "run <unsafe> "+strings.Repeat("x", toolInputPreviewRunes))
	tp.attachResult("ok & "+strings.Repeat("y", toolResultPreviewRunes), false)
	out := tp.render("", "done", true)
	for _, want := range []string{"run &lt;unsafe&gt;", "ok &amp;", "…"} {
		if !strings.Contains(out, want) {
			t.Fatalf("summary missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "run <unsafe>") {
		t.Fatalf("tool summary must not inject card markup:\n%s", out)
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

func TestToolProgressSettledSuccessfullyIsStrict(t *testing.T) {
	var empty toolProgress
	if empty.settledSuccessfully() {
		t.Fatal("empty progress must not prove a completed tool-backed turn")
	}

	var running toolProgress
	running.addWithID("call-1", "Shell", "install")
	if running.settledSuccessfully() {
		t.Fatal("running tool reported settled")
	}
	running.attachResultForID("call-1", "done", false)
	if !running.settledSuccessfully() {
		t.Fatal("completed successful tool did not report settled")
	}

	var failed toolProgress
	failed.addWithID("call-2", "Shell", "install")
	failed.attachResultForID("call-2", "exit 1", true)
	if failed.settledSuccessfully() {
		t.Fatal("failed tool reported settled successfully")
	}
}

func TestToolProgressThinkingPreviewKeepsLatestText(t *testing.T) {
	thinking := strings.Repeat("旧", progressPreviewRunes) + "最新进展"
	out := renderThinkingSummary(thinking, false)
	if !strings.Contains(out, "…") || !strings.Contains(out, "最新进展") || strings.Contains(out, strings.Repeat("旧", progressPreviewRunes)) {
		t.Fatalf("thinking preview was not compacted from the end:\n%s", out)
	}
}

func TestMergePersistentOutputKeepsAuthorizationDetailsInFinalAnswer(t *testing.T) {
	auth := "需要完成授权：\nhttps://login.example/device\n验证码：ABCD-EFGH"
	if got := mergePersistentOutput(auth, auth); got != auth {
		t.Fatalf("initial persistent output = %q", got)
	}
	got := mergePersistentOutput("已启动安装，等待授权。", auth)
	for _, want := range []string{"已启动安装，等待授权。", "https://login.example/device", "ABCD-EFGH"} {
		if !strings.Contains(got, want) {
			t.Fatalf("merged output %q missing %q", got, want)
		}
	}
	if strings.Index(got, "https://login.example/device") > strings.Index(got, "已启动安装，等待授权。") {
		t.Fatalf("action-required output must stay above the streaming answer: %q", got)
	}
	if duplicated := mergePersistentOutput(got, auth); duplicated != got {
		t.Fatalf("persistent output duplicated: %q", duplicated)
	}
}
