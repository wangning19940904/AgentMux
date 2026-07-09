package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- fakes ---

type fakePlatform struct {
	name string

	mu      sync.Mutex
	replies []string
	sends   map[string][]string
	inbound chan<- *Message
	started chan struct{}
}

func newFakePlatform(name string) *fakePlatform {
	return &fakePlatform{name: name, sends: map[string][]string{}, started: make(chan struct{})}
}

func (p *fakePlatform) Name() string { return p.name }
func (p *fakePlatform) Start(ctx context.Context, inbound chan<- *Message) error {
	p.mu.Lock()
	p.inbound = inbound
	p.mu.Unlock()
	close(p.started)
	<-ctx.Done()
	return nil
}
func (p *fakePlatform) Reply(ctx context.Context, msg *Message, text string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.replies = append(p.replies, text)
	return nil
}
func (p *fakePlatform) Send(ctx context.Context, chatID, text string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sends[chatID] = append(p.sends[chatID], text)
	return nil
}
func (p *fakePlatform) Stop(ctx context.Context) error { return nil }

func (p *fakePlatform) push(msg *Message) {
	<-p.started
	p.mu.Lock()
	in := p.inbound
	p.mu.Unlock()
	in <- msg
}

type fakeSession struct {
	id     string
	agent  *fakeAgent
	prefix string
}

func (s *fakeSession) ID() string { return s.id }
func (s *fakeSession) Send(ctx context.Context, text string) (<-chan *Event, error) {
	if s.agent != nil {
		s.agent.mu.Lock()
		s.agent.turns = append(s.agent.turns, text)
		s.agent.mu.Unlock()
	}
	out := make(chan *Event, 2)
	out <- &Event{Type: EventFinal, Text: s.prefix + text, Final: true}
	close(out)
	return out, nil
}
func (s *fakeSession) RespondPermission(ctx context.Context, allow bool) error { return nil }
func (s *fakeSession) Close(ctx context.Context) error                         { return nil }

type scriptedSession struct {
	id     string
	events []*Event
}

func (s *scriptedSession) ID() string { return s.id }
func (s *scriptedSession) Send(ctx context.Context, text string) (<-chan *Event, error) {
	out := make(chan *Event, len(s.events))
	for _, ev := range s.events {
		out <- ev
	}
	close(out)
	return out, nil
}
func (s *scriptedSession) RespondPermission(ctx context.Context, allow bool) error { return nil }
func (s *scriptedSession) Close(ctx context.Context) error                         { return nil }

type fakeAgent struct {
	mu       sync.Mutex
	sessions int
	turns    []string
}

func (a *fakeAgent) Name() string { return "fake" }
func (a *fakeAgent) StartSession(ctx context.Context, workDir string) (AgentSession, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessions++
	return &fakeSession{id: fmt.Sprintf("s%d", a.sessions), agent: a, prefix: "echo: "}, nil
}
func (a *fakeAgent) ListSessions(ctx context.Context) ([]string, error) { return nil, nil }
func (a *fakeAgent) Stop(ctx context.Context) error                     { return nil }

type modelAgent struct {
	mu       sync.Mutex
	last     *modelSession
	sessions int
}

func (a *modelAgent) Name() string { return "model-agent" }
func (a *modelAgent) StartSession(ctx context.Context, workDir string) (AgentSession, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessions++
	s := &modelSession{
		id:             fmt.Sprintf("m%d", a.sessions),
		ModelSelection: NewModelSelection("gpt-5", []string{"gpt-5", "gpt-5-mini"}),
	}
	a.last = s
	return s, nil
}
func (a *modelAgent) ListSessions(ctx context.Context) ([]string, error) { return nil, nil }
func (a *modelAgent) Stop(ctx context.Context) error                     { return nil }

type modelSession struct {
	*ModelSelection
	id    string
	mu    sync.Mutex
	turns []string
}

