package superagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/wangning19940904/AgentMux/core"
)

// Delivery rules for the messages an agent sends without a person asking in
// that turn: Ticket reports, follow-ups, supervision and replies to events no
// person addressed to it. A reply to a person's message always goes at once.
// See docs/architecture/agent-notification-rules.md.

const (
	deliverySettingKey = "super_agent_delivery:"
	// deliveryInterval is how often held messages are looked at.
	deliveryInterval = 15 * time.Second
	// maxHeldMessages bounds one agent's held messages; past it the oldest
	// is dropped and recorded.
	maxHeldMessages = 200
	// maxDroppedKept is how many dropped messages the Console is shown.
	maxDroppedKept = 20
	// maxReleaseAttempts is how often a held message is tried before it is
	// dropped.
	maxReleaseAttempts = 3
	// digestItemRunes bounds each message folded into a digest.
	digestItemRunes     = 600
	maxQuietChatMinutes = 60
	maxDailyCap         = 100
	// chatActivityTTL is how long a chat's last activity is remembered.
	chatActivityTTL = 2 * time.Hour
)

// Why a message is held.
const (
	heldQuietHours = "quiet_hours"
	heldDailyCap   = "daily_cap"
	heldChatActive = "chat_active"
	heldBehind     = "behind_held"
)

// deliveryClass says whether a message is subject to the rules and, for the
// once-per-day rule, which item in which state it is about. An empty item is
// never judged a repeat.
type deliveryClass struct {
	unprompted bool
	item       string
	state      string
	ticketID   string
	triggerID  string
}

func (c deliveryClass) repeatKey() string {
	if c.item == "" {
		return ""
	}
	return c.item + "|" + c.state
}

// triggerDelivery classifies what the agent says back about a Trigger. A
// person's message that addressed the agent, an email to it or a plan decision
// is answered at once. Events AgentMux raised itself, document edits and
// thread traffic that did not address the agent are unprompted.
func triggerDelivery(trigger core.SuperAgentTrigger) deliveryClass {
	class := deliveryClass{ticketID: trigger.TicketID, triggerID: trigger.ID}
	item := "thread:" + trigger.ThreadKey
	if trigger.TicketID != "" {
		item = "ticket:" + trigger.TicketID
	}
	switch trigger.Kind {
	case core.SuperAgentTriggerTicketUpdate:
		// Each finished turn is news of its own, so reports are never
		// judged repeats.
		class.unprompted = true
	case core.SuperAgentTriggerTimer:
		class.unprompted, class.item, class.state = true, item, "follow_up"
	case core.SuperAgentTriggerSupervision:
		class.unprompted, class.item, class.state = true, item, "supervision"
	case core.SuperAgentTriggerPlan:
	default:
		switch {
		case trigger.SourceKind == core.SuperAgentSourceFeishuDoc:
			class.unprompted, class.item, class.state = true, item, "edited"
		case !trigger.Addressed:
			class.unprompted, class.item, class.state = true, item, "message"
		}
	}
	return class
}

// ticketReportDelivery classifies a Ticket's report that AgentMux sends
// itself, when the brain cannot.
func ticketReportDelivery(ticket core.SuperAgentTicket) deliveryClass {
	return deliveryClass{unprompted: true, ticketID: ticket.ID}
}

// recipientKey names the chat or person a message goes to, for the cap and
// the quiet-chat delay. It is empty when the message goes nowhere, such as a
// document Source without a chat to tell.
func recipientKey(source core.SuperAgentSource, target []byte) string {
	switch source.Kind {
	case core.SuperAgentSourceFeishuMessage, core.SuperAgentSourceFeishuDoc, core.SuperAgentSourceChannel:
		var reply feishuReply
		if json.Unmarshal(target, &reply) == nil {
			if reply.ChatID == "" {
				return ""
			}
			return chatKey(reply.ChannelID, reply.ChatID)
		}
	case core.SuperAgentSourceGitHub:
		var reply githubReply
		if json.Unmarshal(target, &reply) == nil && reply.Repo != "" {
			return fmt.Sprintf("github:%s#%d", strings.ToLower(reply.Repo), reply.Number)
		}
	case core.SuperAgentSourceEmail:
		var reply emailReply
		if json.Unmarshal(target, &reply) == nil && reply.To != "" {
			return "email:" + strings.ToLower(reply.To)
		}
	case core.SuperAgentSourceMailbox:
		var reply mailReply
		if json.Unmarshal(target, &reply) == nil && reply.MailTo != "" {
			return "mail:" + strings.ToLower(reply.MailTo)
		}
	}
	return source.ID + ":" + string(target)
}

