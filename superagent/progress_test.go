package superagent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// progressStub records what progress showed in the chat.
type progressStub struct {
	mu        sync.Mutex
	postErr   error
	updateErr error
	posts     []string
	cards     []bool
	updates   []string
	finals    []string // "done" or "failed" for an update that ended the work
	reactions []string
	live      map[string]string // reaction id -> emoji
}

func newProgressStub() *progressStub { return &progressStub{live: map[string]string{}} }

func (p *progressStub) AddChatStatusReaction(_ context.Context, channelID, messageID, emoji string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	id := fmt.Sprintf("s%d", len(p.reactions))
	p.reactions = append(p.reactions, "add "+messageID+" "+emoji)
	p.live[id] = emoji
	return id
}

func (p *progressStub) RemoveChatReaction(_ context.Context, channelID, messageID, reactionID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reactions = append(p.reactions, "remove "+messageID+" "+p.live[reactionID])
	delete(p.live, reactionID)
}

func (p *progressStub) PostChatStatus(_ context.Context, channelID, chatID, messageID, text string, card bool) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.postErr != nil {
		return "", p.postErr
	}
	p.posts = append(p.posts, text)
	p.cards = append(p.cards, card)
	return fmt.Sprintf("status%d", len(p.posts)), nil
}

func (p *progressStub) UpdateChatStatus(_ context.Context, channelID, messageID, text string, card, done, failed bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.updateErr != nil {
		return p.updateErr
	}
	p.updates = append(p.updates, text)
	switch {
	case failed:
		p.finals = append(p.finals, "failed")
	case done:
		p.finals = append(p.finals, "done")
	}
	return nil
}

func (p *progressStub) liveEmojis() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, emoji := range p.live {
		out = append(out, emoji)
	}
	slices.Sort(out)
	return out
}

func (p *progressStub) lastUpdate() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.updates) == 0 {
		return ""
	}
	return p.updates[len(p.updates)-1]
}

func (p *progressStub) counts() (posts, updates int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.posts), len(p.updates)
}

type progressRun struct {
	h       *harness
	stub    *progressStub
	release chan struct{}
	// clockMu guards the harness clock, which these tests move while a
	// worker's turn is still running.
	clockMu sync.Mutex
}

