package superagent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// Progress shows a person how the work their Feishu message started is going,
// without notifying them again until it ends. Their message carries a status
// reaction for as long as the work lasts, and the agent's reply, the one that
// said the work was taken on, is a status message AgentMux edits in place: as
// plain text or as a card, as the agent's Progress setting says. Edits come
// from what AgentMux already records about the Tickets, so no model is asked.
// The worker's report still arrives as a new reply, which notifies.
//
// The state lives on the request's Trigger, so it survives a restart. A loop
// keeps every open request's reaction and message up to date.

// ChatProgress is the Engine's surface for showing progress in a chat.
type ChatProgress interface {
	AddChatStatusReaction(ctx context.Context, channelID, messageID, emoji string) string
	RemoveChatReaction(ctx context.Context, channelID, messageID, reactionID string)
	PostChatStatus(ctx context.Context, channelID, chatID, messageID, text string, card bool) (string, error)
	UpdateChatStatus(ctx context.Context, channelID, messageID, text string, card, done, failed bool) error
}

// Phases of a request's work, as the reaction and the message show them.
const (
	phaseWorking = "working"
	phaseStalled = "stalled"
	phaseDone    = "done"
	phaseFailed  = "failed"
	phaseStopped = "stopped"
)

const (
	progressInterval = 20 * time.Second
	// cardRefresh is the least time between two edits of a card that only
	// bring the times and counts up to date.
	cardRefresh = time.Minute
	// Feishu lets a text message be edited 20 times. Refreshes stop after
	// textRefreshes edits, which leaves the rest for changes of phase.
	textEditLimit = 19
	textRefreshes = 15
	// maxProgressFailures refused edits in a row freeze the message.
	maxProgressFailures = 3
)

// progressReactions are the Feishu emoji types for each phase; a stopped
// request has none.
var progressReactions = map[string]string{
	phaseWorking: "OnIt",
	phaseStalled: "Alarm",
	phaseDone:    "DONE",
	phaseFailed:  "CrossMark",
}

// progressStyle is the agent's setting, text when unset.
func progressStyle(agent core.SuperAgent) string {
	switch style := strings.ToLower(strings.TrimSpace(agent.Progress)); style {
	case core.SuperAgentProgressCard, core.SuperAgentProgressReaction, core.SuperAgentProgressOff:
		return style
	default:
		return core.SuperAgentProgressText
	}
}

func normalizeProgress(agent *core.SuperAgent) error {
	switch style := strings.ToLower(strings.TrimSpace(agent.Progress)); style {
	case "", core.SuperAgentProgressText:
		agent.Progress = ""
	case core.SuperAgentProgressCard, core.SuperAgentProgressReaction, core.SuperAgentProgressOff:
		agent.Progress = style
	default:
		return fmt.Errorf("progress must be %s, %s, %s or %s", core.SuperAgentProgressText, core.SuperAgentProgressCard, core.SuperAgentProgressReaction, core.SuperAgentProgressOff)
	}
	return nil
}

// textRefreshInterval spaces a text message's refreshes further apart as its
// edits run out, so they last about an hour and a half.
func textRefreshInterval(edits int) time.Duration {
	switch {
	case edits < 5:
		return 2 * time.Minute
	case edits < 10:
		return 5 * time.Minute
	default:
		return 10 * time.Minute
	}
}