func chatKey(channelID, chatID string) string {
	return "chat:" + channelID + ":" + chatID
}

// noteChatActivity records that something was said in a chat now.
func (s *Service) noteChatActivity(key string, at time.Time) {
	if key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.chatActivity == nil {
		s.chatActivity = map[string]time.Time{}
	}
	s.chatActivity[key] = at
	if len(s.chatActivity) > 1000 {
		for other, last := range s.chatActivity {
			if at.Sub(last) > chatActivityTTL {
				delete(s.chatActivity, other)
			}
		}
	}
}

func (s *Service) lastChatActivity(key string) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.chatActivity[key]
}

// deliveryWindow is an agent's rules resolved for one moment.
type deliveryWindow struct {
	rules core.SuperAgentDelivery
	loc   *time.Location
	start int // minutes after midnight; -1 without quiet hours
	end   int
}

func newDeliveryWindow(rules core.SuperAgentDelivery) deliveryWindow {
	window := deliveryWindow{rules: rules, loc: time.Local, start: -1, end: -1}
	if rules.TimeZone != "" {
		if loc, err := time.LoadLocation(rules.TimeZone); err == nil {
			window.loc = loc
		}
	}
	if start, err := clockMinutes(rules.QuietStart); err == nil {
		if end, err := clockMinutes(rules.QuietEnd); err == nil && start != end {
			window.start, window.end = start, end
		}
	}
	return window
}

// clockMinutes parses "HH:MM" as minutes after midnight.
func clockMinutes(value string) (int, error) {
	hours, minutes, ok := strings.Cut(strings.TrimSpace(value), ":")
	if !ok {
		return 0, fmt.Errorf("%q is not HH:MM", value)
	}
	h, err := strconv.Atoi(hours)
	if err != nil || h < 0 || h > 23 || len(hours) > 2 {
		return 0, fmt.Errorf("%q is not HH:MM", value)
	}
	m, err := strconv.Atoi(minutes)
	if err != nil || m < 0 || m > 59 || len(minutes) != 2 {
		return 0, fmt.Errorf("%q is not HH:MM", value)
	}
	return h*60 + m, nil
}

// quiet reports whether at falls in quiet hours. A window whose start is
// after its end crosses midnight.
func (w deliveryWindow) quiet(at time.Time) bool {
	if w.start < 0 {
		return false
	}
	local := at.In(w.loc)
	minute := local.Hour()*60 + local.Minute()
	if w.start < w.end {
		return minute >= w.start && minute < w.end
	}
	return minute >= w.start || minute < w.end
}

// day is the date at a moment in the agent's time zone; the cap counts per day.
func (w deliveryWindow) day(at time.Time) string {
	return at.In(w.loc).Format(time.DateOnly)
}

// opens is when a message held at a moment for a reason may go next, for the
// Console. A message held by the cap waits for the next day out of quiet hours.
func (w deliveryWindow) opens(at time.Time, reason string) time.Time {
	local := at.In(w.loc)
	switch reason {
	case heldQuietHours:
		return w.nextOpen(local)
	case heldDailyCap:
		next := time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, w.loc)
		if w.quiet(next) {
			return w.nextOpen(next)
		}
		return next
	}
	return time.Time{}
}

// nextOpen is the end of the quiet hours at, or after, a moment.
func (w deliveryWindow) nextOpen(local time.Time) time.Time {
	if !w.quiet(local) {
		return local
	}
	end := time.Date(local.Year(), local.Month(), local.Day(), w.end/60, w.end%60, 0, 0, w.loc)
	if !end.After(local) {
		end = time.Date(local.Year(), local.Month(), local.Day()+1, w.end/60, w.end%60, 0, 0, w.loc)
	}
	return end
}

// deliveryState is what an agent's rules remember, kept as a setting like its
// plans.
type deliveryState struct {
	Held    []HeldMessage          `json:"held,omitempty"`
	Days    map[string]deliveryDay `json:"days,omitempty"`
	Dropped []DroppedMessage       `json:"dropped,omitempty"`
}

// deliveryDay counts what one recipient was sent on one day.
type deliveryDay struct {
	Date  string   `json:"date"`
	Count int      `json:"count"`
	Items []string `json:"items,omitempty"`
}

