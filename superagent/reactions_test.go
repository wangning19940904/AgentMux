package superagent

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
)

// reactorStub records each reaction, and how many replies had been sent when
// it was removed.
type reactorStub struct {
	mu      sync.Mutex
	replies *replyStub
	events  []string
	live    map[string]string
}

func (r *reactorStub) AddChatReaction(_ context.Context, channelID, messageID string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := fmt.Sprintf("r%d", len(r.events))
	r.events = append(r.events, "add "+channelID+"/"+messageID)
	r.live[id] = messageID
	return id
}

func (r *reactorStub) RemoveChatReaction(_ context.Context, channelID, messageID, reactionID string) {
	r.replies.mu.Lock()
	sent := len(r.replies.texts)
	r.replies.mu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.live[reactionID] != messageID {
		r.events = append(r.events, "remove unknown "+reactionID)
		return
	}
	delete(r.live, reactionID)
	r.events = append(r.events, fmt.Sprintf("remove %s/%s after %d replies", channelID, messageID, sent))
}

func (r *reactorStub) snapshot() ([]string, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events), len(r.live)
}

func reactionHarness(t *testing.T, config map[string]string) (*harness, *reactorStub) {
	t.Helper()
	h := feishuHarness(t)
	reactor := &reactorStub{replies: h.replies, live: map[string]string{}}
	h.service.reactor = reactor
	agent := h.agent
	agent.Sources = []core.SuperAgentSource{{ID: "msg", Kind: core.SuperAgentSourceFeishuMessage, Enabled: true, AllowedActors: []string{"ou_alice"}, Config: config}}
	if _, err := h.service.Save(context.Background(), agent); err != nil {
		t.Fatal(err)
	}
	return h, reactor
}

func groupMessage(id, user, text string, mentioned bool) *core.Message {
	return &core.Message{ID: id, SourceMessageID: id, ChannelID: "ch1", ChatID: "oc_team", UserID: user, Text: text, MentionedBot: mentioned, ChatType: "group"}
}

func TestMentionIsMarkedFromIntakeUntilTheRouterReplies(t *testing.T) {
	h, reactor := reactionHarness(t, map[string]string{"channel_id": "ch1", "chat_ids": "oc_team"})
	h.router.answer = func(core.InvocationRequest) (string, error) { return "hello", nil }
	ctx := context.Background()

	if claimed, err := h.service.InterceptMessage(ctx, groupMessage("m1", "ou_alice", "hi", true)); err != nil || !claimed {
		t.Fatalf("claimed=%v err=%v", claimed, err)
	}
	if events, live := reactor.snapshot(); !slices.Equal(events, []string{"add ch1/m1"}) || live != 1 {
		t.Fatalf("before the router: %v (live %d)", events, live)
	}
	h.settle()
	if events, live := reactor.snapshot(); !slices.Equal(events, []string{"add ch1/m1", "remove ch1/m1 after 1 replies"}) || live != 0 {
		t.Fatalf("after the router: %v (live %d)", events, live)
	}

	// A redelivery is not stored again, so its mark comes straight off.
	if _, err := h.service.InterceptMessage(ctx, groupMessage("m1", "ou_alice", "hi", true)); err != nil {
		t.Fatal(err)
	}
	if _, live := reactor.snapshot(); live != 0 {
		t.Fatal("a redelivered mention kept its mark")
	}

	// A sender who is not allowed is recorded but never answered, so it is
	// never marked.
	before, _ := reactor.snapshot()
	if _, err := h.service.InterceptMessage(ctx, groupMessage("m2", "ou_eve", "hi", true)); err != nil {
		t.Fatal(err)
	}
	h.settle()
	if after, _ := reactor.snapshot(); len(after) != len(before) {
		t.Fatalf("a filtered mention was marked: %v", after[len(before):])
	}
}

func TestMentionMarkComesOffWhenTheRouterSaysNothing(t *testing.T) {
	h, reactor := reactionHarness(t, map[string]string{"channel_id": "ch1"})
	h.router.answer = func(core.InvocationRequest) (string, error) { return "", nil }
	if _, err := h.service.InterceptMessage(context.Background(), &core.Message{ID: "d1", SourceMessageID: "d1", ChannelID: "ch1", ChatID: "oc_dm", UserID: "ou_alice", Text: "hi", ChatType: "p2p"}); err != nil {
		t.Fatal(err)
	}
	h.settle()
	if events, live := reactor.snapshot(); !slices.Equal(events, []string{"add ch1/d1", "remove ch1/d1 after 0 replies"}) || live != 0 {
		t.Fatalf("direct message: %v (live %d)", events, live)
	}
}

func TestUnmentionedMessageIsMarkedOnlyWhenTheRouterReplies(t *testing.T) {
	h, reactor := reactionHarness(t, map[string]string{"channel_id": "ch1", "chat_ids": "oc_team", "require_mention": "false"})
	answer := ""
	h.router.answer = func(core.InvocationRequest) (string, error) { return answer, nil }
	ctx := context.Background()

	// The router lets ordinary chatter pass: nothing is marked.
	if _, err := h.service.InterceptMessage(ctx, groupMessage("m1", "ou_alice", "lunch?", false)); err != nil {
		t.Fatal(err)
	}
	h.settle()
	if events, _ := reactor.snapshot(); len(events) != 0 {
		t.Fatalf("an unanswered message was marked: %v", events)
	}

	// The router answers: the message is marked until the reply is out.
	answer = "noted"
	if _, err := h.service.InterceptMessage(ctx, groupMessage("m2", "ou_alice", "the build is red", false)); err != nil {
		t.Fatal(err)
	}
	h.settle()
	if events, live := reactor.snapshot(); !slices.Equal(events, []string{"add ch1/m2", "remove ch1/m2 after 1 replies"}) || live != 0 {
		t.Fatalf("answered message: %v (live %d)", events, live)
	}
}