// openProgress starts showing the work a person's request started. reply is
// the agent's answer, which becomes the status message's opening, or "" when
// it was already sent or there is none. It reports whether reply was
// delivered; when it was not, the caller sends it as usual.
func (s *Service) openProgress(ctx context.Context, agent core.SuperAgent, trigger *core.SuperAgentTrigger, reply string, ticketIDs []string) bool {
	style := progressStyle(agent)
	if s.progress == nil || style == core.SuperAgentProgressOff || !reactsTo(*trigger) || len(ticketIDs) == 0 {
		return false
	}
	var target feishuReply
	if err := json.Unmarshal(trigger.Reply, &target); err != nil || target.ChannelID == "" || target.ChatID == "" || target.MessageID == "" {
		return false
	}
	progress := &core.SuperAgentProgress{
		Style: style, ChannelID: target.ChannelID, ChatID: target.ChatID, RequestID: target.MessageID,
	}
	trigger.Progress = progress
	if style == core.SuperAgentProgressReaction {
		return false
	}
	var tickets []core.SuperAgentTicket
	for _, id := range ticketIDs {
		if ticket, err := s.store.GetSuperAgentTicket(ctx, id); err == nil && ticket != nil {
			tickets = append(tickets, *ticket)
		}
	}
	progress.Header = truncateRunes(strings.TrimSpace(reply), maxChatReplyRunes-1000)
	phase, status := s.describeProgress(ctx, tickets)
	text := progressText(progress.Header, status)
	id, err := s.progress.PostChatStatus(ctx, target.ChannelID, target.ChatID, target.MessageID, text, style == core.SuperAgentProgressCard)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Warn("super agent could not post its progress; it shows only a reaction", "trigger", trigger.ID, "err", err)
		}
		progress.Header = ""
		return false
	}
	progress.MessageID, progress.Shown, progress.ShownAt, progress.Phase = id, text, s.now().UTC(), phase
	s.rememberSent(ctx, agent.ID, id, text, sentAbout{Thread: trigger.ThreadKey, Tickets: compactIDs(ticketIDs...)})
	return progress.Header != ""
}

func progressText(header, status string) string {
	if header == "" {
		return status
	}
	return header + "\n\n" + status
}

// watchProgress has the progress loop look at a request's Trigger soon.
func (s *Service) watchProgress(triggerID string) {
	if triggerID == "" {
		return
	}
	s.progressMu.Lock()
	s.progressSeq++
	s.progressWatch[triggerID] = s.progressSeq
	s.progressMu.Unlock()
	select {
	case s.progressWake <- struct{}{}:
	default:
	}
}

// runProgress keeps open requests' progress up to date until the daemon
// stops. It starts with the requests whose Tickets are still to finish.
func (s *Service) runProgress(ctx context.Context) {
	if s.progress == nil {
		return
	}
	if open, err := s.store.ListSuperAgentTicketsByStatus(ctx, core.SuperAgentTicketQueued, core.SuperAgentTicketRunning); err == nil {
		for _, ticket := range open {
			s.watchProgress(ticket.TriggerID)
		}
	} else if ctx.Err() == nil {
		s.log.Warn("list open super agent tickets for progress", "err", err)
	}
	ticker := time.NewTicker(progressInterval)
	defer ticker.Stop()
	for {
		s.progressPass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.progressWake:
		}
	}
}

func (s *Service) progressPass(ctx context.Context) {
	s.progressMu.Lock()
	watched := make(map[string]uint64, len(s.progressWatch))
	for id, seq := range s.progressWatch {
		watched[id] = seq
	}
	s.progressMu.Unlock()
	for id, seq := range watched {
		if ctx.Err() != nil {
			return
		}
		finished, err := s.syncProgress(ctx, id)
		if err != nil {
			if ctx.Err() == nil {
				s.log.Warn("update super agent progress", "trigger", id, "err", err)
			}
			continue
		}
		if finished {
			s.progressMu.Lock()
			if s.progressWatch[id] == seq {
				delete(s.progressWatch, id)
			}
			s.progressMu.Unlock()
		}
	}
}

