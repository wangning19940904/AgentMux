package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
	"github.com/wangning19940904/AgentMux/core"
)

func TestParseMeetingVoiceConfig(t *testing.T) {
	disabled, err := parseMeetingVoiceConfig(nil)
	if err != nil || disabled.Enabled {
		t.Fatalf("disabled config = %+v, err=%v", disabled, err)
	}

	enabled, err := parseMeetingVoiceConfig(map[string]any{
		"meeting_voice_enabled":     "true",
		"meeting_voice_tts_api_key": "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !enabled.Enabled ||
		enabled.TTSMode != core.MeetingTTSModeAPI ||
		enabled.TTSBaseURL != "https://api.openai.com/v1" ||
		enabled.TTSModel != "gpt-4o-mini-tts" ||
		enabled.TTSVoice != "alloy" {
		t.Fatalf("enabled config = %+v", enabled)
	}

	local, err := parseMeetingVoiceConfig(map[string]any{
		"meeting_voice_enabled":     "true",
		"meeting_voice_tts_mode":    "local",
		"meeting_voice_local_model": "kokoro-82m-zh-int8",
		"meeting_voice_local_voice": "58",
	})
	if err != nil || local.TTSMode != core.MeetingTTSModeLocal || local.LocalModel != "kokoro-82m-zh-int8" || local.LocalVoice != "58" {
		t.Fatalf("local config = %+v, err=%v", local, err)
	}

	for name, config := range map[string]map[string]any{
		"bad boolean": {"meeting_voice_enabled": "sometimes"},
		"missing key": {"meeting_voice_enabled": "true"},
		"bad URL": {
			"meeting_voice_enabled":      "true",
			"meeting_voice_tts_api_key":  "secret",
			"meeting_voice_tts_base_url": "file:///tmp/tts",
		},
		"bad local model": {
			"meeting_voice_enabled":     "true",
			"meeting_voice_tts_mode":    "local",
			"meeting_voice_local_model": "missing",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseMeetingVoiceConfig(config); err == nil {
				t.Fatal("invalid meeting voice config unexpectedly succeeded")
			}
		})
	}
}

func TestMeetingVoiceHotEnablePreservesActiveMeeting(t *testing.T) {
	disabled := newMeetingVoiceManager(nil, meetingVoiceConfig{})
	disabled.Activate("meeting-1", "ou_inviter", "oc_chat")
	if disabled.ActiveMeetingID() != "meeting-1" || disabled.IsActive("meeting-1") {
		t.Fatalf("disabled manager activation = %q active=%v", disabled.ActiveMeetingID(), disabled.IsActive("meeting-1"))
	}

	client := &larkClient{meetingVoice: disabled}
	client.ConfigureMeetingVoice(meetingVoiceConfig{
		Enabled: true, TTSBaseURL: "https://tts.example.test/v1", TTSAPIKey: "secret", TTSModel: "tts", TTSVoice: "voice",
	})
	enabled := client.currentMeetingVoice()
	if enabled == nil || enabled.ActiveMeetingID() != "meeting-1" || !enabled.IsActive("meeting-1") {
		t.Fatalf("enabled manager did not preserve meeting activation")
	}
	enabled.Close()
}

func TestMeetingSpeechReplyQueuesOnlyNewCompletedText(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := &meetingVoiceManager{
		enabled:         true,
		ctx:             ctx,
		cancel:          cancel,
		jobs:            make(chan meetingVoiceJob, 8),
		activeMeetingID: "meeting-1",
		activeUserID:    "ou_inviter",
		activeChatID:    "oc_direct",
		synthesizer:     nil,
		sessions:        nil,
	}
	reply := &meetingSpeechReply{manager: manager, meetingID: "meeting-1"}

	if err := reply.Update(ctx, "**你好，世界。**", false); err != nil {
		t.Fatal(err)
	}
	first := <-manager.jobs
	if first.text != "你好，世界。" {
		t.Fatalf("first speech segment = %q", first.text)
	}

	if err := reply.Update(ctx, "**你好，世界。** 下一句", false); err != nil {
		t.Fatal(err)
	}
	select {
	case unexpected := <-manager.jobs:
		t.Fatalf("unfinished text was queued: %+v", unexpected)
	default:
	}

	if err := reply.Update(ctx, "**你好，世界。** 下一句完成", true); err != nil {
		t.Fatal(err)
	}
	second := <-manager.jobs
	if second.text != "下一句完成" {
		t.Fatalf("final speech segment = %q", second.text)
	}
	if err := reply.Update(ctx, "**你好，世界。** 下一句完成", true); err != nil {
		t.Fatal(err)
	}
	select {
	case duplicate := <-manager.jobs:
		t.Fatalf("duplicate final text was queued: %+v", duplicate)
	default:
	}
}

func TestMeetingVoiceReplyIsScopedToInviter(t *testing.T) {
	manager := &meetingVoiceManager{
		enabled:         true,
		activeMeetingID: "meeting-1",
		activeUserID:    "ou_inviter",
		activeChatID:    "oc_direct",
	}
	if reply := manager.BeginReply(&core.Message{UserID: "ou_someone_else", ChatID: "oc_direct"}); reply != nil {
		t.Fatal("another user's Agent reply would be sent to the meeting")
	}
	if reply := manager.BeginReply(&core.Message{UserID: "ou_inviter", ChatID: "oc_other"}); reply != nil {
		t.Fatal("an unrelated chat would be sent to the meeting")
	}
	if reply := manager.BeginReply(&core.Message{UserID: "ou_inviter", ChatID: "oc_direct"}); reply == nil {
		t.Fatal("inviter's Agent reply was not connected to meeting speech")
	}
	if reply := manager.BeginReply(&core.Message{MeetingID: "meeting-1", ChatID: "meeting:meeting-1"}); reply == nil {
		t.Fatal("meeting-originated Agent reply was not connected to meeting speech")
	}
	if reply := manager.BeginReply(&core.Message{MeetingID: "meeting-2", ChatID: "meeting:meeting-2"}); reply != nil {
		t.Fatal("another meeting's Agent reply would be sent to the active meeting")
	}
}

func TestOpenAICompatibleTTSSynthesizerRequestsRawPCM(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/audio/speech" {
			http.NotFound(writer, request)
			return
		}
		if got := request.Header.Get("Authorization"); got != "Bearer tts-secret" {
			t.Errorf("Authorization = %q", got)
		}
		if err := json.NewDecoder(request.Body).Decode(&requestBody); err != nil {
			t.Error(err)
		}
		writer.Header().Set("Content-Type", "audio/pcm")
		_, _ = writer.Write([]byte{1, 2, 3, 4})
	}))
	defer server.Close()

	synth := &openAICompatibleTTSSynthesizer{
		baseURL: server.URL + "/v1",
		apiKey:  "tts-secret",
		model:   "tts-model",
		voice:   "voice-a",
		client:  server.Client(),
	}
	audio, err := synth.Synthesize(context.Background(), "你好")
	if err != nil {
		t.Fatal(err)
	}
	defer audio.Close()
	got, err := io.ReadAll(audio)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte{1, 2, 3, 4}) {
		t.Fatalf("audio = %v", got)
	}
	if requestBody["model"] != "tts-model" ||
		requestBody["voice"] != "voice-a" ||
		requestBody["input"] != "你好" ||
		requestBody["response_format"] != "pcm" {
		t.Fatalf("TTS request = %+v", requestBody)
	}
}

