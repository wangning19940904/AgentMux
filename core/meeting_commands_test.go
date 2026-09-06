package core

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

type meetingCommandTestPlatform struct {
	*fakePlatform
	active          []ActiveMeeting
	visible         []ActiveMeeting
	users           []string
	detail          MeetingDetail
	meetingMessages chan string
	configured      map[string]string
}

type meetingResponseModeTestStore struct {
	channelID string
	mode      string
}

func (s *meetingResponseModeTestStore) SetMeetingResponseMode(_ context.Context, channelID, mode string) (string, error) {
	s.channelID = channelID
	s.mode = NormalizeMeetingResponseMode(mode)
	return s.mode, nil
}

func (p *meetingCommandTestPlatform) MeetingInvitations() []MeetingInvitation { return nil }
func (p *meetingCommandTestPlatform) RespondMeetingInvitation(context.Context, string, string, string) (MeetingInvitation, error) {
	return MeetingInvitation{}, nil
}
func (p *meetingCommandTestPlatform) JoinMeetingByNumber(context.Context, string) (MeetingJoinResult, error) {
	return MeetingJoinResult{}, nil
}
func (p *meetingCommandTestPlatform) ActiveMeetings() []ActiveMeeting {
	return append([]ActiveMeeting(nil), p.active...)
}
func (p *meetingCommandTestPlatform) MeetingActivity(string) (MeetingDetail, error) {
	return p.detail, nil
}

func (p *meetingCommandTestPlatform) SendMeetingMessage(_ context.Context, _ string, text, _ string) error {
	if p.meetingMessages != nil {
		p.meetingMessages <- text
	}
	return nil
}
func (p *meetingCommandTestPlatform) UserActiveMeetings(_ context.Context, userID string) ([]ActiveMeeting, error) {
	p.users = append(p.users, userID)
	return append([]ActiveMeeting(nil), p.visible...), nil
}
func (p *meetingCommandTestPlatform) MeetingPromptContext(string) string { return "" }
func (p *meetingCommandTestPlatform) UpsertMeetingTurn(MeetingTurn)      {}
func (p *meetingCommandTestPlatform) ConfigureMeetingResponseMode(config map[string]string) error {
	p.configured = copyChannelConfig(config)
	return nil
}

func TestParseMeetingCommands(t *testing.T) {
	tests := []struct{ input, kind, number, question string }{
		{"/meeting", "help", "", ""},
		{"/meeting 帮助", "help", "", ""},
		{"/meeting join 123456789", "join", "123456789", ""},
		{"/meeting 加入 123456789", "join", "123456789", ""},
		{"/meeting list", "list", "", ""},
		{"/meeting 我的", "mine", "", ""},
		{"/meeting mode", "mode", "", ""},
		{"/meeting 模式 文字+语音", "mode", "", ""},
		{"/meeting voice only", "mode", "", ""},
		{"/meeting 123456789", "help", "", ""},
		{"/meeting 123456789 “结论是什么？”", "ask", "123456789", "结论是什么？"},
		{"/meeting '请总结'", "ask", "", "请总结"},
		{"/meeting 现在讲到哪里", "ask", "", "现在讲到哪里"},
	}
	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			got, ok := parseMeetingCommand(test.input)
			if !ok {
				t.Fatal("not parsed")
			}
			if got.Kind != test.kind || got.MeetingNumber != test.number || got.Question != test.question {
				t.Fatalf("got %+v", got)
			}
		})
	}
	if got, _ := parseMeetingCommand("/meeting 模式 文字+语音"); got.Mode != MeetingResponseModeTextVoice {
		t.Fatalf("text+voice mode = %+v", got)
	}
	if got, _ := parseMeetingCommand("/meeting voice only"); got.Mode != MeetingResponseModeVoice {
		t.Fatalf("voice-only mode = %+v", got)
	}
	if isMeetingCommand("/meetings list") {
		t.Fatal("prefix command should not match")
	}
}