// syncProgress brings one request's reaction and message up to date and
// reports whether there is nothing left to watch.
func (s *Service) syncProgress(ctx context.Context, triggerID string) (bool, error) {
	trigger, err := s.store.GetSuperAgentTrigger(ctx, triggerID)
	if err != nil {
		return false, err
	}
	if trigger == nil || trigger.Progress == nil {
		return true, nil
	}
	all, err := s.store.ListSuperAgentThreadTickets(ctx, trigger.SuperAgentID, trigger.ThreadKey)
	if err != nil {
		return false, err
	}
	var tickets []core.SuperAgentTicket
	for _, ticket := range all {
		if ticket.TriggerID == trigger.ID {
			tickets = append(tickets, ticket)
		}
	}
	if len(tickets) == 0 {
		return true, nil
	}
	progress := trigger.Progress
	before := *progress
	phase, status := s.describeProgress(ctx, tickets)
	s.syncReaction(ctx, progress, phase)
	upToDate := s.syncMessage(ctx, progress, phase, progressText(progress.Header, status))
	progress.Phase = phase
	ended := phase == phaseDone || phase == phaseFailed || phase == phaseStopped
	progress.Final = ended && upToDate
	if *progress != before {
		if err := s.store.UpdateSuperAgentTrigger(ctx, *trigger); err != nil {
			return false, err
		}
	}
	return progress.Final, nil
}

func (s *Service) syncReaction(ctx context.Context, progress *core.SuperAgentProgress, phase string) {
	want := progressReactions[phase]
	if want == progress.Reaction {
		return
	}
	// The new mark goes on before the old one comes off, so the message is
	// never left unmarked while work goes on.
	id := ""
	if want != "" {
		id = s.progress.AddChatStatusReaction(ctx, progress.ChannelID, progress.RequestID, want)
	}
	if progress.ReactionID != "" {
		s.progress.RemoveChatReaction(ctx, progress.ChannelID, progress.RequestID, progress.ReactionID)
	}
	progress.Reaction, progress.ReactionID = want, id
}

// syncMessage edits the status message when it is due and reports whether
// it shows the latest status, or never will.
func (s *Service) syncMessage(ctx context.Context, progress *core.SuperAgentProgress, phase, text string) bool {
	if progress.MessageID == "" || progress.Frozen || text == progress.Shown {
		return true
	}
	card := progress.Style == core.SuperAgentProgressCard
	now := s.now().UTC()
	changed := phase != progress.Phase
	switch {
	case !card && progress.Edits >= textEditLimit:
		return true
	case changed:
	case card && now.Sub(progress.ShownAt) < cardRefresh:
		return false
	case !card && (progress.Edits >= textRefreshes || now.Sub(progress.ShownAt) < textRefreshInterval(progress.Edits)):
		return false
	}
	ended := phase == phaseDone || phase == phaseFailed || phase == phaseStopped
	err := s.progress.UpdateChatStatus(ctx, progress.ChannelID, progress.MessageID, text, card, ended, phase == phaseFailed)
	if err != nil {
		progress.Failures++
		if progress.Failures >= maxProgressFailures {
			progress.Frozen = true
			s.log.Warn("super agent stops editing its progress message", "message", progress.MessageID, "err", err)
			return true
		}
		return false
	}
	progress.Shown, progress.ShownAt, progress.Failures = text, now, 0
	if !card {
		progress.Edits++
	}
	return true
}

// describeProgress is the phase of a request's Tickets and the status lines
// its message shows: one line, or one per Ticket when it started several.
func (s *Service) describeProgress(ctx context.Context, tickets []core.SuperAgentTicket) (string, string) {
	now := s.now().UTC()
	var lines []string
	live, stalled, failed, stopped := false, false, false, 0
	for _, ticket := range tickets {
		line := s.ticketStatusLine(ctx, ticket, now)
		if len(tickets) > 1 {
			line = "• " + truncateRunes(ticket.Title, 40) + "：" + line
		}
		lines = append(lines, line)
		switch ticket.Status {
		case core.SuperAgentTicketQueued, core.SuperAgentTicketRunning:
			live = true
			stalled = stalled || openSignal(ticket, core.SuperAgentSignalStalled)
		case core.SuperAgentTicketFailed:
			failed = true
		case core.SuperAgentTicketStopped:
			stopped++
		}
	}
	phase := phaseDone
	switch {
	case live && stalled:
		phase = phaseStalled
	case live:
		phase = phaseWorking
	case failed:
		phase = phaseFailed
	case stopped == len(tickets):
		phase = phaseStopped
	}
	return phase, strings.Join(lines, "\n")
}

