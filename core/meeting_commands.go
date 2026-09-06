package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrMeetingBusy = errors.New("meeting agent is already answering")

const (
	meetingStreamMinRunes = 80
	meetingStreamMaxRunes = 600
)

const meetingContextCLIInstruction = `会中上下文查询能力：
- 上方的当前会议上下文是本地快照，可能不完整或不是最新状态。
- 当问题涉及“现在/刚刚/最新”，或现有上下文不足以可靠回答时，可以使用 shell 执行下面的只读 lark-cli 命令，重新获取当前会议内可见的字幕、聊天、参会人进出和共享事件。
- 必须使用当前会议的长 meeting_id 和应用身份，不要用 9 位会议号替代 meeting_id，也不要自动执行入会、离会或发送消息等写操作。
- 查询后只基于与用户问题相关的会议内容作答，不要复述命令、工具调用过程、原始工具输出或权限信息。`

var meetingStreamFlushInterval = 3 * time.Second

type meetingCommand struct {
	Kind          string
	MeetingNumber string
	Question      string
	Mode          string
}

func isMeetingCommand(text string) bool {
	text = strings.TrimSpace(text)
	if len(text) < len("/meeting") || !strings.EqualFold(text[:len("/meeting")], "/meeting") {
		return false
	}
	return len(text) == len("/meeting") || text[len("/meeting")] == ' ' || text[len("/meeting")] == '\t' || text[len("/meeting")] == '\n'
}

func parseMeetingCommand(text string) (meetingCommand, bool) {
	if !isMeetingCommand(text) {
		return meetingCommand{}, false
	}
	rest := strings.TrimSpace(text[len("/meeting"):])
	if rest == "" {
		return meetingCommand{Kind: "help"}, true
	}
	fields := strings.Fields(rest)
	switch strings.ToLower(fields[0]) {
	case "help", "帮助":
		return meetingCommand{Kind: "help"}, true
	case "list", "列表":
		return meetingCommand{Kind: "list"}, true
	case "mine", "我的":
		return meetingCommand{Kind: "mine"}, true
	case "mode", "模式", "reply", "回复":
		if len(fields) == 1 {
			return meetingCommand{Kind: "mode"}, true
		}
		if len(fields) == 2 {
			if mode := NormalizeMeetingResponseMode(fields[1]); mode != "" {
				return meetingCommand{Kind: "mode", Mode: mode}, true
			}
		}
		return meetingCommand{Kind: "help"}, true
	case "voice", "语音回复":
		if len(fields) == 1 {
			return meetingCommand{Kind: "mode"}, true
		}
		if len(fields) == 2 {
			switch strings.ToLower(fields[1]) {
			case "on", "true", "开启", "开":
				return meetingCommand{Kind: "mode", Mode: MeetingResponseModeTextVoice}, true
			case "off", "false", "关闭", "关":
				return meetingCommand{Kind: "mode", Mode: MeetingResponseModeStreamText}, true
			case "only", "仅语音":
				return meetingCommand{Kind: "mode", Mode: MeetingResponseModeVoice}, true
			}
		}
		return meetingCommand{Kind: "help"}, true
	case "join", "加入":
		if len(fields) == 2 && isNineDigitMeetingNumber(fields[1]) {
			return meetingCommand{Kind: "join", MeetingNumber: fields[1]}, true
		}
		return meetingCommand{Kind: "help"}, true
	}
	if isNineDigitMeetingNumber(fields[0]) {
		question := strings.TrimSpace(rest[len(fields[0]):])
		if question == "" {
			return meetingCommand{Kind: "help"}, true
		}
		return meetingCommand{Kind: "ask", MeetingNumber: fields[0], Question: trimMeetingQuestionQuotes(question)}, true
	}
	return meetingCommand{Kind: "ask", Question: trimMeetingQuestionQuotes(rest)}, true
}

func trimMeetingQuestionQuotes(value string) string {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) < 2 {
		return value
	}
	first, firstSize := utf8.DecodeRuneInString(value)
	last, lastSize := utf8.DecodeLastRuneInString(value)
	pairs := map[rune]rune{'"': '"', '\'': '\'', '“': '”', '‘': '’'}
	if pairs[first] == last {
		return strings.TrimSpace(value[firstSize : len(value)-lastSize])
	}
	return value
}