// HeldMessage is a message the rules hold back, for the Console.
type HeldMessage struct {
	ID        string          `json:"id"`
	Recipient string          `json:"recipient"`
	SourceID  string          `json:"source_id"`
	Target    json.RawMessage `json:"target"`
	Text      string          `json:"text"`
	Reason    string          `json:"reason"`
	Item      string          `json:"item,omitempty"`
	TicketID  string          `json:"ticket_id,omitempty"`
	TriggerID string          `json:"trigger_id,omitempty"`
	HeldAt    time.Time       `json:"held_at"`
	// Until is when the message may go next, when that is known: the end of
	// quiet hours or of the day. A message waiting for a quiet chat has none.
	Until    time.Time `json:"until,omitzero"`
	Attempts int       `json:"attempts,omitempty"`
	LastErr  string    `json:"last_error,omitempty"`
}

// DroppedMessage is a message the rules decided not to send.
type DroppedMessage struct {
	Recipient string    `json:"recipient"`
	Text      string    `json:"text"`
	Reason    string    `json:"reason"`
	TicketID  string    `json:"ticket_id,omitempty"`
	At        time.Time `json:"at"`
}

// DeliveryActivity is the Console's view of an agent's delivery rules.
type DeliveryActivity struct {
	Held    []HeldMessage    `json:"held"`
	Dropped []DroppedMessage `json:"dropped,omitempty"`
	// SentToday counts today's unprompted messages by recipient.
	SentToday map[string]int `json:"sent_today,omitempty"`
}

func (st *deliveryState) today(key, day string) deliveryDay {
	if record := st.Days[key]; record.Date == day {
		return record
	}
	return deliveryDay{Date: day}
}

func (st *deliveryState) setToday(key string, record deliveryDay) {
	if st.Days == nil {
		st.Days = map[string]deliveryDay{}
	}
	st.Days[key] = record
}

// prune forgets counts from earlier days.
func (st *deliveryState) prune(w deliveryWindow, now time.Time) {
	day := w.day(now)
	for key, record := range st.Days {
		if record.Date != day {
			delete(st.Days, key)
		}
	}
}

func (st *deliveryState) drop(message DroppedMessage) {
	message.Text = truncateRunes(message.Text, 300)
	st.Dropped = append(st.Dropped, message)
	if len(st.Dropped) > maxDroppedKept {
		st.Dropped = st.Dropped[len(st.Dropped)-maxDroppedKept:]
	}
}

func (s *Service) loadDelivery(ctx context.Context, agentID string) (deliveryState, error) {
	var state deliveryState
	err := s.loadSetting(ctx, deliverySettingKey+agentID, &state)
	return state, err
}

func (s *Service) saveDelivery(ctx context.Context, agentID string, state deliveryState) error {
	if len(state.Held) == 0 && len(state.Days) == 0 && len(state.Dropped) == 0 {
		return s.store.SetSetting(ctx, deliverySettingKey+agentID, "")
	}
	return s.saveSetting(ctx, deliverySettingKey+agentID, state)
}

// transmit writes one message to its Channel.
func (s *Service) transmit(ctx context.Context, source core.SuperAgentSource, sourceID string, target []byte, text string) error {
	if source.Kind == core.SuperAgentSourceMailbox {
		return s.sendMail(ctx, target, text)
	}
	if s.replies == nil {
		return nil
	}
	err := s.replies.Send(ctx, source, target, text)
	if err != nil && ctx.Err() == nil {
		s.log.Warn("super agent reply failed", "source", sourceID, "kind", source.Kind, "err", err)
	}
	return err
}

