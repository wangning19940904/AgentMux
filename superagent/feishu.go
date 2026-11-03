package superagent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

const (
	feishuDocEditEvent     = "drive.file.edit_v1"
	defaultDocDebounceSecs = 600
)

// feishuReply addresses a chat on a Feishu channel. MessageID, when set, makes
// the reply a thread under the message that asked.
type feishuReply struct {
	ChannelID string `json:"channel_id"`
	ChatID    string `json:"chat_id"`
	MessageID string `json:"message_id,omitempty"`
}

// InterceptMessage is the Engine's inbound hook. It claims a message for a
// Super Agent, so the channel's own Agent does not also answer it. Commands
// stay with the channel, and a message nobody addressed falls through unless
// the channel has no Agent to take it.
func (s *Service) InterceptMessage(ctx context.Context, msg *core.Message) (bool, error) {
	if msg == nil || msg.Callback != nil || strings.HasPrefix(strings.TrimSpace(msg.Text), "/") {
		return false, nil
	}
	claimed := false
	for _, claim := range s.claims.channelSources(msg.ChannelID, core.SuperAgentSourceFeishuMessage) {
		if chats := splitList(claim.source.Config["chat_ids"]); len(chats) > 0 && !slices.Contains(chats, msg.ChatID) {
			continue
		}
		trigger := feishuMessageTrigger(claim.source, msg)
		inserted, err := s.Ingest(ctx, claim.agent, claim.source, trigger)
		if err != nil {
			return true, err
		}
		if inserted || trigger.Addressed {
			claimed = true
		} else if s.channelHasAgent != nil && !s.channelHasAgent(ctx, msg.ChannelID) {
			claimed = true
		}
	}
	return claimed, nil
}

func feishuMessageTrigger(source core.SuperAgentSource, msg *core.Message) core.SuperAgentTrigger {
	addressed := msg.MentionedBot || msg.ChatType == "p2p" || source.Config["require_mention"] == "false"
	root := firstNonEmpty(msg.RootID, msg.ThreadID, msg.SourceMessageID, msg.ID)
	reply, _ := json.Marshal(feishuReply{ChannelID: msg.ChannelID, ChatID: msg.ChatID, MessageID: firstNonEmpty(msg.SourceMessageID, msg.ID)})
	return core.SuperAgentTrigger{
		DedupeKey: firstNonEmpty(msg.SourceMessageID, msg.ID),
		ThreadKey: "feishu:" + msg.ChannelID + ":" + msg.ChatID + ":" + root,
		Actor:     msg.UserID, ActorName: msg.UserName, Addressed: addressed, Text: msg.Text, Reply: reply,
	}
}

// EventTypes tells the event relay which raw platform events to request on a
// channel's long connection on behalf of document Sources.
func (s *Service) EventTypes(ref, source string) []string {
	if source != "feishu" && source != "lark" {
		return nil
	}
	channelID, found := strings.CutPrefix(ref, "channel:")
	if !found || len(s.claims.channelSources(channelID, core.SuperAgentSourceFeishuDoc)) == 0 {
		return nil
	}
	return []string{feishuDocEditEvent}
}

// HandleRelayEvent ingests a document edit. Returning an error makes Feishu
// redeliver the event, and the dedupe key absorbs the redelivery.
func (s *Service) HandleRelayEvent(ctx context.Context, event core.RelayEvent) error {
	if event.Type != feishuDocEditEvent {
		return nil
	}
	channelID, found := strings.CutPrefix(event.SourceRef, "channel:")
	if !found {
		return nil
	}
	claims := s.claims.channelSources(channelID, core.SuperAgentSourceFeishuDoc)
	if len(claims) == 0 {
		return nil
	}
	edit, err := parseDocEdit(event.Data)
	if err != nil {
		return err
	}
	for _, claim := range claims {
		trigger, ok := docEditTrigger(claim.source, channelID, edit, event.OccurredAt, s.documentTokens(claim.source))
		if !ok {
			continue
		}
		if _, err := s.Ingest(ctx, claim.agent, claim.source, trigger); err != nil {
			return err
		}
	}
	return nil
}

type docEdit struct {
	FileToken string
	FileType  string
	Operators []string
}