const meetingHelpText = `会议命令：
/meeting join|加入 123456789 — 让机器人加入会议
/meeting list|列表 — 查看 Bot 已加入的进行中会议
/meeting mine|我的 — 查看你与 Bot 同时在场的会议
/meeting mode|模式 — 查看会议回答方式
/meeting mode stream|final|text+voice|voice — 切换流式文字、非流式文字、文字+语音或仅语音
/meeting 123456789 具体问题 — 向指定会议的 Agent 提问
/meeting 具体问题 — 向最近加入的会议提问`

func (e *Engine) handleMeetingMessage(ctx context.Context, rt *channelRuntime, msg *Message, data map[string]string) bool {
	if rt == nil || msg == nil {
		return false
	}
	if msg.Origin == OriginMeeting && msg.MeetingID != "" {
		_, err := e.AskMeeting(rt.channel.ID, msg.MeetingID, msg.Text, "meeting", msg.UserID)
		if errors.Is(err, ErrMeetingBusy) {
			if activity, ok := rt.platform.(MeetingActivityPlatform); ok {
				_ = activity.SendMeetingMessage(ctx, msg.MeetingID, "当前正在回答，请稍后再试。", NewChannelControlID("meeting-busy"))
			}
		} else if err != nil {
			e.log.Warn("start meeting question", "meeting_id", msg.MeetingID, "err", err)
		}
		return true
	}
	command, ok := parseMeetingCommand(msg.Text)
	if !ok {
		return false
	}
	reply := func(text string) {
		if err := rt.platform.Reply(ctx, msg, text); err != nil {
			e.log.Warn("reply meeting command", "err", err)
		}
	}
	activity, supported := rt.platform.(MeetingActivityPlatform)
	if !supported {
		reply("当前渠道不支持会中助手。")
		return true
	}
	switch command.Kind {
	case "help":
		reply(meetingHelpText)
	case "join":
		result, err := e.JoinMeetingByNumber(ctx, rt.channel.ID, command.MeetingNumber)
		if err != nil {
			reply("加入会议失败：" + err.Error())
		} else if result.GreetingWarning != "" {
			reply("已加入会议，但自我介绍发送失败：" + result.GreetingWarning)
		} else {
			reply("已加入会议并发送自我介绍。")
		}
	case "list":
		reply(formatMeetingList(refreshVisibleMeetings(ctx, activity, msg.UserID), false))
	case "mine":
		meetings, err := activity.UserActiveMeetings(ctx, msg.UserID)
		if err != nil {
			reply("查询当前会议失败：" + err.Error())
		} else {
			reply(formatMeetingList(meetings, true))
		}
	case "mode":
		if command.Mode == "" {
			reply("当前会议回答方式：" + meetingResponseModeLabel(rt.currentMeetingResponseMode()) + "。")
			break
		}
		mode, err := e.SetMeetingResponseMode(ctx, rt.channel.ID, command.Mode)
		if err != nil {
			reply("切换会议回答方式失败：" + err.Error())
		} else {
			reply("会议回答方式已切换为：" + meetingResponseModeLabel(mode) + "。")
		}
	case "ask":
		meeting, err := selectMeetingForQuestion(refreshVisibleMeetings(ctx, activity, msg.UserID), command.MeetingNumber)
		if err != nil {
			reply(err.Error())
			break
		}
		turn, err := e.AskMeeting(rt.channel.ID, meeting.ID, command.Question, "command", msg.UserID)
		if errors.Is(err, ErrMeetingBusy) {
			reply("当前会议的智能体正在回答，请稍后再试。")
		} else if err != nil {
			reply("提交会议问题失败：" + err.Error())
		} else {
			reply("问题已提交（仅当前聊天可见），答案会发送到会议中。Turn：" + turn.ID)
		}
	}
	e.emit(ctx, HookMessageSent, data)
	return true
}

func meetingResponseModeLabel(mode string) string {
	switch NormalizeMeetingResponseMode(mode) {
	case MeetingResponseModeFinalText:
		return "非流式文字"
	case MeetingResponseModeTextVoice:
		return "文字+语音"
	case MeetingResponseModeVoice:
		return "仅语音"
	default:
		return "流式文字"
	}
}

func refreshVisibleMeetings(ctx context.Context, activity MeetingActivityPlatform, userID string) []ActiveMeeting {
	meetings := activity.ActiveMeetings()
	if strings.TrimSpace(userID) == "" {
		return meetings
	}
	visible, err := activity.UserActiveMeetings(ctx, userID)
	if err != nil {
		return meetings
	}
	byID := make(map[string]int, len(meetings)+len(visible))
	for i := range meetings {
		byID[meetings[i].ID] = i
	}
	for _, meeting := range visible {
		if index, ok := byID[meeting.ID]; ok {
			meetings[index] = meeting
			continue
		}
		byID[meeting.ID] = len(meetings)
		meetings = append(meetings, meeting)
	}
	return meetings
}

