package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
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

type modelPickerPlatform struct {
	*fakePlatform
	modelCards []ModelPickerState
}

type runtimeSettingsPickerPlatform struct {
	*fakePlatform
	pickerMu          sync.Mutex
	cards             []RuntimeSettingsPickerState
	updates           []RuntimeSettingsPickerState
	updatedMessageIDs []string
}

func newRuntimeSettingsPickerPlatform(name string) *runtimeSettingsPickerPlatform {
	return &runtimeSettingsPickerPlatform{fakePlatform: newFakePlatform(name)}
}

func (p *runtimeSettingsPickerPlatform) ReplyRuntimeSettingsPicker(ctx context.Context, msg *Message, state RuntimeSettingsPickerState) error {
	p.pickerMu.Lock()
	defer p.pickerMu.Unlock()
	p.cards = append(p.cards, state)
	return nil
}

func (p *runtimeSettingsPickerPlatform) UpdateRuntimeSettingsPicker(ctx context.Context, msg *Message, state RuntimeSettingsPickerState) error {
	p.pickerMu.Lock()
	defer p.pickerMu.Unlock()
	messageID := msg.InteractionMessageID
	if messageID == "" {
		messageID = msg.ID
	}
	p.updatedMessageIDs = append(p.updatedMessageIDs, messageID)
	p.updates = append(p.updates, state)
	return nil
}

type fakeRuntimeDefaultsStore struct {
	mu       sync.Mutex
	agentID  string
	settings RuntimeSettings
}

func (s *fakeRuntimeDefaultsStore) UpdateAgentRuntimeSettings(ctx context.Context, id string, settings RuntimeSettings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agentID = id
	s.settings = settings
	return nil
}

func newModelPickerPlatform(name string) *modelPickerPlatform {
	return &modelPickerPlatform{fakePlatform: newFakePlatform(name)}
}

func (p *modelPickerPlatform) ReplyModelPicker(ctx context.Context, msg *Message, state ModelPickerState) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.modelCards = append(p.modelCards, state)
	return nil
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
	mu           sync.Mutex
	last         *modelSession
	sessions     int
	defaultModel string
	models       []string
}

func (a *modelAgent) Name() string { return "model-agent" }
func (a *modelAgent) StartSession(ctx context.Context, workDir string) (AgentSession, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessions++
	defaultModel := a.defaultModel
	models := a.models
	if models == nil {
		defaultModel = "gpt-5"
		models = []string{"gpt-5", "gpt-5-mini"}
	}
	s := &modelSession{
		id: fmt.Sprintf("m%d", a.sessions),
		settings: NewRuntimeSettingsSelection(
			RuntimeSettings{Model: defaultModel},
			RuntimeSettingsCapabilities{Models: RuntimeOptions(models)},
		),
	}
	a.last = s
	return s, nil
}
func (a *modelAgent) ListSessions(ctx context.Context) ([]string, error) { return nil, nil }
func (a *modelAgent) Stop(ctx context.Context) error                     { return nil }

type modelSession struct {
	settings *RuntimeSettingsSelection
	id       string
	mu       sync.Mutex
	turns    []string
}

func (s *modelSession) ModelSwitchingSupported() bool { return len(s.SupportedModels()) > 0 }
func (s *modelSession) CurrentModel() string          { return s.settings.CurrentRuntimeSettings().Model }
func (s *modelSession) DefaultModel() string          { return s.settings.DefaultRuntimeSettings().Model }
func (s *modelSession) SupportedModels() []string {
	options := s.settings.RuntimeSettingsCapabilities().Models
	models := make([]string, 0, len(options))
	for _, option := range options {
		models = append(models, option.Value)
	}
	return models
}
func (s *modelSession) SetModel(model string) error {
	return s.settings.SetRuntimeSetting(RuntimeSettingModel, model)
}
func (s *modelSession) ResetModel() error {
	return s.settings.ResetRuntimeSetting(RuntimeSettingModel)
}

type approvalAgent struct {
	mu   sync.Mutex
	last *approvalSession
}

func (a *approvalAgent) Name() string { return "approval-agent" }
func (a *approvalAgent) StartSession(context.Context, string) (AgentSession, error) {
	s := &approvalSession{settings: NewRuntimeSettingsSelection(
		RuntimeSettings{ApprovalMode: ApprovalModeManual},
		RuntimeSettingsCapabilities{ApprovalModes: RuntimeOptions(ApprovalModeValuesForRuntime("codex"))},
	)}
	a.mu.Lock()
	a.last = s
	a.mu.Unlock()
	return s, nil
}
func (a *approvalAgent) ListSessions(context.Context) ([]string, error) { return nil, nil }
func (a *approvalAgent) Stop(context.Context) error                     { return nil }

type approvalSession struct {
	settings *RuntimeSettingsSelection
	mu       sync.Mutex
	turns    []string
}