func parseDocEdit(data json.RawMessage) (docEdit, error) {
	var envelope struct {
		Event struct {
			FileToken      string `json:"file_token"`
			FileType       string `json:"file_type"`
			OperatorIDList []struct {
				OpenID string `json:"open_id"`
			} `json:"operator_id_list"`
		} `json:"event"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return docEdit{}, fmt.Errorf("decode document event: %w", err)
	}
	edit := docEdit{FileToken: envelope.Event.FileToken, FileType: envelope.Event.FileType}
	for _, operator := range envelope.Event.OperatorIDList {
		if operator.OpenID != "" {
			edit.Operators = append(edit.Operators, operator.OpenID)
		}
	}
	return edit, nil
}

// docEditTrigger collapses a burst of edits into one Trigger per debounce
// window: Feishu emits an event for every save, but a person's editing session
// is one change. The actor is the first editor on the allowed list, so an edit
// by anyone else, including the workers' own bot identity, starts nothing.
func docEditTrigger(source core.SuperAgentSource, channelID string, edit docEdit, occurred time.Time, tokens []documentRef) (core.SuperAgentTrigger, bool) {
	if !slices.ContainsFunc(tokens, func(ref documentRef) bool { return ref.Token == edit.FileToken }) {
		return core.SuperAgentTrigger{}, false
	}
	actor := ""
	for _, operator := range edit.Operators {
		if actorAllowed(source, operator) {
			actor = operator
			break
		}
	}
	if actor == "" {
		return core.SuperAgentTrigger{}, false
	}
	debounce, err := strconv.Atoi(source.Config["debounce_seconds"])
	if err != nil || debounce <= 0 {
		debounce = defaultDocDebounceSecs
	}
	reply, _ := json.Marshal(feishuReply{ChannelID: channelID, ChatID: strings.TrimSpace(source.Config["notify_chat_id"])})
	bucket := occurred.Unix() / int64(debounce)
	return core.SuperAgentTrigger{
		DedupeKey: fmt.Sprintf("%s:%d", edit.FileToken, bucket),
		ThreadKey: "feishu_doc:" + edit.FileToken,
		Actor:     actor, Addressed: true,
		Title: "Feishu document updated",
		Text: fmt.Sprintf("A watched Feishu document was edited.\nfile_token: %s\nfile_type: %s\n%s",
			edit.FileToken, edit.FileType, strings.TrimSpace(source.Config["instructions"])),
		Reply: reply,
	}, true
}

// documentRef is one watched document: a token and the type Feishu's
// subscription API needs. WikiNode marks a token that must be resolved first.
type documentRef struct {
	Token    string
	FileType string
	WikiNode bool
}

// parseDocuments accepts document URLs or bare docx tokens.
func parseDocuments(value string) []documentRef {
	var refs []documentRef
	for _, item := range splitList(value) {
		parsed, err := url.Parse(item)
		if err != nil || parsed.Host == "" {
			refs = append(refs, documentRef{Token: item, FileType: "docx"})
			continue
		}
		segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		if len(segments) < 2 {
			continue
		}
		kind, token := segments[len(segments)-2], segments[len(segments)-1]
		switch kind {
		case "docx", "doc", "file", "slides":
			refs = append(refs, documentRef{Token: token, FileType: kind})
		case "sheets":
			refs = append(refs, documentRef{Token: token, FileType: "sheet"})
		case "base":
			refs = append(refs, documentRef{Token: token, FileType: "bitable"})
		case "wiki":
			refs = append(refs, documentRef{Token: token, WikiNode: true})
		}
	}
	return refs
}

// DocumentSubscriber asks Feishu to send edit events for a document and resolves
// wiki nodes to the underlying file they name.
type DocumentSubscriber interface {
	ResolveWikiNode(ctx context.Context, channelID, token string) (documentRef, error)
	Subscribe(ctx context.Context, channelID string, document documentRef) error
}

func (s *Service) documentTokens(source core.SuperAgentSource) []documentRef {
	s.mu.Lock()
	resolved, ok := s.documents[source.ID]
	s.mu.Unlock()
	if ok {
		return resolved
	}
	return parseDocuments(source.Config["documents"])
}

// subscribeDocuments runs outside the save request: Feishu may be slow or the
// bot may lack access, which is a Source health problem and not a save failure.
func (s *Service) subscribeDocuments(ctx context.Context, source core.SuperAgentSource) {
	if s.docs == nil {
		return
	}
	channelID := strings.TrimSpace(source.Config["channel_id"])
	var resolved []documentRef
	var firstErr error
	for _, document := range parseDocuments(source.Config["documents"]) {
		if document.WikiNode {
			node, err := s.docs.ResolveWikiNode(ctx, channelID, document.Token)
			if err != nil {
				firstErr = fmt.Errorf("resolve wiki node %s: %w", document.Token, err)
				continue
			}
			document = node
		}
		if err := s.docs.Subscribe(ctx, channelID, document); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("subscribe to document %s: %w", document.Token, err)
		}
		resolved = append(resolved, document)
	}
	s.mu.Lock()
	s.documents[source.ID] = resolved
	s.mu.Unlock()
	s.recordSourceError(source.ID, firstErr)
}
