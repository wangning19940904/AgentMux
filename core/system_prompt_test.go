package core

import "testing"

func TestComposeSystemPromptEmpty(t *testing.T) {
	if got := ComposeSystemPrompt("", nil, nil); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestComposeSystemPromptBaseOnly(t *testing.T) {
	if got := ComposeSystemPrompt("hello\n\n", nil, nil); got != "hello" {
		t.Fatalf("expected trimmed base, got %q", got)
	}
}

func TestComposeSystemPromptWithLogsAndCLIs(t *testing.T) {
	got := ComposeSystemPrompt(
		"base",
		[]string{"/a.jsonl", "/b.jsonl"},
		[]CLINote{{Name: "lark-cli", Note: "Feishu CLI"}, {Name: "empty"}},
	)
	want := "base\n\n绑定的事件回调日志路径为：\n- /a.jsonl\n- /b.jsonl\n\n已启用以下 CLI 工具：\n- lark-cli：Feishu CLI\n- empty"
	if got != want {
		t.Fatalf("unexpected prompt:\n got=%q\nwant=%q", got, want)
	}
}

func TestComposeSystemPromptNoBase(t *testing.T) {
	got := ComposeSystemPrompt("", []string{"/a.jsonl"}, nil)
	want := "绑定的事件回调日志路径为：\n- /a.jsonl"
	if got != want {
		t.Fatalf("unexpected prompt:\n got=%q\nwant=%q", got, want)
	}
}
