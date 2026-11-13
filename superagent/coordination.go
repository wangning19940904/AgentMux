package superagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/wangning19940904/AgentMux/core"
)

// A Super Agent coordinates work across threads and channels. The router sees
// every open Ticket, the places it can post to and who is there, and wakes up
// whenever a worker finishes a turn. This file builds that view and carries
// out what the router decides to do with it.

const (
	// openTicketWindow keeps finished Tickets in the router's view for a while,
	// so it can still answer questions about them and build on them.
	openTicketWindow = 72 * time.Hour
	maxOpenTickets   = 30
	ticketListLimit  = 200
	// maxReportRunes bounds the worker report quoted in a ticket update. The
	// router reads result.md for the rest.
	maxReportRunes   = 6000
	placesTTL        = 10 * time.Minute
	placesErrorTTL   = 2 * time.Minute
	placesTimeout    = 8 * time.Second
	scopeCheckTTL    = 30 * time.Minute
	maxPeoplePerChat = 40
	ticketProgress   = "progress.md"
	maxProgressBytes = 256 << 10
	outputThrottle   = 20 * time.Second
	// feishuGroupMessages is the scope that delivers group messages that do
	// not mention the bot, such as replies in a ticket's thread.
	feishuGroupMessages = "im:message.group_msg"
)

// ticketView is one Ticket as the router is shown it.
type ticketView struct {
	ID         string       `json:"id"`
	Title      string       `json:"title"`
	Status     string       `json:"status"`
	Worker     string       `json:"worker_id"`
	Thread     string       `json:"thread"`
	WaitingFor []string     `json:"waiting_for,omitempty"`
	DependsOn  []string     `json:"depends_on,omitempty"`
	BaseTicket string       `json:"base_ticket,omitempty"`
	Linked     []threadLink `json:"linked_threads,omitempty"`
	Started    string       `json:"started,omitempty"`
	Updated    string       `json:"updated"`
	Activity   string       `json:"last_activity,omitempty"`
	Folder     string       `json:"folder,omitempty"`
}

type threadLink struct {
	Where string `json:"where"`
	To    string `json:"to"`
}

// placeView is somewhere post can send to, with the people there.
type placeView struct {
	To     string       `json:"to"`
	Kind   string       `json:"kind"`
	Name   string       `json:"name,omitempty"`
	People []personView `json:"people,omitempty"`
}

type personView struct {
	Name   string `json:"name"`
	OpenID string `json:"open_id"`
}

// Directory resolves the names of a Feishu bot's groups and the people in
// them. FeishuAPI implements it.
type Directory interface {
	Chats(ctx context.Context, channelID string) (FeishuChatList, error)
	Members(ctx context.Context, channelID string, chatIDs []string) (FeishuUserList, error)
}

// feishuPlaces is one channel's groups and members, cached.
type feishuPlaces struct {
	at     time.Time
	err    error
	names  map[string]string
	people map[string][]personView
}

// scopeState is the latest scope check of one channel.
type scopeState struct {
	at          time.Time
	mentionOnly bool
	applyURL    string
}

// activity is the latest thing a running worker did.
type activity struct {
	at   time.Time
	text string
}