func formatMeetingList(meetings []ActiveMeeting, limited bool) string {
	if len(meetings) == 0 {
		if limited {
			return "没有发现你与当前 Bot 同时在场的会议。平台只返回这一可见范围。"
		}
		return "当前 Bot 没有加入进行中的会议。"
	}
	lines := []string{"当前 Bot 已加入的会议："}
	if limited {
		lines[0] = "你与当前 Bot 同时在场的会议（受平台可见范围限制）："
	}
	for _, meeting := range meetings {
		title := meeting.Topic
		if title == "" {
			title = "未命名会议"
		}
		lines = append(lines, fmt.Sprintf("- %s · %s · ID %s", meeting.MeetingNumber, title, meeting.ID))
	}
	return strings.Join(lines, "\n")
}

func selectMeetingForQuestion(meetings []ActiveMeeting, meetingNumber string) (ActiveMeeting, error) {
	if meetingNumber != "" {
		for _, meeting := range meetings {
			if meeting.MeetingNumber == meetingNumber {
				return meeting, nil
			}
		}
		return ActiveMeeting{}, fmt.Errorf("当前 Bot 尚未加入会议 %s。", meetingNumber)
	}
	if len(meetings) == 0 {
		return ActiveMeeting{}, errors.New("当前 Bot 没有加入进行中的会议。")
	}
	selected := meetings[0]
	for _, meeting := range meetings[1:] {
		if meeting.JoinedAt.After(selected.JoinedAt) {
			selected = meeting
		}
	}
	return selected, nil
}

func (e *Engine) AskMeeting(channelID, meetingID, question, source, userID string) (MeetingTurn, error) {
	rt, _, err := e.meetingPlatform(channelID)
	if err != nil {
		return MeetingTurn{}, err
	}
	activity, ok := rt.platform.(MeetingActivityPlatform)
	if !ok {
		return MeetingTurn{}, fmt.Errorf("channel %q does not support meeting questions", channelID)
	}
	question, meetingID = strings.TrimSpace(question), strings.TrimSpace(meetingID)
	if question == "" || meetingID == "" {
		return MeetingTurn{}, errors.New("meeting_id and question are required")
	}
	found := false
	var selected ActiveMeeting
	for _, meeting := range activity.ActiveMeetings() {
		if meeting.ID == meetingID {
			found, selected = true, meeting
			break
		}
	}
	// A message delivered by the meeting activity callback is itself proof
	// that the bot can currently observe this meeting. Do not let a delayed
	// ended callback race suppress the resulting Agent turn.
	if !found && source == "meeting" {
		selected = ActiveMeeting{ID: meetingID, Status: "active"}
		if detail, detailErr := activity.MeetingActivity(meetingID); detailErr == nil {
			if detail.Meeting.ID != "" {
				selected = detail.Meeting
				selected.Status = "active"
			}
		}
		found = true
	}
	if !found {
		return MeetingTurn{}, fmt.Errorf("meeting %q is not active for this bot", meetingID)
	}
	base := rt.runCtx
	if base == nil {
		base = context.Background()
	}
	turnCtx, cancel := context.WithTimeout(base, ChannelTurnTimeout(rt.channel))
	guard, started := rt.beginDirectTurn(turnCtx, "meeting:"+meetingID, userID, cancel)
	if !started {
		cancel()
		return MeetingTurn{}, ErrMeetingBusy
	}
	now := time.Now().UTC()
	turn := MeetingTurn{ID: NewChannelControlID("meeting-turn"), MeetingID: meetingID, Question: question, Source: source, Status: "running", CreatedAt: now, StartedAt: now}
	activity.UpsertMeetingTurn(turn)
	go e.runMeetingTurn(turnCtx, cancel, rt, activity, guard, selected, turn, userID)
	return turn, nil
}