// deliver sends one message, unless the agent's rules hold it back or drop
// it. The note says what the rules did, empty when the message went.
func (s *Service) deliver(ctx context.Context, agent core.SuperAgent, sourceID string, target []byte, text string, class deliveryClass) (string, error) {
	if strings.TrimSpace(text) == "" || len(target) == 0 {
		return "", nil
	}
	source, ok := replySource(agent, sourceID)
	if !ok {
		return "", nil
	}
	key := recipientKey(source, target)
	if !class.unprompted || !agent.Delivery.Active() || key == "" {
		err := s.transmit(ctx, source, sourceID, target, text)
		if err == nil {
			s.noteChatActivity(key, s.now())
		}
		return "", err
	}
	// Unprompted messages with rules on go one at a time, so the cap and the
	// order of held messages hold.
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	now := s.now()
	window := newDeliveryWindow(agent.Delivery)
	state, err := s.loadDelivery(ctx, agent.ID)
	if err != nil {
		return "", err
	}
	state.prune(window, now)
	record := state.today(key, window.day(now))
	repeat := ""
	if agent.Delivery.OncePerDay {
		repeat = class.repeatKey()
	}
	if repeat != "" {
		if containsString(record.Items, repeat) {
			state.drop(DroppedMessage{Recipient: key, Text: text, Reason: "already sent today about " + class.item, TicketID: class.ticketID, At: now.UTC()})
			return "dropped: a message about " + class.item + " was already sent today", s.saveDelivery(ctx, agent.ID, state)
		}
		// A newer message about the same item replaces the one waiting.
		for index, held := range state.Held {
			if held.Recipient == key && held.Item == repeat {
				state.Held[index].Text, state.Held[index].Target, state.Held[index].SourceID = text, target, sourceID
				return "held: replaced an earlier held message about " + class.item, s.saveDelivery(ctx, agent.ID, state)
			}
		}
	}
	reason := ""
	switch {
	case window.quiet(now):
		reason = heldQuietHours
	case agent.Delivery.DailyCap > 0 && record.Count >= agent.Delivery.DailyCap:
		reason = heldDailyCap
	case heldFor(state.Held, key):
		reason = heldBehind
	case s.chatBusy(agent.Delivery, key, now):
		reason = heldChatActive
	}
	if reason != "" {
		held := HeldMessage{
			ID: "hld_" + uuid.NewString(), Recipient: key, SourceID: sourceID, Target: target, Text: text,
			Reason: reason, Item: repeat, TicketID: class.ticketID, TriggerID: class.triggerID,
			HeldAt: now.UTC(), Until: window.opens(now, reason),
		}
		state.Held = append(state.Held, held)
		if len(state.Held) > maxHeldMessages {
			oldest := state.Held[0]
			state.Held = state.Held[1:]
			state.drop(DroppedMessage{Recipient: oldest.Recipient, Text: oldest.Text, Reason: "too many held messages", TicketID: oldest.TicketID, At: now.UTC()})
		}
		if err := s.saveDelivery(ctx, agent.ID, state); err != nil {
			return "", err
		}
		s.markHeld(agent.ID, true)
		return "held: " + heldNote(reason), nil
	}
	if err := s.transmit(ctx, source, sourceID, target, text); err != nil {
		return "", err
	}
	s.noteChatActivity(key, s.now())
	record.Count++
	if repeat != "" {
		record.Items = append(record.Items, repeat)
	}
	state.setToday(key, record)
	return "", s.saveDelivery(ctx, agent.ID, state)
}

func heldNote(reason string) string {
	switch reason {
	case heldQuietHours:
		return "quiet hours"
	case heldDailyCap:
		return "today's limit for this chat is reached"
	case heldBehind:
		return "earlier messages to this chat are waiting"
	case heldChatActive:
		return "waiting for the chat to go quiet"
	}
	return reason
}