// progressHarness is a Feishu agent with the given progress style whose
// worker holds its turn until release is closed.
func progressHarness(t *testing.T, style string) *progressRun {
	t.Helper()
	h, _ := reactionHarness(t, map[string]string{"channel_id": "ch1", "chat_ids": "oc_team"})
	agent := h.agent
	agent.Progress = style
	saved, err := h.service.Save(context.Background(), agent)
	if err != nil {
		t.Fatal(err)
	}
	h.agent = saved
	stub := newProgressStub()
	h.service.progress = stub
	run := &progressRun{h: h, stub: stub, release: make(chan struct{})}
	h.service.now = func() time.Time {
		run.clockMu.Lock()
		defer run.clockMu.Unlock()
		return *h.clock
	}
	h.workers.run = func(ctx context.Context, _ workerCall) (string, error) {
		select {
		case <-run.release:
			return "checked every region", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	t.Cleanup(func() {
		select {
		case <-run.release:
		default:
			close(run.release)
		}
	})
	return run
}

// start sends a request the router takes on and waits until its Ticket runs.
func (r *progressRun) start(id string) core.SuperAgentTicket {
	r.h.t.Helper()
	ctx := context.Background()
	if _, err := r.h.service.InterceptMessage(ctx, groupMessage(id, "ou_alice", "check the page", true)); err != nil {
		r.h.t.Fatal(err)
	}
	for range 2000 {
		r.h.service.Pass(ctx)
		r.h.service.mu.Lock()
		evaluating := len(r.h.service.evaluating)
		r.h.service.mu.Unlock()
		pending, _ := r.h.store.ListPendingSuperAgentTriggers(ctx, 10)
		if running, _ := r.h.store.ListSuperAgentTicketsByStatus(ctx, core.SuperAgentTicketRunning); len(running) == 1 && evaluating == 0 && len(pending) == 0 {
			return running[0]
		}
		time.Sleep(time.Millisecond)
	}
	r.h.t.Fatal("ticket never ran")
	return core.SuperAgentTicket{}
}

func (r *progressRun) advance(d time.Duration) {
	r.clockMu.Lock()
	defer r.clockMu.Unlock()
	*r.h.clock = r.h.clock.Add(d)
}

func (r *progressRun) pass() { r.h.service.progressPass(context.Background()) }

func (r *progressRun) finish() {
	close(r.release)
	r.h.settle()
	r.pass()
}

func (r *progressRun) progress(t *testing.T) *core.SuperAgentProgress {
	t.Helper()
	for _, trigger := range r.h.store.triggers {
		if trigger.Progress != nil {
			return trigger.Progress
		}
	}
	t.Fatal("no request carries progress")
	return nil
}

func TestTextProgressEditsTheReplyAndMarksTheRequest(t *testing.T) {
	run := progressHarness(t, "")
	ticket := run.start("m1")

	// The router's reply is the status message, not a reply of its own.
	if sent := run.h.replies.sentTo(`{"channel_id":"ch1","chat_id":"oc_team","message_id":"m1"}`); len(sent) != 0 {
		t.Fatalf("the reply went out on its own as well: %q", sent)
	}
	if len(run.stub.posts) != 1 || run.stub.cards[0] || !strings.HasPrefix(run.stub.posts[0], "on it\n\n") || !strings.ContainsAny(run.stub.posts[0], "⏳🕒") {
		t.Fatalf("posts = %q cards = %v", run.stub.posts, run.stub.cards)
	}

	run.pass()
	if got := run.stub.liveEmojis(); !slices.Equal(got, []string{"OnIt"}) {
		t.Fatalf("while running the request shows %v", got)
	}
	// Still working: a text message is not edited again within 2 minutes.
	if _, updates := run.stub.counts(); updates != 0 {
		t.Fatalf("edited too soon: %q", run.stub.updates)
	}

	run.h.service.mu.Lock()
	run.h.service.activities[ticket.ID] = activity{at: *run.h.clock, steps: 4, tool: toolKind("mcp__Claude_Browser__navigate")}
	run.h.service.mu.Unlock()
	run.advance(3 * time.Minute)
	run.pass()
	if got := run.stub.lastUpdate(); !strings.Contains(got, "进行中 · 已用 3 分钟 · 4 步 · 最近：浏览网页（3 分钟前）") || !strings.HasPrefix(got, "on it\n\n") {
		t.Fatalf("refresh = %q", got)
	}

	run.finish()
	if got := run.stub.liveEmojis(); !slices.Equal(got, []string{"DONE"}) {
		t.Fatalf("after the work the request shows %v; events %v", got, run.stub.reactions)
	}
	if got := run.stub.lastUpdate(); !strings.Contains(got, "✅ 已完成 · 用时 3 分钟") {
		t.Fatalf("final = %q", got)
	}
	if !slices.Equal(run.stub.finals, []string{"done"}) {
		t.Fatalf("finals = %v", run.stub.finals)
	}
	// The worker's report still arrives as a reply of its own.
	if sent := run.h.replies.sentTo(`{"channel_id":"ch1","chat_id":"oc_team","message_id":"m1"}`); len(sent) != 1 {
		t.Fatalf("report replies = %q", sent)
	}
	progress := run.progress(t)
	if !progress.Final || progress.Phase != phaseDone || progress.Edits != 2 {
		t.Fatalf("progress = %+v", progress)
	}
	// Nothing is left to watch, so later passes change nothing.
	_, before := run.stub.counts()
	run.advance(time.Hour)
	run.pass()
	if _, after := run.stub.counts(); after != before {
		t.Fatal("a finished request was edited again")
	}
}

func TestCardProgressRefreshesEveryMinuteAndShowsAStall(t *testing.T) {
	run := progressHarness(t, core.SuperAgentProgressCard)
	ticket := run.start("m1")
	if len(run.stub.posts) != 1 || !run.stub.cards[0] {
		t.Fatalf("posts = %q cards = %v", run.stub.posts, run.stub.cards)
	}
	run.pass()
	run.advance(time.Minute)
	run.pass()
	if _, updates := run.stub.counts(); updates != 1 {
		t.Fatalf("a card refreshes after a minute; updates = %q", run.stub.updates)
	}

	// Supervision notices a stall: the reaction and the card say so at once.
	run.h.service.mu.Lock()
	run.h.service.activities[ticket.ID] = activity{at: *run.h.clock, steps: 2, tool: "运行命令"}
	run.h.service.mu.Unlock()
	run.advance(25 * time.Minute)
	if err := run.h.service.mutateTicket(context.Background(), ticket.ID, func(current *core.SuperAgentTicket) error {
		current.Signals = append(current.Signals, core.SuperAgentTicketSignal{Kind: core.SuperAgentSignalStalled, Since: *run.h.clock, At: *run.h.clock})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	run.pass()
	if got := run.stub.liveEmojis(); !slices.Equal(got, []string{"Alarm"}) {
		t.Fatalf("a stalled request shows %v", got)
	}
	if got := run.stub.lastUpdate(); !strings.Contains(got, "⚠️ 进行中") || !strings.Contains(got, "25 分钟没有新进展") {
		t.Fatalf("stalled card = %q", got)
	}
}

func TestTextProgressKeepsEditsForTheEnd(t *testing.T) {
	run := progressHarness(t, "")
	ticket := run.start("m1")
	for minute := range 240 {
		run.h.service.mu.Lock()
		run.h.service.activities[ticket.ID] = activity{at: *run.h.clock, steps: minute + 1, tool: "运行命令"}
		run.h.service.mu.Unlock()
		run.advance(time.Minute)
		run.pass()
	}
	if _, updates := run.stub.counts(); updates != textRefreshes {
		t.Fatalf("refreshes over four hours = %d, want %d", updates, textRefreshes)
	}
	run.finish()
	if got := run.stub.lastUpdate(); !strings.Contains(got, "✅ 已完成") {
		t.Fatalf("the end was not shown: %q", got)
	}
	if edits := run.progress(t).Edits; edits > textEditLimit {
		t.Fatalf("edits = %d, more than Feishu allows", edits)
	}
}

func TestStoppedWorkTakesTheReactionOff(t *testing.T) {
	run := progressHarness(t, "")
	ticket := run.start("m1")
	run.pass()
	if err := run.h.service.StopTicket(context.Background(), ticket.ID); err != nil {
		t.Fatal(err)
	}
	run.h.settle()
	run.pass()
	if got := run.stub.liveEmojis(); len(got) != 0 {
		t.Fatalf("a stopped request still shows %v", got)
	}
	if got := run.stub.lastUpdate(); !strings.Contains(got, "⏹ 已停止") {
		t.Fatalf("stopped = %q", got)
	}
}

func TestProgressStylesWithoutAMessage(t *testing.T) {
	const target = `{"channel_id":"ch1","chat_id":"oc_team","message_id":"m1"}`
	t.Run("reaction", func(t *testing.T) {
		run := progressHarness(t, core.SuperAgentProgressReaction)
		run.start("m1")
		run.pass()
		if posts, _ := run.stub.counts(); posts != 0 {
			t.Fatalf("posts = %q", run.stub.posts)
		}
		if sent := run.h.replies.sentTo(target); !slices.Equal(sent, []string{"on it"}) {
			t.Fatalf("replies = %q", sent)
		}
		if got := run.stub.liveEmojis(); !slices.Equal(got, []string{"OnIt"}) {
			t.Fatalf("reactions = %v", got)
		}
	})
	t.Run("off", func(t *testing.T) {
		run := progressHarness(t, core.SuperAgentProgressOff)
		run.start("m1")
		run.pass()
		if posts, _ := run.stub.counts(); posts != 0 || len(run.stub.reactions) != 0 {
			t.Fatalf("posts = %q reactions = %v", run.stub.posts, run.stub.reactions)
		}
		if sent := run.h.replies.sentTo(target); !slices.Equal(sent, []string{"on it"}) {
			t.Fatalf("replies = %q", sent)
		}
	})
	t.Run("post refused", func(t *testing.T) {
		run := progressHarness(t, core.SuperAgentProgressCard)
		run.stub.postErr = errors.New("no card permission")
		run.start("m1")
		run.pass()
		if sent := run.h.replies.sentTo(target); !slices.Equal(sent, []string{"on it"}) {
			t.Fatalf("the reply was lost: %q", sent)
		}
		if got := run.stub.liveEmojis(); !slices.Equal(got, []string{"OnIt"}) {
			t.Fatalf("reactions = %v", got)
		}
	})
}

func TestProgressMessageFreezesWhenEditsAreRefused(t *testing.T) {
	run := progressHarness(t, core.SuperAgentProgressCard)
	run.start("m1")
	run.stub.updateErr = errors.New("message can only be modified within 14 days")
	for range maxProgressFailures + 2 {
		run.advance(time.Minute)
		run.pass()
	}
	if progress := run.progress(t); !progress.Frozen || progress.Failures != maxProgressFailures {
		t.Fatalf("progress = %+v", progress)
	}
	run.finish()
	if got := run.stub.liveEmojis(); !slices.Equal(got, []string{"DONE"}) {
		t.Fatalf("the reaction stopped following the work: %v", got)
	}
}

func TestNormalizeProgress(t *testing.T) {
	for input, want := range map[string]string{"": "", "text": "", " Card ": "card", "reaction": "reaction", "OFF": "off"} {
		agent := core.SuperAgent{Progress: input}
		if err := normalizeProgress(&agent); err != nil || agent.Progress != want {
			t.Fatalf("%q: got %q, %v", input, agent.Progress, err)
		}
	}
	if err := normalizeProgress(&core.SuperAgent{Progress: "loud"}); err == nil {
		t.Fatal("an unknown style was accepted")
	}
}

func TestToolKindNeverShowsTheInput(t *testing.T) {
	for name, want := range map[string]string{
		"Bash": "运行命令", "Edit": "修改文件", "Read": "查看文件", "Grep": "查看文件",
		"WebFetch": "浏览网页", "mcp__claude-in-chrome__navigate": "浏览网页", "something_else": "使用工具",
	} {
		if got := toolKind(name); got != want {
			t.Errorf("toolKind(%q) = %q, want %q", name, got, want)
		}
	}
}