func TestGetMeetingRealtimeEndpointUsesTenantToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			_, _ = writer.Write([]byte(`{"code":0,"expire":7200,"tenant_access_token":"tenant-token"}`))
		case meetingRealtimeEndpointAPIPath:
			if request.URL.Query().Get("meeting_id") != "meeting long/id" {
				t.Errorf("meeting_id = %q", request.URL.Query().Get("meeting_id"))
			}
			if got := request.Header.Get("Authorization"); got != "Bearer tenant-token" {
				t.Errorf("Authorization = %q", got)
			}
			_, _ = writer.Write([]byte(`{"code":0,"msg":"success","data":{"websocket_url":"wss://example.test/realtime","expires_time":"2030-01-02T03:04:05Z"}}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client := &larkClient{
		platform: "feishu",
		api: lark.NewClient(
			"cli_test",
			"secret",
			lark.WithOpenBaseUrl(server.URL),
		),
	}
	endpoint, err := client.GetMeetingRealtimeEndpoint(context.Background(), "meeting long/id")
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.WebSocketURL != "wss://example.test/realtime" ||
		!endpoint.ExpiresAt.Equal(time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("endpoint = %+v", endpoint)
	}
}

func TestParseMeetingRealtimeExpiryFormats(t *testing.T) {
	wantUnix := time.Unix(1_786_283_800, 0).UTC()
	for name, test := range map[string]struct {
		raw  string
		want time.Time
	}{
		"unix string": {raw: `"1786283800"`, want: wantUnix},
		"unix number": {raw: `1786283800`, want: wantUnix},
		"unix millis": {raw: `1786283800000`, want: wantUnix},
		"rfc3339":     {raw: `"2030-01-02T03:04:05Z"`, want: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)},
		"empty":       {raw: `""`, want: time.Time{}},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := parseMeetingRealtimeExpiry(json.RawMessage(test.raw))
			if err != nil {
				t.Fatal(err)
			}
			if !got.Equal(test.want) {
				t.Fatalf("expiry = %s, want %s", got, test.want)
			}
		})
	}
	if _, err := parseMeetingRealtimeExpiry(json.RawMessage(`"not-a-time"`)); err == nil {
		t.Fatal("invalid expiry unexpectedly parsed")
	}
}

type staticMeetingRealtimeEndpoint struct {
	endpoint meetingRealtimeEndpoint
}

func (p staticMeetingRealtimeEndpoint) GetMeetingRealtimeEndpoint(context.Context, string) (meetingRealtimeEndpoint, error) {
	return p.endpoint, nil
}

type capturedRealtimeEvent struct {
	eventType       string
	eventID         string
	sessionID       uint64
	createdAt       string
	pcm             []byte
	serviceID       int32
	method          int32
	payloadEncoding string
	payloadType     string
	messageID       string
	frameType       uint64
	closeReason     uint64
	upstream        capturedAudioFormat
	downstream      capturedAudioFormat
}

type capturedAudioFormat struct {
	mediaType  string
	encoding   string
	sampleRate uint64
}

func TestLarkRealtimeSessionCreatesAndSendsBinaryPCM(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	captured := make(chan capturedRealtimeEvent, 3)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		conn, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		for i := 0; i < 3; i++ {
			messageType, body, err := conn.ReadMessage()
			if err != nil {
				t.Error(err)
				return
			}
			if messageType != websocket.BinaryMessage {
				t.Errorf("websocket message type = %d", messageType)
				return
			}
			var frame larkws.Frame
			if err := frame.Unmarshal(body); err != nil {
				t.Error(err)
				return
			}
			eventType, _ := testPBStringField(frame.Payload, 1)
			eventID, _ := testPBStringField(frame.Payload, 2)
			sessionID, _ := testPBVarintField(frame.Payload, 3)
			createdAt, _ := testPBStringField(frame.Payload, 4)
			messageID, _ := testPBStringField(body, 11)
			frameType, _ := testPBVarintField(body, 12)
			var pcm []byte
			var closeReason uint64
			var upstream, downstream capturedAudioFormat
			if appendPayload, ok := testPBBytesField(frame.Payload, 11); ok {
				pcm, _ = testPBBytesField(appendPayload, 1)
			}
			if createPayload, ok := testPBBytesField(frame.Payload, 10); ok {
				if sessionPayload, ok := testPBBytesField(createPayload, 1); ok {
					if mediaPayload, ok := testPBBytesField(sessionPayload, 1); ok {
						upstream = testAudioFormat(mediaPayload, 1)
						downstream = testAudioFormat(mediaPayload, 2)
					}
				}
			}
			if closePayload, ok := testPBBytesField(frame.Payload, 13); ok {
				closeReason, _ = testPBVarintField(closePayload, 1)
			}
			captured <- capturedRealtimeEvent{
				eventType:       eventType,
				eventID:         eventID,
				sessionID:       sessionID,
				createdAt:       createdAt,
				pcm:             append([]byte(nil), pcm...),
				serviceID:       frame.Service,
				method:          frame.Method,
				payloadEncoding: frame.PayloadEncoding,
				payloadType:     frame.PayloadType,
				messageID:       messageID,
				frameType:       frameType,
				closeReason:     closeReason,
				upstream:        upstream,
				downstream:      downstream,
			}
			if i == 0 {
				serverEvent := appendPBString(nil, 1, "session.created")
				serverEvent = appendPBVarint(serverEvent, 3, 42)
				responseFrame := &larkws.Frame{
					Service:         meetingRealtimeFrontierService,
					Method:          meetingRealtimeFrontierMethod,
					PayloadEncoding: meetingRealtimePayloadEncoding,
					PayloadType:     meetingRealtimePayloadType,
					Payload:         serverEvent,
				}
				encoded, _ := responseFrame.Marshal()
				if err := conn.WriteMessage(websocket.BinaryMessage, encoded); err != nil {
					t.Error(err)
					return
				}
			}
		}
	}))
	defer server.Close()

	websocketURL := "ws" + strings.TrimPrefix(server.URL, "http")
	factory := &larkRealtimeSessionFactory{
		endpoints: staticMeetingRealtimeEndpoint{
			endpoint: meetingRealtimeEndpoint{
				WebSocketURL: websocketURL,
				ExpiresAt:    time.Now().Add(time.Minute),
			},
		},
		dialer: websocket.DefaultDialer,
	}
	session, err := factory.Open(context.Background(), "meeting-1")
	if err != nil {
		t.Fatal(err)
	}
	pcm := []byte{1, 2, 3, 4}
	if err := session.SendPCM(context.Background(), pcm); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	create := <-captured
	appendEvent := <-captured
	closeEvent := <-captured
	if create.eventType != "session.create" ||
		create.serviceID != meetingRealtimeFrontierService ||
		create.method != meetingRealtimeFrontierMethod ||
		create.payloadEncoding != meetingRealtimePayloadEncoding ||
		create.payloadType != meetingRealtimePayloadType ||
		create.eventID == "" || create.messageID != create.eventID || create.frameType != 0 ||
		create.sessionID != 0 {
		t.Fatalf("create event = %+v", create)
	}
	if _, err := time.Parse(time.RFC3339, create.createdAt); err != nil {
		t.Fatalf("create created_at = %q: %v", create.createdAt, err)
	}
	wantFormat := capturedAudioFormat{mediaType: "audio/pcm", encoding: "s16le", sampleRate: 24000}
	if create.upstream != wantFormat || create.downstream != wantFormat {
		t.Fatalf("session formats = upstream %+v downstream %+v", create.upstream, create.downstream)
	}
	if appendEvent.eventType != "audio.upstream.append" ||
		appendEvent.sessionID != 42 ||
		appendEvent.eventID != "" || appendEvent.messageID != "" ||
		!bytes.Equal(appendEvent.pcm, pcm) {
		t.Fatalf("append event = %+v", appendEvent)
	}
	if _, err := time.Parse(time.RFC3339, appendEvent.createdAt); err != nil {
		t.Fatalf("append created_at = %q: %v", appendEvent.createdAt, err)
	}
	if closeEvent.eventType != "session.close" || closeEvent.sessionID != 42 ||
		closeEvent.eventID == "" || closeEvent.messageID != closeEvent.eventID || closeEvent.closeReason != 1 {
		t.Fatalf("close event = %+v", closeEvent)
	}
}

type recordingMeetingAudioSession struct {
	mu     sync.Mutex
	frames [][]byte
}

func (s *recordingMeetingAudioSession) SendPCM(_ context.Context, pcm []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frames = append(s.frames, append([]byte(nil), pcm...))
	return nil
}

func (s *recordingMeetingAudioSession) Close(context.Context) error { return nil }

func TestStreamMeetingPCMFramesAndRejectsWAV(t *testing.T) {
	session := &recordingMeetingAudioSession{}
	pcm := make([]byte, meetingRealtimeFrameBytes+2)
	if err := streamMeetingPCM(context.Background(), session, bytes.NewReader(pcm)); err != nil {
		t.Fatal(err)
	}
	if len(session.frames) != 2 ||
		len(session.frames[0]) != meetingRealtimeFrameBytes ||
		len(session.frames[1]) != 2 {
		t.Fatalf("PCM frames = %v", []int{len(session.frames[0]), len(session.frames[1])})
	}
	if err := streamMeetingPCM(context.Background(), session, bytes.NewReader([]byte("RIFF-not-pcm"))); err == nil {
		t.Fatal("WAV payload unexpectedly accepted")
	}
}

func TestMeetingEndedDeactivatesVoice(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := &meetingVoiceManager{
		enabled:         true,
		activeMeetingID: "meeting-1",
		activeUserID:    "ou_inviter",
		activeChatID:    "oc_direct",
		ctx:             ctx,
		cancel:          cancel,
		jobs:            make(chan meetingVoiceJob, 1),
	}
	manager.activeCtx, manager.activeCancel = context.WithCancel(ctx)
	activeCtx := manager.activeCtx
	manager.HandleMeetingEnded([]byte(`{"event":{"meeting":{"id":"meeting-1"}}}`))
	if got := manager.ActiveMeetingID(); got != "" {
		t.Fatalf("active meeting after ended event = %q", got)
	}
	select {
	case <-activeCtx.Done():
	default:
		t.Fatal("meeting audio context was not cancelled")
	}
}

func testPBStringField(payload []byte, wanted int) (string, bool) {
	value, ok := testPBBytesField(payload, wanted)
	return string(value), ok
}

func testPBVarintField(payload []byte, wanted int) (uint64, bool) {
	for len(payload) > 0 {
		field, wire, rest, err := consumePBTag(payload)
		if err != nil {
			return 0, false
		}
		payload = rest
		if field == wanted && wire == 0 {
			value, _, err := consumePBVarint(payload)
			return value, err == nil
		}
		next, err := skipPBValue(payload, wire)
		if err != nil {
			return 0, false
		}
		payload = next
	}
	return 0, false
}

func testPBBytesField(payload []byte, wanted int) ([]byte, bool) {
	for len(payload) > 0 {
		field, wire, rest, err := consumePBTag(payload)
		if err != nil {
			return nil, false
		}
		payload = rest
		if field == wanted && wire == 2 {
			value, _, err := consumePBBytes(payload)
			return value, err == nil
		}
		next, err := skipPBValue(payload, wire)
		if err != nil {
			return nil, false
		}
		payload = next
	}
	return nil, false
}

func testAudioFormat(mediaPayload []byte, field int) capturedAudioFormat {
	formatPayload, ok := testPBBytesField(mediaPayload, field)
	if !ok {
		return capturedAudioFormat{}
	}
	mediaType, _ := testPBStringField(formatPayload, 1)
	encoding, _ := testPBStringField(formatPayload, 2)
	sampleRate, _ := testPBVarintField(formatPayload, 3)
	return capturedAudioFormat{mediaType: mediaType, encoding: encoding, sampleRate: sampleRate}
}
