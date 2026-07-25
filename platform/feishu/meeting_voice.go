package feishu

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gorilla/websocket"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
	"github.com/wangning19940904/AgentMux/core"
)

const (
	meetingEndedEventType            = "vc.bot.meeting_ended_v1"
	meetingRealtimeEndpointAPIPath   = "/open-apis/vc/v1/realtime/endpoint"
	meetingRealtimeSampleRate        = 24000
	meetingRealtimeBytesPerSecond    = meetingRealtimeSampleRate * 2
	meetingRealtimeFrameBytes        = 4800 // 100 ms of 24 kHz mono s16le PCM.
	meetingRealtimeMaxFrameBytes     = 8000
	meetingRealtimeSessionTimeout    = 10 * time.Second
	meetingRealtimeWriteTimeout      = 10 * time.Second
	meetingVoiceQueueSize            = 64
	meetingVoiceMaxSegmentRunes      = 240
	meetingVoiceTTSResponseLimit     = 32 << 20
	meetingRealtimePayloadType       = "protobuf"
	meetingRealtimeFrontierServiceID = 0
)

type meetingVoiceConfig struct {
	Enabled    bool
	TTSBaseURL string
	TTSAPIKey  string
	TTSModel   string
	TTSVoice   string
}

func parseMeetingVoiceConfig(cfg map[string]any) (meetingVoiceConfig, error) {
	enabled, err := meetingVoiceBool(cfg[core.ChannelConfigMeetingVoice])
	if err != nil {
		return meetingVoiceConfig{}, err
	}
	result := meetingVoiceConfig{Enabled: enabled}
	if !enabled {
		return result, nil
	}

	result.TTSBaseURL = strings.TrimRight(meetingVoiceString(cfg[core.ChannelConfigMeetingTTSBaseURL]), "/")
	if result.TTSBaseURL == "" {
		result.TTSBaseURL = core.DefaultMeetingTTSBaseURL
	}
	parsed, err := url.Parse(result.TTSBaseURL)
	if err != nil ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" ||
		parsed.User != nil ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return meetingVoiceConfig{}, fmt.Errorf("invalid %s %q", core.ChannelConfigMeetingTTSBaseURL, result.TTSBaseURL)
	}
	result.TTSAPIKey = meetingVoiceString(cfg[core.ChannelConfigMeetingTTSAPIKey])
	if result.TTSAPIKey == "" || result.TTSAPIKey == "<redacted>" {
		return meetingVoiceConfig{}, fmt.Errorf("%s is required when meeting voice is enabled", core.ChannelConfigMeetingTTSAPIKey)
	}
	result.TTSModel = meetingVoiceString(cfg[core.ChannelConfigMeetingTTSModel])
	if result.TTSModel == "" {
		result.TTSModel = core.DefaultMeetingTTSModel
	}
	result.TTSVoice = meetingVoiceString(cfg[core.ChannelConfigMeetingTTSVoice])
	if result.TTSVoice == "" {
		result.TTSVoice = core.DefaultMeetingTTSVoice
	}
	return result, nil
}

func meetingVoiceString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		return ""
	}
}

func meetingVoiceBool(value any) (bool, error) {
	switch typed := value.(type) {
	case nil:
		return false, nil
	case bool:
		return typed, nil
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "", "false", "0", "no", "off":
			return false, nil
		case "true", "1", "yes", "on":
			return true, nil
		default:
			return false, fmt.Errorf("invalid %s %q (want true or false)", core.ChannelConfigMeetingVoice, typed)
		}
	default:
		return false, fmt.Errorf("invalid %s value", core.ChannelConfigMeetingVoice)
	}
}

type meetingVoiceClient interface {
	BeginMeetingSpeech(context.Context, string, string) (core.SpeechReply, error)
}

type meetingRealtimeEndpointProvider interface {
	GetMeetingRealtimeEndpoint(context.Context, string) (meetingRealtimeEndpoint, error)
}

type meetingRealtimeEndpoint struct {
	WebSocketURL string
	ExpiresAt    time.Time
}

type meetingSpeechSynthesizer interface {
	Synthesize(context.Context, string) (io.ReadCloser, error)
}

type meetingAudioSession interface {
	SendPCM(context.Context, []byte) error
	Close(context.Context) error
}

type meetingAudioSessionFactory interface {
	Open(context.Context, string) (meetingAudioSession, error)
}

