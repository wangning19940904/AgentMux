package core

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

type helpCardTestPlatform struct {
	*fakePlatform
	helpMu sync.Mutex
	help   []HelpCardState
}

func (p *helpCardTestPlatform) ReplyHelpCard(_ context.Context, _ *Message, state HelpCardState) error {
	p.helpMu.Lock()
	p.help = append(p.help, state)
	p.helpMu.Unlock()
	return nil
}

func TestChannelHelpReturnsCardWithoutStartingAgentSession(t *testing.T) {
	eng := NewEngine(nil, NewHookRunner(nil, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Start(ctx) }()

	platform := &helpCardTestPlatform{fakePlatform: newFakePlatform("fake-help-card")}
	restore := stubPlatformFactory(t, "fake-help-card", platform)
	defer restore()

	agent := &fakeAgent{}
	channel := Channel{ID: "help-card", Name: "help", Type: "fake-help-card", Enabled: true, UpdatedAt: time.Now()}
	if err := eng.AttachChannel(ctx, channel, agent, "", WorkspaceInitOptions{
		AgentID: "agent-1", AgentName: "代码助手", RuntimeID: "codex",
	}); err != nil {
		t.Fatal(err)
	}

	platform.push(&Message{ID: "help-1", ChatID: "chat-1", Text: " /HELP ", Platform: "fake-help-card"})
	waitFor(t, "help card", func() bool {
		platform.helpMu.Lock()
		defer platform.helpMu.Unlock()
		return len(platform.help) == 1
	})

	platform.helpMu.Lock()
	state := platform.help[0]
	platform.helpMu.Unlock()
	if state.AgentName != "代码助手" || state.RuntimeName != "codex" || !strings.Contains(state.Introduction, "代码助手") {
		t.Fatalf("help card identity = %+v", state)
	}
	for _, want := range []string{"/model", "/clear", "/approval"} {
		found := false
		for _, command := range state.Commands {
			if command.Command == want && command.Actionable {
				found = true
			}
		}
		if !found {
			t.Fatalf("help card missing actionable command %q: %+v", want, state.Commands)
		}
	}
	agent.mu.Lock()
	sessions := agent.sessions
	turns := append([]string(nil), agent.turns...)
	agent.mu.Unlock()
	if sessions != 0 || len(turns) != 0 {
		t.Fatalf("/help reached Agent: sessions=%d turns=%v", sessions, turns)
	}
}

func TestHelpTextAndRemoteCommands(t *testing.T) {
	state := buildHelpCardState("运维助手", "codex", true)
	text := formatHelpText(state)
	for _, want := range []string{"运维助手", "当前运行时：codex", "/help", "/clear", "/model", "/status", "/queue <内容>", "/bind <序号或 thread_id>"} {
		if !strings.Contains(text, want) {
			t.Fatalf("help text missing %q:\n%s", want, text)
		}
	}
	if !IsHelpCommandAction("/clear") || IsHelpCommandAction("run arbitrary prompt") {
		t.Fatal("help action allowlist is invalid")
	}
}