func TestMeetingTurnPromptInjectsReadOnlyLarkCLIContextLookup(t *testing.T) {
	prompt := meetingTurnPrompt(
		"当前会议上下文：\n最近字幕：\nAlice: 先看指标",
		ActiveMeeting{ID: "752233445566", MeetingNumber: "123456789", Topic: "Roadmap"},
		"现在的结论是什么？",
	)

	wants := []string{
		"当前会议上下文：\n最近字幕：\nAlice: 先看指标",
		"meeting_id=752233445566",
		"meeting_number=123456789",
		"lark-cli vc +meeting-events --as bot --meeting-id 752233445566 --page-all --format pretty",
		"不要用 9 位会议号替代 meeting_id",
		"不要自动执行入会、离会或发送消息等写操作",
		"用户问题：现在的结论是什么？",
	}
	for _, want := range wants {
		if !strings.Contains(prompt, want) {
			t.Fatalf("meeting prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "--meeting-id 123456789") {
		t.Fatalf("meeting number was injected as meeting_id:\n%s", prompt)
	}
}

func TestMeetingAnswerDeltaAndSegmentation(t *testing.T) {
	if got := meetingAnswerDelta("答案", "答案继续"); got != "继续" {
		t.Fatalf("delta = %q", got)
	}
	if got := meetingAnswerDelta("答案继续", "答案"); got != "" {
		t.Fatalf("regression delta = %q", got)
	}
	joke := "同事问我：“这个需求急吗？”产品经理说：“不急，下班前上线就行。”"
	segments, rest := splitMeetingStreamAnswer(joke, true, false)
	if len(segments) != 0 || rest != joke {
		t.Fatalf("short paragraph should accumulate: segments=%q rest=%q", segments, rest)
	}
	segments, rest = splitMeetingStreamAnswer(rest, true, true)
	if len(segments) != 1 || segments[0] != joke || rest != "" {
		t.Fatalf("final paragraph=%q rest=%q", segments, rest)
	}
	if segments[0] == "”" || !strings.HasSuffix(segments[0], "。”") {
		t.Fatalf("closing quote split incorrectly: %q", segments)
	}
}

func TestMeetingStreamReplyIsThrottledAndCoherent(t *testing.T) {
	previousInterval := meetingStreamFlushInterval
	meetingStreamFlushInterval = 40 * time.Millisecond
	defer func() { meetingStreamFlushInterval = previousInterval }()

	events := make(chan *Event)
	sent := make(chan string, 2)
	done := make(chan error, 1)
	go func() {
		done <- deliverMeetingAnswerObserved(context.Background(), events, MeetingReplyModeStream, func(text string) error {
			sent <- text
			return nil
		}, nil)
	}()
	paragraph := strings.Repeat("这是一个用于验证节流发送的完整句子。", 8)
	events <- &Event{Type: EventOutput, Text: paragraph}
	select {
	case value := <-sent:
		t.Fatalf("stream reply was sent before throttle interval: %q", value)
	case <-time.After(15 * time.Millisecond):
	}
	select {
	case value := <-sent:
		if utf8.RuneCountInString(value) < meetingStreamMinRunes || !strings.HasSuffix(value, "。") {
			t.Fatalf("stream chunk is not coherent: %q", value)
		}
	case <-time.After(time.Second):
		t.Fatal("stream reply was not sent after throttle interval")
	}
	close(events)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestMeetingFinalReplyWaitsForTaskCompletionAndSendsOnce(t *testing.T) {
	events := make(chan *Event)
	sent := make(chan string, 2)
	done := make(chan error, 1)
	go func() {
		done <- deliverMeetingAnswerObserved(context.Background(), events, MeetingReplyModeFinal, func(text string) error {
			sent <- text
			return nil
		}, nil)
	}()
	events <- &Event{Type: EventOutput, Text: "第一段。"}
	events <- &Event{Type: EventFinal, Text: "第一段。最终答案。", Final: true}
	select {
	case value := <-sent:
		t.Fatalf("final mode replied before task completion: %q", value)
	case <-time.After(30 * time.Millisecond):
	}
	close(events)
	select {
	case value := <-sent:
		if value != "第一段。最终答案。" {
			t.Fatalf("final reply = %q", value)
		}
	case <-time.After(time.Second):
		t.Fatal("final reply was not sent after task completion")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case value := <-sent:
		t.Fatalf("final mode sent more than once: %q", value)
	default:
	}
}

func TestMeetingAnswerMirrorsAccumulatedTextToSpeechObserver(t *testing.T) {
	events := make(chan *Event, 3)
	events <- &Event{Type: EventOutput, Text: "第一句。"}
	events <- &Event{Type: EventFinal, Text: "第一句。第二句。", Final: true}
	close(events)

	type observation struct {
		text string
		done bool
	}
	var observed []observation
	if err := deliverMeetingAnswerObserved(context.Background(), events, MeetingReplyModeFinal, func(string) error {
		return nil
	}, func(text string, done bool) {
		observed = append(observed, observation{text: text, done: done})
	}); err != nil {
		t.Fatal(err)
	}
	if len(observed) != 3 || observed[0].text != "第一句。" || observed[0].done || observed[1].text != "第一句。第二句。" || observed[1].done || observed[2].text != "第一句。第二句。" || !observed[2].done {
		t.Fatalf("speech observations = %+v", observed)
	}
}

func TestChannelMeetingResponseMode(t *testing.T) {
	if got := ChannelMeetingResponseMode(Channel{}); got != MeetingResponseModeStreamText {
		t.Fatalf("default mode = %q", got)
	}
	if got := ChannelMeetingResponseMode(Channel{Config: map[string]string{ChannelConfigMeetingReplyMode: MeetingReplyModeFinal}}); got != MeetingResponseModeFinalText {
		t.Fatalf("configured mode = %q", got)
	}
	tests := []struct {
		mode      string
		reply     string
		usesText  bool
		usesVoice bool
	}{
		{MeetingResponseModeStreamText, MeetingReplyModeStream, true, false},
		{MeetingResponseModeFinalText, MeetingReplyModeFinal, true, false},
		{MeetingResponseModeTextVoice, MeetingReplyModeStream, true, true},
		{MeetingResponseModeVoice, MeetingReplyModeFinal, false, true},
	}
	for _, test := range tests {
		channel := Channel{}
		if err := ApplyMeetingResponseMode(&channel, test.mode); err != nil {
			t.Fatal(err)
		}
		if got := ChannelMeetingResponseMode(channel); got != test.mode ||
			channel.Config[ChannelConfigMeetingReplyMode] != test.reply ||
			MeetingResponseModeUsesText(got) != test.usesText ||
			MeetingResponseModeUsesVoice(got) != test.usesVoice {
			t.Fatalf("mode %s mapped incorrectly: channel=%+v got=%s", test.mode, channel.Config, got)
		}
	}
}

func TestMeetingModeCommandUpdatesChannelWithoutAccessList(t *testing.T) {
	platform := &meetingCommandTestPlatform{fakePlatform: newFakePlatform("meeting-test")}
	store := &meetingResponseModeTestStore{}
	engine := NewEngine(nil, nil)
	engine.SetMeetingResponseModeStore(store)
	runtime := &channelRuntime{
		owner:    engine,
		channel:  Channel{ID: "channel-1", Type: "feishu"},
		platform: platform,
	}
	msg := &Message{UserID: "ou_user", Text: "/meeting mode text+voice"}
	if !engine.handleMeetingMessage(context.Background(), runtime, msg, map[string]string{}) {
		t.Fatal("meeting mode command was not handled")
	}
	if store.channelID != "channel-1" || store.mode != MeetingResponseModeTextVoice {
		t.Fatalf("saved setting = channel %q mode %q", store.channelID, store.mode)
	}
	platform.mu.Lock()
	defer platform.mu.Unlock()
	if len(platform.replies) != 1 || !strings.Contains(platform.replies[0], "文字+语音") {
		t.Fatalf("replies = %q", platform.replies)
	}
}

func TestMeetingListRefreshesVisibleMeetingsBeforeReply(t *testing.T) {
	platform := &meetingCommandTestPlatform{
		fakePlatform: newFakePlatform("meeting-test"),
		visible: []ActiveMeeting{{
			ID: "meeting-1", MeetingNumber: "955199153", Topic: "测试1", Status: "active",
		}},
	}
	engine := NewEngine(nil, nil)
	runtime := &channelRuntime{
		owner: engine, channel: Channel{ID: "channel-1", Type: "feishu"}, platform: platform,
	}
	msg := &Message{UserID: "ou_user", Text: "/meeting list"}
	if !engine.handleMeetingMessage(context.Background(), runtime, msg, map[string]string{}) {
		t.Fatal("meeting list was not handled")
	}
	if len(platform.users) != 1 || platform.users[0] != "ou_user" {
		t.Fatalf("user-active queries = %v", platform.users)
	}
	platform.mu.Lock()
	replies := append([]string(nil), platform.replies...)
	platform.mu.Unlock()
	if len(replies) != 1 || replies[0] == "当前 Bot 没有加入进行中的会议。" {
		t.Fatalf("replies = %q", replies)
	}
}

func TestMeetingOriginQuestionBypassesStaleEndedCache(t *testing.T) {
	platform := &meetingCommandTestPlatform{
		fakePlatform:    newFakePlatform("meeting-test"),
		detail:          MeetingDetail{Meeting: ActiveMeeting{ID: "meeting-1", Status: "ended"}},
		meetingMessages: make(chan string, 2),
	}
	engine := NewEngine(nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtime := &channelRuntime{
		owner: engine, channel: Channel{ID: "channel-1", Type: "feishu"}, platform: platform,
		agent: &fakeAgent{}, runCtx: ctx, connected: true, state: ChannelStateRunning,
		sessions: map[string]*channelSessionBinding{}, controlTasks: map[string]*channelControlState{},
	}
	engine.channels["channel-1"] = runtime
	if _, err := engine.AskMeeting("channel-1", "meeting-1", "讲个笑话", "meeting", "ou_user"); err != nil {
		t.Fatalf("meeting-origin question was rejected: %v", err)
	}
	select {
	case answer := <-platform.meetingMessages:
		if answer == "" {
			t.Fatal("empty meeting answer")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("meeting answer was not sent")
	}
}