func (s *Service) ticketStatusLine(ctx context.Context, ticket core.SuperAgentTicket, now time.Time) string {
	switch ticket.Status {
	case core.SuperAgentTicketQueued:
		if len(s.unmetDependencies(ctx, ticket)) > 0 {
			return "🕒 等待前置任务完成"
		}
		if !ticket.StartedAt.IsZero() {
			return "⏳ 进行中 · 已用 " + minutesSince(ticket.StartedAt, now) + " · 即将继续"
		}
		return "🕒 排队中，即将开始"
	case core.SuperAgentTicketRunning:
		parts := []string{"⏳ 进行中", "已用 " + minutesSince(ticket.StartedAt, now)}
		if ticket.ReworkRounds > 0 {
			parts = append(parts, fmt.Sprintf("第 %d 轮修改", ticket.ReworkRounds))
		}
		latest, ok := s.latestActivity(ticket.ID)
		if ok && latest.steps > 0 {
			parts = append(parts, fmt.Sprintf("%d 步", latest.steps))
		}
		if openSignal(ticket, core.SuperAgentSignalStalled) && ok {
			parts[0] = "⚠️ 进行中"
			parts = append(parts, minutesSince(latest.at, now)+"没有新进展")
		} else if ok && latest.tool != "" {
			parts = append(parts, "最近："+latest.tool+"（"+agoText(latest.at, now)+"）")
		}
		return strings.Join(parts, " · ")
	case core.SuperAgentTicketReady:
		return "✅ 已完成 · 用时 " + durationText(ticket.StartedAt, ticket.FinishedAt)
	case core.SuperAgentTicketFailed:
		return "❌ 未完成 · 用时 " + durationText(ticket.StartedAt, ticket.FinishedAt)
	default:
		return "⏹ 已停止"
	}
}

func openSignal(ticket core.SuperAgentTicket, kind string) bool {
	return slices.ContainsFunc(ticket.Signals, func(signal core.SuperAgentTicketSignal) bool {
		return signal.Kind == kind && signal.ClearedAt.IsZero()
	})
}

// minutesSince is whole minutes, so a status changes at most once a minute.
func minutesSince(since, now time.Time) string {
	if since.IsZero() {
		return "不到 1 分钟"
	}
	return minutesText(now.Sub(since))
}

func durationText(start, end time.Time) string {
	if start.IsZero() || end.IsZero() {
		return "不到 1 分钟"
	}
	return minutesText(end.Sub(start))
}

func minutesText(elapsed time.Duration) string {
	minutes := int(elapsed / time.Minute)
	switch {
	case minutes < 1:
		return "不到 1 分钟"
	case minutes < 60:
		return fmt.Sprintf("%d 分钟", minutes)
	case minutes%60 == 0:
		return fmt.Sprintf("%d 小时", minutes/60)
	default:
		return fmt.Sprintf("%d 小时 %d 分钟", minutes/60, minutes%60)
	}
}

func agoText(at, now time.Time) string {
	if now.Sub(at) < time.Minute {
		return "刚刚"
	}
	return minutesText(now.Sub(at)) + "前"
}

// toolKind names what a worker's tool does in a few words. A status message
// can reach a whole group, so it never shows the tool's input, which can hold
// paths, commands or addresses.
func toolKind(name string) string {
	lower := strings.ToLower(name)
	has := func(words ...string) bool {
		return slices.ContainsFunc(words, func(word string) bool { return strings.Contains(lower, word) })
	}
	switch {
	case has("browser", "navigate", "webfetch", "web_fetch", "websearch", "web_search", "chrome", "playwright", "screenshot"):
		return "浏览网页"
	case has("bash", "shell", "exec", "command", "terminal"):
		return "运行命令"
	case has("edit", "write", "patch", "apply"):
		return "修改文件"
	case has("read", "grep", "glob", "search", "find", "view", "list") || lower == "ls":
		return "查看文件"
	case has("task", "agent", "dispatch"):
		return "分派子任务"
	case has("todo", "plan"):
		return "整理计划"
	default:
		return "使用工具"
	}
}