func (e *Engine) runMeetingTurn(ctx context.Context, cancel context.CancelFunc, rt *channelRuntime, activity MeetingActivityPlatform, guard *directChannelTurn, meeting ActiveMeeting, turn MeetingTurn, userID string) {
	defer cancel()
	defer rt.finishDirectTurn("meeting:"+meeting.ID, guard)
	finish := func(status string, err error) {
		turn.Status = status
		turn.EndedAt = time.Now().UTC()
		if err != nil {
			turn.Error = err.Error()
		}
		activity.UpsertMeetingTurn(turn)
	}
	msg := &Message{ID: turn.ID, ChatID: "meeting:" + meeting.ID, ChatType: "meeting", ConversationKey: "meeting:" + meeting.ID, UserID: userID, Text: turn.Question, Platform: rt.channel.Type, ChannelID: rt.channel.ID, Origin: OriginAPI, MeetingID: meeting.ID, MeetingNumber: meeting.MeetingNumber, MeetingTopic: meeting.Topic}
	sess, conv, _, generation, releaseSession, err := rt.session(ctx, msg)
	if err != nil {
		finish("failed", err)
		return
	}
	defer releaseSession()
	defer e.persistConversationTurn(context.Background(), conv, sess)
	responseMode := rt.currentMeetingResponseMode()
	var speech SpeechReply
	if MeetingResponseModeUsesVoice(responseMode) {
		speech = e.beginSpeechReply(ctx, rt.platform, msg)
	}
	if speech != nil {
		defer func() {
			if closeErr := speech.Close(context.Background()); closeErr != nil {
				e.log.Warn("close meeting speech reply", "meeting_id", meeting.ID, "err", closeErr)
			}
		}()
	}
	prompt := meetingTurnPrompt(activity.MeetingPromptContext(meeting.ID), meeting, turn.Question)
	data := eventData(msg)
	data["agent_id"] = generation.workspace.AgentID
	data["runtime_id"] = generation.workspace.RuntimeID
	data["memory_scope"] = generation.workspace.MemoryScope
	data["meeting_id"] = meeting.ID
	data["meeting_turn_id"] = turn.ID
	replyMode := MeetingReplyModeStream
	if responseMode == MeetingResponseModeFinalText || responseMode == MeetingResponseModeVoice {
		replyMode = MeetingReplyModeFinal
	}
	data["meeting_reply_mode"] = replyMode
	data["meeting_response_mode"] = responseMode
	events, err := e.observeSend(ctx, sess, prompt, data)
	if err != nil {
		finish("failed", err)
		return
	}
	sequence := 0
	textEnabled := MeetingResponseModeUsesText(responseMode)
	// If the platform cannot open a speech reply, keep the answer visible
	// rather than silently discarding a voice-only response.
	if responseMode == MeetingResponseModeVoice && speech == nil {
		textEnabled = true
	}
	send := func(text string) error {
		if !textEnabled {
			return nil
		}
		sequence++
		return activity.SendMeetingMessage(ctx, meeting.ID, text, fmt.Sprintf("%s-%03d", turn.ID, sequence))
	}
	var speechFailed bool
	observeAnswer := func(text string, done bool) {
		if speech == nil || speechFailed {
			return
		}
		if speechErr := speech.Update(ctx, text, done); speechErr != nil {
			speechFailed = true
			e.log.Warn("update meeting speech reply", "meeting_id", meeting.ID, "err", speechErr)
		}
	}
	if deliveryErr := deliverMeetingAnswerObserved(ctx, events, replyMode, send, observeAnswer); deliveryErr != nil {
		err = deliveryErr
	}
	if err != nil {
		finish("failed", err)
	} else {
		finish("succeeded", nil)
	}
}

func meetingTurnPrompt(localContext string, meeting ActiveMeeting, question string) string {
	meetingID := strings.TrimSpace(meeting.ID)
	meetingNumber := strings.TrimSpace(meeting.MeetingNumber)
	if meetingNumber == "" {
		meetingNumber = "未知"
	}
	sections := make([]string, 0, 4)
	if localContext = strings.TrimSpace(localContext); localContext != "" {
		sections = append(sections, localContext)
	}
	sections = append(sections, fmt.Sprintf(
		"%s\n当前会议：meeting_id=%s，meeting_number=%s\n只读查询命令：\nlark-cli vc +meeting-events --as bot --meeting-id %s --page-all --format pretty",
		meetingContextCLIInstruction, meetingID, meetingNumber, meetingID,
	))
	sections = append(sections, "用户问题："+strings.TrimSpace(question))
	sections = append(sections, "请直接给出适合在会议中口头朗读、同时可发送到会议聊天中的简洁最终回答。使用自然口语，不要使用 Markdown、链接，不要输出思考过程、工具调用、工具结果或权限提示。")
	return strings.Join(sections, "\n\n")
}