func heldFor(held []HeldMessage, key string) bool {
	for _, message := range held {
		if message.Recipient == key {
			return true
		}
	}
	return false
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// chatBusy reports whether something was said in a chat within the agent's
// quiet-chat delay.
func (s *Service) chatBusy(rules core.SuperAgentDelivery, key string, now time.Time) bool {
	if rules.QuietChatMinutes <= 0 {
		return false
	}
	last := s.lastChatActivity(key)
	return !last.IsZero() && now.Sub(last) < time.Duration(rules.QuietChatMinutes)*time.Minute
}

func (s *Service) markHeld(agentID string, held bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if held {
		s.heldAgents[agentID] = true
	} else {
		delete(s.heldAgents, agentID)
	}
}

// runDeliveries releases held messages until the daemon stops. Agents that
// held messages before a restart are found first.
func (s *Service) runDeliveries(ctx context.Context) {
	s.findHeld(ctx)
	ticker := time.NewTicker(deliveryInterval)
	defer ticker.Stop()
	for {
		s.releaseHeld(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// findHeld notes the agents that hold messages in the store.
func (s *Service) findHeld(ctx context.Context) {
	agents, err := s.store.ListSuperAgents(ctx)
	if err != nil {
		s.log.Warn("find held messages", "err", err)
		return
	}
	for _, agent := range agents {
		if state, err := s.loadDelivery(ctx, agent.ID); err == nil && len(state.Held) > 0 {
			s.markHeld(agent.ID, true)
		}
	}
}

// releaseHeld sends the held messages whose window has opened.
func (s *Service) releaseHeld(ctx context.Context) {
	s.mu.Lock()
	ids := make([]string, 0, len(s.heldAgents))
	for id := range s.heldAgents {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		agent, err := s.store.GetSuperAgent(ctx, id)
		if err != nil {
			s.log.Warn("load agent to release held messages", "agent", id, "err", err)
			continue
		}
		if agent == nil {
			s.markHeld(id, false)
			continue
		}
		if err := s.releaseAgent(ctx, *agent); err != nil && ctx.Err() == nil {
			s.log.Warn("release held messages", "agent", id, "err", err)
		}
	}
}

// outgoing is one message a release sends: a held message, or a digest of
// several to one recipient.
type outgoing struct {
	sourceID string
	target   json.RawMessage
	text     string
	held     []HeldMessage
}

func (s *Service) releaseAgent(ctx context.Context, agent core.SuperAgent) error {
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	state, err := s.loadDelivery(ctx, agent.ID)
	if err != nil {
		return err
	}
	if len(state.Held) == 0 {
		s.markHeld(agent.ID, false)
		return nil
	}
	now := s.now()
	rules := agent.Delivery
	window := newDeliveryWindow(rules)
	state.prune(window, now)
	var order []string
	byRecipient := map[string][]HeldMessage{}
	for _, held := range state.Held {
		if _, ok := byRecipient[held.Recipient]; !ok {
			order = append(order, held.Recipient)
		}
		byRecipient[held.Recipient] = append(byRecipient[held.Recipient], held)
	}
	var kept []HeldMessage
	for _, key := range order {
		messages := byRecipient[key]
		record := state.today(key, window.day(now))
		remaining := len(messages)
		if rules.Active() {
			if rules.DailyCap > 0 {
				remaining = rules.DailyCap - record.Count
			}
			reason := ""
			switch {
			case window.quiet(now):
				reason = heldQuietHours
			case remaining <= 0:
				reason = heldDailyCap
			case s.chatBusy(rules, key, now):
				reason = heldChatActive
			}
			if reason != "" {
				kept = append(kept, waiting(messages, reason, window, now)...)
				continue
			}
		}
		for _, out := range releaseBatch(messages, remaining, rules.Held == core.SuperAgentHeldDigest, window) {
			source, ok := replySource(agent, out.sourceID)
			if !ok {
				for _, held := range out.held {
					state.drop(DroppedMessage{Recipient: key, Text: held.Text, Reason: "its source was removed", TicketID: held.TicketID, At: now.UTC()})
				}
				continue
			}
			if err := s.transmit(ctx, source, out.sourceID, out.target, out.text); err != nil {
				if ctx.Err() != nil {
					kept = append(kept, out.held...)
					continue
				}
				for _, held := range out.held {
					held.Attempts++
					held.LastErr = err.Error()
					if held.Attempts >= maxReleaseAttempts {
						state.drop(DroppedMessage{Recipient: key, Text: held.Text, Reason: "could not be sent: " + err.Error(), TicketID: held.TicketID, At: now.UTC()})
						continue
					}
					kept = append(kept, held)
				}
				continue
			}
			s.noteChatActivity(key, s.now())
			record.Count++
			for _, held := range out.held {
				if held.Item != "" && !containsString(record.Items, held.Item) {
					record.Items = append(record.Items, held.Item)
				}
			}
		}
		state.setToday(key, record)
	}
	state.Held = kept
	if len(kept) == 0 {
		s.markHeld(agent.ID, false)
	}
	return s.saveDelivery(ctx, agent.ID, state)
}

// waiting records why messages that stay held are waiting now.
func waiting(messages []HeldMessage, reason string, window deliveryWindow, now time.Time) []HeldMessage {
	out := make([]HeldMessage, len(messages))
	for index, message := range messages {
		message.Reason, message.Until = reason, window.opens(now, reason)
		out[index] = message
	}
	return out
}

// releaseBatch decides what a recipient is sent when its window opens: each
// held message on its own while the cap allows, and the rest folded into one
// digest as the last message; in digest mode everything goes as one digest.
func releaseBatch(messages []HeldMessage, remaining int, digest bool, window deliveryWindow) []outgoing {
	if len(messages) == 0 || remaining <= 0 {
		return nil
	}
	if digest && len(messages) > 1 {
		return []outgoing{digestOf(messages, window)}
	}
	if len(messages) <= remaining {
		out := make([]outgoing, len(messages))
		for index, message := range messages {
			out[index] = outgoing{sourceID: message.SourceID, target: message.Target, text: message.Text, held: []HeldMessage{message}}
		}
		return out
	}
	out := releaseBatch(messages[:remaining-1], remaining-1, false, window)
	return append(out, digestOf(messages[remaining-1:], window))
}

// digestOf folds held messages into one, sent where the newest would have gone.
func digestOf(messages []HeldMessage, window deliveryWindow) outgoing {
	if len(messages) == 1 {
		message := messages[0]
		return outgoing{sourceID: message.SourceID, target: message.Target, text: message.Text, held: messages}
	}
	var text strings.Builder
	fmt.Fprintf(&text, "%d messages I held back:\n", len(messages))
	for index, message := range messages {
		fmt.Fprintf(&text, "\n%d. [%s] %s\n", index+1, message.HeldAt.In(window.loc).Format("01-02 15:04"), truncateRunes(strings.TrimSpace(message.Text), digestItemRunes))
	}
	newest := messages[len(messages)-1]
	return outgoing{sourceID: newest.SourceID, target: newest.Target, text: strings.TrimSpace(text.String()), held: messages}
}

// deliveryActivity is the Console's view of an agent's held and dropped
// messages and today's counts.
func (s *Service) deliveryActivity(ctx context.Context, agent core.SuperAgent) (*DeliveryActivity, error) {
	state, err := s.loadDelivery(ctx, agent.ID)
	if err != nil {
		return nil, err
	}
	if len(state.Held) == 0 && len(state.Dropped) == 0 && len(state.Days) == 0 {
		return nil, nil
	}
	window := newDeliveryWindow(agent.Delivery)
	state.prune(window, s.now())
	activity := &DeliveryActivity{Held: state.Held, Dropped: state.Dropped}
	if activity.Held == nil {
		activity.Held = []HeldMessage{}
	}
	for key, record := range state.Days {
		if record.Count > 0 {
			if activity.SentToday == nil {
				activity.SentToday = map[string]int{}
			}
			activity.SentToday[key] = record.Count
		}
	}
	return activity, nil
}

// normalizeDelivery checks an agent's delivery rules.
func normalizeDelivery(agent *core.SuperAgent) error {
	rules := &agent.Delivery
	rules.QuietStart = strings.TrimSpace(rules.QuietStart)
	rules.QuietEnd = strings.TrimSpace(rules.QuietEnd)
	rules.TimeZone = strings.TrimSpace(rules.TimeZone)
	rules.Held = strings.ToLower(strings.TrimSpace(rules.Held))
	if (rules.QuietStart == "") != (rules.QuietEnd == "") {
		return errors.New("quiet hours need both a start and an end")
	}
	if rules.QuietStart != "" {
		start, err := clockMinutes(rules.QuietStart)
		if err != nil {
			return fmt.Errorf("quiet hours start: %w", err)
		}
		end, err := clockMinutes(rules.QuietEnd)
		if err != nil {
			return fmt.Errorf("quiet hours end: %w", err)
		}
		if start == end {
			return errors.New("quiet hours must start and end at different times")
		}
		rules.QuietStart = fmt.Sprintf("%02d:%02d", start/60, start%60)
		rules.QuietEnd = fmt.Sprintf("%02d:%02d", end/60, end%60)
	}
	if rules.TimeZone != "" {
		if _, err := time.LoadLocation(rules.TimeZone); err != nil {
			return fmt.Errorf("time zone %q is not known", rules.TimeZone)
		}
	}
	if rules.DailyCap < 0 || rules.DailyCap > maxDailyCap {
		return fmt.Errorf("daily cap must be between 0 and %d", maxDailyCap)
	}
	if rules.QuietChatMinutes < 0 || rules.QuietChatMinutes > maxQuietChatMinutes {
		return fmt.Errorf("quiet-chat delay must be between 0 and %d minutes", maxQuietChatMinutes)
	}
	switch rules.Held {
	case "", core.SuperAgentHeldQueue:
		rules.Held = ""
	case core.SuperAgentHeldDigest:
	default:
		return fmt.Errorf("held messages must be %s or %s", core.SuperAgentHeldQueue, core.SuperAgentHeldDigest)
	}
	return nil
}