type meetingVoiceJob struct {
	meetingID string
	text      string
	reset     bool
}

// meetingVoiceManager owns the single ordered audio queue for one application
// bot. It intentionally keeps TTS and WebSocket writes off the agent event
// loop so textual replies remain responsive if speech is slow or unavailable.
type meetingVoiceManager struct {
	enabled     bool
	synthesizer meetingSpeechSynthesizer
	sessions    meetingAudioSessionFactory
	report      func(string, error)

	ctx    context.Context
	cancel context.CancelFunc
	jobs   chan meetingVoiceJob
	wg     sync.WaitGroup

	mu              sync.RWMutex
	activeMeetingID string
	activeUserID    string
	activeChatID    string
	activeCtx       context.Context
	activeCancel    context.CancelFunc
}

func newMeetingVoiceManager(provider meetingRealtimeEndpointProvider, config meetingVoiceConfig) *meetingVoiceManager {
	manager := &meetingVoiceManager{enabled: config.Enabled}
	if !config.Enabled {
		return manager
	}
	manager.synthesizer = &openAICompatibleTTSSynthesizer{
		baseURL: config.TTSBaseURL,
		apiKey:  config.TTSAPIKey,
		model:   config.TTSModel,
		voice:   config.TTSVoice,
		client:  &http.Client{Timeout: 90 * time.Second},
	}
	manager.sessions = &larkRealtimeSessionFactory{
		endpoints: provider,
		dialer:    websocket.DefaultDialer,
	}
	manager.report = func(message string, err error) {
		slog.Error(message, "error", err)
	}
	manager.ctx, manager.cancel = context.WithCancel(context.Background())
	manager.jobs = make(chan meetingVoiceJob, meetingVoiceQueueSize)
	manager.wg.Add(1)
	go manager.run()
	return manager
}

func (m *meetingVoiceManager) Activate(meetingID, userID, chatID string) {
	if m == nil || !m.enabled {
		return
	}
	meetingID = strings.TrimSpace(meetingID)
	userID = strings.TrimSpace(userID)
	chatID = strings.TrimSpace(chatID)
	if meetingID == "" || userID == "" || chatID == "" {
		return
	}
	m.mu.Lock()
	changed := m.activeMeetingID != meetingID || m.activeUserID != userID || m.activeChatID != chatID
	if changed && m.activeCancel != nil {
		m.activeCancel()
	}
	m.activeMeetingID = meetingID
	m.activeUserID = userID
	m.activeChatID = chatID
	if changed {
		parent := m.ctx
		if parent == nil {
			parent = context.Background()
		}
		m.activeCtx, m.activeCancel = context.WithCancel(parent)
	}
	m.mu.Unlock()
	if changed {
		m.enqueueControl(meetingVoiceJob{meetingID: meetingID, reset: true})
	}
}

func (m *meetingVoiceManager) Deactivate(meetingID string) {
	if m == nil || !m.enabled {
		return
	}
	meetingID = strings.TrimSpace(meetingID)
	m.mu.Lock()
	if meetingID != "" && meetingID != m.activeMeetingID {
		m.mu.Unlock()
		return
	}
	previous := m.activeMeetingID
	if m.activeCancel != nil {
		m.activeCancel()
	}
	m.activeMeetingID = ""
	m.activeUserID = ""
	m.activeChatID = ""
	m.activeCtx = nil
	m.activeCancel = nil
	m.mu.Unlock()
	if previous != "" {
		m.enqueueControl(meetingVoiceJob{meetingID: previous, reset: true})
	}
}