// deliverMeetingAnswerObserved mirrors the accumulated final-answer text to
// observe without coupling meeting chat delivery to an optional speech sink.
// Speech failures are handled by the caller and never suppress the text path.
func deliverMeetingAnswerObserved(ctx context.Context, events <-chan *Event, mode string, send func(string) error, observe func(string, bool)) error {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != MeetingReplyModeFinal {
		mode = MeetingReplyModeStream
	}

	var previous, answer, pending string
	var resultErr error
	var timer *time.Timer
	var timerC <-chan time.Time
	recordErr := func(err error) {
		if err != nil && resultErr == nil {
			resultErr = err
		}
	}
	stopTimer := func() {
		if timer != nil && !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer, timerC = nil, nil
	}
	armTimer := func() {
		if mode != MeetingReplyModeStream || timerC != nil || strings.TrimSpace(pending) == "" {
			return
		}
		timer = time.NewTimer(meetingStreamFlushInterval)
		timerC = timer.C
	}
	sendText := func(text string) {
		text = strings.TrimSpace(text)
		if text != "" {
			recordErr(send(text))
		}
	}
	flushStream := func(ready, final bool) {
		segments, rest := splitMeetingStreamAnswer(pending, ready, final)
		pending = rest
		for _, segment := range segments {
			sendText(segment)
		}
	}

	defer stopTimer()
	for {
		select {
		case <-ctx.Done():
			recordErr(ctx.Err())
			return resultErr
		case <-timerC:
			timer, timerC = nil, nil
			flushStream(true, false)
			armTimer()
		case event, open := <-events:
			if !open {
				if observe != nil && strings.TrimSpace(answer) != "" {
					observe(answer, true)
				}
				if mode == MeetingReplyModeFinal {
					sendText(answer)
				} else {
					flushStream(true, true)
				}
				return resultErr
			}
			if event == nil {
				continue
			}
			if event.Type == EventError {
				recordErr(fmt.Errorf("%s", errString(event.Err)))
				continue
			}
			if event.Type != EventOutput && event.Type != EventFinal {
				continue
			}
			text := strings.TrimSpace(event.Text)
			if text == "" || text == "NO_REPLY" {
				continue
			}
			delta := meetingAnswerDelta(previous, text)
			if len(text) >= len(previous) {
				previous = text
			}
			if delta == "" {
				continue
			}
			answer += delta
			if observe != nil {
				observe(answer, false)
			}
			if mode == MeetingReplyModeStream {
				pending += delta
				armTimer()
			}
		}
	}
}

func meetingAnswerDelta(previous, next string) string {
	if previous == "" {
		return next
	}
	if strings.HasPrefix(next, previous) {
		return next[len(previous):]
	}
	if strings.HasPrefix(previous, next) {
		return ""
	}
	return next
}

func splitMeetingStreamAnswer(value string, ready, final bool) ([]string, string) {
	runes := []rune(value)
	segments := []string{}
	for len(runes) > 0 {
		if !final && (!ready || len(runes) < meetingStreamMinRunes) {
			break
		}
		limit := len(runes)
		if limit > meetingStreamMaxRunes {
			limit = meetingStreamMaxRunes
		}
		cut := meetingStreamBoundary(runes, limit)
		if cut == 0 {
			switch {
			case len(runes) >= meetingStreamMaxRunes:
				cut = meetingStreamMaxRunes
			case final:
				cut = len(runes)
			default:
				return segments, string(runes)
			}
		}
		if text := strings.TrimSpace(string(runes[:cut])); text != "" {
			segments = append(segments, text)
		}
		runes = runes[cut:]
		// While a turn is live, one timer tick can publish at most one chunk.
		if !final {
			break
		}
	}
	return segments, string(runes)
}

func meetingStreamBoundary(runes []rune, limit int) int {
	last := 0
	for index := 0; index < len(runes) && index < limit; index++ {
		if !strings.ContainsRune("。！？；.!?;\n", runes[index]) {
			continue
		}
		end := index + 1
		// Keep sentence-closing quotes/brackets with their sentence. This
		// prevents tiny standalone messages such as a single Chinese quote.
		for end < len(runes) && end < limit+8 && isMeetingSentenceCloser(runes[end]) {
			end++
		}
		if end >= meetingStreamMinRunes {
			last = end
		}
	}
	return last
}

func isMeetingSentenceCloser(r rune) bool {
	return strings.ContainsRune("\"'”’」』）》）】〕〉》]}", r)
}
