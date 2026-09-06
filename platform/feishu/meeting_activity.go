package feishu

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/wangning19940904/AgentMux/core"
)

const (
	meetingActivityEventType = "vc.bot.meeting_activity_v1"
	meetingEventsAPIPath     = "/open-apis/vc/v1/bots/events"
	meetingUserActiveAPIPath = "/open-apis/vc/v1/bots/user_active_meeting"
	meetingTimelineLimit     = 5000
	meetingEndedRetention    = 5 * time.Minute
	meetingVoiceWakeTTL      = 15 * time.Second
)

type meetingActivityState struct {
	meeting      core.ActiveMeeting
	items        []core.MeetingTimelineItem
	turns        []core.MeetingTurn
	seen         map[string]struct{}
	participants map[string]core.MeetingActor
	voiceWake    map[string]time.Time
	pageToken    string
	joinCallID   string
}

type meetingActivityManager struct {
	client *larkClient
	now    func() time.Time

	mu      sync.Mutex
	states  map[string]*meetingActivityState
	project string
	inbound chan<- *core.Message
}

func newMeetingActivityManager(client *larkClient) *meetingActivityManager {
	return &meetingActivityManager{client: client, now: time.Now, states: map[string]*meetingActivityState{}}
}

// Keep the low-level client itself compatible with meetingActivityClient.
// Platform delegates through that optional interface, so omitting any one of
// these methods would otherwise compile but make every meeting read/write
// operation look unavailable at runtime.
func (c *larkClient) ActiveMeetings() []core.ActiveMeeting {
	if c == nil || c.meetingActivity == nil {
		return []core.ActiveMeeting{}
	}
	return c.meetingActivity.ActiveMeetings()
}

func (c *larkClient) MeetingActivity(meetingID string) (core.MeetingDetail, error) {
	if c == nil || c.meetingActivity == nil {
		return core.MeetingDetail{}, errors.New("meeting activity is unavailable")
	}
	return c.meetingActivity.MeetingActivity(meetingID)
}

func (c *larkClient) UserActiveMeetings(ctx context.Context, userID string) ([]core.ActiveMeeting, error) {
	if c == nil || c.meetingActivity == nil {
		return nil, errors.New("active meeting lookup is unavailable")
	}
	return c.meetingActivity.UserActiveMeetings(ctx, userID)
}

func (c *larkClient) MeetingPromptContext(meetingID string) string {
	if c == nil || c.meetingActivity == nil {
		return ""
	}
	return c.meetingActivity.MeetingPromptContext(meetingID)
}

func (c *larkClient) UpsertMeetingTurn(turn core.MeetingTurn) {
	if c != nil && c.meetingActivity != nil {
		c.meetingActivity.UpsertMeetingTurn(turn)
	}
}

func (m *meetingActivityManager) report(message string, err error) {
	if err != nil {
		slog.Warn(message, "error", err)
	}
}

func (m *meetingActivityManager) SetInbound(project string, inbound chan<- *core.Message) {
	m.mu.Lock()
	m.project, m.inbound = project, inbound
	m.mu.Unlock()
}

func (m *meetingActivityManager) Register(meetingID, meetingNo, topic string) {
	m.RegisterJoin(meetingID, meetingNo, topic, "")
}

// RegisterJoin starts a new bot attendance generation. A bot can leave and
// rejoin the same meeting ID, so each successful join must replace the prior
// generation instead of preserving its JoinedAt/ended state.
func (m *meetingActivityManager) RegisterJoin(meetingID, meetingNo, topic, callID string) {
	meetingID = strings.TrimSpace(meetingID)
	if meetingID == "" {
		return
	}
	now := m.now().UTC()
	m.mu.Lock()
	state := m.stateLocked(meetingID)
	state.meeting.ID = meetingID
	if meetingNo != "" {
		state.meeting.MeetingNumber = strings.TrimSpace(meetingNo)
	}
	if topic != "" {
		state.meeting.Topic = strings.TrimSpace(topic)
	}
	state.meeting.Status = "active"
	state.meeting.JoinedAt = now
	state.meeting.EndedAt = time.Time{}
	state.meeting.LastActivityAt = now
	state.joinCallID = strings.TrimSpace(callID)
	meeting := state.meeting
	m.mu.Unlock()
	m.notify(core.MeetingEvent{Type: "meeting.changed", MeetingID: meetingID, Meeting: &meeting})
}

