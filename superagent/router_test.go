package superagent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
)

func TestRouterAnswersDirectly(t *testing.T) {
	h := newHarness(t)
	h.router.answer = func(core.InvocationRequest) (string, error) {
		return "The last build passed; tickets/tkt_1/result.md has the log.", nil
	}
	h.ingest("alice", "github:o/r#1", "did the build pass?", true)
	h.settle()
	if got := h.replies.sent(); !slices.Equal(got, []string{"The last build passed; tickets/tkt_1/result.md has the log."}) {
		t.Fatalf("replies = %v", got)
	}
	if trigger := h.disposition(0); trigger.Disposition != core.SuperAgentDispositionAnswered {
		t.Fatalf("trigger = %+v", trigger)
	}
	if h.workers.callCount() != 0 || len(h.store.allTickets()) != 0 {
		t.Fatal("an answered event must not start work")
	}
}

func TestRouterKeepsOneConversationPerThread(t *testing.T) {
	h := newHarness(t)
	h.router.answer = func(core.InvocationRequest) (string, error) { return "", nil }
	h.ingest("alice", "github:o/r#1", "first", true)
	h.settle()
	h.ingest("alice", "github:o/r#1", "second", true)
	h.ingest("alice", "github:o/r#2", "elsewhere", true)
	h.settle()
	// The second pass routes both threads at once, in either order.
	byText := map[string]string{}
	for index, prompt := range h.router.prompts {
		for _, text := range []string{"first", "second", "elsewhere"} {
			if strings.Contains(prompt, `"text": "`+text+`"`) {
				byText[text] = h.router.conversations[index]
			}
		}
	}
	if len(byText) != 3 || byText["first"] != byText["second"] || byText["first"] == byText["elsewhere"] {
		t.Fatalf("conversations = %v", byText)
	}
	if !strings.Contains(byText["first"], h.agent.ID) {
		t.Fatalf("a conversation must name its Super Agent: %q", byText["first"])
	}
	if trigger := h.disposition(0); trigger.Disposition != core.SuperAgentDispositionIgnored || len(h.replies.sent()) != 0 {
		t.Fatalf("an empty answer stays quiet: trigger = %+v, replies = %v", trigger, h.replies.sent())
	}
}

func TestRouterSeesItsMemoryAndTicketFolders(t *testing.T) {
	h := newHarness(t)
	home, _ := h.service.homeDir(h.agent.ID)
	index := filepath.Join(home, memoryDirName, memoryIndexFile)
	if starter := readText(t, index); !strings.Contains(starter, "One line per memory file") {
		t.Fatalf("MEMORY.md starter = %q", starter)
	}
	if err := os.WriteFile(index, []byte("- [Repos](repos.md) — widgets lives in ~/src/widgets\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.ingest("alice", "github:o/r#1", "fix it", true)
	h.settle()
	ticket := h.store.allTickets()[0]
	h.router.answer = func(core.InvocationRequest) (string, error) { return "", nil }
	h.ingest("alice", "github:o/r#1", "how is it going?", true)
	h.settle()

	prompt := h.router.prompts[1]
	for _, want := range []string{
		"MEMORY INDEX (memory/MEMORY.md):\n- [Repos](repos.md) — widgets lives in ~/src/widgets",
		`"folder": "tickets/` + ticket.ID + `"`,
		"EVENT " + h.disposition(1).ID,
		"Never store secrets",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt lacks %q:\n%s", want, prompt)
		}
	}
	// The operator's notes in MEMORY.md survive the next save.
	if _, err := h.service.Save(context.Background(), h.agent); err != nil {
		t.Fatal(err)
	}
	if got := readText(t, index); !strings.Contains(got, "widgets lives") {
		t.Fatalf("MEMORY.md was overwritten: %q", got)
	}

	if err := os.WriteFile(index, []byte(strings.Repeat("x", maxMemoryIndexRunes+100)), 0o644); err != nil {
		t.Fatal(err)
	}
	if shown := h.service.memoryIndex(h.agent.ID); len([]rune(shown)) > maxMemoryIndexRunes+200 || !strings.Contains(shown, "cut short") {
		t.Fatalf("a long index must be cut short: %d runes", len([]rune(shown)))
	}
}

func TestAttachPassesTheRoutersNote(t *testing.T) {
	h := newHarness(t)
	h.ingest("alice", "github:o/r#1", "fix it", true)
	h.settle()
	ticket := h.store.allTickets()[0]
	h.router.answer = func(core.InvocationRequest) (string, error) {
		return "<agentmux-action>{\"action\":\"attach\",\"ticket_id\":\"" + ticket.ID + "\",\"instructions\":\"use the staging database\"}</agentmux-action>", nil
	}
	h.ingest("alice", "github:o/r#1", "also check the migrations", true)
	h.settle()
	if h.workers.callCount() != 2 {
		t.Fatalf("calls = %d", h.workers.callCount())
	}
	input := h.workers.calls[1].input
	if !strings.Contains(input, "Router's note:\nuse the staging database") || !strings.Contains(input, "also check the migrations") {
		t.Fatalf("follow-up input:\n%s", input)
	}
	// "on it", then the worker's answer after each turn; the attach itself is silent.
	if trigger := h.disposition(1); trigger.Disposition != core.SuperAgentDispositionFollowUp || len(h.replies.sent()) != 3 {
		t.Fatalf("trigger = %+v, replies = %v", trigger, h.replies.sent())
	}
}
