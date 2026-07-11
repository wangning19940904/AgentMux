package core

import "strings"

import "testing"

func TestToolProgressRenderEmptyPassthrough(t *testing.T) {
	var tp toolProgress
	if got := tp.render("", "hello", true); got != "hello" {
		t.Fatalf("empty progress should pass answer through, got %q", got)
	}
	if !tp.empty() {
		t.Fatal("expected empty")
	}
}

func TestToolProgressRendersStepsAndResult(t *testing.T) {
	var tp toolProgress
	tp.add("Bash", "lark-cli im send")
	tp.attachResult("ok", false)

	out := tp.render("", "这是笑话", true)
	for _, want := range []string{"工具执行 (1)", "Bash", "lark-cli im send", "↳ ok", "这是笑话"} {
		if !strings.Contains(out, want) {
			t.Fatalf("render missing %q:\n%s", want, out)
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

func TestToolProgressCollapsesWhenLong(t *testing.T) {
	var tp toolProgress
	for i := 0; i < toolProgressCollapseThreshold+2; i++ {
		tp.add("Tool", "arg")
	}
	out := tp.render("", "answer", true)
	if !strings.Contains(out, "已折叠前 2 个步骤") {
		t.Fatalf("expected collapse summary, got:\n%s", out)
	}
	if !strings.Contains(out, "工具执行 (5)") {
		t.Fatalf("expected total count 5, got:\n%s", out)
	}
}

func TestToolProgressRendersThinkingSummary(t *testing.T) {
	var tp toolProgress
	out := tp.render("我先检查配置。\n然后读取设置。", "结果", false)
	for _, want := range []string{"> <font color=grey>💭 思考摘要 · 进行中…</font>", "> <font color=grey>我先检查配置。</font>", "> <font color=grey>然后读取设置。</font>", "\n---\n结果"} {
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
	for _, want := range []string{"> <font color=grey>💭 思考摘要</font>", "> <font color=grey>检查完成。</font>", "\n---\n最终答复"} {
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
	if !strings.Contains(out, "`A`\n<font color=grey>  ↳ resA") {
		t.Fatalf("call-a result not attached to A:\n%s", out)
	}
	if !strings.Contains(out, "✗ `B`\n<font color=grey>  ↳ resB") {
		t.Fatalf("call-b failure not attached to B:\n%s", out)
	}
}