func (s *approvalSession) ID() string { return "approval-session" }
func (s *approvalSession) RuntimeSettingsCapabilities() RuntimeSettingsCapabilities {
	return s.settings.RuntimeSettingsCapabilities()
}
func (s *approvalSession) CurrentRuntimeSettings() RuntimeSettings {
	return s.settings.CurrentRuntimeSettings()
}
func (s *approvalSession) DefaultRuntimeSettings() RuntimeSettings {
	return s.settings.DefaultRuntimeSettings()
}
func (s *approvalSession) SetRuntimeSetting(setting RuntimeSetting, value string) error {
	return s.settings.SetRuntimeSetting(setting, value)
}
func (s *approvalSession) ResetRuntimeSetting(setting RuntimeSetting) error {
	return s.settings.ResetRuntimeSetting(setting)
}
func (s *approvalSession) Send(_ context.Context, text string) (<-chan *Event, error) {
	s.mu.Lock()
	s.turns = append(s.turns, text)
	mode := s.CurrentRuntimeSettings().ApprovalMode
	s.mu.Unlock()
	out := make(chan *Event, 1)
	out <- &Event{Type: EventFinal, Text: "mode:" + mode + " " + text, Final: true}
	close(out)
	return out, nil
}
func (s *approvalSession) RespondPermission(context.Context, bool) error { return nil }
func (s *approvalSession) Close(context.Context) error                   { return nil }

func (s *approvalSession) turnCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.turns)
}