func (m *meetingVoiceManager) ActiveMeetingID() string {
	if m == nil || !m.enabled {
		return ""
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.activeMeetingID
}

func (m *meetingVoiceManager) activeContext(meetingID string) context.Context {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if meetingID == "" || meetingID != m.activeMeetingID {
		return nil
	}
	if m.activeCtx != nil {
		return m.activeCtx
	}
	return m.ctx
}

func (m *meetingVoiceManager) HandleMeetingEnded(payload []byte) {
	var event struct {
		Event struct {
			Meeting struct {
				ID string `json:"id"`
			} `json:"meeting"`
		} `json:"event"`
	}
	if json.Unmarshal(payload, &event) != nil {
		return
	}
	m.Deactivate(event.Event.Meeting.ID)
}

func (m *meetingVoiceManager) BeginReply(userID, chatID string) core.SpeechReply {
	m.mu.RLock()
	meetingID := m.activeMeetingID
	activeUserID := m.activeUserID
	activeChatID := m.activeChatID
	m.mu.RUnlock()
	if meetingID == "" ||
		strings.TrimSpace(userID) != activeUserID ||
		strings.TrimSpace(chatID) != activeChatID {
		return nil
	}
	return &meetingSpeechReply{manager: m, meetingID: meetingID}
}

func (m *meetingVoiceManager) enqueue(meetingID, text string) error {
	if m == nil || !m.enabled || strings.TrimSpace(text) == "" {
		return nil
	}
	if m.ActiveMeetingID() != meetingID {
		return errors.New("meeting voice is no longer active for this meeting")
	}
	select {
	case m.jobs <- meetingVoiceJob{meetingID: meetingID, text: text}:
		return nil
	case <-m.ctx.Done():
		return errors.New("meeting voice is closed")
	default:
		return errors.New("meeting voice queue is full")
	}
}

func (m *meetingVoiceManager) enqueueControl(job meetingVoiceJob) {
	if m == nil || !m.enabled {
		return
	}
	select {
	case m.jobs <- job:
	case <-m.ctx.Done():
	default:
		// activeMeetingID is authoritative. Even if the reset marker cannot
		// be queued, pending jobs are discarded by the worker before sending.
	}
}

func (m *meetingVoiceManager) Close() {
	if m == nil || !m.enabled || m.cancel == nil {
		return
	}
	m.cancel()
	m.wg.Wait()
}

func (m *meetingVoiceManager) run() {
	defer m.wg.Done()
	var session meetingAudioSession
	var sessionMeetingID string
	closeSession := func() {
		if session != nil {
			closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = session.Close(closeCtx)
			cancel()
		}
		session = nil
		sessionMeetingID = ""
	}
	defer closeSession()

	for {
		select {
		case <-m.ctx.Done():
			return
		case job := <-m.jobs:
			if job.reset {
				if sessionMeetingID != job.meetingID || m.ActiveMeetingID() == "" {
					closeSession()
				}
				continue
			}
			if job.meetingID == "" || job.meetingID != m.ActiveMeetingID() {
				continue
			}
			jobCtx := m.activeContext(job.meetingID)
			if jobCtx == nil {
				continue
			}
			if session == nil || sessionMeetingID != job.meetingID {
				closeSession()
				openCtx, cancel := context.WithTimeout(jobCtx, meetingRealtimeSessionTimeout)
				opened, err := m.sessions.Open(openCtx, job.meetingID)
				cancel()
				if err != nil {
					if jobCtx.Err() == nil {
						m.reportError("open meeting realtime audio session", err)
					}
					continue
				}
				session = opened
				sessionMeetingID = job.meetingID
			}

			audio, err := m.synthesizer.Synthesize(jobCtx, job.text)
			if err != nil {
				if jobCtx.Err() == nil {
					m.reportError("synthesize meeting speech", err)
				}
				continue
			}
			err = streamMeetingPCM(jobCtx, session, audio)
			closeErr := audio.Close()
			if err != nil {
				if jobCtx.Err() == nil {
					m.reportError("send speech to meeting", err)
				}
				closeSession()
			} else if closeErr != nil {
				m.reportError("close TTS audio response", closeErr)
			}
		}
	}
}

func (m *meetingVoiceManager) reportError(message string, err error) {
	if err == nil {
		return
	}
	if m.report != nil {
		m.report(message, err)
	}
}

type meetingSpeechReply struct {
	manager   *meetingVoiceManager
	meetingID string

	mu      sync.Mutex
	seen    string
	pending string
	done    bool
}

func (r *meetingSpeechReply) Update(_ context.Context, text string, done bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done {
		return nil
	}

	delta := appendedMeetingSpeechText(r.seen, text)
	r.seen = text
	r.pending += speechPlainText(delta)
	segments, pending := splitMeetingSpeech(r.pending, done)
	r.pending = pending
	if done {
		r.done = true
	}
	for _, segment := range segments {
		if err := r.manager.enqueue(r.meetingID, segment); err != nil {
			return err
		}
	}
	return nil
}

func (r *meetingSpeechReply) Close(ctx context.Context) error {
	r.mu.Lock()
	done := r.done
	text := r.seen
	r.mu.Unlock()
	if done {
		return nil
	}
	return r.Update(ctx, text, true)
}

func appendedMeetingSpeechText(previous, current string) string {
	if strings.HasPrefix(current, previous) {
		return current[len(previous):]
	}
	// Some adapters replace an earlier cumulative snapshot with their final
	// normalized answer. Only emit the suffix after the shared prefix so
	// already spoken content is never repeated.
	common := 0
	limit := len(previous)
	if len(current) < limit {
		limit = len(current)
	}
	for common < limit && previous[common] == current[common] {
		common++
	}
	for common > 0 && (!utf8.RuneStart(previous[common]) || !utf8.RuneStart(current[common])) {
		common--
	}
	if common < len(previous) {
		return ""
	}
	return current[common:]
}

var (
	meetingSpeechURLPattern      = regexp.MustCompile(`https?://[^\s<>()]+`)
	meetingSpeechHTMLPattern     = regexp.MustCompile(`<[^>]+>`)
	meetingSpeechInlineCode      = regexp.MustCompile("`[^`]*`")
	meetingSpeechMarkdownLink    = regexp.MustCompile(`\[([^\]]+)\]\([^)]+\)`)
	meetingSpeechMarkdownSymbols = strings.NewReplacer(
		"```", " ", "**", "", "__", "", "~~", "", "#", "",
		"*", "", "_", "", ">", "", "|", " ", "[", "", "]", "",
	)
)

func speechPlainText(value string) string {
	value = meetingSpeechMarkdownLink.ReplaceAllString(value, "$1")
	value = meetingSpeechURLPattern.ReplaceAllString(value, "")
	value = meetingSpeechHTMLPattern.ReplaceAllString(value, " ")
	value = meetingSpeechInlineCode.ReplaceAllString(value, " ")
	value = meetingSpeechMarkdownSymbols.Replace(value)
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, value)
}