// routerView assembles everything the router is shown with an event.
func (s *Service) routerView(ctx context.Context, agent core.SuperAgent, trigger core.SuperAgentTrigger, tickets []core.SuperAgentTicket) routerContext {
	view := routerContext{Memory: s.memoryIndex(agent.ID), Machines: s.machineStatuses(ctx, agent)}
	names := map[string]string{}
	for _, source := range agent.Sources {
		if !source.Enabled {
			continue
		}
		switch source.Kind {
		case core.SuperAgentSourceFeishuMessage:
			channelID := strings.TrimSpace(source.Config["channel_id"])
			places := s.feishuPlacesFor(ctx, channelID, splitList(source.Config["chat_ids"]))
			chats := splitList(source.Config["chat_ids"])
			if len(chats) == 0 {
				for id := range places.names {
					chats = append(chats, id)
				}
				slices.Sort(chats)
			}
			var groupNames []string
			for _, chatID := range chats {
				name := places.names[chatID]
				names[chatID] = name
				groupNames = append(groupNames, firstNonEmpty(name, chatID))
				view.Places = append(view.Places, placeView{
					To: "chat:" + chatID, Kind: "Feishu group: a new message in the group", Name: name, People: places.people[chatID],
				})
			}
			if state := s.scopeState(channelID); state.mentionOnly && source.Config["require_mention"] != "false" {
				view.Notes = append(view.Notes, fmt.Sprintf("Feishu delivers only messages that @mention you in %s: the bot's app lacks the %s permission. When you need an answer in a thread, ask people to @mention you in it.", strings.Join(groupNames, ", "), feishuGroupMessages))
			}
		case core.SuperAgentSourceGitHub:
			if repo := strings.TrimSpace(source.Config["repo"]); repo != "" {
				view.Places = append(view.Places, placeView{To: "github:" + repo + "#<number>", Kind: "GitHub: a comment on any issue or pull request of " + repo})
			}
		}
	}
	view.Where = describeThread(trigger.ThreadKey, trigger.DedupeKey, names)
	view.Asks = s.asksIn(agent.ID, trigger.ThreadKey, names)
	now := s.now()
	shown := 0
	for _, ticket := range newestFirst(tickets) {
		onThread := ticket.ThreadKey == trigger.ThreadKey
		recent := now.Sub(ticket.UpdatedAt) < openTicketWindow
		if shown >= maxOpenTickets || (!onThread && (ticket.Status == core.SuperAgentTicketStopped || !recent)) {
			continue
		}
		if onThread && ticket.Status == core.SuperAgentTicketStopped && !recent {
			continue
		}
		shown++
		entry := ticketView{
			ID: ticket.ID, Title: ticket.Title, Status: ticket.Status, Worker: ticket.Worker.ID,
			Thread: "this thread", DependsOn: ticket.DependsOn, BaseTicket: ticket.BaseTicket,
			Updated: ago(now, ticket.UpdatedAt),
		}
		if !onThread {
			entry.Thread = describeThread(ticket.ThreadKey, "", names)
		}
		if !ticket.StartedAt.IsZero() {
			entry.Started = ago(now, ticket.StartedAt)
		}
		if ticket.Status == core.SuperAgentTicketQueued {
			entry.WaitingFor = s.unmetDependencies(ctx, ticket)
		}
		for _, link := range ticket.Links {
			if link.ThreadKey != trigger.ThreadKey {
				entry.Linked = append(entry.Linked, threadLink{Where: describeThread(link.ThreadKey, "", names), To: link.ThreadKey})
			}
		}
		if ticket.Status == core.SuperAgentTicketRunning {
			if latest, ok := s.latestActivity(ticket.ID); ok {
				entry.Activity = ago(now, latest.at) + ": " + latest.text
			}
		}
		if s.existingTicketDir(ticket) != "" {
			entry.Folder = ticketsDirName + "/" + ticket.ID
		}
		view.Tickets = append(view.Tickets, entry)
	}
	return view
}