// observeMeeting records activity at its business timestamp. Activity newer
// than a recorded end proves that the bot has rejoined (or that an old end
// callback arrived out of order), while historical backfill must not revive a
// genuinely ended meeting.
func (m *meetingActivityManager) observeMeeting(meetingID, meetingNo, topic string, observedAt time.Time) core.ActiveMeeting {
	if observedAt.IsZero() {
		observedAt = m.now().UTC()
	} else {
		observedAt = observedAt.UTC()
	}
	m.mu.Lock()
	state := m.stateLocked(meetingID)
	if meetingNo != "" {
		state.meeting.MeetingNumber = strings.TrimSpace(meetingNo)
	}
	if topic != "" {
		state.meeting.Topic = strings.TrimSpace(topic)
	}
	if state.meeting.Status != "ended" || state.meeting.EndedAt.IsZero() || observedAt.After(state.meeting.EndedAt) {
		if state.meeting.Status == "ended" || state.meeting.JoinedAt.IsZero() {
			state.meeting.JoinedAt = observedAt
			state.joinCallID = ""
		}
		state.meeting.Status = "active"
		state.meeting.EndedAt = time.Time{}
	}
	if observedAt.After(state.meeting.LastActivityAt) {
		state.meeting.LastActivityAt = observedAt
	}
	meeting := state.meeting
	m.mu.Unlock()
	m.notify(core.MeetingEvent{Type: "meeting.changed", MeetingID: meetingID, Meeting: &meeting})
	return meeting
}

// confirmActiveMeeting applies a current-state API result. Unlike historical
// activity, an ongoing/user-active response is authoritative at query time.
func (m *meetingActivityManager) confirmActiveMeeting(meetingID, meetingNo, topic string) core.ActiveMeeting {
	meetingID = strings.TrimSpace(meetingID)
	if meetingID == "" {
		return core.ActiveMeeting{}
	}
	now := m.now().UTC()
	m.mu.Lock()
	state := m.stateLocked(meetingID)
	if meetingNo != "" {
		state.meeting.MeetingNumber = strings.TrimSpace(meetingNo)
	}
	if topic != "" {
		state.meeting.Topic = strings.TrimSpace(topic)
	}
	if state.meeting.Status != "active" || state.meeting.JoinedAt.IsZero() {
		state.meeting.JoinedAt = now
		state.joinCallID = ""
	}
	state.meeting.Status = "active"
	state.meeting.EndedAt = time.Time{}
	if now.After(state.meeting.LastActivityAt) {
		state.meeting.LastActivityAt = now
	}
	meeting := state.meeting
	m.mu.Unlock()
	m.notify(core.MeetingEvent{Type: "meeting.changed", MeetingID: meetingID, Meeting: &meeting})
	return meeting
}

func (m *meetingActivityManager) stateLocked(meetingID string) *meetingActivityState {
	state := m.states[meetingID]
	if state == nil {
		state = &meetingActivityState{
			meeting: core.ActiveMeeting{ID: meetingID, Status: "active"},
			seen:    map[string]struct{}{}, participants: map[string]core.MeetingActor{}, voiceWake: map[string]time.Time{},
		}
		m.states[meetingID] = state
	}
	return state
}

func (m *meetingActivityManager) Handle(ctx context.Context, payload []byte) error {
	items, err := activityPayloadItems(payload)
	if err != nil {
		return err
	}
	for _, raw := range items {
		if err := m.handleActivityItem(ctx, raw, true); err != nil {
			m.report("skip malformed meeting activity item", err)
		}
	}
	return nil
}

