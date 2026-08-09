package feishu

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/wangning19940904/AgentMux/core"
)

func TestLarkClientDelegatesMeetingActivityCapability(t *testing.T) {
	client := &larkClient{}
	manager := newMeetingActivityManager(client)
	client.meetingActivity = manager
	manager.Register("meeting-1", "123456789", "Demo")

	if got := client.ActiveMeetings(); len(got) != 1 || got[0].ID != "meeting-1" {
		t.Fatalf("active meetings = %+v", got)
	}
	emptyDetail, err := client.MeetingActivity("meeting-1")
	if err != nil {
		t.Fatal(err)
	}
	if emptyDetail.Items == nil || emptyDetail.Turns == nil {
		t.Fatalf("empty activity collections must be non-nil: items=%#v turns=%#v", emptyDetail.Items, emptyDetail.Turns)
	}
	client.UpsertMeetingTurn(core.MeetingTurn{ID: "turn-1", MeetingID: "meeting-1", Status: "running"})
	detail, err := client.MeetingActivity("meeting-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Turns) != 1 || detail.Turns[0].ID != "turn-1" {
		t.Fatalf("turns = %+v", detail.Turns)
	}
}

func TestMeetingActivityExpandsBatchAndDeduplicates(t *testing.T) {
	client := &larkClient{botOpenID: "ou_bot", botName: "Meeting Bot"}
	manager := newMeetingActivityManager(client)
	client.meetingActivity = manager
	payload := []byte(`{
  "header":{"event_type":"vc.bot.meeting_activity_v1"},
  "event":{"meeting_activity_items":[{
    "meeting":{"id":"752233445566","meeting_no":"123456789","topic":"Roadmap"},
    "activity_event_type":"chat_received",
    "chat_received_items":[
      {"operator":{"id":{"open_id":"ou_user"},"user_name":"Alice","user_type":"future_type"},"message_id":"msg-1","message_type":1,"content":"hello","send_time":"1786240800000"},
      {"operator":{"id":{"open_id":"ou_user"},"user_name":"Alice"},"message_id":"msg-2","message_type":3,"content":"THUMBSUP","send_time":"1786240801000"}
    ]
  }]}}
`)
	if err := manager.Handle(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	if err := manager.Handle(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	detail, err := manager.MeetingActivity("752233445566")
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(detail.Items))
	}
	if detail.Items[0].Kind != "chat" || detail.Items[1].Kind != "reaction" {
		t.Fatalf("items = %+v", detail.Items)
	}
	if detail.Items[0].Actor == nil || detail.Items[0].Actor.ParticipantType != "future_type" {
		t.Fatalf("actor = %+v", detail.Items[0].Actor)
	}
}