func newestFirst(tickets []core.SuperAgentTicket) []core.SuperAgentTicket {
	sorted := slices.Clone(tickets)
	slices.SortStableFunc(sorted, func(a, b core.SuperAgentTicket) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return sorted
}

func ago(now, at time.Time) string {
	if at.IsZero() {
		return ""
	}
	elapsed := now.Sub(at)
	switch {
	case elapsed < time.Minute:
		return "just now"
	case elapsed < time.Hour:
		return fmt.Sprintf("%dm ago", int(elapsed.Minutes()))
	case elapsed < 48*time.Hour:
		return fmt.Sprintf("%dh%02dm ago", int(elapsed.Hours()), int(elapsed.Minutes())%60)
	default:
		return fmt.Sprintf("%dd ago", int(elapsed.Hours()/24))
	}
}

// describeThread says where a thread is in words people use. messageID, when
// it is the thread's root, marks a new message rather than a reply.
func describeThread(threadKey, messageID string, chatNames map[string]string) string {
	switch {
	case strings.HasPrefix(threadKey, "feishu:"):
		parts := strings.SplitN(strings.TrimPrefix(threadKey, "feishu:"), ":", 3)
		if len(parts) != 3 {
			return threadKey
		}
		group := parts[1]
		if name := chatNames[parts[1]]; name != "" {
			group = strconv.Quote(name)
		}
		if messageID != "" && parts[2] == messageID {
			return "Feishu group " + group + ", a new message"
		}
		return "Feishu group " + group + ", a thread"
	case strings.HasPrefix(threadKey, "github:"):
		return "GitHub " + strings.TrimPrefix(threadKey, "github:")
	case strings.HasPrefix(threadKey, "email:"):
		return "an email thread"
	case strings.HasPrefix(threadKey, "feishu_doc:"):
		return "Feishu document " + strings.TrimPrefix(threadKey, "feishu_doc:")
	case strings.HasPrefix(threadKey, "console:"):
		return "the AgentMux Console"
	case strings.HasPrefix(threadKey, channelSourcePrefix):
		return "a chat with you on channel " + strings.SplitN(strings.TrimPrefix(threadKey, channelSourcePrefix), ":", 2)[0]
	}
	return threadKey
}

// feishuPlacesFor returns a channel's group names and members, from cache when
// fresh. A failure leaves the router with chat ids only.
func (s *Service) feishuPlacesFor(ctx context.Context, channelID string, chatIDs []string) feishuPlaces {
	if s.directory == nil || channelID == "" {
		return feishuPlaces{}
	}
	key := channelID + "|" + strings.Join(chatIDs, ",")
	s.mu.Lock()
	cached, ok := s.places[key]
	s.mu.Unlock()
	ttl := placesTTL
	if cached.err != nil {
		ttl = placesErrorTTL
	}
	if ok && s.now().Sub(cached.at) < ttl {
		return cached
	}
	lookupCtx, cancel := context.WithTimeout(ctx, placesTimeout)
	defer cancel()
	places := feishuPlaces{at: s.now(), names: map[string]string{}, people: map[string][]personView{}}
	chats, err := s.directory.Chats(lookupCtx, channelID)
	if err == nil {
		for _, chat := range chats.Items {
			places.names[chat.ChatID] = chat.Name
		}
		members := chatIDs
		if len(members) == 0 {
			for id := range places.names {
				members = append(members, id)
			}
		}
		if list, memberErr := s.directory.Members(lookupCtx, channelID, members); memberErr == nil {
			for _, user := range list.Items {
				for _, chatID := range user.ChatIDs {
					if len(places.people[chatID]) < maxPeoplePerChat {
						places.people[chatID] = append(places.people[chatID], personView{Name: user.Name, OpenID: user.OpenID})
					}
				}
			}
		} else {
			s.log.Debug("super agent could not list Feishu group members", "channel", channelID, "err", memberErr)
		}
	} else {
		places.err = err
		s.log.Debug("super agent could not list Feishu groups", "channel", channelID, "err", err)
	}
	s.mu.Lock()
	s.places[key] = places
	s.mu.Unlock()
	return places
}

// checkFeishuScopes records whether a Feishu message Source can receive group
// messages that do not mention its bot. Without that scope, replies in a
// ticket's thread silently never arrive, so the Source shows a warning and the
// router is told to ask for mentions.
func (s *Service) checkFeishuScopes(ctx context.Context, source core.SuperAgentSource) {
	if s.scopes == nil || source.Kind != core.SuperAgentSourceFeishuMessage || source.Config["require_mention"] == "false" {
		s.recordSourceWarning(source.ID, "")
		return
	}
	channelID := strings.TrimSpace(source.Config["channel_id"])
	s.mu.Lock()
	state, ok := s.scopeChecks[channelID]
	s.mu.Unlock()
	if !ok || s.now().Sub(state.at) >= scopeCheckTTL {
		checkCtx, cancel := context.WithTimeout(ctx, placesTimeout)
		status, err := s.scopes.ScopeStatus(checkCtx, channelID, []string{feishuGroupMessages}, nil)
		cancel()
		if err != nil {
			s.log.Debug("super agent could not check Feishu scopes", "channel", channelID, "err", err)
			return
		}
		state = scopeState{at: s.now(), mentionOnly: len(status.Tenant.Missing)+len(status.Tenant.Pending) > 0, applyURL: status.Tenant.ApplyURL}
		s.mu.Lock()
		s.scopeChecks[channelID] = state
		s.mu.Unlock()
	}
	if !state.mentionOnly {
		s.recordSourceWarning(source.ID, "")
		return
	}
	warning := "Only messages that @mention the bot reach this Super Agent, including replies in a ticket's thread: the bot's Feishu app lacks the " + feishuGroupMessages + " permission. Add it and publish a new app version to receive the rest."
	if state.applyURL != "" {
		warning += " " + state.applyURL
	}
	s.recordSourceWarning(source.ID, warning)
}

func (s *Service) scopeState(channelID string) scopeState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scopeChecks[channelID]
}

