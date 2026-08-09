package feishu

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
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
	ttspkg "github.com/wangning19940904/AgentMux/tts"
)

const (
	meetingEndedEventType          = "vc.bot.meeting_ended_v1"
	meetingRealtimeEndpointAPIPath = "/open-apis/vc/v1/realtime/endpoint"
	meetingRealtimeSampleRate      = 24000
	meetingRealtimeBytesPerSecond  = meetingRealtimeSampleRate * 2
	meetingRealtimeFrameBytes      = 4800 // 100 ms of 24 kHz mono s16le PCM.
	meetingRealtimeMaxFrameBytes   = 8000
	meetingRealtimeSessionTimeout  = 10 * time.Second
	meetingRealtimeWriteTimeout    = 10 * time.Second
	meetingVoiceQueueSize          = 64
	meetingVoiceMaxSegmentRunes    = 240
	meetingVoiceTTSResponseLimit   = 32 << 20
	meetingRealtimePayloadEncoding = "binary"
	meetingRealtimePayloadType     = "application/x-protobuf"
	meetingRealtimeFrontierService = 33555721
	meetingRealtimeFrontierMethod  = 1
	meetingRealtimeFrontierNormal  = 0
)

type meetingVoiceConfig struct {
	Enabled    bool
	TTSBaseURL string
	TTSAPIKey  string
	TTSModel   string
	TTSVoice   string
	TTSMode    string
	LocalModel string
	LocalVoice string
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
	result.TTSMode = strings.ToLower(meetingVoiceString(cfg[core.ChannelConfigMeetingTTSMode]))
	if result.TTSMode == "" {
		result.TTSMode = core.DefaultMeetingTTSMode
	}
	if result.TTSMode == core.MeetingTTSModeLocal {
		result.LocalModel = meetingVoiceString(cfg[core.ChannelConfigMeetingLocalModel])
		if result.LocalModel == "" {
			result.LocalModel = core.DefaultMeetingLocalModel
		}
		model, ok := ttspkg.Lookup(result.LocalModel)
		if !ok {
			return meetingVoiceConfig{}, fmt.Errorf("unknown local TTS model %q", result.LocalModel)
		}
		result.LocalVoice = meetingVoiceString(cfg[core.ChannelConfigMeetingLocalVoice])
		if result.LocalVoice == "" {
			result.LocalVoice = model.Voices[0].ID
		}
		validVoice := false
		for _, voice := range model.Voices {
			if voice.ID == result.LocalVoice {
				validVoice = true
				break
			}
		}
		if !validVoice {
			return meetingVoiceConfig{}, fmt.Errorf("invalid local TTS voice %q", result.LocalVoice)
		}
		return result, nil
	}
	if result.TTSMode != core.MeetingTTSModeAPI {
		return meetingVoiceConfig{}, fmt.Errorf("invalid %s %q", core.ChannelConfigMeetingTTSMode, result.TTSMode)
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
	BeginMeetingSpeech(context.Context, *core.Message) (core.SpeechReply, error)
}

type meetingVoiceConfigurableClient interface {
	ConfigureMeetingVoice(meetingVoiceConfig)
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
	if config.TTSMode == core.MeetingTTSModeLocal {
		manager.synthesizer = &localTTSSynthesizer{
			manager: ttspkg.NewManager("", nil), model: config.LocalModel, voice: config.LocalVoice,
		}
	} else {
		manager.synthesizer = &openAICompatibleTTSSynthesizer{
			baseURL: config.TTSBaseURL,
			apiKey:  config.TTSAPIKey,
			model:   config.TTSModel,
			voice:   config.TTSVoice,
			client:  &http.Client{Timeout: 90 * time.Second},
		}
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
	if m == nil {
		return
	}
	meetingID = strings.TrimSpace(meetingID)
	userID = strings.TrimSpace(userID)
	chatID = strings.TrimSpace(chatID)
	if meetingID == "" {
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
	if m == nil {
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
	if m == nil {
		return ""
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.activeMeetingID
}

func (m *meetingVoiceManager) IsActive(meetingID string) bool {
	return m != nil && m.enabled && strings.TrimSpace(meetingID) != "" && m.ActiveMeetingID() == strings.TrimSpace(meetingID)
}

func (m *meetingVoiceManager) activation() (meetingID, userID, chatID string) {
	if m == nil {
		return "", "", ""
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.activeMeetingID, m.activeUserID, m.activeChatID
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

func (m *meetingVoiceManager) BeginReply(msg *core.Message) core.SpeechReply {
	if m == nil || !m.enabled || msg == nil {
		return nil
	}
	m.mu.RLock()
	meetingID := m.activeMeetingID
	activeUserID := m.activeUserID
	activeChatID := m.activeChatID
	m.mu.RUnlock()
	if meetingID == "" {
		return nil
	}
	// Meeting-originated questions and explicit /meeting commands already
	// carry the long meeting id, so they can speak even when the bot was joined
	// directly from the console and has no approval-chat correlation.
	if strings.TrimSpace(msg.MeetingID) == meetingID {
		return &meetingSpeechReply{manager: m, meetingID: meetingID}
	}
	if activeUserID == "" || activeChatID == "" ||
		strings.TrimSpace(msg.UserID) != activeUserID ||
		strings.TrimSpace(msg.ChatID) != activeChatID {
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
	if m == nil {
		return
	}
	m.mu.Lock()
	activeCancel := m.activeCancel
	m.activeCancel = nil
	m.activeCtx = nil
	m.mu.Unlock()
	if activeCancel != nil {
		activeCancel()
	}
	if !m.enabled || m.cancel == nil {
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

func (c *larkClient) BeginMeetingSpeech(_ context.Context, msg *core.Message) (core.SpeechReply, error) {
	manager := c.currentMeetingVoice()
	if manager == nil {
		return nil, nil
	}
	return manager.BeginReply(msg), nil
}

func (c *larkClient) MeetingInviteJoined(meetingID, inviterOpenID, approvalChatID string) {
	c.meetingVoiceMu.RLock()
	defer c.meetingVoiceMu.RUnlock()
	if c.meetingVoice != nil {
		c.meetingVoice.Activate(meetingID, inviterOpenID, approvalChatID)
	}
}

func (c *larkClient) currentMeetingVoice() *meetingVoiceManager {
	if c == nil {
		return nil
	}
	c.meetingVoiceMu.RLock()
	defer c.meetingVoiceMu.RUnlock()
	return c.meetingVoice
}

func (c *larkClient) ConfigureMeetingVoice(config meetingVoiceConfig) {
	if c == nil {
		return
	}
	next := newMeetingVoiceManager(c, config)
	c.meetingVoiceMu.Lock()
	previous := c.meetingVoice
	meetingID, userID, chatID := previous.activation()
	c.meetingVoice = next
	if next != nil && meetingID != "" {
		next.Activate(meetingID, userID, chatID)
	}
	c.meetingVoiceMu.Unlock()
	if previous != nil {
		previous.Close()
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
			WebSocketURL string          `json:"websocket_url"`
			ExpiresTime  json.RawMessage `json:"expires_time"`
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
	expiresAt, err := parseMeetingRealtimeExpiry(result.Data.ExpiresTime)
	if err != nil {
		return meetingRealtimeEndpoint{}, fmt.Errorf("decode meeting realtime endpoint expires_time: %w", err)
	}
	return meetingRealtimeEndpoint{WebSocketURL: webSocketURL, ExpiresAt: expiresAt}, nil
}

func parseMeetingRealtimeExpiry(raw json.RawMessage) (time.Time, error) {
	value := strings.TrimSpace(string(bytes.TrimSpace(raw)))
	if value == "" || value == "null" {
		return time.Time{}, nil
	}
	if strings.HasPrefix(value, `"`) {
		if err := json.Unmarshal(raw, &value); err != nil {
			return time.Time{}, err
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return time.Time{}, nil
		}
	}
	if timestamp, parseErr := strconv.ParseInt(value, 10, 64); parseErr == nil {
		if timestamp > 1e12 {
			return time.UnixMilli(timestamp).UTC(), nil
		}
		return time.Unix(timestamp, 0).UTC(), nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

type openAICompatibleTTSSynthesizer struct {
	baseURL string
	apiKey  string
	model   string
	voice   string
	client  *http.Client
}

type localTTSSynthesizer struct {
	manager *ttspkg.Manager
	model   string
	voice   string
}

func (s *localTTSSynthesizer) Synthesize(ctx context.Context, text string) (io.ReadCloser, error) {
	if s == nil || s.manager == nil {
		return nil, errors.New("local TTS manager is unavailable")
	}
	return s.manager.SynthesizePCM(ctx, s.model, s.voice, text)
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
	session := &larkRealtimeSession{
		conn:     conn,
		readDone: make(chan struct{}),
	}
	if err := session.create(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	go session.readLoop()
	return session, nil
}

type larkRealtimeSession struct {
	conn      *websocket.Conn
	sessionID uint64
	sequence  uint64
	writeMu   sync.Mutex
	closeOnce sync.Once
	readDone  chan struct{}
}

func (s *larkRealtimeSession) create(ctx context.Context) error {
	event := marshalMeetingRealtimeSessionCreate()
	if err := s.writeClientEvent(ctx, event); err != nil {
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
		if messageType == websocket.TextMessage {
			message := strings.TrimSpace(string(body))
			if message != "" {
				if len(message) > 512 {
					message = message[:512]
				}
				return fmt.Errorf("meeting realtime server returned text while creating session: %s", message)
			}
			continue
		}
		if messageType != websocket.BinaryMessage {
			continue
		}
		event, err := unmarshalMeetingRealtimeServerEvent(body)
		if err != nil {
			return fmt.Errorf("decode meeting realtime server event: %w", err)
		}
		switch event.Type {
		case "":
			if event.Skip {
				continue
			}
		case "session.created":
			if event.SessionID == 0 {
				return errors.New("meeting realtime session.created is missing session_id")
			}
			s.sessionID = event.SessionID
			return nil
		case "error":
			if event.ErrorMessage != "" {
				return fmt.Errorf("meeting realtime server rejected session.create: %s (code %d)", event.ErrorMessage, event.ErrorCode)
			}
			return fmt.Errorf("meeting realtime server rejected session.create (code %d)", event.ErrorCode)
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

func (s *larkRealtimeSession) writeClientEvent(ctx context.Context, event meetingRealtimeClientEvent) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.sequence++
	frame := &larkws.Frame{
		SeqID:           s.sequence,
		LogID:           0,
		Service:         meetingRealtimeFrontierService,
		Method:          meetingRealtimeFrontierMethod,
		PayloadEncoding: meetingRealtimePayloadEncoding,
		PayloadType:     meetingRealtimePayloadType,
		Payload:         event.payload,
		LogIDNew:        "",
	}
	encoded, err := frame.Marshal()
	if err != nil {
		return err
	}
	// The OpenAPI SDK's Frontier proto predates these two optional fields.
	// Realtime audio requires the current schema, so append them using their
	// official field numbers from frontier.proto.
	encoded = appendPBString(encoded, 11, event.eventID)
	encoded = appendPBVarint(encoded, 12, meetingRealtimeFrontierNormal)
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
// protobuf messages. Keep the small wire codec local, matching the official
// meeting_realtime.proto distributed with the voice-meeting starter.
type meetingRealtimeClientEvent struct {
	eventID string
	payload []byte
}

func marshalMeetingRealtimeSessionCreate() meetingRealtimeClientEvent {
	eventID := newMeetingRealtimeEventID()
	format := appendPBString(nil, 1, "audio/pcm")
	format = appendPBString(format, 2, "s16le")
	format = appendPBVarint(format, 3, meetingRealtimeSampleRate)
	media := appendPBMessage(nil, 1, format)
	media = appendPBMessage(media, 2, format)
	session := appendPBMessage(nil, 1, media)
	create := appendPBMessage(nil, 1, session)
	event := appendPBString(nil, 1, "session.create")
	event = appendPBString(event, 2, eventID)
	event = appendPBString(event, 4, meetingRealtimeCreatedAt())
	event = appendPBMessage(event, 10, create)
	return meetingRealtimeClientEvent{eventID: eventID, payload: event}
}

func marshalMeetingRealtimeAudioAppend(sessionID uint64, pcm []byte) meetingRealtimeClientEvent {
	appendEvent := appendPBBytes(nil, 1, pcm)
	event := appendPBString(nil, 1, "audio.upstream.append")
	event = appendPBVarint(event, 3, sessionID)
	event = appendPBString(event, 4, meetingRealtimeCreatedAt())
	event = appendPBMessage(event, 11, appendEvent)
	return meetingRealtimeClientEvent{payload: event}
}

func marshalMeetingRealtimeSessionClose(sessionID uint64) meetingRealtimeClientEvent {
	eventID := newMeetingRealtimeEventID()
	closeEvent := appendPBVarint(nil, 1, 1) // ClientCloseReason.USER_LEFT.
	event := appendPBString(nil, 1, "session.close")
	event = appendPBString(event, 2, eventID)
	event = appendPBVarint(event, 3, sessionID)
	event = appendPBString(event, 4, meetingRealtimeCreatedAt())
	event = appendPBMessage(event, 13, closeEvent)
	return meetingRealtimeClientEvent{eventID: eventID, payload: event}
}

func meetingRealtimeCreatedAt() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func newMeetingRealtimeEventID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("agentmux-%d", time.Now().UnixNano())
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}

type meetingRealtimeServerEvent struct {
	Type         string
	SessionID    uint64
	ErrorCode    uint64
	ErrorMessage string
	Skip         bool
}

func unmarshalMeetingRealtimeServerEvent(frameBytes []byte) (meetingRealtimeServerEvent, error) {
	frameType, ok, err := protobufVarintField(frameBytes, 12)
	if err != nil {
		return meetingRealtimeServerEvent{}, err
	}
	if ok && (frameType == 1 || frameType == 2 || frameType == 16 || frameType == 32) {
		return meetingRealtimeServerEvent{Skip: true}, nil
	}
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
		case field == 3 && wire == 0:
			value, next, err := consumePBVarint(payload)
			if err != nil {
				return event, err
			}
			event.SessionID = value
			payload = next
		case field == 90 && wire == 2:
			value, next, err := consumePBBytes(payload)
			if err != nil {
				return event, err
			}
			if err := unmarshalMeetingRealtimeError(value, &event); err != nil {
				return event, err
			}
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

func unmarshalMeetingRealtimeError(payload []byte, event *meetingRealtimeServerEvent) error {
	for len(payload) > 0 {
		field, wire, rest, err := consumePBTag(payload)
		if err != nil {
			return err
		}
		payload = rest
		switch {
		case field == 2 && wire == 0:
			value, next, err := consumePBVarint(payload)
			if err != nil {
				return err
			}
			event.ErrorCode = value
			payload = next
		case field == 3 && wire == 2:
			value, next, err := consumePBBytes(payload)
			if err != nil {
				return err
			}
			event.ErrorMessage = string(value)
			payload = next
		default:
			next, err := skipPBValue(payload, wire)
			if err != nil {
				return err
			}
			payload = next
		}
	}
	return nil
}

func protobufVarintField(payload []byte, wanted int) (uint64, bool, error) {
	for len(payload) > 0 {
		field, wire, rest, err := consumePBTag(payload)
		if err != nil {
			return 0, false, err
		}
		payload = rest
		if field == wanted && wire == 0 {
			value, _, err := consumePBVarint(payload)
			return value, err == nil, err
		}
		next, err := skipPBValue(payload, wire)
		if err != nil {
			return 0, false, err
		}
		payload = next
	}
	return 0, false, nil
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