func activityPayloadItems(payload []byte) ([]json.RawMessage, error) {
	var envelope struct {
		Event struct {
			Items []json.RawMessage `json:"meeting_activity_items"`
		} `json:"event"`
		Items []json.RawMessage `json:"meeting_activity_items"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, fmt.Errorf("decode meeting activity: %w", err)
	}
	items := envelope.Event.Items
	if len(items) == 0 {
		items = envelope.Items
	}
	if len(items) == 0 {
		return nil, errors.New("meeting activity contains no meeting_activity_items")
	}
	return items, nil
}

func (m *meetingActivityManager) handleActivityItem(ctx context.Context, raw json.RawMessage, forwardMentions bool) error {
	var item map[string]any
	if err := json.Unmarshal(raw, &item); err != nil {
		return err
	}
	meeting := mapValue(item["meeting"])
	meetingID := firstString(meeting, "id", "meeting_id")
	if meetingID == "" {
		meetingID = firstString(item, "meeting_id")
	}
	if meetingID == "" {
		return errors.New("activity item missing meeting id")
	}

	activityType := strings.TrimSpace(firstString(item, "activity_event_type", "event_type"))
	normalized := normalizeMeetingActivity(meetingID, activityType, item)
	observedAt := meetingTime(item, "event_time", "timestamp")
	for _, timeline := range normalized {
		if timeline.EventTime.After(observedAt) {
			observedAt = timeline.EventTime
		}
	}
	m.observeMeeting(meetingID, firstString(meeting, "meeting_no"), firstString(meeting, "topic", "meeting_title"), observedAt)
	if len(normalized) == 0 {
		slog.Debug("unknown meeting activity item acknowledged", "meeting_id", meetingID, "activity_event_type", activityType)
		return nil
	}

	accepted := make([]core.MeetingTimelineItem, 0, len(normalized))
	m.mu.Lock()
	state := m.stateLocked(meetingID)
	for _, timeline := range normalized {
		if _, exists := state.seen[timeline.ID]; exists {
			continue
		}
		state.seen[timeline.ID] = struct{}{}
		state.items = append(state.items, timeline)
		if timeline.Actor != nil && timeline.Actor.ID != "" {
			switch timeline.Kind {
			case "participant_joined":
				state.participants[timeline.Actor.ID] = *timeline.Actor
			case "participant_left":
				delete(state.participants, timeline.Actor.ID)
			}
		}
		if len(state.items) > meetingTimelineLimit {
			removed := state.items[:len(state.items)-meetingTimelineLimit]
			state.items = append([]core.MeetingTimelineItem(nil), state.items[len(state.items)-meetingTimelineLimit:]...)
			for _, old := range removed {
				delete(state.seen, old.ID)
			}
		}
		if timeline.EventTime.After(state.meeting.LastActivityAt) {
			state.meeting.LastActivityAt = timeline.EventTime
		}
		accepted = append(accepted, timeline)
	}
	state.meeting.ParticipantCount = len(state.participants)
	meetingCopy := state.meeting
	project, inbound := m.project, m.inbound
	m.mu.Unlock()
	if len(accepted) == 0 {
		return nil
	}
	m.notify(core.MeetingEvent{Type: "meeting.activity", MeetingID: meetingID, Meeting: &meetingCopy, Items: accepted})
	if forwardMentions {
		for _, timeline := range accepted {
			m.forwardMeetingMention(ctx, project, inbound, meetingCopy, timeline)
		}
	}
	return nil
}

func (m *meetingActivityManager) forwardMeetingMention(ctx context.Context, project string, inbound chan<- *core.Message, meeting core.ActiveMeeting, item core.MeetingTimelineItem) {
	if inbound == nil || item.Actor == nil || item.Actor.ID == "" || item.Actor.ID == m.client.botOpenID {
		return
	}
	text := ""
	switch item.Kind {
	case "chat":
		var ok bool
		text, ok = stripMeetingBotMention(item.Text, m.client.botName)
		if !ok {
			return
		}
	case "transcript":
		voice := m.client.currentMeetingVoice()
		if voice == nil || !voice.IsActive(meeting.ID) {
			return
		}
		wakeNames := meetingVoiceWakeNames(m.client)
		if meetingVoiceActorIsBot(item.Actor, m.client.botOpenID, meetingVoiceBotNames(m.client)) {
			return
		}
		var ok bool
		text, ok = m.transcriptVoiceQuestion(meeting.ID, item, wakeNames)
		if !ok {
			return
		}
	default:
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	msg := &core.Message{
		ID: item.ID + "-agent-question", ChatID: "meeting:" + meeting.ID, ChatType: "meeting",
		ConversationKey: "meeting:" + meeting.ID, UserID: item.Actor.ID, UserName: item.Actor.Name,
		Text: text, Timestamp: item.EventTime, MentionedBot: true, Platform: m.client.platform,
		Project: project, Origin: core.OriginMeeting, MeetingID: meeting.ID,
		MeetingNumber: meeting.MeetingNumber, MeetingTopic: meeting.Topic,
	}
	select {
	case inbound <- msg:
	case <-ctx.Done():
	default:
		m.report("drop meeting mention because channel relay is full", errors.New(meeting.ID))
	}
}

func meetingVoiceWakeNames(client *larkClient) []string {
	if client == nil {
		return nil
	}
	return uniqueMeetingVoiceNames(append(append([]string(nil), client.meetingWakeWords...), client.botName, client.agentName, client.channelName))
}

func meetingVoiceBotNames(client *larkClient) []string {
	if client == nil {
		return nil
	}
	return uniqueMeetingVoiceNames([]string{client.botName, client.agentName, client.channelName})
}

func uniqueMeetingVoiceNames(candidates []string) []string {
	seen := map[string]struct{}{}
	var names []string
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		key := strings.ToLower(candidate)
		if candidate == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		names = append(names, candidate)
	}
	sort.SliceStable(names, func(i, j int) bool { return len([]rune(names[i])) > len([]rune(names[j])) })
	return names
}

func meetingVoiceActorIsBot(actor *core.MeetingActor, botOpenID string, wakeNames []string) bool {
	if actor == nil {
		return false
	}
	if botOpenID != "" && actor.ID == botOpenID {
		return true
	}
	if strings.Contains(strings.ToLower(actor.ParticipantType), "bot") {
		return true
	}
	for _, name := range wakeNames {
		if strings.EqualFold(strings.TrimSpace(actor.Name), name) {
			return true
		}
	}
	return false
}

func (m *meetingActivityManager) transcriptVoiceQuestion(meetingID string, item core.MeetingTimelineItem, wakeNames []string) (string, bool) {
	text := strings.TrimSpace(item.Text)
	if text == "" || item.Actor == nil || item.Actor.ID == "" || len(wakeNames) == 0 {
		return "", false
	}
	question, woke := stripMeetingTranscriptWakeWord(text, wakeNames)
	now := m.now().UTC()
	speakerID := item.Actor.ID

	m.mu.Lock()
	state := m.stateLocked(meetingID)
	if state.voiceWake == nil {
		state.voiceWake = map[string]time.Time{}
	}
	expiresAt := state.voiceWake[speakerID]
	if !expiresAt.IsZero() && !now.Before(expiresAt) {
		delete(state.voiceWake, speakerID)
		expiresAt = time.Time{}
	}
	if woke {
		delete(state.voiceWake, speakerID)
		if strings.TrimSpace(question) == "" {
			state.voiceWake[speakerID] = now.Add(meetingVoiceWakeTTL)
			m.mu.Unlock()
			return "", false
		}
		m.mu.Unlock()
		return question, true
	}
	if !expiresAt.IsZero() {
		delete(state.voiceWake, speakerID)
		m.mu.Unlock()
		return text, true
	}
	m.mu.Unlock()
	return "", false
}

func stripMeetingTranscriptWakeWord(text string, wakeNames []string) (string, bool) {
	text = trimMeetingVoiceDelimiters(strings.TrimSpace(text))
	for {
		trimmed := text
		for _, greeting := range []string{"你好", "您好", "嗨", "嘿", "请问"} {
			if hasMeetingVoicePrefix(text, greeting) {
				text = trimMeetingVoiceDelimiters(text[len(greeting):])
				break
			}
		}
		if text == trimmed {
			break
		}
	}
	for _, name := range wakeNames {
		for _, prefix := range []string{"@" + name, "＠" + name, name} {
			if hasMeetingVoicePrefix(text, prefix) {
				return trimMeetingVoiceDelimiters(text[len(prefix):]), true
			}
		}
	}
	return "", false
}

func hasMeetingVoicePrefix(text, prefix string) bool {
	if prefix == "" || len(text) < len(prefix) || !strings.EqualFold(text[:len(prefix)], prefix) {
		return false
	}
	if len(text) == len(prefix) {
		return true
	}
	last := prefix[len(prefix)-1]
	next := text[len(prefix)]
	if isASCIIAlphaNumeric(last) && isASCIIAlphaNumeric(next) {
		return false
	}
	return true
}

func isASCIIAlphaNumeric(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}

func trimMeetingVoiceDelimiters(value string) string {
	return strings.TrimLeftFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("，,：:。.!！？?；;、-—", r)
	})
}

func stripMeetingBotMention(text, botName string) (string, bool) {
	text = strings.TrimSpace(text)
	botName = strings.TrimSpace(botName)
	if text == "" || botName == "" {
		return "", false
	}
	for _, prefix := range []string{"@" + botName, "＠" + botName} {
		if strings.HasPrefix(strings.ToLower(text), strings.ToLower(prefix)) {
			return strings.TrimSpace(strings.TrimLeft(text[len(prefix):], " ：:，,")), true
		}
	}
	return "", false
}

func normalizeMeetingActivity(meetingID, activityType string, item map[string]any) []core.MeetingTimelineItem {
	typeSpec := []struct {
		keys []string
		kind string
	}{
		{[]string{"chat_received_items", "chat_items"}, "chat"},
		{[]string{"transcript_received_items", "transcript_items"}, "transcript"},
		{[]string{"participant_joined_items", "participants_joined_items"}, "participant_joined"},
		{[]string{"participant_left_items", "participants_left_items"}, "participant_left"},
		{[]string{"magic_share_started_items", "share_started_items"}, "share_started"},
		{[]string{"magic_share_ended_items", "share_ended_items"}, "share_ended"},
	}
	var out []core.MeetingTimelineItem
	for _, spec := range typeSpec {
		for _, key := range spec.keys {
			for _, value := range sliceValue(item[key]) {
				out = append(out, normalizeMeetingTimelineItem(meetingID, spec.kind, mapValue(value)))
			}
		}
	}
	if len(out) == 0 && activityType != "" {
		kind := strings.TrimSuffix(activityType, "_received")
		switch activityType {
		case "chat_received":
			kind = "chat"
		case "transcript_received":
			kind = "transcript"
		case "magic_share_started":
			kind = "share_started"
		case "magic_share_ended":
			kind = "share_ended"
		}
		if kind == "chat" || kind == "transcript" || kind == "participant_joined" || kind == "participant_left" || kind == "share_started" || kind == "share_ended" {
			out = append(out, normalizeMeetingTimelineItem(meetingID, kind, item))
		}
	}
	filtered := out[:0]
	for _, value := range out {
		if value.ID != "" {
			filtered = append(filtered, value)
		}
	}
	return filtered
}

func normalizeMeetingTimelineItem(meetingID, kind string, raw map[string]any) core.MeetingTimelineItem {
	actorRaw := mapValue(raw["operator"])
	if len(actorRaw) == 0 {
		actorRaw = mapValue(raw["speaker"])
	}
	if len(actorRaw) == 0 {
		actorRaw = mapValue(raw["participant"])
	}
	actor := meetingActor(actorRaw)
	text := firstString(raw, "content", "text")
	if decoded := decodeMeetingText(text); decoded != "" {
		text = decoded
	}
	messageType := intValue(raw["message_type"])
	if kind == "chat" && messageType == 3 {
		kind = "reaction"
	}
	shareDoc := mapValue(raw["share_doc"])
	if len(shareDoc) == 0 {
		shareDoc = mapValue(raw["document"])
	}
	shareTitle, shareURL := firstString(shareDoc, "title", "name"), firstString(shareDoc, "url", "link")
	eventTime := meetingTime(raw, "send_time", "start_time_ms", "join_time", "leave_time", "timestamp", "event_time")
	if eventTime.IsZero() {
		eventTime = time.Now().UTC()
	}
	businessID := firstString(raw, "message_id", "sentence_id", "event_id", "id")
	if businessID == "" {
		actorID := ""
		if actor != nil {
			actorID = actor.ID
		}
		sum := sha256.Sum256([]byte(strings.Join([]string{meetingID, kind, actorID, eventTime.Format(time.RFC3339Nano), text, shareURL}, "\x00")))
		businessID = "meeting-event-" + hex.EncodeToString(sum[:12])
	}
	return core.MeetingTimelineItem{ID: businessID, MeetingID: meetingID, Kind: kind, EventTime: eventTime, Actor: actor, Text: strings.TrimSpace(text), MessageType: messageType, ShareTitle: shareTitle, ShareURL: shareURL}
}

func meetingActor(raw map[string]any) *core.MeetingActor {
	if len(raw) == 0 {
		return nil
	}
	id := firstString(raw, "open_id", "user_id", "id")
	if nested := mapValue(raw["id"]); id == "" {
		id = firstString(nested, "open_id", "user_id", "id")
	}
	name := firstString(raw, "user_name", "name", "display_name", "label")
	participantType := firstString(raw, "participant_type", "user_type", "type")
	return &core.MeetingActor{ID: id, Name: name, ParticipantType: participantType, Role: firstString(raw, "role")}
}

func decodeMeetingText(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var text string
	if json.Unmarshal([]byte(value), &text) == nil {
		return strings.TrimSpace(text)
	}
	var object map[string]any
	if json.Unmarshal([]byte(value), &object) == nil {
		return firstString(object, "text", "content")
	}
	return value
}

func mapValue(value any) map[string]any {
	if out, ok := value.(map[string]any); ok {
		return out
	}
	return map[string]any{}
}
func sliceValue(value any) []any {
	if out, ok := value.([]any); ok {
		return out
	}
	return nil
}
func firstString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		switch value := values[key].(type) {
		case string:
			if strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		case json.Number:
			return value.String()
		case float64:
			return strconv.FormatInt(int64(value), 10)
		}
	}
	return ""
}
func intValue(value any) int {
	switch v := value.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		n, _ := strconv.Atoi(v)
		return n
	}
	return 0
}
func meetingTime(values map[string]any, keys ...string) time.Time {
	for _, key := range keys {
		raw := strings.TrimSpace(firstString(values, key))
		if raw == "" {
			continue
		}
		if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			return parsed.UTC()
		}
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
			if n > 1e12 {
				return time.UnixMilli(n).UTC()
			}
			return time.Unix(n, 0).UTC()
		}
	}
	return time.Time{}
}

func (m *meetingActivityManager) notify(event core.MeetingEvent) {
	if event.CreatedAt.IsZero() {
		event.CreatedAt = m.now().UTC()
	}
	if m.client.meetingNotify != nil {
		m.client.meetingNotify(event)
	}
}

func (m *meetingActivityManager) ActiveMeetings() []core.ActiveMeeting {
	now := m.now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked(now)
	out := []core.ActiveMeeting{}
	for _, state := range m.states {
		if state.meeting.Status == "active" {
			out = append(out, state.meeting)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].JoinedAt.After(out[j].JoinedAt) })
	return out
}

func (m *meetingActivityManager) MeetingActivity(meetingID string) (core.MeetingDetail, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked(m.now().UTC())
	state := m.states[strings.TrimSpace(meetingID)]
	if state == nil {
		return core.MeetingDetail{}, fmt.Errorf("meeting %q is not active or retained", meetingID)
	}
	// Start the copies with non-nil slices so an idle meeting is encoded as
	// `items: []` / `turns: []` instead of JSON null. The web client treats
	// these fields as collections and may render the detail before the first
	// activity item or Agent turn arrives.
	return core.MeetingDetail{Meeting: state.meeting, Items: append([]core.MeetingTimelineItem{}, state.items...), Turns: append([]core.MeetingTurn{}, state.turns...)}, nil
}

func (m *meetingActivityManager) UpsertMeetingTurn(turn core.MeetingTurn) {
	m.mu.Lock()
	state := m.stateLocked(turn.MeetingID)
	replaced := false
	for i := range state.turns {
		if state.turns[i].ID == turn.ID {
			state.turns[i], replaced = turn, true
			break
		}
	}
	if !replaced {
		state.turns = append(state.turns, turn)
	}
	if len(state.turns) > 100 {
		state.turns = append([]core.MeetingTurn(nil), state.turns[len(state.turns)-100:]...)
	}
	m.mu.Unlock()
	m.notify(core.MeetingEvent{Type: "meeting.turn", MeetingID: turn.MeetingID, Turn: &turn})
}

func (m *meetingActivityManager) RecordBotMessage(meetingID, text, uuid string) {
	now := m.now().UTC()
	id := strings.TrimSpace(uuid)
	if id == "" {
		sum := sha256.Sum256([]byte(meetingID + "\x00" + text + "\x00" + now.Format(time.RFC3339Nano)))
		id = "out-" + hex.EncodeToString(sum[:12])
	}
	item := core.MeetingTimelineItem{ID: id, MeetingID: meetingID, Kind: "bot", EventTime: now, Actor: &core.MeetingActor{ID: m.client.botOpenID, Name: m.client.botName, ParticipantType: "bot"}, Text: text}
	m.mu.Lock()
	state := m.stateLocked(meetingID)
	if _, exists := state.seen[id]; exists {
		m.mu.Unlock()
		return
	}
	state.seen[id] = struct{}{}
	state.items = append(state.items, item)
	state.meeting.LastActivityAt = now
	meeting := state.meeting
	m.mu.Unlock()
	m.notify(core.MeetingEvent{Type: "meeting.activity", MeetingID: meetingID, Meeting: &meeting, Items: []core.MeetingTimelineItem{item}})
}

func (m *meetingActivityManager) HandleMeetingEnded(payload []byte) {
	var event map[string]any
	if json.Unmarshal(payload, &event) != nil {
		return
	}
	data := mapValue(event["event"])
	meeting := mapValue(data["meeting"])
	meetingID := firstString(meeting, "id", "meeting_id")
	if meetingID == "" {
		meetingID = firstString(data, "meeting_id")
	}
	if meetingID == "" {
		return
	}
	callID := firstString(data, "call_id")
	if callID == "" {
		callID = firstString(mapValue(data["call"]), "id", "call_id")
	}
	if callID == "" {
		callID = firstString(meeting, "call_id")
	}
	endedAt := meetingTime(data, "end_time", "leave_time", "event_time", "timestamp")
	if endedAt.IsZero() {
		endedAt = meetingTime(mapValue(event["header"]), "create_time")
	}
	if endedAt.IsZero() {
		endedAt = meetingTime(meeting, "end_time")
	}
	if endedAt.IsZero() {
		endedAt = m.now().UTC()
	}
	m.mu.Lock()
	state := m.stateLocked(meetingID)
	if callID != "" && state.joinCallID != "" && callID != state.joinCallID {
		m.mu.Unlock()
		slog.Debug("ignore stale meeting ended event from a previous call", "meeting_id", meetingID)
		return
	}
	if !state.meeting.JoinedAt.IsZero() && endedAt.Before(state.meeting.JoinedAt) {
		m.mu.Unlock()
		slog.Debug("ignore meeting ended event older than the current join", "meeting_id", meetingID)
		return
	}
	if state.meeting.Status == "ended" && !state.meeting.EndedAt.IsZero() && !endedAt.After(state.meeting.EndedAt) {
		m.mu.Unlock()
		return
	}
	state.meeting.Status = "ended"
	state.meeting.EndedAt = endedAt
	if endedAt.After(state.meeting.LastActivityAt) {
		state.meeting.LastActivityAt = endedAt
	}
	copy := state.meeting
	m.mu.Unlock()
	m.notify(core.MeetingEvent{Type: "meeting.changed", MeetingID: meetingID, Meeting: &copy})
}

func (m *meetingActivityManager) pruneLocked(now time.Time) {
	for id, state := range m.states {
		if state.meeting.Status == "ended" && !state.meeting.EndedAt.IsZero() && now.Sub(state.meeting.EndedAt) > meetingEndedRetention {
			delete(m.states, id)
		}
	}
}

func (m *meetingActivityManager) Recover(ctx context.Context) {
	for _, meeting := range m.ActiveMeetings() {
		m.Backfill(ctx, meeting.ID, false)
	}
}

// BootstrapActiveMeetings restores in-memory state after a process restart.
// The VC API does not offer a tenant-wide "meetings joined by this bot"
// endpoint, but it can return meetings where a user and the bot are
// simultaneously present. Discovery uses identities received from the platform
// when users interact with the bot, without a configured user list or polling.
func (m *meetingActivityManager) BootstrapActiveMeetings(ctx context.Context, userIDs []string) {
	seenUsers := map[string]struct{}{}
	meetings := map[string]core.ActiveMeeting{}
	for _, value := range userIDs {
		userID := strings.TrimSpace(value)
		if userID == "" {
			continue
		}
		if _, exists := seenUsers[userID]; exists {
			continue
		}
		seenUsers[userID] = struct{}{}
		visible, err := m.UserActiveMeetings(ctx, userID)
		if err != nil {
			m.report("bootstrap active meetings", err)
			continue
		}
		for _, meeting := range visible {
			if meeting.ID != "" {
				meetings[meeting.ID] = meeting
			}
		}
	}
	for meetingID := range meetings {
		m.Backfill(ctx, meetingID, true)
	}
	// Rebuilding a channel creates a new meeting voice manager. Restore its
	// active meeting after the read-only bootstrap has confirmed that the bot
	// is still present, otherwise voice replies are silently skipped until the
	// bot leaves and joins again.
	if active := m.ActiveMeetings(); len(active) > 0 && m.client != nil {
		m.client.MeetingInviteJoined(active[0].ID, "", "")
	}
}

func (m *meetingActivityManager) Backfill(ctx context.Context, meetingID string, full bool) {
	if m.client == nil || strings.TrimSpace(meetingID) == "" {
		return
	}
	m.mu.Lock()
	state := m.stateLocked(meetingID)
	token := state.pageToken
	if full {
		token = ""
	}
	m.mu.Unlock()
	for page := 0; page < 100 && ctx.Err() == nil; page++ {
		params := url.Values{"meeting_id": {meetingID}, "page_size": {"100"}}
		if token != "" {
			params.Set("page_token", token)
		}
		resp, err := m.client.api.Get(ctx, meetingEventsAPIPath+"?"+params.Encode(), nil, larkcore.AccessTokenTypeTenant)
		if err != nil {
			m.report("backfill meeting activity", err)
			return
		}
		if resp == nil || resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			m.report("backfill meeting activity", fmt.Errorf("HTTP %d", responseStatus(resp)))
			return
		}
		var result struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
			Data struct {
				Items     []json.RawMessage `json:"meeting_activity_items"`
				Events    []json.RawMessage `json:"events"`
				PageToken string            `json:"page_token"`
				HasMore   bool              `json:"has_more"`
				Meeting   map[string]any    `json:"meeting"`
			} `json:"data"`
		}
		if err := json.Unmarshal(resp.RawBody, &result); err != nil || result.Code != 0 {
			if err == nil {
				err = fmt.Errorf("%s (code %d)", result.Msg, result.Code)
			}
			m.report("decode meeting activity backfill", err)
			return
		}
		meetingMeta := result.Data.Meeting
		if firstString(meetingMeta, "id", "meeting_id") == "" {
			if meetingMeta == nil {
				meetingMeta = map[string]any{}
			}
			meetingMeta["id"] = meetingID
		}
		status := strings.ToLower(firstString(meetingMeta, "status", "meeting_status"))
		if status == "ongoing" || status == "active" {
			m.confirmActiveMeeting(meetingID, firstString(meetingMeta, "meeting_no", "meeting_number"), firstString(meetingMeta, "topic", "meeting_title"))
		}
		items := result.Data.Items
		if len(items) == 0 {
			items = result.Data.Events
		}
		for _, item := range items {
			normalized, err := normalizeMeetingBackfillItem(item, meetingMeta)
			if err == nil {
				// A full compensation seeds history and the page token. Historical
				// @ messages must not start fresh Agent turns when the bot rejoins;
				// only live callbacks and incremental reconnect compensation do.
				err = m.handleActivityItem(ctx, normalized, !full)
			}
			if err != nil {
				m.report("normalize meeting activity backfill", err)
			}
		}
		if result.Data.PageToken != "" {
			token = result.Data.PageToken
			m.mu.Lock()
			m.stateLocked(meetingID).pageToken = token
			m.mu.Unlock()
		}
		if !result.Data.HasMore {
			return
		}
	}
}

// normalizeMeetingBackfillItem accepts both raw meeting_activity_items and
// the public events[] envelope returned by the compensation API. The latter
// stores the original activity body under payload.
func normalizeMeetingBackfillItem(raw json.RawMessage, fallbackMeeting map[string]any) (json.RawMessage, error) {
	var event map[string]any
	if err := json.Unmarshal(raw, &event); err != nil {
		return nil, err
	}
	item := event
	if payload := mapValue(event["payload"]); len(payload) != 0 {
		item = make(map[string]any, len(payload)+2)
		for key, value := range payload {
			item[key] = value
		}
		if firstString(item, "activity_event_type", "event_type") == "" {
			item["activity_event_type"] = firstString(event, "event_type", "activity_event_type")
		}
		if eventTime := firstString(event, "event_time", "timestamp"); eventTime != "" {
			item["event_time"] = eventTime
		}
	}
	if len(mapValue(item["meeting"])) == 0 {
		meeting := make(map[string]any, len(fallbackMeeting))
		for key, value := range fallbackMeeting {
			meeting[key] = value
		}
		item["meeting"] = meeting
	}
	return json.Marshal(item)
}

func responseStatus(resp *larkcore.ApiResp) int {
	if resp == nil {
		return 0
	}
	return resp.StatusCode
}

func (m *meetingActivityManager) UserActiveMeetings(ctx context.Context, userID string) ([]core.ActiveMeeting, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, errors.New("user id is required")
	}
	params := url.Values{"user_id": {userID}}
	resp, err := m.client.api.Get(ctx, meetingUserActiveAPIPath+"?"+params.Encode(), nil, larkcore.AccessTokenTypeTenant)
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("list active meetings returned HTTP %d", responseStatus(resp))
	}
	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Meetings []map[string]any `json:"meetings"`
			Items    []map[string]any `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(resp.RawBody, &result); err != nil {
		return nil, err
	}
	if result.Code != 0 {
		return nil, fmt.Errorf("list active meetings failed: %s (code %d)", result.Msg, result.Code)
	}
	items := result.Data.Meetings
	if len(items) == 0 {
		items = result.Data.Items
	}
	out := make([]core.ActiveMeeting, 0, len(items))
	for _, value := range items {
		meeting := mapValue(value["meeting"])
		if len(meeting) == 0 {
			meeting = value
		}
		id := firstString(meeting, "meeting_id", "id")
		if id == "" {
			continue
		}
		out = append(out, m.confirmActiveMeeting(id, firstString(meeting, "meeting_no", "meeting_number"), firstString(meeting, "meeting_title", "topic")))
	}
	return out, nil
}

func (m *meetingActivityManager) MeetingPromptContext(meetingID string) string {
	detail, err := m.MeetingActivity(meetingID)
	if err != nil {
		return ""
	}
	chats, transcripts, shares := []string{}, []string{}, []string{}
	for i := len(detail.Items) - 1; i >= 0; i-- {
		item := detail.Items[i]
		name := "参会者"
		if item.Actor != nil && item.Actor.Name != "" {
			name = item.Actor.Name
		}
		switch item.Kind {
		case "chat":
			if len(chats) < 15 {
				chats = append(chats, fmt.Sprintf("%s: %s", name, item.Text))
			}
		case "transcript":
			if len(transcripts) < 15 {
				transcripts = append(transcripts, fmt.Sprintf("%s: %s", name, item.Text))
			}
		case "share_started":
			if len(shares) < 3 {
				shares = append(shares, strings.TrimSpace(item.ShareTitle+" "+item.ShareURL))
			}
		}
	}
	reverse := func(values []string) {
		for i, j := 0, len(values)-1; i < j; i, j = i+1, j-1 {
			values[i], values[j] = values[j], values[i]
		}
	}
	reverse(chats)
	reverse(transcripts)
	reverse(shares)
	return "当前会议上下文（只作为回答依据，不要复述内部工具过程）：\n最近字幕：\n" + strings.Join(transcripts, "\n") + "\n最近聊天：\n" + strings.Join(chats, "\n") + "\n最近共享：\n" + strings.Join(shares, "\n")
}