// destination is where a post goes: a Source and its reply address.
type destination struct {
	sourceID string
	reply    []byte
}

// resolveDestination checks a post's "to" against what this Super Agent
// knows: its Tickets and their linked threads, its Feishu groups and its
// GitHub repository. Nothing else can be addressed.
func (s *Service) resolveDestination(ctx context.Context, agent core.SuperAgent, to string, refs map[string]string) (destination, error) {
	to = strings.TrimSpace(to)
	if id, ok := refs[to]; ok {
		to = id
	}
	unknown := fmt.Errorf("cannot post to %q: use a ticket id, a \"to\" value from OPEN TICKETS or PLACES, or github:<owner/repo>#<number>", to)
	switch {
	case strings.HasPrefix(to, "tkt_"):
		ticket, err := s.agentTicket(ctx, agent.ID, to)
		if err != nil {
			return destination{}, err
		}
		if len(ticket.Reply) == 0 {
			return destination{}, fmt.Errorf("ticket %s came from the Console, which has no thread to post to", to)
		}
		return destination{sourceID: ticket.SourceID, reply: ticket.Reply}, nil
	case strings.HasPrefix(to, "chat:"):
		chatID := strings.TrimPrefix(to, "chat:")
		for _, source := range agent.Sources {
			if !source.Enabled || source.Kind != core.SuperAgentSourceFeishuMessage {
				continue
			}
			channelID := strings.TrimSpace(source.Config["channel_id"])
			chats := splitList(source.Config["chat_ids"])
			allowed := slices.Contains(chats, chatID)
			if len(chats) == 0 {
				_, allowed = s.feishuPlacesFor(ctx, channelID, nil).names[chatID]
			}
			if allowed {
				reply, _ := json.Marshal(feishuReply{ChannelID: channelID, ChatID: chatID})
				return destination{sourceID: source.ID, reply: reply}, nil
			}
		}
		return destination{}, fmt.Errorf("cannot post to %q: it is not one of this Super Agent's Feishu groups", to)
	case strings.HasPrefix(to, "github:"):
		if known, ok := s.knownThread(ctx, agent.ID, to); ok {
			return known, nil
		}
		repo, number, found := strings.Cut(strings.TrimPrefix(to, "github:"), "#")
		n, err := strconv.Atoi(number)
		if !found || err != nil || n <= 0 || !strings.Contains(repo, "/") {
			return destination{}, unknown
		}
		for _, source := range agent.Sources {
			if !source.Enabled || source.Kind != core.SuperAgentSourceGitHub {
				continue
			}
			if want := strings.TrimSpace(source.Config["repo"]); want == "" || strings.EqualFold(want, repo) {
				reply, _ := json.Marshal(githubReply{Repo: repo, Number: n})
				return destination{sourceID: source.ID, reply: reply}, nil
			}
		}
		return destination{}, fmt.Errorf("cannot post to %q: no GitHub trigger of this Super Agent covers %s", to, repo)
	}
	if known, ok := s.knownThread(ctx, agent.ID, to); ok {
		return known, nil
	}
	return destination{}, unknown
}

// knownThread finds the latest reply address of a thread one of this Super
// Agent's Tickets lives on or is linked to, or that waits on an ask.
func (s *Service) knownThread(ctx context.Context, agentID, threadKey string) (destination, bool) {
	tickets, err := s.store.ListSuperAgentTickets(ctx, agentID, ticketListLimit)
	if err != nil {
		return s.askedFrom(agentID, threadKey)
	}
	for _, ticket := range newestFirst(tickets) {
		if strings.EqualFold(ticket.ThreadKey, threadKey) && len(ticket.Reply) > 0 {
			return destination{sourceID: ticket.SourceID, reply: ticket.Reply}, true
		}
		for _, link := range ticket.Links {
			if strings.EqualFold(link.ThreadKey, threadKey) && len(link.Reply) > 0 {
				return destination{sourceID: link.SourceID, reply: link.Reply}, true
			}
		}
	}
	return s.askedFrom(agentID, threadKey)
}

// agentTicket loads a Ticket and checks that it belongs to this Super Agent.
func (s *Service) agentTicket(ctx context.Context, agentID, id string) (*core.SuperAgentTicket, error) {
	ticket, err := s.store.GetSuperAgentTicket(ctx, id)
	if err != nil {
		return nil, err
	}
	if ticket == nil || ticket.SuperAgentID != agentID {
		return nil, fmt.Errorf("ticket %q is not one of this Super Agent's tickets", id)
	}
	return ticket, nil
}