func TestMeetingTranscriptWakeWordForwardsAgentQuestion(t *testing.T) {
	client := &larkClient{
		botOpenID: "ou_bot",
		botName:   "Meeting Bot",
		agentName: "会议助手",
		meetingVoice: &meetingVoiceManager{
			enabled:         true,
			activeMeetingID: "meeting-1",
		},
	}
	manager := newMeetingActivityManager(client)
	client.meetingActivity = manager
	inbound := make(chan *core.Message, 2)
	manager.SetInbound("channel:channel-1", inbound)
	payload := []byte(`{
  "event":{"meeting_activity_items":[{
    "meeting":{"id":"meeting-1","meeting_no":"123456789","topic":"Roadmap"},
    "activity_event_type":"transcript_received",
    "transcript_received_items":[
      {"speaker":{"id":"ou_user","user_name":"Alice"},"sentence_id":"sentence-1","text":"大家先看下一页","start_time_ms":"1786240800000"},
      {"speaker":{"id":"ou_user","user_name":"Alice"},"sentence_id":"sentence-2","text":"会议助手，请总结刚才的结论","start_time_ms":"1786240801000"}
    ]
  }]}
}`)
	if err := manager.Handle(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	select {
	case message := <-inbound:
		if message.Text != "请总结刚才的结论" || message.MeetingID != "meeting-1" || message.Origin != core.OriginMeeting {
			t.Fatalf("meeting transcript question = %+v", message)
		}
	case <-time.After(time.Second):
		t.Fatal("wake-word transcript was not forwarded")
	}
	select {
	case unexpected := <-inbound:
		t.Fatalf("unaddressed transcript was forwarded: %+v", unexpected)
	default:
	}
}

func TestMeetingTranscriptCustomWakeWordForwardsUserWithMatchingName(t *testing.T) {
	client := &larkClient{
		botOpenID:        "ou_bot",
		botName:          "WangNing",
		meetingWakeWords: []string{"王宁同学", "小王小王"},
		meetingVoice: &meetingVoiceManager{
			enabled:         true,
			activeMeetingID: "meeting-1",
		},
	}
	manager := newMeetingActivityManager(client)
	client.meetingActivity = manager
	inbound := make(chan *core.Message, 1)
	manager.SetInbound("channel:channel-1", inbound)
	payload := []byte(`{
  "event":{"meeting_activity_items":[{
    "meeting":{"id":"meeting-1"},
    "activity_event_type":"transcript_received",
    "transcript_received_items":[
      {"speaker":{"id":"ou_user","user_name":"小王小王"},"sentence_id":"sentence-custom","text":"小王小王，讲个笑话吧","start_time_ms":"1786240800000"}
    ]
  }]}
}`)
	if err := manager.Handle(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	select {
	case message := <-inbound:
		if message.Text != "讲个笑话吧" || message.UserName != "小王小王" {
			t.Fatalf("custom wake question = %+v", message)
		}
	case <-time.After(time.Second):
		t.Fatal("custom wake word did not forward the transcript")
	}
}

func TestNewPlatformLoadsCustomMeetingWakeWords(t *testing.T) {
	platform, err := newPlatform("feishu", "https://open.feishu.cn", map[string]any{
		"app_id": "cli_test", "app_secret": "secret",
		core.ChannelConfigMeetingWakeWords: "王宁同学,小王小王",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(platform.meetingWakeWords, "|"); got != "王宁同学|小王小王" {
		t.Fatalf("platform wake words = %q", got)
	}
}

func TestMeetingTranscriptStandaloneWakeArmsNextSentence(t *testing.T) {
	client := &larkClient{
		botName: "小助手",
		meetingVoice: &meetingVoiceManager{
			enabled:         true,
			activeMeetingID: "meeting-1",
		},
	}
	manager := newMeetingActivityManager(client)
	client.meetingActivity = manager
	inbound := make(chan *core.Message, 1)
	manager.SetInbound("channel:channel-1", inbound)
	payload := []byte(`{
  "event":{"meeting_activity_items":[{
    "meeting":{"id":"meeting-1"},
    "activity_event_type":"transcript_received",
    "transcript_received_items":[
      {"speaker":{"id":"ou_user","user_name":"Alice"},"sentence_id":"sentence-1","text":"小助手","start_time_ms":"1786240800000"},
      {"speaker":{"id":"ou_user","user_name":"Alice"},"sentence_id":"sentence-2","text":"帮我列出三个风险","start_time_ms":"1786240801000"}
    ]
  }]}
}`)
	if err := manager.Handle(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	select {
	case message := <-inbound:
		if message.Text != "帮我列出三个风险" {
			t.Fatalf("split wake question = %+v", message)
		}
	case <-time.After(time.Second):
		t.Fatal("sentence after standalone wake word was not forwarded")
	}
}

func TestStripMeetingTranscriptWakeWord(t *testing.T) {
	names := []string{"Meeting Bot", "小助手"}
	for input, want := range map[string]string{
		"你好，小助手，帮我总结":              "帮我总结",
		"@Meeting Bot: next step?": "next step?",
		"Meeting Bot 请回答":          "请回答",
	} {
		got, ok := stripMeetingTranscriptWakeWord(input, names)
		if !ok || got != want {
			t.Errorf("strip %q = %q, %v; want %q, true", input, got, ok, want)
		}
	}
	if _, ok := stripMeetingTranscriptWakeWord("Meeting Botanical", names); ok {
		t.Fatal("partial ASCII wake word unexpectedly matched")
	}
}

func TestMeetingBootstrapRestoresConfiguredUserActiveMeeting(t *testing.T) {
	var lookups atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			_, _ = writer.Write([]byte(`{"code":0,"expire":7200,"tenant_access_token":"tenant-token"}`))
		case meetingUserActiveAPIPath:
			lookups.Add(1)
			if got := request.URL.Query().Get("user_id"); got != "ou_user" {
				t.Errorf("user_id = %q", got)
			}
			_, _ = writer.Write([]byte(`{"code":0,"data":{"meetings":[{"meeting_id":"meeting-1","meeting_no":"123456789","meeting_title":"Demo"}]}}`))
		case meetingEventsAPIPath:
			_, _ = writer.Write([]byte(`{"code":0,"data":{"events":[],"has_more":false}}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	voiceCtx, voiceCancel := context.WithCancel(context.Background())
	client := &larkClient{
		api: lark.NewClient("cli_test", "secret", lark.WithOpenBaseUrl(server.URL)),
		meetingVoice: &meetingVoiceManager{
			enabled: true, ctx: voiceCtx, cancel: voiceCancel, jobs: make(chan meetingVoiceJob, 1),
		},
	}
	defer client.meetingVoice.Close()
	manager := newMeetingActivityManager(client)
	client.meetingActivity = manager
	manager.BootstrapActiveMeetings(context.Background(), []string{"ou_user", "ou_user", ""})

	meetings := client.ActiveMeetings()
	if len(meetings) != 1 || meetings[0].ID != "meeting-1" || meetings[0].MeetingNumber != "123456789" || meetings[0].Topic != "Demo" {
		t.Fatalf("active meetings = %+v", meetings)
	}
	if got := lookups.Load(); got != 1 {
		t.Fatalf("user active lookups = %d, want 1", got)
	}
	if got := client.meetingVoice.ActiveMeetingID(); got != "meeting-1" {
		t.Fatalf("restored meeting voice activation = %q, want meeting-1", got)
	}
}

func TestMeetingActivityUnknownItemACKsAndEndedStateExpires(t *testing.T) {
	client := &larkClient{}
	manager := newMeetingActivityManager(client)
	client.meetingActivity = manager
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	manager.Register("meeting-1", "123456789", "Demo")
	if err := manager.Handle(context.Background(), []byte(`{"event":{"meeting_activity_items":[{"meeting":{"id":"meeting-1"},"activity_event_type":"future_event","future_items":[{}]}]}}`)); err != nil {
		t.Fatalf("unknown item should ACK: %v", err)
	}
	manager.HandleMeetingEnded([]byte(`{"event":{"meeting":{"id":"meeting-1"}}}`))
	if got := manager.ActiveMeetings(); len(got) != 0 {
		t.Fatalf("active meetings = %+v", got)
	}
	now = now.Add(5*time.Minute + time.Second)
	if _, err := manager.MeetingActivity("meeting-1"); err == nil {
		t.Fatal("expired ended meeting remains available")
	}
}

func TestMeetingTurnUpsertPublishesState(t *testing.T) {
	var events []core.MeetingEvent
	client := &larkClient{meetingNotify: func(event core.MeetingEvent) { events = append(events, event) }}
	manager := newMeetingActivityManager(client)
	client.meetingActivity = manager
	turn := core.MeetingTurn{ID: "turn-1", MeetingID: "meeting-1", Question: "why", Status: "running", CreatedAt: time.Now()}
	manager.UpsertMeetingTurn(turn)
	turn.Status = "succeeded"
	manager.UpsertMeetingTurn(turn)
	detail, _ := manager.MeetingActivity("meeting-1")
	if len(detail.Turns) != 1 || detail.Turns[0].Status != "succeeded" {
		t.Fatalf("turns = %+v", detail.Turns)
	}
	if len(events) != 2 || events[1].Type != "meeting.turn" {
		t.Fatalf("events = %+v", events)
	}
}

func TestMeetingEndedFromPriorJoinDoesNotReplaceCurrentGeneration(t *testing.T) {
	client := &larkClient{}
	manager := newMeetingActivityManager(client)
	client.meetingActivity = manager
	now := time.Date(2026, 8, 9, 8, 35, 49, 0, time.UTC)
	manager.now = func() time.Time { return now }
	manager.RegisterJoin("meeting-1", "123456789", "Demo", "call-new")

	// A delayed end callback for the previous call may be delivered after the
	// new join. The call generation wins even when delivery time is newer.
	manager.HandleMeetingEnded([]byte(`{
  "header":{"create_time":"1786264578000"},
  "event":{"call_id":"call-old","meeting":{"id":"meeting-1"}}
}`))
	if got := manager.ActiveMeetings(); len(got) != 1 {
		t.Fatalf("active meetings after old call end = %+v", got)
	}

	// Older callbacks without a call id are rejected by business time.
	manager.HandleMeetingEnded([]byte(`{
  "header":{"create_time":"1786264533000"},
  "event":{"meeting":{"id":"meeting-1"}}
}`))
	if got := manager.ActiveMeetings(); len(got) != 1 {
		t.Fatalf("active meetings after stale timed end = %+v", got)
	}

	manager.HandleMeetingEnded([]byte(`{
  "header":{"create_time":"1786264670000"},
  "event":{"call_id":"call-new","meeting":{"id":"meeting-1"}}
}`))
	if got := manager.ActiveMeetings(); len(got) != 0 {
		t.Fatalf("active meetings after current call end = %+v", got)
	}
}

func TestMeetingBackfillUnwrapsEventsPayloadAndRestoresOngoingMeeting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			_, _ = writer.Write([]byte(`{"code":0,"expire":7200,"tenant_access_token":"tenant-token"}`))
		case meetingEventsAPIPath:
			if got := request.URL.Query().Get("meeting_id"); got != "meeting-1" {
				t.Errorf("meeting_id = %q", got)
			}
			_, _ = writer.Write([]byte(`{
  "code":0,"msg":"success","data":{
    "meeting":{"id":"meeting-1","meeting_no":"123456789","topic":"Demo","status":"ongoing"},
    "events":[
      {"event_id":"event-join","event_type":"participant_joined","event_time":"2026-08-09T08:35:49Z","payload":{
        "activity_event_type":"participant_joined",
        "participant_joined_items":[{"join_time":"2026-08-09T08:35:49Z","participant":{"id":"ou_bot","user_name":"Meeting Bot","user_type":10}}]
      }},
      {"event_id":"event-chat","event_type":"chat_received","event_time":"2026-08-09T08:36:17Z","payload":{
        "activity_event_type":"chat_received",
        "chat_received_items":[{"message_id":"message-1","send_time":"2026-08-09T08:36:17Z","content":"@Meeting Bot hello","operator":{"id":"ou_user","user_name":"Alice","user_type":1}}]
      }}
    ],
    "page_token":"page-next","has_more":false
  }
}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client := &larkClient{
		botOpenID: "ou_bot",
		botName:   "Meeting Bot",
		api:       lark.NewClient("cli_test", "secret", lark.WithOpenBaseUrl(server.URL)),
	}
	manager := newMeetingActivityManager(client)
	client.meetingActivity = manager
	inbound := make(chan *core.Message, 1)
	manager.SetInbound("channel:channel-1", inbound)
	now := time.Date(2026, 8, 9, 8, 36, 30, 0, time.UTC)
	manager.now = func() time.Time { return now }
	manager.RegisterJoin("meeting-1", "123456789", "Demo", "call-current")
	manager.HandleMeetingEnded([]byte(`{"header":{"create_time":"1786264590000"},"event":{"call_id":"call-current","meeting":{"id":"meeting-1"}}}`))
	if got := manager.ActiveMeetings(); len(got) != 0 {
		t.Fatalf("meeting should start ended, got %+v", got)
	}

	manager.Backfill(context.Background(), "meeting-1", true)
	detail, err := manager.MeetingActivity("meeting-1")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Meeting.Status != "active" || detail.Meeting.MeetingNumber != "123456789" {
		t.Fatalf("meeting = %+v", detail.Meeting)
	}
	if len(detail.Items) != 2 || detail.Items[0].Kind != "participant_joined" || detail.Items[1].Kind != "chat" {
		t.Fatalf("items = %+v", detail.Items)
	}
	manager.mu.Lock()
	pageToken := manager.states["meeting-1"].pageToken
	manager.mu.Unlock()
	if pageToken != "page-next" {
		t.Fatalf("page token = %q", pageToken)
	}
	select {
	case message := <-inbound:
		t.Fatalf("historical full backfill forwarded Agent question: %+v", message)
	default:
	}

	if err := manager.Handle(context.Background(), []byte(`{
  "event":{"meeting_activity_items":[{
    "meeting":{"id":"meeting-1","meeting_no":"123456789","topic":"Demo"},
    "activity_event_type":"chat_received",
    "chat_received_items":[{"message_id":"message-live","send_time":"2026-08-09T08:36:40Z","content":"@Meeting Bot new question","operator":{"id":"ou_user","user_name":"Alice","user_type":1}}]
  }]}
}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case message := <-inbound:
		if message.Text != "new question" || message.MeetingID != "meeting-1" {
			t.Fatalf("live meeting question = %+v", message)
		}
	case <-time.After(time.Second):
		t.Fatal("live meeting question was not forwarded")
	}
}