func (s *modelSession) ID() string                    { return s.id }
func (s *modelSession) ModelSwitchingSupported() bool { return true }
func (s *modelSession) Send(ctx context.Context, text string) (<-chan *Event, error) {
	s.mu.Lock()
	s.turns = append(s.turns, text)
	model := s.CurrentModel()
	s.mu.Unlock()
	out := make(chan *Event, 1)
	out <- &Event{Type: EventFinal, Text: "model:" + model + " " + text, Final: true}
	close(out)
	return out, nil
}
func (s *modelSession) RespondPermission(ctx context.Context, allow bool) error { return nil }
func (s *modelSession) Close(ctx context.Context) error                         { return nil }

func (s *modelSession) turnCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.turns)
}

type fakeStore struct {
	mu       sync.Mutex
	channels []Channel
	triggers []Trigger
	runs     map[string]string // trigger id -> last status
}

func (s *fakeStore) ListChannels(ctx context.Context) ([]Channel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Channel(nil), s.channels...), nil
}
func (s *fakeStore) GetChannel(ctx context.Context, id string) (*Channel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ch := range s.channels {
		if ch.ID == id {
			c := ch
			return &c, nil
		}
	}
	return nil, nil
}
func (s *fakeStore) ListTriggers(ctx context.Context) ([]Trigger, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Trigger(nil), s.triggers...), nil
}
func (s *fakeStore) GetTrigger(ctx context.Context, id string) (*Trigger, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, tr := range s.triggers {
		if tr.ID == id {
			t := tr
			return &t, nil
		}
	}
	return nil, nil
}
func (s *fakeStore) UpdateTriggerRun(ctx context.Context, id string, lastRun time.Time, status, errMsg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runs == nil {
		s.runs = map[string]string{}
	}
	s.runs[id] = status
	return nil
}
func (s *fakeStore) GetAgentInstance(ctx context.Context, id string) (*AgentInstance, error) {
	return nil, nil
}
func (s *fakeStore) ActiveProviderRoutes(ctx context.Context) ([]ProviderRoute, error) {
	return nil, nil
}
func (s *fakeStore) GetProvider(ctx context.Context, id string) (*Provider, error) {
	return nil, nil
}