// unmetDependencies lists the Tickets a queued Ticket still waits for.
func (s *Service) unmetDependencies(ctx context.Context, ticket core.SuperAgentTicket) []string {
	var waiting []string
	for _, id := range ticket.DependsOn {
		dependency, err := s.store.GetSuperAgentTicket(ctx, id)
		if err != nil || dependency == nil || dependency.Status != core.SuperAgentTicketReady {
			waiting = append(waiting, id)
		}
	}
	return waiting
}

// unblockedBy lists the Tickets that waited for this one and now have all
// they need, including any that already started.
func (s *Service) unblockedBy(ctx context.Context, ticketID string) []string {
	waiting, err := s.store.ListSuperAgentTicketsByStatus(ctx, core.SuperAgentTicketQueued, core.SuperAgentTicketRunning)
	if err != nil {
		return nil
	}
	var out []string
	for _, ticket := range waiting {
		if slices.Contains(ticket.DependsOn, ticketID) && len(s.unmetDependencies(ctx, ticket)) == 0 {
			out = append(out, ticket.ID)
		}
	}
	return out
}

// baseCommit is the commit a Ticket's new worktree starts from: the latest
// commit in its base Ticket's work/.
func (s *Service) baseCommit(ctx context.Context, ticket core.SuperAgentTicket) string {
	if ticket.BaseTicket == "" {
		return ""
	}
	base, err := s.store.GetSuperAgentTicket(ctx, ticket.BaseTicket)
	if err != nil || base == nil {
		return ""
	}
	dir := s.existingTicketDir(*base)
	if dir == "" {
		return ""
	}
	output, err := exec.CommandContext(ctx, "git", "-C", filepath.Join(dir, ticketWorkDir), "rev-parse", "HEAD").Output()
	if err != nil {
		s.log.Warn("super agent could not read base ticket's commit", "ticket", ticket.ID, "base", base.ID, "err", err)
		return ""
	}
	return strings.TrimSpace(string(output))
}

// ticketBackground tells a new worker about the Tickets it builds on.
func (s *Service) ticketBackground(ctx context.Context, agentID string, dependsOn []string, base string) []string {
	home, err := s.homeDir(agentID)
	if err != nil {
		return nil
	}
	var lines []string
	if len(dependsOn) > 0 {
		var refs []string
		for _, id := range dependsOn {
			label := id
			if ticket, err := s.store.GetSuperAgentTicket(ctx, id); err == nil && ticket != nil {
				label = fmt.Sprintf("%q (%s)", ticket.Title, filepath.Join(home, ticketsDirName, id, ticketResultFile))
			}
			refs = append(refs, label)
		}
		lines = append(lines, "This ticket starts after "+strings.Join(refs, ", ")+" finished. Read their results before you start.")
	}
	if base != "" {
		lines = append(lines, "Your working directory starts from the last commit of ticket "+base+", so its changes are already there. Build on them; don't redo them.")
	}
	return lines
}

// wakeRouter tells the router a Ticket's worker finished a turn, on the
// Ticket's thread, so it can tell people and move dependent work along.
func (s *Service) wakeRouter(ctx context.Context, agent core.SuperAgent, ticket core.SuperAgentTicket, replyTo []byte, report string, failed bool) error {
	now := s.now().UTC()
	var text strings.Builder
	if failed {
		fmt.Fprintf(&text, "Ticket %s %q failed: %s\n", ticket.ID, ticket.Title, report)
	} else {
		fmt.Fprintf(&text, "Ticket %s %q finished a turn and is ready.\n", ticket.ID, ticket.Title)
	}
	if unblocked := s.unblockedBy(ctx, ticket.ID); len(unblocked) > 0 {
		fmt.Fprintf(&text, "AgentMux is now starting %s, which waited for it.\n", strings.Join(unblocked, ", "))
	}
	if len(ticket.Links) > 0 {
		var where []string
		for _, link := range ticket.Links {
			where = append(where, link.ThreadKey)
		}
		fmt.Fprintf(&text, "Other threads that care about it (post \"to\" these): %s\n", strings.Join(where, ", "))
	}
	if !failed {
		fmt.Fprintf(&text, "\nWorker's report:\n%s\n", truncateRunes(strings.TrimSpace(report), maxReportRunes))
	}
	trigger := core.SuperAgentTrigger{
		ID: "sat_" + uuid.NewString(), SuperAgentID: agent.ID, SourceID: ticket.SourceID, SourceKind: ticket.SourceKind,
		DedupeKey: "ticket:" + ticket.ID + ":" + uuid.NewString(), ThreadKey: ticket.ThreadKey,
		Actor: "agentmux", ActorName: "AgentMux", Addressed: true, Kind: core.SuperAgentTriggerTicketUpdate,
		TicketID: ticket.ID, Title: "Ticket update: " + ticket.Title, Text: text.String(), Reply: replyTo,
		Disposition: core.SuperAgentDispositionPending, ReceivedAt: now, UpdatedAt: now,
	}
	if _, err := s.store.InsertSuperAgentTrigger(ctx, trigger); err != nil {
		return err
	}
	s.notify()
	return nil
}