func splitMeetingSpeech(value string, flush bool) ([]string, string) {
	var segments []string
	var current strings.Builder
	runeCount := 0
	emit := func() {
		text := strings.Join(strings.Fields(current.String()), " ")
		current.Reset()
		runeCount = 0
		if text != "" {
			segments = append(segments, text)
		}
	}

	for _, r := range value {
		current.WriteRune(r)
		runeCount++
		if isMeetingSpeechBoundary(r) || runeCount >= meetingVoiceMaxSegmentRunes {
			emit()
		}
	}
	if flush {
		emit()
		return segments, ""
	}
	return segments, current.String()
}

func isMeetingSpeechBoundary(r rune) bool {
	switch r {
	case '。', '！', '？', '；', '.', '!', '?', ';', '\n':
		return true
	default:
		return false
	}
}

func (c *larkClient) BeginMeetingSpeech(_ context.Context, userID, chatID string) (core.SpeechReply, error) {
	if c.meetingVoice == nil {
		return nil, nil
	}
	return c.meetingVoice.BeginReply(userID, chatID), nil
}

func (c *larkClient) MeetingInviteJoined(meetingID, inviterOpenID, approvalChatID string) {
	if c.meetingVoice != nil {
		c.meetingVoice.Activate(meetingID, inviterOpenID, approvalChatID)
	}
}