func (s *fakeStore) lastStatus(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runs[id]
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

// --- tests ---

func TestChannelMessageRouting(t *testing.T) {
	eng := NewEngine(nil, NewHookRunner(nil, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Start(ctx) }()

	plat := newFakePlatform("fake")
	restore := stubPlatformFactory(t, "fake-chan", plat)
	defer restore()

	agent := &fakeAgent{}
	ch := Channel{ID: "c1", Name: "ops", Type: "fake-chan", Enabled: true, UpdatedAt: time.Now()}
	if err := eng.AttachChannel(ctx, ch, agent, ""); err != nil {
		t.Fatal(err)
	}

	plat.push(&Message{ChatID: "chat-9", UserID: "u1", Text: "hello", Platform: "fake"})

	waitFor(t, "reply", func() bool {
		plat.mu.Lock()
		defer plat.mu.Unlock()
		return len(plat.replies) == 1
	})
	if plat.replies[0] != "echo: hello" {
		t.Fatalf("reply = %q", plat.replies[0])
	}

	statuses := eng.ChannelStatuses()
	if len(statuses) != 1 || statuses[0].State != ChannelStateRunning {
		t.Fatalf("statuses = %+v", statuses)
	}

	// Same chat reuses the session; a second chat creates a new one.
	plat.push(&Message{ChatID: "chat-9", Text: "again", Platform: "fake"})
	plat.push(&Message{ChatID: "chat-10", Text: "other", Platform: "fake"})
	waitFor(t, "three replies", func() bool {
		plat.mu.Lock()
		defer plat.mu.Unlock()
		return len(plat.replies) == 3
	})
	agent.mu.Lock()
	sessions := agent.sessions
	agent.mu.Unlock()
	if sessions != 2 {
		t.Fatalf("sessions = %d, want 2", sessions)
	}

	eng.DetachChannel("c1")
	if got := eng.ChannelStatuses(); len(got) != 0 {
		t.Fatalf("after detach: %+v", got)
	}
}

func TestChannelMessageDeduplicatesMessageID(t *testing.T) {
	eng := NewEngine(nil, NewHookRunner(nil, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Start(ctx) }()

	plat := newFakePlatform("fake")
	restore := stubPlatformFactory(t, "fake-dedup", plat)
	defer restore()

	agent := &fakeAgent{}
	ch := Channel{ID: "c1", Name: "ops", Type: "fake-dedup", Enabled: true, UpdatedAt: time.Now()}
	if err := eng.AttachChannel(ctx, ch, agent, ""); err != nil {
		t.Fatal(err)
	}

	plat.push(&Message{ID: "m1", ChatID: "chat-9", UserID: "u1", Text: "hello", Platform: "fake"})
	waitFor(t, "first reply", func() bool {
		plat.mu.Lock()
		defer plat.mu.Unlock()
		return len(plat.replies) == 1
	})

	plat.push(&Message{ID: "m1", ChatID: "chat-9", UserID: "u1", Text: "hello", Platform: "fake"})
	time.Sleep(150 * time.Millisecond)
	plat.mu.Lock()
	replies := len(plat.replies)
	plat.mu.Unlock()
	if replies != 1 {
		t.Fatalf("duplicate reply count = %d, want 1", replies)
	}

	plat.push(&Message{ID: "m2", ChatID: "chat-9", UserID: "u1", Text: "hello", Platform: "fake"})
	waitFor(t, "second unique reply", func() bool {
		plat.mu.Lock()
		defer plat.mu.Unlock()
		return len(plat.replies) == 2
	})
	agent.mu.Lock()
	turns := append([]string(nil), agent.turns...)
	agent.mu.Unlock()
	if len(turns) != 2 {
		t.Fatalf("turns = %+v, want two unique message turns", turns)
	}
}

func TestChannelModelCommandSwitchesSessionModelWithoutSendingTurn(t *testing.T) {
	eng := NewEngine(nil, NewHookRunner(nil, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Start(ctx) }()

	plat := newFakePlatform("fake")
	restore := stubPlatformFactory(t, "fake-model", plat)
	defer restore()

	agent := &modelAgent{}
	ch := Channel{ID: "c1", Name: "ops", Type: "fake-model", Enabled: true, UpdatedAt: time.Now()}
	if err := eng.AttachChannel(ctx, ch, agent, ""); err != nil {
		t.Fatal(err)
	}

	plat.push(&Message{ID: "m1", ChatID: "chat-1", Text: "/model", Platform: "fake"})
	waitFor(t, "model status reply", func() bool {
		plat.mu.Lock()
		defer plat.mu.Unlock()
		return len(plat.replies) == 1
	})
	agent.mu.Lock()
	sess := agent.last
	agent.mu.Unlock()
	if sess == nil {
		t.Fatal("model command did not create a session")
	}
	if sess.turnCount() != 0 {
		t.Fatalf("model status reached Send: turns=%d", sess.turnCount())
	}

	plat.push(&Message{ID: "m2", ChatID: "chat-1", Text: "/model gpt-5-mini", Platform: "fake"})
	waitFor(t, "model switch reply", func() bool {
		plat.mu.Lock()
		defer plat.mu.Unlock()
		return len(plat.replies) == 2
	})
	if got := sess.CurrentModel(); got != "gpt-5-mini" {
		t.Fatalf("current model = %q", got)
	}
	if sess.turnCount() != 0 {
		t.Fatalf("model switch reached Send: turns=%d", sess.turnCount())
	}

	plat.push(&Message{ID: "m3", ChatID: "chat-1", Text: "hello", Platform: "fake"})
	waitFor(t, "normal reply", func() bool {
		plat.mu.Lock()
		defer plat.mu.Unlock()
		return len(plat.replies) == 3
	})
	if sess.turnCount() != 1 {
		t.Fatalf("normal message turns = %d", sess.turnCount())
	}
	plat.mu.Lock()
	normalReply := plat.replies[2]
	plat.mu.Unlock()
	if normalReply != "model:gpt-5-mini hello" {
		t.Fatalf("normal reply = %q", normalReply)
	}

	plat.push(&Message{ID: "m4", ChatID: "chat-1", Text: "/model missing", Platform: "fake"})
	waitFor(t, "invalid model reply", func() bool {
		plat.mu.Lock()
		defer plat.mu.Unlock()
		return len(plat.replies) == 4
	})
	plat.mu.Lock()
	invalidReply := plat.replies[3]
	plat.mu.Unlock()
	if !strings.Contains(invalidReply, "not supported") || sess.turnCount() != 1 {
		t.Fatalf("invalid model reply=%q turns=%d", invalidReply, sess.turnCount())
	}

	plat.push(&Message{ID: "m5", ChatID: "chat-1", Text: "/model reset", Platform: "fake"})
	waitFor(t, "model reset reply", func() bool {
		plat.mu.Lock()
		defer plat.mu.Unlock()
		return len(plat.replies) == 5
	})
	if got := sess.CurrentModel(); got != "gpt-5" {
		t.Fatalf("reset current model = %q", got)
	}
	if sess.turnCount() != 1 {
		t.Fatalf("model reset reached Send: turns=%d", sess.turnCount())
	}
}

func TestStreamTurnSkipsDuplicateOutputAndFinal(t *testing.T) {
	eng := NewEngine(nil, NewHookRunner(nil, nil))
	sess := &scriptedSession{
		id: "scripted",
		events: []*Event{
			{Type: EventOutput, Text: "same answer"},
			{Type: EventFinal, Text: "same answer", Final: true},
		},
	}
	var replies []string
	result, err := eng.streamTurn(context.Background(), sess, "hello", func(text string) {
		replies = append(replies, text)
	}, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if result != "same answer" {
		t.Fatalf("result = %q", result)
	}
	if len(replies) != 1 || replies[0] != "same answer" {
		t.Fatalf("replies = %+v, want one deduplicated reply", replies)
	}
}

// streamingPlatform is a fakePlatform that also implements StreamReplier, so
// the engine should render channel turns as one in-place updating message.
type streamingPlatform struct {
	*fakePlatform
	mu               sync.Mutex
	cardUpdates      []string
	cardDoneText     string
	cardDoneCalls    int
	messageUpdates   []string
	messageDoneText  string
	messageDoneCalls int
	beginErr         error
	reactionErr      error
	deleteErr        error
	addedReactions   []string
	deletedReactions []string
}

func newStreamingPlatform(name string) *streamingPlatform {
	return &streamingPlatform{fakePlatform: newFakePlatform(name)}
}

func (p *streamingPlatform) BeginReply(ctx context.Context, msg *Message) (ReplyStream, error) {
	if p.beginErr != nil {
		return nil, p.beginErr
	}
	return &fakeReplyStream{parent: p, kind: "card"}, nil
}

func (p *streamingPlatform) BeginMessageReply(ctx context.Context, msg *Message) (ReplyStream, error) {
	if p.beginErr != nil {
		return nil, p.beginErr
	}
	return &fakeReplyStream{parent: p, kind: "message"}, nil
}

func (p *streamingPlatform) AddReaction(ctx context.Context, msg *Message, emojiType string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.reactionErr != nil {
		return "", p.reactionErr
	}
	p.addedReactions = append(p.addedReactions, emojiType)
	return fmt.Sprintf("reaction-%d", len(p.addedReactions)), nil
}

func (p *streamingPlatform) DeleteReaction(ctx context.Context, msg *Message, reactionID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.deleteErr != nil {
		return p.deleteErr
	}
	p.deletedReactions = append(p.deletedReactions, reactionID)
	return nil
}

type fakeReplyStream struct {
	parent *streamingPlatform
	kind   string
}

func (s *fakeReplyStream) Update(ctx context.Context, text string, done, failed bool) error {
	s.parent.mu.Lock()
	defer s.parent.mu.Unlock()
	if s.kind == "message" {
		s.parent.messageUpdates = append(s.parent.messageUpdates, text)
		if done {
			s.parent.messageDoneText = text
			s.parent.messageDoneCalls++
		}
		return nil
	}
	s.parent.cardUpdates = append(s.parent.cardUpdates, text)
	if done {
		s.parent.cardDoneText = text
		s.parent.cardDoneCalls++
	}
	return nil
}
func (s *fakeReplyStream) Close(ctx context.Context) error { return nil }

func TestChannelMessagePrefersStreamingCard(t *testing.T) {
	eng := NewEngine(nil, NewHookRunner(nil, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Start(ctx) }()

	plat := newStreamingPlatform("fake")
	restore := stubPlatformFactory(t, "fake-stream", plat)
	defer restore()

	agent := &fakeAgent{}
	ch := Channel{
		ID: "c1", Name: "ops", Type: "fake-stream", Enabled: true, UpdatedAt: time.Now(),
		Config: map[string]string{ChannelConfigReplyMode: ReplyModeStreamCard},
	}
	if err := eng.AttachChannel(ctx, ch, agent, ""); err != nil {
		t.Fatal(err)
	}

	plat.push(&Message{ChatID: "chat-1", Text: "hello", Platform: "fake"})

	waitFor(t, "streaming finalize", func() bool {
		plat.mu.Lock()
		defer plat.mu.Unlock()
		return plat.cardDoneCalls == 1
	})

	plat.mu.Lock()
	doneText := plat.cardDoneText
	plat.mu.Unlock()
	if doneText != "echo: hello" {
		t.Fatalf("final card text = %q, want %q", doneText, "echo: hello")
	}

	// The streaming path must not post plain-text replies.
	plat.fakePlatform.mu.Lock()
	replies := len(plat.fakePlatform.replies)
	plat.fakePlatform.mu.Unlock()
	if replies != 0 {
		t.Fatalf("plain replies = %d, want 0 (streaming path)", replies)
	}
}

func TestChannelMessageDefaultsToStreamingMessage(t *testing.T) {
	eng := NewEngine(nil, NewHookRunner(nil, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Start(ctx) }()

	plat := newStreamingPlatform("fake")
	restore := stubPlatformFactory(t, "fake-message-stream", plat)
	defer restore()

	ch := Channel{ID: "c1", Name: "ops", Type: "fake-message-stream", Enabled: true, UpdatedAt: time.Now()}
	if err := eng.AttachChannel(ctx, ch, &fakeAgent{}, ""); err != nil {
		t.Fatal(err)
	}

	plat.push(&Message{ChatID: "chat-1", Text: "hello", Platform: "fake"})

	waitFor(t, "message stream finalize", func() bool {
		plat.mu.Lock()
		defer plat.mu.Unlock()
		return plat.messageDoneCalls == 1
	})

	plat.mu.Lock()
	messageDoneText := plat.messageDoneText
	cardDoneCalls := plat.cardDoneCalls
	plat.mu.Unlock()
	if messageDoneText != "echo: hello" {
		t.Fatalf("final message text = %q, want %q", messageDoneText, "echo: hello")
	}
	if cardDoneCalls != 0 {
		t.Fatalf("card stream calls = %d, want 0", cardDoneCalls)
	}
}

func TestFeishuLikeChannelReplyScopeFiltersMessages(t *testing.T) {
	eng := NewEngine(nil, NewHookRunner(nil, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Start(ctx) }()

	plat := newStreamingPlatform("fake")
	restore := stubPlatformFactory(t, "feishu", plat)
	defer restore()

	agent := &fakeAgent{}
	ch := Channel{ID: "c1", Name: "ops", Type: "feishu", Enabled: true, UpdatedAt: time.Now()}
	if err := eng.AttachChannel(ctx, ch, agent, ""); err != nil {
		t.Fatal(err)
	}

	plat.push(&Message{ID: "m1", ChatID: "chat-1", ChatType: "group", Text: "group", Platform: "fake"})
	time.Sleep(150 * time.Millisecond)
	if got := currentMessageDoneCalls(plat); got != 0 {
		t.Fatalf("group without mention replies = %d, want 0", got)
	}

	plat.push(&Message{ID: "m2", ChatID: "chat-2", ChatType: "p2p", Text: "dm", Platform: "fake"})
	waitFor(t, "dm accepted", func() bool { return currentMessageDoneCalls(plat) == 1 })

	plat.push(&Message{ID: "m3", ChatID: "chat-3", ChatType: "topic_group", Text: "topic", MentionedBot: true, Platform: "fake"})
	waitFor(t, "topic mention accepted", func() bool { return currentMessageDoneCalls(plat) == 2 })

	eng.DetachChannel("c1")

	platAll := newStreamingPlatform("fake")
	restoreAll := stubPlatformFactory(t, "lark", platAll)
	defer restoreAll()
	chAll := Channel{
		ID: "c2", Name: "all", Type: "lark", Enabled: true, UpdatedAt: time.Now(),
		Config: map[string]string{ChannelConfigReplyScope: ReplyScopeAll},
	}
	if err := eng.AttachChannel(ctx, chAll, &fakeAgent{}, ""); err != nil {
		t.Fatal(err)
	}
	platAll.push(&Message{ID: "m4", ChatID: "chat-4", ChatType: "group", Text: "all", Platform: "fake"})
	waitFor(t, "all scope accepted", func() bool { return currentMessageDoneCalls(platAll) == 1 })
	eng.DetachChannel("c2")

	platMentions := newStreamingPlatform("fake")
	restoreMentions := stubPlatformFactory(t, "feishu", platMentions)
	defer restoreMentions()
	chMentions := Channel{
		ID: "c3", Name: "mentions", Type: "feishu", Enabled: true, UpdatedAt: time.Now(),
		Config: map[string]string{ChannelConfigReplyScope: ReplyScopeMentionsOnly},
	}
	if err := eng.AttachChannel(ctx, chMentions, &fakeAgent{}, ""); err != nil {
		t.Fatal(err)
	}
	platMentions.push(&Message{ID: "m5", ChatID: "chat-5", ChatType: "p2p", Text: "dm", Platform: "fake"})
	time.Sleep(150 * time.Millisecond)
	if got := currentMessageDoneCalls(platMentions); got != 0 {
		t.Fatalf("mentions_only dm replies = %d, want 0", got)
	}
	platMentions.push(&Message{ID: "m6", ChatID: "chat-6", ChatType: "group", Text: "mention", MentionedBot: true, Platform: "fake"})
	waitFor(t, "mentions_only accepted", func() bool { return currentMessageDoneCalls(platMentions) == 1 })
}

func TestFeishuLikeChannelAckReactionLifecycle(t *testing.T) {
	eng := NewEngine(nil, NewHookRunner(nil, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Start(ctx) }()

	plat := newStreamingPlatform("fake")
	restore := stubPlatformFactory(t, "feishu", plat)
	defer restore()

	ch := Channel{
		ID: "c1", Name: "ops", Type: "feishu", Enabled: true, UpdatedAt: time.Now(),
		Config: map[string]string{
			ChannelConfigReplyScope:        ReplyScopeAll,
			ChannelConfigReplyMode:         ReplyModeStreamMessage,
			ChannelConfigAckReactionEmojis: "OK",
		},
	}
	if err := eng.AttachChannel(ctx, ch, &fakeAgent{}, ""); err != nil {
		t.Fatal(err)
	}

	plat.push(&Message{ID: "m1", ChatID: "chat-1", ChatType: "group", Text: "hello", Platform: "fake"})
	waitFor(t, "reaction deleted", func() bool {
		plat.mu.Lock()
		defer plat.mu.Unlock()
		return len(plat.deletedReactions) == 1
	})

	plat.mu.Lock()
	added := append([]string(nil), plat.addedReactions...)
	deleted := append([]string(nil), plat.deletedReactions...)
	messageDoneText := plat.messageDoneText
	plat.mu.Unlock()
	if len(added) != 1 || added[0] != "OK" {
		t.Fatalf("added reactions = %+v, want [OK]", added)
	}
	if len(deleted) != 1 || deleted[0] != "reaction-1" {
		t.Fatalf("deleted reactions = %+v, want [reaction-1]", deleted)
	}
	if messageDoneText != "echo: hello" {
		t.Fatalf("final message text = %q", messageDoneText)
	}
}

func TestFeishuLikeChannelAckReactionErrorDoesNotBlockReply(t *testing.T) {
	eng := NewEngine(nil, NewHookRunner(nil, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Start(ctx) }()

	plat := newStreamingPlatform("fake")
	plat.reactionErr = errors.New("reaction denied")
	restore := stubPlatformFactory(t, "feishu", plat)
	defer restore()

	ch := Channel{
		ID: "c1", Name: "ops", Type: "feishu", Enabled: true, UpdatedAt: time.Now(),
		Config: map[string]string{
			ChannelConfigReplyScope:        ReplyScopeAll,
			ChannelConfigAckReactionEmojis: "OK",
		},
	}
	if err := eng.AttachChannel(ctx, ch, &fakeAgent{}, ""); err != nil {
		t.Fatal(err)
	}

	plat.push(&Message{ID: "m1", ChatID: "chat-1", ChatType: "group", Text: "hello", Platform: "fake"})
	waitFor(t, "reply despite reaction error", func() bool { return currentMessageDoneCalls(plat) == 1 })

	plat.mu.Lock()
	added := len(plat.addedReactions)
	deleted := len(plat.deletedReactions)
	messageDoneText := plat.messageDoneText
	plat.mu.Unlock()
	if added != 0 || deleted != 0 {
		t.Fatalf("reaction lifecycle = added %d deleted %d, want no stored reaction", added, deleted)
	}
	if messageDoneText != "echo: hello" {
		t.Fatalf("final message text = %q", messageDoneText)
	}
}

func currentMessageDoneCalls(p *streamingPlatform) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.messageDoneCalls
}

func TestExecuteTriggerPushesToChannel(t *testing.T) {
	eng := NewEngine(nil, NewHookRunner(nil, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	plat := newFakePlatform("fake")
	restore := stubPlatformFactory(t, "fake-trig", plat)
	defer restore()

	agent := &fakeAgent{}
	ch := Channel{ID: "c1", Name: "ops", Type: "fake-trig", Enabled: true, UpdatedAt: time.Now()}
	if err := eng.AttachChannel(ctx, ch, agent, ""); err != nil {
		t.Fatal(err)
	}

	tr := Trigger{
		ID: "t1", Name: "daily", Kind: TriggerCron,
		ChannelID: "c1", ChatID: "chat-1", Prompt: "summarize",
		SessionMode: SessionModeNewPerRun,
	}
	result, err := eng.ExecuteTrigger(ctx, tr, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if result != "echo: summarize" {
		t.Fatalf("result = %q", result)
	}
	plat.mu.Lock()
	sent := plat.sends["chat-1"]
	plat.mu.Unlock()
	if len(sent) != 1 || sent[0] != "echo: summarize" {
		t.Fatalf("sends = %+v", sent)
	}

	// Webhook input is appended to the prompt.
	tr2 := Trigger{ID: "t2", Name: "hook", Kind: TriggerWebhook, ChannelID: "c1", ChatID: "chat-1", Prompt: "review"}
	if _, err := eng.ExecuteTrigger(ctx, tr2, nil, "", "payload body"); err != nil {
		t.Fatal(err)
	}
	agent.mu.Lock()
	turns := append([]string(nil), agent.turns...)
	agent.mu.Unlock()
	last := turns[len(turns)-1]
	if last != "review\n\npayload body" {
		t.Fatalf("turn = %q", last)
	}

	// Missing prompt errors.
	if _, err := eng.ExecuteTrigger(ctx, Trigger{ID: "t3", Name: "empty", Kind: TriggerCron, ChannelID: "c1"}, nil, "", ""); err == nil {
		t.Fatal("expected error for empty prompt")
	}
}

func TestSchedulerSync(t *testing.T) {
	var mu sync.Mutex
	fired := map[string]int{}
	s := NewScheduler(nil, func(id string) {
		mu.Lock()
		fired[id]++
		mu.Unlock()
	})
	triggers := []Trigger{
		{ID: "a", Kind: TriggerCron, Enabled: true, CronExpr: "* * * * *"},
		{ID: "b", Kind: TriggerCron, Enabled: false, CronExpr: "* * * * *"},
		{ID: "c", Kind: TriggerWebhook, Enabled: true},
		{ID: "d", Kind: TriggerCron, Enabled: true, CronExpr: "not a cron"},
	}
	s.Sync(triggers)
	if got := s.Scheduled(); got != 1 {
		t.Fatalf("scheduled = %d, want 1", got)
	}
	// Change expression: entry is replaced, count stays 1.
	triggers[0].CronExpr = "*/5 * * * *"
	s.Sync(triggers)
	if got := s.Scheduled(); got != 1 {
		t.Fatalf("after change = %d, want 1", got)
	}
	// Disable: entry removed.
	triggers[0].Enabled = false
	s.Sync(triggers)
	if got := s.Scheduled(); got != 0 {
		t.Fatalf("after disable = %d, want 0", got)
	}
}

func TestValidateCronExpr(t *testing.T) {
	if err := ValidateCronExpr("0 9 * * *"); err != nil {
		t.Fatalf("valid expr rejected: %v", err)
	}
	if err := ValidateCronExpr("banana"); err == nil {
		t.Fatal("invalid expr accepted")
	}
}

func TestEventTriggerDispatch(t *testing.T) {
	received := make(chan map[string][]string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- map[string][]string{"event": {r.Header.Get("X-Hook-Event")}}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	st := &fakeStore{triggers: []Trigger{
		{ID: "ev1", Name: "on error", Kind: TriggerEvent, Enabled: true,
			Event: string(HookError), ActionType: ActionHTTP, ActionTarget: srv.URL},
		{ID: "ev2", Name: "other channel", Kind: TriggerEvent, Enabled: true,
			Event: string(HookError), ChannelID: "other", ActionType: ActionHTTP, ActionTarget: srv.URL},
	}}
	eng := NewEngine(nil, NewHookRunner(nil, nil))
	svc := NewConnectService(nil, eng, st)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = svc // sink registered in constructor

	eng.emit(ctx, HookError, map[string]string{"channel_id": "c1", "error": "boom"})

	select {
	case got := <-received:
		if got["event"][0] != string(HookError) {
			t.Fatalf("event header = %+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("event trigger did not fire")
	}
	// The channel-filtered trigger must not fire (channel_id mismatch).
	select {
	case <-received:
		t.Fatal("channel-filtered trigger fired unexpectedly")
	case <-time.After(300 * time.Millisecond):
	}
	waitFor(t, "run bookkeeping", func() bool { return st.lastStatus("ev1") == "ok" })
}

// stubPlatformFactory registers a factory returning the given platform and
// returns a cleanup that unregisters it.
func stubPlatformFactory(t *testing.T, name string, p Platform) func() {
	t.Helper()
	regMu.Lock()
	old, hadOld := platforms[name]
	platforms[name] = func(cfg map[string]any) (Platform, error) { return p, nil }
	regMu.Unlock()
	return func() {
		regMu.Lock()
		if hadOld {
			platforms[name] = old
		} else {
			delete(platforms, name)
		}
		regMu.Unlock()
	}
}