// relayReport is what people get when the router cannot tell them about a
// finished turn itself: the worker's own words.
func (s *Service) relayReport(ctx context.Context, agent core.SuperAgent, trigger core.SuperAgentTrigger) {
	ticket, err := s.store.GetSuperAgentTicket(ctx, trigger.TicketID)
	if err != nil || ticket == nil {
		return
	}
	text := ticket.Output
	if ticket.Status == core.SuperAgentTicketFailed {
		text = fmt.Sprintf("I could not finish “%s”: %s", ticket.Title, ticket.Error)
	}
	if err := s.sendReply(ctx, agent, ticket.SourceID, trigger.Reply, text); err != nil {
		_ = s.mutateTicket(ctx, ticket.ID, func(current *core.SuperAgentTicket) error {
			current.ReplyError = err.Error()
			return nil
		})
	}
}

// workerSteerer is a WorkerRunner that can add input to a running turn.
type workerSteerer interface {
	Steer(ctx context.Context, worker core.SuperAgentWorker, conversationID, text string) error
}

// steer delivers text to a running Ticket's current turn, when its worker's
// framework allows it.
func (s *Service) steer(ctx context.Context, ticket core.SuperAgentTicket, text string) error {
	steerer, ok := s.workers.(workerSteerer)
	if !ok {
		return core.ErrSteerUnsupported
	}
	steerCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return steerer.Steer(steerCtx, ticket.Worker, ticket.ConversationID, text)
}

// progressRecorder writes what a worker does during a turn to progress.md,
// so the router can say how work is going while it runs.
func (s *Service) progressRecorder(ticket core.SuperAgentTicket) func(core.InvocationStreamEvent) {
	seen := map[string]bool{}
	var lastOutput time.Time
	return func(event core.InvocationStreamEvent) {
		var line string
		switch {
		case event.ToolName != "" && event.ToolCallID != "" && !seen[event.ToolCallID]:
			seen[event.ToolCallID] = true
			line = event.ToolName
			if input := oneLine(event.ToolInput, 160); input != "" {
				line += ": " + input
			}
		case event.Type == string(core.EventOutput) && !event.Final && strings.TrimSpace(event.Text) != "":
			if s.now().Sub(lastOutput) < outputThrottle {
				return
			}
			lastOutput = s.now()
			line = "said: " + oneLine(event.Text, 200)
		default:
			return
		}
		at := s.now().UTC()
		s.mu.Lock()
		s.activities[ticket.ID] = activity{at: at, text: line}
		s.mu.Unlock()
		s.appendProgress(ticket, fmt.Sprintf("- %s %s\n", at.Format("15:04:05"), line))
	}
}

func (s *Service) latestActivity(ticketID string) (activity, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	latest, ok := s.activities[ticketID]
	return latest, ok
}

func (s *Service) appendProgress(ticket core.SuperAgentTicket, line string) {
	dir := s.existingTicketDir(ticket)
	if dir == "" {
		return
	}
	path := filepath.Join(dir, ticketProgress)
	if info, err := os.Stat(path); err == nil && info.Size() > maxProgressBytes {
		_ = os.Rename(path, filepath.Join(dir, "progress.old.md"))
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	_, _ = file.WriteString(line)
	_ = file.Close()
}

// oneLine shortens text to one line of at most limit runes.
func oneLine(text string, limit int) string {
	text = strings.Join(strings.FieldsFunc(text, func(r rune) bool { return r == '\n' || r == '\r' }), " ")
	text = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, text))
	if runes := []rune(text); len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return text
}