func (c *larkClient) GetMeetingRealtimeEndpoint(ctx context.Context, meetingID string) (meetingRealtimeEndpoint, error) {
	meetingID = strings.TrimSpace(meetingID)
	if meetingID == "" {
		return meetingRealtimeEndpoint{}, errors.New("meeting realtime endpoint requires meeting id")
	}
	path := meetingRealtimeEndpointAPIPath + "?meeting_id=" + url.QueryEscape(meetingID)
	resp, err := c.api.Get(ctx, path, nil, larkcore.AccessTokenTypeTenant)
	if err != nil {
		return meetingRealtimeEndpoint{}, err
	}
	if resp == nil {
		return meetingRealtimeEndpoint{}, errors.New("meeting realtime endpoint returned an empty response")
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return meetingRealtimeEndpoint{}, fmt.Errorf("meeting realtime endpoint returned HTTP %d", resp.StatusCode)
	}
	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			WebSocketURL string `json:"websocket_url"`
			ExpiresTime  string `json:"expires_time"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.RawBody, &result); err != nil {
		return meetingRealtimeEndpoint{}, fmt.Errorf("decode meeting realtime endpoint: %w", err)
	}
	if result.Code != 0 {
		return meetingRealtimeEndpoint{}, fmt.Errorf("get meeting realtime endpoint failed: %s (code %d)", strings.TrimSpace(result.Msg), result.Code)
	}
	webSocketURL := strings.TrimSpace(result.Data.WebSocketURL)
	parsed, err := url.Parse(webSocketURL)
	if err != nil || (parsed.Scheme != "ws" && parsed.Scheme != "wss") || parsed.Host == "" {
		return meetingRealtimeEndpoint{}, errors.New("meeting realtime endpoint returned an invalid websocket_url")
	}
	var expiresAt time.Time
	if raw := strings.TrimSpace(result.Data.ExpiresTime); raw != "" {
		expiresAt, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			return meetingRealtimeEndpoint{}, fmt.Errorf("decode meeting realtime endpoint expires_time: %w", err)
		}
	}
	return meetingRealtimeEndpoint{WebSocketURL: webSocketURL, ExpiresAt: expiresAt}, nil
}

type openAICompatibleTTSSynthesizer struct {
	baseURL string
	apiKey  string
	model   string
	voice   string
	client  *http.Client
}

func (s *openAICompatibleTTSSynthesizer) Synthesize(ctx context.Context, text string) (io.ReadCloser, error) {
	payload, err := json.Marshal(map[string]any{
		"model":           s.model,
		"voice":           s.voice,
		"input":           text,
		"response_format": "pcm",
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.baseURL, "/")+"/audio/speech", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "audio/pcm, application/octet-stream")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return nil, fmt.Errorf("TTS returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return &limitedReadCloser{
		Reader: io.LimitReader(resp.Body, meetingVoiceTTSResponseLimit+1),
		closer: resp.Body,
		limit:  meetingVoiceTTSResponseLimit,
	}, nil
}

type limitedReadCloser struct {
	io.Reader
	closer io.Closer
	limit  int64
	read   int64
}

func (r *limitedReadCloser) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.read += int64(n)
	if r.read > r.limit {
		return n, errors.New("TTS response exceeds 32 MiB limit")
	}
	return n, err
}

func (r *limitedReadCloser) Close() error { return r.closer.Close() }

type websocketContextDialer interface {
	DialContext(context.Context, string, http.Header) (*websocket.Conn, *http.Response, error)
}

type larkRealtimeSessionFactory struct {
	endpoints meetingRealtimeEndpointProvider
	dialer    websocketContextDialer
}

func (f *larkRealtimeSessionFactory) Open(ctx context.Context, meetingID string) (meetingAudioSession, error) {
	endpoint, err := f.endpoints.GetMeetingRealtimeEndpoint(ctx, meetingID)
	if err != nil {
		return nil, err
	}
	if !endpoint.ExpiresAt.IsZero() && !time.Now().Before(endpoint.ExpiresAt) {
		return nil, errors.New("meeting realtime websocket URL is already expired")
	}
	conn, response, err := f.dialer.DialContext(ctx, endpoint.WebSocketURL, nil)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, fmt.Errorf("dial meeting realtime websocket: %w", err)
	}
	serviceID := meetingRealtimeServiceID(endpoint.WebSocketURL)
	session := &larkRealtimeSession{
		conn:      conn,
		serviceID: serviceID,
		readDone:  make(chan struct{}),
	}
	if err := session.create(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	go session.readLoop()
	return session, nil
}

func meetingRealtimeServiceID(rawURL string) int32 {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return meetingRealtimeFrontierServiceID
	}
	value := parsed.Query().Get("service_id")
	number, err := strconv.ParseInt(value, 10, 32)
	if err != nil {
		return meetingRealtimeFrontierServiceID
	}
	return int32(number)
}

type larkRealtimeSession struct {
	conn      *websocket.Conn
	serviceID int32
	sessionID uint64
	sequence  uint64
	writeMu   sync.Mutex
	closeOnce sync.Once
	readDone  chan struct{}
}

func (s *larkRealtimeSession) create(ctx context.Context) error {
	payload := marshalMeetingRealtimeSessionCreate()
	if err := s.writeClientEvent(ctx, payload); err != nil {
		return fmt.Errorf("send meeting realtime session.create: %w", err)
	}
	deadline := time.Now().Add(meetingRealtimeSessionTimeout)
	if requested, ok := ctx.Deadline(); ok && requested.Before(deadline) {
		deadline = requested
	}
	if err := s.conn.SetReadDeadline(deadline); err != nil {
		return err
	}
	defer s.conn.SetReadDeadline(time.Time{})
	for {
		messageType, body, err := s.conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("wait for meeting realtime session.created: %w", err)
		}
		if messageType != websocket.BinaryMessage {
			continue
		}
		event, err := unmarshalMeetingRealtimeServerEvent(body)
		if err != nil {
			return fmt.Errorf("decode meeting realtime server event: %w", err)
		}
		switch event.Type {
		case "session.created":
			if event.SessionID == 0 {
				return errors.New("meeting realtime session.created is missing session_id")
			}
			s.sessionID = event.SessionID
			return nil
		case "error":
			return errors.New("meeting realtime server rejected session.create")
		}
	}
}

func (s *larkRealtimeSession) readLoop() {
	defer close(s.readDone)
	for {
		if _, _, err := s.conn.ReadMessage(); err != nil {
			return
		}
	}
}

func (s *larkRealtimeSession) SendPCM(ctx context.Context, pcm []byte) error {
	if len(pcm) == 0 {
		return nil
	}
	if len(pcm) > meetingRealtimeMaxFrameBytes {
		return fmt.Errorf("meeting realtime PCM frame is %d bytes; max is %d", len(pcm), meetingRealtimeMaxFrameBytes)
	}
	if len(pcm)%2 != 0 {
		return errors.New("meeting realtime PCM frame must contain complete s16le samples")
	}
	return s.writeClientEvent(ctx, marshalMeetingRealtimeAudioAppend(s.sessionID, pcm))
}

func (s *larkRealtimeSession) Close(ctx context.Context) error {
	var closeErr error
	s.closeOnce.Do(func() {
		if s.sessionID != 0 {
			closeErr = s.writeClientEvent(ctx, marshalMeetingRealtimeSessionClose(s.sessionID))
		}
		if err := s.conn.Close(); closeErr == nil {
			closeErr = err
		}
	})
	return closeErr
}

func (s *larkRealtimeSession) writeClientEvent(ctx context.Context, payload []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.sequence++
	frame := &larkws.Frame{
		SeqID:       s.sequence,
		Service:     s.serviceID,
		Method:      int32(larkws.FrameTypeData),
		PayloadType: meetingRealtimePayloadType,
		Payload:     payload,
	}
	encoded, err := frame.Marshal()
	if err != nil {
		return err
	}
	deadline := time.Now().Add(meetingRealtimeWriteTimeout)
	if requested, ok := ctx.Deadline(); ok && requested.Before(deadline) {
		deadline = requested
	}
	if err := s.conn.SetWriteDeadline(deadline); err != nil {
		return err
	}
	return s.conn.WriteMessage(websocket.BinaryMessage, encoded)
}

func streamMeetingPCM(ctx context.Context, session meetingAudioSession, source io.Reader) error {
	reader := bufio.NewReaderSize(source, meetingRealtimeFrameBytes)
	if prefix, _ := reader.Peek(4); bytes.Equal(prefix, []byte("RIFF")) {
		return errors.New("TTS returned WAV data; raw 24 kHz s16le PCM is required")
	}
	buffer := make([]byte, meetingRealtimeFrameBytes)
	first := true
	previousBytes := 0
	for {
		n, err := io.ReadFull(reader, buffer)
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
			return err
		}
		if n > 0 {
			if n%2 != 0 {
				return errors.New("TTS returned an incomplete s16le sample")
			}
			if !first {
				delay := time.Duration(previousBytes) * time.Second / meetingRealtimeBytesPerSecond
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
			}
			if err := session.SendPCM(ctx, buffer[:n]); err != nil {
				return err
			}
			first = false
			previousBytes = n
		}
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			return nil
		}
	}
}

// The limited-preview realtime API exposes ClientEvent/ServerEvent as
// protobuf messages. Keeping their small wire codec local avoids checking in
// generated code while preserving the documented field layout.
func marshalMeetingRealtimeSessionCreate() []byte {
	format := appendPBString(nil, 1, "audio/pcm")
	format = appendPBString(format, 2, "s16le")
	format = appendPBVarint(format, 3, meetingRealtimeSampleRate)
	media := appendPBMessage(nil, 1, format)
	media = appendPBMessage(media, 2, format)
	session := appendPBMessage(nil, 1, media)
	create := appendPBMessage(nil, 1, session)
	event := appendPBString(nil, 1, "session.create")
	event = appendPBVarint(event, 2, 0)
	return appendPBMessage(event, 3, create)
}

func marshalMeetingRealtimeAudioAppend(sessionID uint64, pcm []byte) []byte {
	appendEvent := appendPBBytes(nil, 1, pcm)
	event := appendPBString(nil, 1, "audio.upstream.append")
	event = appendPBVarint(event, 2, sessionID)
	return appendPBMessage(event, 4, appendEvent)
}

func marshalMeetingRealtimeSessionClose(sessionID uint64) []byte {
	event := appendPBString(nil, 1, "session.close")
	event = appendPBVarint(event, 2, sessionID)
	return appendPBMessage(event, 5, nil)
}

type meetingRealtimeServerEvent struct {
	Type      string
	SessionID uint64
}

func unmarshalMeetingRealtimeServerEvent(frameBytes []byte) (meetingRealtimeServerEvent, error) {
	var frame larkws.Frame
	if err := frame.Unmarshal(frameBytes); err != nil {
		return meetingRealtimeServerEvent{}, err
	}
	var event meetingRealtimeServerEvent
	payload := frame.Payload
	for len(payload) > 0 {
		field, wire, rest, err := consumePBTag(payload)
		if err != nil {
			return event, err
		}
		payload = rest
		switch {
		case field == 1 && wire == 2:
			value, next, err := consumePBBytes(payload)
			if err != nil {
				return event, err
			}
			event.Type = string(value)
			payload = next
		case field == 2 && wire == 0:
			value, next, err := consumePBVarint(payload)
			if err != nil {
				return event, err
			}
			event.SessionID = value
			payload = next
		default:
			next, err := skipPBValue(payload, wire)
			if err != nil {
				return event, err
			}
			payload = next
		}
	}
	return event, nil
}

func appendPBTag(dst []byte, field int, wire byte) []byte {
	return binary.AppendUvarint(dst, uint64(field<<3)|uint64(wire))
}

func appendPBVarint(dst []byte, field int, value uint64) []byte {
	dst = appendPBTag(dst, field, 0)
	return binary.AppendUvarint(dst, value)
}

func appendPBString(dst []byte, field int, value string) []byte {
	return appendPBBytes(dst, field, []byte(value))
}

func appendPBMessage(dst []byte, field int, value []byte) []byte {
	return appendPBBytes(dst, field, value)
}

func appendPBBytes(dst []byte, field int, value []byte) []byte {
	dst = appendPBTag(dst, field, 2)
	dst = binary.AppendUvarint(dst, uint64(len(value)))
	return append(dst, value...)
}

func consumePBTag(src []byte) (int, byte, []byte, error) {
	value, n := binary.Uvarint(src)
	if n <= 0 {
		return 0, 0, nil, errors.New("invalid protobuf tag")
	}
	field := int(value >> 3)
	wire := byte(value & 7)
	if field <= 0 {
		return 0, 0, nil, errors.New("invalid protobuf field number")
	}
	return field, wire, src[n:], nil
}

func consumePBVarint(src []byte) (uint64, []byte, error) {
	value, n := binary.Uvarint(src)
	if n <= 0 {
		return 0, nil, errors.New("invalid protobuf varint")
	}
	return value, src[n:], nil
}

func consumePBBytes(src []byte) ([]byte, []byte, error) {
	length, n := binary.Uvarint(src)
	if n <= 0 || length > uint64(len(src)-n) {
		return nil, nil, errors.New("invalid protobuf bytes field")
	}
	end := n + int(length)
	return src[n:end], src[end:], nil
}

func skipPBValue(src []byte, wire byte) ([]byte, error) {
	switch wire {
	case 0:
		_, rest, err := consumePBVarint(src)
		return rest, err
	case 1:
		if len(src) < 8 {
			return nil, errors.New("invalid protobuf fixed64 field")
		}
		return src[8:], nil
	case 2:
		_, rest, err := consumePBBytes(src)
		return rest, err
	case 5:
		if len(src) < 4 {
			return nil, errors.New("invalid protobuf fixed32 field")
		}
		return src[4:], nil
	default:
		return nil, fmt.Errorf("unsupported protobuf wire type %d", wire)
	}
}