func (s *modelSession) ID() string { return s.id }
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
	agents   map[string]AgentInstance
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
func (s *fakeStore) UpsertChannel(_ context.Context, channel *Channel) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index := range s.channels {
		if s.channels[index].ID == channel.ID {
			s.channels[index] = *channel
			return nil
		}
	}
	s.channels = append(s.channels, *channel)
	return nil
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if agent, ok := s.agents[id]; ok {
		copy := agent
		return &copy, nil
	}
	return nil, nil
}
func (s *fakeStore) UpdateAgentRuntimeSettings(ctx context.Context, id string, settings RuntimeSettings) error {
	return nil
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

func TestSetMeetingResponseModePersistsAndHotApplies(t *testing.T) {
	platform := &meetingCommandTestPlatform{fakePlatform: newFakePlatform("feishu")}
	restorePlatform := stubPlatformFactory(t, "feishu", platform)
	defer restorePlatform()

	now := time.Now()
	store := &fakeStore{channels: []Channel{{
		ID: "channel-voice", Name: "Meeting bot", Type: "feishu", Enabled: true, UpdatedAt: now,
		Config: map[string]string{ChannelConfigMeetingTTSAPIKey: "tts-secret"},
	}}}
	engine := NewEngine(nil, NewHookRunner(nil, nil))
	connect := NewConnectService(nil, engine, store)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := connect.Start(ctx); err != nil {
		t.Fatal(err)
	}
	mode, err := connect.SetMeetingResponseMode(ctx, "channel-voice", MeetingResponseModeVoice)
	if err != nil {
		t.Fatal(err)
	}
	if mode != MeetingResponseModeVoice {
		t.Fatalf("mode = %q", mode)
	}
	stored, err := store.GetChannel(ctx, "channel-voice")
	if err != nil || stored == nil {
		t.Fatal(err)
	}
	if ChannelMeetingResponseMode(*stored) != MeetingResponseModeVoice ||
		stored.Config[ChannelConfigMeetingVoice] != "true" ||
		stored.Config[ChannelConfigMeetingReplyMode] != MeetingReplyModeFinal {
		t.Fatalf("stored channel = %+v", stored.Config)
	}
	if platform.configured[ChannelConfigMeetingVoice] != "true" ||
		engine.channelRuntime("channel-voice").currentMeetingResponseMode() != MeetingResponseModeVoice {
		t.Fatalf("hot configuration = %+v", platform.configured)
	}
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

func TestRestartChannelsForAgentRefreshesChangedRuntime(t *testing.T) {
	const (
		platformName = "fake-agent-refresh"
		oldRuntime   = "fake-runtime-old"
		newRuntime   = "fake-runtime-new"
	)
	restorePlatform := stubPlatformFactoryFunc(t, platformName, func(map[string]any) (Platform, error) {
		return newFakePlatform(platformName), nil
	})
	defer restorePlatform()
	restoreOld := stubAgentFactory(t, oldRuntime, func(map[string]any) (Agent, error) {
		return &namedFakeAgent{name: oldRuntime, fakeAgent: &fakeAgent{}}, nil
	})
	defer restoreOld()
	restoreNew := stubAgentFactory(t, newRuntime, func(map[string]any) (Agent, error) {
		return &namedFakeAgent{name: newRuntime, fakeAgent: &fakeAgent{}}, nil
	})
	defer restoreNew()

	now := time.Now()
	store := &fakeStore{
		channels: []Channel{{
			ID: "channel-1", Name: "bot", Type: platformName, AgentID: "agent-1", Enabled: true, UpdatedAt: now,
		}},
		agents: map[string]AgentInstance{
			"agent-1": {ID: "agent-1", Name: "Agent", RuntimeID: oldRuntime, Enabled: true, UpdatedAt: now},
		},
	}
	eng := NewEngine(nil, NewHookRunner(nil, nil))
	connect := NewConnectService(nil, eng, store)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := connect.Start(ctx); err != nil {
		t.Fatal(err)
	}
	before := eng.channelRuntime("channel-1")
	if before == nil || before.agent == nil || before.agent.Name() != oldRuntime {
		t.Fatalf("initial channel runtime = %#v", before)
	}

	store.mu.Lock()
	agent := store.agents["agent-1"]
	agent.RuntimeID = newRuntime
	agent.UpdatedAt = now.Add(time.Second)
	store.agents["agent-1"] = agent
	store.mu.Unlock()
	if err := connect.RestartChannelsForAgent(ctx, "agent-1"); err != nil {
		t.Fatal(err)
	}

	after := eng.channelRuntime("channel-1")
	if after == nil || after == before {
		t.Fatalf("channel runtime was not replaced: before=%p after=%p", before, after)
	}
	if after.agent == nil || after.agent.Name() != newRuntime || after.workspace.RuntimeID != newRuntime {
		t.Fatalf("refreshed channel still uses stale runtime: agent=%v workspace=%+v", after.agent, after.workspace)
	}
}

func TestProjectConversationCommandClearsInMemorySession(t *testing.T) {
	eng := NewEngine(nil, NewHookRunner(nil, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	plat := newFakePlatform("fake")
	agent := &fakeAgent{}
	eng.AddProject("demo", "", agent, []Platform{plat})
	go func() { _ = eng.Start(ctx) }()

	plat.push(&Message{ChatID: "chat-1", Text: "hello", Platform: "fake", Project: "demo"})
	waitFor(t, "first project reply", func() bool {
		plat.mu.Lock()
		defer plat.mu.Unlock()
		return len(plat.replies) == 1
	})

	plat.push(&Message{ChatID: "chat-1", Text: "/new", Platform: "fake", Project: "demo"})
	waitFor(t, "project reset reply", func() bool {
		plat.mu.Lock()
		defer plat.mu.Unlock()
		return len(plat.replies) == 2
	})

	agent.mu.Lock()
	sessionsAfterReset := agent.sessions
	turnsAfterReset := append([]string(nil), agent.turns...)
	agent.mu.Unlock()
	if sessionsAfterReset != 1 {
		t.Fatalf("sessions after /new = %d, want existing session only", sessionsAfterReset)
	}
	if len(turnsAfterReset) != 1 || turnsAfterReset[0] != "hello" {
		t.Fatalf("turns after /new = %+v, command should not reach Send", turnsAfterReset)
	}

	plat.push(&Message{ChatID: "chat-1", Text: "again", Platform: "fake", Project: "demo"})
	waitFor(t, "second project reply", func() bool {
		plat.mu.Lock()
		defer plat.mu.Unlock()
		return len(plat.replies) == 3
	})

	agent.mu.Lock()
	defer agent.mu.Unlock()
	if agent.sessions != 2 {
		t.Fatalf("sessions after next message = %d, want new session", agent.sessions)
	}
	if len(agent.turns) != 2 || agent.turns[1] != "again" {
		t.Fatalf("turns after next message = %+v", agent.turns)
	}
}

func TestChannelConversationCommandClearsInMemorySession(t *testing.T) {
	eng := NewEngine(nil, NewHookRunner(nil, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Start(ctx) }()

	plat := newFakePlatform("fake")
	restore := stubPlatformFactory(t, "fake-clear", plat)
	defer restore()

	agent := &fakeAgent{}
	ch := Channel{ID: "c1", Name: "ops", Type: "fake-clear", Enabled: true, UpdatedAt: time.Now()}
	if err := eng.AttachChannel(ctx, ch, agent, ""); err != nil {
		t.Fatal(err)
	}

	plat.push(&Message{ChatID: "chat-1", Text: "hello", Platform: "fake"})
	waitFor(t, "first channel reply", func() bool {
		plat.mu.Lock()
		defer plat.mu.Unlock()
		return len(plat.replies) == 1
	})

	plat.push(&Message{ChatID: "chat-1", Text: "/clear", Platform: "fake"})
	waitFor(t, "channel reset reply", func() bool {
		plat.mu.Lock()
		defer plat.mu.Unlock()
		return len(plat.replies) == 2
	})

	agent.mu.Lock()
	sessionsAfterReset := agent.sessions
	turnsAfterReset := append([]string(nil), agent.turns...)
	agent.mu.Unlock()
	if sessionsAfterReset != 1 {
		t.Fatalf("sessions after /clear = %d, want existing session only", sessionsAfterReset)
	}
	if len(turnsAfterReset) != 1 || turnsAfterReset[0] != "hello" {
		t.Fatalf("turns after /clear = %+v, command should not reach Send", turnsAfterReset)
	}

	plat.push(&Message{ChatID: "chat-1", Text: "again", Platform: "fake"})
	waitFor(t, "second channel reply", func() bool {
		plat.mu.Lock()
		defer plat.mu.Unlock()
		return len(plat.replies) == 3
	})

	agent.mu.Lock()
	defer agent.mu.Unlock()
	if agent.sessions != 2 {
		t.Fatalf("sessions after next message = %d, want new session", agent.sessions)
	}
	if len(agent.turns) != 2 || agent.turns[1] != "again" {
		t.Fatalf("turns after next message = %+v", agent.turns)
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

func TestChannelLogOnlyCallbackIsLoggedWithoutAgentDispatch(t *testing.T) {
	eng := NewEngine(nil, NewHookRunner(nil, nil))
	logRoot := t.TempDir()
	eng.SetMessageLogger(NewMessageLogger(logRoot))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Start(ctx) }()

	plat := newFakePlatform("fake")
	restore := stubPlatformFactory(t, "fake-callback-log", plat)
	defer restore()

	agent := &fakeAgent{}
	ch := Channel{ID: "c1", Name: "ops", Type: "fake-callback-log", Enabled: true, UpdatedAt: time.Now()}
	if err := eng.AttachChannel(ctx, ch, agent, ""); err != nil {
		t.Fatal(err)
	}

	plat.push(&Message{
		ID: "evt-choice", ChatID: "chat-1", UserID: "ou_actor", Platform: "feishu", LogOnly: true,
		Callback: &CallbackEvent{
			Type: "card.action.trigger", MessageID: "om_card", Host: "im_message", ActionTag: "button",
			ActionName: "choiceA", ActionValue: `{"choice":"option_a","label":"方案 A"}`,
		},
	})

	path := eng.MessageLogger().ChannelLogPath("c1")
	var record map[string]string
	waitFor(t, "callback log", func() bool {
		b, err := os.ReadFile(path)
		if err != nil || len(b) == 0 {
			return false
		}
		return json.Unmarshal(b, &record) == nil
	})
	if record["event_type"] != "card.action.trigger" || record["event_id"] != "evt-choice" || record["message_id"] != "om_card" || record["action_value"] != `{"choice":"option_a","label":"方案 A"}` {
		t.Fatalf("callback log = %+v", record)
	}
	time.Sleep(100 * time.Millisecond)
	agent.mu.Lock()
	sessions := agent.sessions
	turns := append([]string(nil), agent.turns...)
	agent.mu.Unlock()
	plat.mu.Lock()
	replies := len(plat.replies)
	plat.mu.Unlock()
	if sessions != 0 || len(turns) != 0 || replies != 0 {
		t.Fatalf("log-only callback dispatched: sessions=%d turns=%v replies=%d", sessions, turns, replies)
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

func TestChannelModelCommandRendersModelPickerCardWhenSupported(t *testing.T) {
	eng := NewEngine(nil, NewHookRunner(nil, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Start(ctx) }()

	plat := newModelPickerPlatform("fake")
	restore := stubPlatformFactory(t, "fake-model-picker", plat)
	defer restore()

	agent := &modelAgent{}
	ch := Channel{ID: "c1", Name: "ops", Type: "fake-model-picker", Enabled: true, UpdatedAt: time.Now()}
	if err := eng.AttachChannel(ctx, ch, agent, ""); err != nil {
		t.Fatal(err)
	}

	plat.push(&Message{ID: "m1", ChatID: "chat-1", Text: "/model", Platform: "fake"})
	waitFor(t, "model picker card", func() bool {
		plat.mu.Lock()
		defer plat.mu.Unlock()
		return len(plat.modelCards) == 1
	})

	agent.mu.Lock()
	sess := agent.last
	agent.mu.Unlock()
	if sess == nil {
		t.Fatal("model command did not create a session")
	}
	if sess.turnCount() != 0 {
		t.Fatalf("model picker reached Send: turns=%d", sess.turnCount())
	}

	plat.fakePlatform.mu.Lock()
	defer plat.fakePlatform.mu.Unlock()
	if len(plat.replies) != 0 {
		t.Fatalf("plain replies = %+v, want none when picker is supported", plat.replies)
	}
	state := plat.modelCards[0]
	if state.CurrentModel != "gpt-5" || state.DefaultModel != "gpt-5" {
		t.Fatalf("model picker state current=%q default=%q", state.CurrentModel, state.DefaultModel)
	}
	if len(state.Options) != 2 || state.Options[0].Model != "gpt-5" || !state.Options[0].Current || !state.Options[0].Default {
		t.Fatalf("model picker options = %+v", state.Options)
	}
}

func TestRuntimeSettingsCardActionUpdatesOriginalCardWithoutReply(t *testing.T) {
	eng := NewEngine(nil, NewHookRunner(nil, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Start(ctx) }()

	plat := newRuntimeSettingsPickerPlatform("fake")
	restore := stubPlatformFactory(t, "fake-runtime-settings", plat)
	defer restore()
	agent := &modelAgent{}
	ch := Channel{ID: "c1", Name: "ops", Type: "fake-runtime-settings", Enabled: true, UpdatedAt: time.Now()}
	if err := eng.AttachChannel(ctx, ch, agent, ""); err != nil {
		t.Fatal(err)
	}

	plat.push(&Message{ID: "m1", ChatID: "chat-1", Text: "/model", Platform: "fake"})
	waitFor(t, "runtime settings card", func() bool {
		plat.pickerMu.Lock()
		defer plat.pickerMu.Unlock()
		return len(plat.cards) == 1
	})
	plat.push(&Message{ID: "action-1", InteractionMessageID: "picker-1", ChatID: "chat-1", Platform: "fake", RuntimeSettingsAction: &RuntimeSettingsAction{
		Scope: RuntimeSettingsScopeConversation, Setting: RuntimeSettingModel, Value: "gpt-5-mini",
	}})
	waitFor(t, "runtime settings card update", func() bool {
		plat.pickerMu.Lock()
		defer plat.pickerMu.Unlock()
		return len(plat.updates) == 1
	})
	agent.mu.Lock()
	sess := agent.last
	agent.mu.Unlock()
	if got := sess.CurrentModel(); got != "gpt-5-mini" {
		t.Fatalf("current model = %q", got)
	}
	plat.fakePlatform.mu.Lock()
	if len(plat.replies) != 0 {
		plat.fakePlatform.mu.Unlock()
		t.Fatalf("settings action posted replies = %+v, want no confirmation message", plat.replies)
	}
	plat.fakePlatform.mu.Unlock()
	plat.pickerMu.Lock()
	if got := plat.updatedMessageIDs[0]; got != "picker-1" {
		plat.pickerMu.Unlock()
		t.Fatalf("updated message id = %q, want original picker id", got)
	}
	plat.pickerMu.Unlock()
	plat.push(&Message{ID: "action-2", InteractionMessageID: "picker-1", ChatID: "chat-1", Platform: "fake", RuntimeSettingsAction: &RuntimeSettingsAction{
		Scope: RuntimeSettingsScopeConversation, Setting: RuntimeSettingModel, Value: "gpt-5",
	}})
	waitFor(t, "second action updates the same card", func() bool {
		plat.pickerMu.Lock()
		defer plat.pickerMu.Unlock()
		return len(plat.updates) == 2 && plat.updatedMessageIDs[1] == "picker-1"
	})
	if got := sess.CurrentModel(); got != "gpt-5" {
		t.Fatalf("second card action was deduplicated: current model = %q", got)
	}
}

func TestAgentDefaultRuntimeSettingDoesNotOverrideCurrentSession(t *testing.T) {
	eng := NewEngine(nil, NewHookRunner(nil, nil))
	defaultsStore := &fakeRuntimeDefaultsStore{}
	eng.SetRuntimeSettingsDefaultStore(defaultsStore)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Start(ctx) }()

	plat := newRuntimeSettingsPickerPlatform("fake")
	restore := stubPlatformFactory(t, "fake-agent-default-settings", plat)
	defer restore()
	agent := &modelAgent{}
	ch := Channel{ID: "c1", Name: "ops", Type: "fake-agent-default-settings", AgentID: "agent-1", Enabled: true, UpdatedAt: time.Now()}
	if err := eng.AttachChannel(ctx, ch, agent, "", WorkspaceInitOptions{AgentID: "agent-1", RuntimeDefaults: RuntimeSettings{Model: "gpt-5"}}); err != nil {
		t.Fatal(err)
	}

	plat.push(&Message{ID: "m1", ChatID: "chat-1", Text: "/model", Platform: "fake"})
	waitFor(t, "runtime settings card", func() bool {
		plat.pickerMu.Lock()
		defer plat.pickerMu.Unlock()
		return len(plat.cards) == 1
	})
	plat.push(&Message{ID: "action-1", InteractionMessageID: "picker-1", ChatID: "chat-1", Platform: "fake", RuntimeSettingsAction: &RuntimeSettingsAction{
		Scope: RuntimeSettingsScopeAgent, Setting: RuntimeSettingModel, Value: "gpt-5-mini",
	}})
	waitFor(t, "agent default settings update", func() bool {
		defaultsStore.mu.Lock()
		defer defaultsStore.mu.Unlock()
		return defaultsStore.agentID == "agent-1"
	})
	agent.mu.Lock()
	sess := agent.last
	agent.mu.Unlock()
	if got := sess.CurrentModel(); got != "gpt-5" {
		t.Fatalf("current session model = %q, Agent default must not override active session", got)
	}
	defaultsStore.mu.Lock()
	got := defaultsStore.settings
	defaultsStore.mu.Unlock()
	if got.Model != "gpt-5-mini" {
		t.Fatalf("stored Agent defaults = %+v", got)
	}
	plat.pickerMu.Lock()
	updated := plat.updates[len(plat.updates)-1]
	plat.pickerMu.Unlock()
	if !strings.Contains(updated.Hint, "仅新会话生效") || !strings.Contains(updated.Hint, "当前会话未改变") {
		t.Fatalf("Agent-default feedback did not explain scope: %+v", updated)
	}
	plat.push(&Message{ID: "m2", ChatID: "chat-2", Text: "hello", Platform: "fake"})
	waitFor(t, "future session uses Agent default", func() bool {
		plat.fakePlatform.mu.Lock()
		defer plat.fakePlatform.mu.Unlock()
		for _, reply := range plat.replies {
			if reply == "model:gpt-5-mini hello" {
				return true
			}
		}
		return false
	})
}

func TestNewConversationPromptsForApprovalModeBeforeRunningAgent(t *testing.T) {
	eng := NewEngine(nil, NewHookRunner(nil, nil))
	eng.SetConversationStore(&senderConversationStore{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Start(ctx) }()

	plat := newRuntimeSettingsPickerPlatform("fake-approval-prompt")
	restore := stubPlatformFactory(t, "fake-approval-prompt", plat)
	defer restore()
	agent := &approvalAgent{}
	ch := Channel{ID: "c-approval", Name: "approval", Type: "fake-approval-prompt", AgentID: "agent-approval", Enabled: true, UpdatedAt: time.Now()}
	if err := eng.AttachChannel(ctx, ch, agent, t.TempDir(), WorkspaceInitOptions{AgentID: "agent-approval"}); err != nil {
		t.Fatal(err)
	}

	plat.push(&Message{ID: "m1", ChatID: "chat-1", Text: "hello", Platform: "fake-approval-prompt"})
	waitFor(t, "first workspace approval picker", func() bool {
		plat.pickerMu.Lock()
		defer plat.pickerMu.Unlock()
		return len(plat.cards) == 1
	})
	agent.mu.Lock()
	sess := agent.last
	agent.mu.Unlock()
	if sess == nil || sess.turnCount() != 0 {
		t.Fatalf("first prompt reached Agent before approval configuration")
	}
	plat.pickerMu.Lock()
	state := plat.cards[0]
	plat.pickerMu.Unlock()
	if len(state.Capabilities.ApprovalModes) < 2 || !strings.Contains(state.Notice, "首次对话") || !strings.Contains(state.Notice, "自动继续") {
		t.Fatalf("approval picker state = %+v", state)
	}

	plat.push(&Message{ID: "action-1", InteractionMessageID: "picker-1", ChatID: "chat-1", Platform: "fake-approval-prompt", RuntimeSettingsAction: &RuntimeSettingsAction{
		Scope: RuntimeSettingsScopeConversation, Setting: RuntimeSettingApprovalMode, Value: ApprovalModeYolo,
	}})
	waitFor(t, "approval selection resumes original Agent turn", func() bool {
		return sess.CurrentRuntimeSettings().ApprovalMode == ApprovalModeYolo && sess.turnCount() == 1
	})
	plat.pickerMu.Lock()
	updated := plat.updates[len(plat.updates)-1]
	plat.pickerMu.Unlock()
	if !strings.Contains(updated.Hint, "当前会话启用 YOLO") {
		t.Fatalf("YOLO feedback did not confirm immediate scope: %+v", updated)
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if len(sess.turns) != 1 || sess.turns[0] != "hello" {
		t.Fatalf("resumed Agent turns = %#v", sess.turns)
	}
}

func TestNewConversationUsesAgentApprovalDefaultWithoutPrompt(t *testing.T) {
	eng := NewEngine(nil, NewHookRunner(nil, nil))
	eng.SetConversationStore(&senderConversationStore{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Start(ctx) }()

	plat := newRuntimeSettingsPickerPlatform("fake-agent-approval-default")
	restore := stubPlatformFactory(t, "fake-agent-approval-default", plat)
	defer restore()
	agent := &approvalAgent{}
	ch := Channel{
		ID: "c-agent-approval-default", Name: "approval-default", Type: "fake-agent-approval-default",
		AgentID: "agent-approval", Enabled: true,
		// A legacy channel value must not override or force confirmation of the
		// Agent-owned default.
		Config:    map[string]string{"approval_mode": "prompt"},
		UpdatedAt: time.Now(),
	}
	workspace := WorkspaceInitOptions{
		AgentID: "agent-approval", RuntimeDefaults: RuntimeSettings{ApprovalMode: ApprovalModeYolo},
	}
	if err := eng.AttachChannel(ctx, ch, agent, t.TempDir(), workspace); err != nil {
		t.Fatal(err)
	}

	plat.push(&Message{ID: "m1", ChatID: "chat-1", Text: "hello", Platform: "fake-agent-approval-default"})
	waitFor(t, "Agent approval default dispatch", func() bool {
		agent.mu.Lock()
		sess := agent.last
		agent.mu.Unlock()
		return sess != nil && sess.turnCount() == 1
	})

	agent.mu.Lock()
	sess := agent.last
	agent.mu.Unlock()
	if got := sess.CurrentRuntimeSettings().ApprovalMode; got != ApprovalModeYolo {
		t.Fatalf("approval mode = %q, want Agent default yolo", got)
	}
	plat.pickerMu.Lock()
	cardCount := len(plat.cards)
	plat.pickerMu.Unlock()
	if cardCount != 0 {
		t.Fatalf("runtime settings cards = %d, want none when Agent has a default", cardCount)
	}
}

func TestNewConversationResumesOriginalTurnAfterInitialModelSelection(t *testing.T) {
	eng := NewEngine(nil, NewHookRunner(nil, nil))
	eng.SetConversationStore(&senderConversationStore{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Start(ctx) }()

	plat := newRuntimeSettingsPickerPlatform("fake-model-prompt")
	restore := stubPlatformFactory(t, "fake-model-prompt", plat)
	defer restore()
	agent := &modelAgent{models: []string{"gpt-5", "gpt-5-mini"}}
	ch := Channel{ID: "c-model-prompt", Name: "model", Type: "fake-model-prompt", AgentID: "agent-model", Enabled: true, UpdatedAt: time.Now()}
	if err := eng.AttachChannel(ctx, ch, agent, t.TempDir(), WorkspaceInitOptions{AgentID: "agent-model"}); err != nil {
		t.Fatal(err)
	}

	plat.push(&Message{ID: "m1", ChatID: "chat-1", Text: "hello", Platform: "fake-model-prompt"})
	waitFor(t, "first workspace model picker", func() bool {
		plat.pickerMu.Lock()
		defer plat.pickerMu.Unlock()
		return len(plat.cards) == 1
	})
	agent.mu.Lock()
	sess := agent.last
	agent.mu.Unlock()
	if sess == nil || sess.turnCount() != 0 {
		t.Fatalf("first prompt reached Agent before model selection")
	}
	plat.pickerMu.Lock()
	state := plat.cards[0]
	plat.pickerMu.Unlock()
	if state.Settings.Model != "" || !strings.Contains(state.Notice, "选择模型") || !strings.Contains(state.Notice, "自动继续") {
		t.Fatalf("model picker state = %+v", state)
	}

	plat.push(&Message{ID: "action-1", InteractionMessageID: "picker-1", ChatID: "chat-1", Platform: "fake-model-prompt", RuntimeSettingsAction: &RuntimeSettingsAction{
		Scope: RuntimeSettingsScopeConversation, Setting: RuntimeSettingModel, Value: "gpt-5-mini",
	}})
	waitFor(t, "model selection resumes original Agent turn", func() bool {
		return sess.CurrentModel() == "gpt-5-mini" && sess.turnCount() == 1
	})
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if len(sess.turns) != 1 || sess.turns[0] != "hello" {
		t.Fatalf("resumed Agent turns = %#v", sess.turns)
	}
}

func TestNewConversationResumesOriginalTurnAfterFallbackModelCommand(t *testing.T) {
	eng := NewEngine(nil, NewHookRunner(nil, nil))
	eng.SetConversationStore(&senderConversationStore{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Start(ctx) }()

	plat := newFakePlatform("fake-model-command-prompt")
	restore := stubPlatformFactory(t, "fake-model-command-prompt", plat)
	defer restore()
	agent := &modelAgent{models: []string{"gpt-5", "gpt-5-mini"}}
	ch := Channel{ID: "c-model-command-prompt", Name: "model", Type: "fake-model-command-prompt", AgentID: "agent-model", Enabled: true, UpdatedAt: time.Now()}
	if err := eng.AttachChannel(ctx, ch, agent, t.TempDir(), WorkspaceInitOptions{AgentID: "agent-model"}); err != nil {
		t.Fatal(err)
	}

	plat.push(&Message{ID: "m1", ChatID: "chat-1", Text: "hello", Platform: "fake-model-command-prompt"})
	waitFor(t, "first workspace model command prompt", func() bool {
		plat.mu.Lock()
		defer plat.mu.Unlock()
		return len(plat.replies) == 1 && strings.Contains(plat.replies[0], "/model <模型>")
	})
	agent.mu.Lock()
	sess := agent.last
	agent.mu.Unlock()
	if sess == nil || sess.turnCount() != 0 {
		t.Fatalf("first prompt reached Agent before model command")
	}

	plat.push(&Message{ID: "m2", ChatID: "chat-1", Text: "/model gpt-5-mini", Platform: "fake-model-command-prompt"})
	waitFor(t, "model command resumes original Agent turn", func() bool {
		return sess.CurrentModel() == "gpt-5-mini" && sess.turnCount() == 1
	})
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if len(sess.turns) != 1 || sess.turns[0] != "hello" {
		t.Fatalf("resumed Agent turns = %#v", sess.turns)
	}
}

func TestChannelApprovalSlashCommandSwitchesModeBeforeAgentDispatch(t *testing.T) {
	eng := NewEngine(nil, NewHookRunner(nil, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Start(ctx) }()

	plat := newFakePlatform("fake-approval-command")
	restore := stubPlatformFactory(t, "fake-approval-command", plat)
	defer restore()
	agent := &approvalAgent{}
	ch := Channel{
		ID: "c-approval-command", Name: "approval-command", Type: "fake-approval-command", AgentID: "agent-approval", Enabled: true,
		UpdatedAt: time.Now(),
	}
	if err := eng.AttachChannel(ctx, ch, agent, t.TempDir(), WorkspaceInitOptions{AgentID: "agent-approval"}); err != nil {
		t.Fatal(err)
	}

	plat.push(&Message{ID: "m1", ChatID: "chat-1", UserID: "user-1", Text: "/yolo on", Platform: "fake-approval-command"})
	waitFor(t, "approval command reply", func() bool {
		plat.mu.Lock()
		defer plat.mu.Unlock()
		return len(plat.replies) == 1
	})
	agent.mu.Lock()
	sess := agent.last
	agent.mu.Unlock()
	if sess == nil || sess.turnCount() != 0 {
		t.Fatalf("slash command reached Agent: session=%v", sess)
	}
	if got := sess.CurrentRuntimeSettings().ApprovalMode; got != ApprovalModeYolo {
		t.Fatalf("approval mode = %q, want yolo", got)
	}

	plat.push(&Message{ID: "m2", ChatID: "chat-1", UserID: "user-1", Text: "hello", Platform: "fake-approval-command"})
	waitFor(t, "agent reply after approval switch", func() bool {
		plat.mu.Lock()
		defer plat.mu.Unlock()
		return len(plat.replies) == 2
	})
	plat.mu.Lock()
	got := plat.replies[1]
	plat.mu.Unlock()
	if got != "mode:yolo hello" {
		t.Fatalf("Agent reply = %q, want switched approval mode", got)
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
	taskReplyID      string
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

func (p *streamingPlatform) BeginTaskReply(ctx context.Context, msg *Message, taskID string) (ReplyStream, error) {
	p.mu.Lock()
	p.taskReplyID = taskID
	p.mu.Unlock()
	return p.BeginReply(ctx, msg)
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

func TestCodexRemoteControlForcesOneFeishuStatusCard(t *testing.T) {
	eng := NewEngine(nil, NewHookRunner(nil, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = eng.Start(ctx) }()

	plat := newStreamingPlatform("feishu")
	restore := stubPlatformFactory(t, "feishu", plat)
	defer restore()

	ch := Channel{
		ID: "codex-remote", Name: "Codex remote", Type: "feishu", Enabled: true, UpdatedAt: time.Now(),
		Config: map[string]string{
			ChannelConfigReplyMode:           ReplyModeStreamMessage,
			ChannelConfigCodexControlEnabled: "true",
			ChannelConfigAllowedUserIDs:      "member",
		},
	}
	if err := eng.AttachChannel(ctx, ch, &remoteControlTestAgent{}, t.TempDir(),
		WorkspaceInitOptions{RuntimeID: "codex"}); err != nil {
		t.Fatal(err)
	}

	plat.push(&Message{
		ID: "om_remote", ChatID: "oc_dm", ChatType: "p2p",
		UserID: "member", Text: "hello", Platform: "feishu",
	})
	waitFor(t, "remote status card finalize", func() bool {
		plat.mu.Lock()
		defer plat.mu.Unlock()
		return plat.cardDoneCalls == 1
	})
	plat.mu.Lock()
	cardDoneCalls := plat.cardDoneCalls
	messageDoneCalls := plat.messageDoneCalls
	taskReplyID := plat.taskReplyID
	initialCard := ""
	if len(plat.cardUpdates) > 0 {
		initialCard = plat.cardUpdates[0]
	}
	plat.mu.Unlock()
	if cardDoneCalls != 1 || messageDoneCalls != 0 {
		t.Fatalf("card done=%d message done=%d", cardDoneCalls, messageDoneCalls)
	}
	if taskReplyID == "" || initialCard != "正在处理…" {
		t.Fatalf("task reply id=%q initial card=%q", taskReplyID, initialCard)
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
	if !strings.Contains(messageDoneText, `"text":"hello"`) {
		t.Fatalf("final message text does not contain injected user text: %q", messageDoneText)
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
	if !strings.Contains(messageDoneText, `"text":"hello"`) {
		t.Fatalf("final message text does not contain injected user text: %q", messageDoneText)
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
	return stubPlatformFactoryFunc(t, name, func(map[string]any) (Platform, error) { return p, nil })
}

func stubPlatformFactoryFunc(t *testing.T, name string, factory PlatformFactory) func() {
	t.Helper()
	regMu.Lock()
	old, hadOld := platforms[name]
	platforms[name] = factory
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

func stubAgentFactory(t *testing.T, name string, factory AgentFactory) func() {
	t.Helper()
	regMu.Lock()
	old, hadOld := agents[name]
	agents[name] = factory
	regMu.Unlock()
	return func() {
		regMu.Lock()
		if hadOld {
			agents[name] = old
		} else {
			delete(agents, name)
		}
		regMu.Unlock()
	}
}

type namedFakeAgent struct {
	*fakeAgent
	name string
}

func (a *namedFakeAgent) Name() string { return a.name }
