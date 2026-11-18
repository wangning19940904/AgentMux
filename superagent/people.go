package superagent

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// Feishu tells AgentMux who sent a message only by open_id. Before a Trigger
// is stored, and when the Console reads an agent's activity, the sender is
// named from the channel app's contacts, with their portrait, or, without the
// contact scope, from the members of the group the message came from.

const (
	personTTL       = 6 * time.Hour
	personMissTTL   = 15 * time.Minute
	personTimeout   = 3 * time.Second
	maxPeopleCached = 2000
)

// personEntry is one cached lookup. An empty name is a person Feishu would
// not name, kept for a shorter while.
type personEntry struct {
	at     time.Time
	name   string
	avatar string
}

// personDirectory is the part of FeishuAPI that names people by open_id.
type personDirectory interface {
	Users(ctx context.Context, channelID string, openIDs, emails []string) ([]FeishuUser, error)
}

func isFeishuOpenID(actor string) bool {
	return strings.HasPrefix(strings.TrimSpace(actor), "ou_")
}

// replyPlace reads the channel and chat a Trigger replies to.
func replyPlace(reply json.RawMessage) (channelID, chatID string) {
	if len(reply) == 0 {
		return "", ""
	}
	var place feishuReply
	if json.Unmarshal(reply, &place) != nil {
		return "", ""
	}
	return strings.TrimSpace(place.ChannelID), strings.TrimSpace(place.ChatID)
}

func needsNaming(trigger core.SuperAgentTrigger) bool {
	if !isFeishuOpenID(trigger.Actor) {
		return false
	}
	return trigger.ActorName == "" || trigger.ActorName == trigger.Actor || trigger.ActorAvatar == ""
}

// nameTrigger names a Feishu sender before their Trigger is stored. The
// router then sees who asked, and the Console keeps their name and portrait.
func (s *Service) nameTrigger(ctx context.Context, trigger *core.SuperAgentTrigger) {
	if !needsNaming(*trigger) {
		return
	}
	channelID, chatID := replyPlace(trigger.Reply)
	if channelID == "" {
		return
	}
	people := s.feishuPeople(ctx, channelID, chatID, []string{trigger.Actor})
	applyPerson(trigger, people)
}

// nameTriggers names the senders of Triggers stored without a name, such as
// those stored before AgentMux looked people up. It only changes what the
// Console is shown.
func (s *Service) nameTriggers(ctx context.Context, triggers []core.SuperAgentTrigger) {
	type place struct{ channelID, chatID string }
	wanted := map[place][]string{}
	var order []place
	for _, trigger := range triggers {
		if !needsNaming(trigger) {
			continue
		}
		channelID, chatID := replyPlace(trigger.Reply)
		if channelID == "" {
			continue
		}
		key := place{channelID, chatID}
		if _, seen := wanted[key]; !seen {
			order = append(order, key)
		}
		if !slices.Contains(wanted[key], trigger.Actor) {
			wanted[key] = append(wanted[key], trigger.Actor)
		}
	}
	if len(order) == 0 {
		return
	}
	lookupCtx, cancel := context.WithTimeout(ctx, personTimeout)
	defer cancel()
	named := map[place]map[string]personEntry{}
	for _, key := range order {
		named[key] = s.feishuPeople(lookupCtx, key.channelID, key.chatID, wanted[key])
	}
	for index := range triggers {
		channelID, chatID := replyPlace(triggers[index].Reply)
		if people := named[place{channelID, chatID}]; people != nil {
			applyPerson(&triggers[index], people)
		}
	}
}

func applyPerson(trigger *core.SuperAgentTrigger, people map[string]personEntry) {
	person, ok := people[trigger.Actor]
	if !ok {
		return
	}
	if trigger.ActorName == "" || trigger.ActorName == trigger.Actor {
		trigger.ActorName = person.name
	}
	if trigger.ActorAvatar == "" {
		trigger.ActorAvatar = person.avatar
	}
}

// feishuPeople names openIDs on a channel, from cache when fresh. It returns
// only the people it could name.
func (s *Service) feishuPeople(ctx context.Context, channelID, chatID string, openIDs []string) map[string]personEntry {
	result := map[string]personEntry{}
	if s.directory == nil || channelID == "" {
		return result
	}
	now := s.now()
	var missing []string
	s.mu.Lock()
	for _, openID := range openIDs {
		entry, ok := s.people[channelID+"|"+openID]
		ttl := personTTL
		if entry.name == "" {
			ttl = personMissTTL
		}
		if ok && now.Sub(entry.at) < ttl {
			if entry.name != "" {
				result[openID] = entry
			}
			continue
		}
		missing = append(missing, openID)
	}
	s.mu.Unlock()
	if len(missing) == 0 {
		return result
	}

	lookupCtx, cancel := context.WithTimeout(ctx, personTimeout)
	defer cancel()
	found := map[string]personEntry{}
	if users, ok := s.directory.(personDirectory); ok {
		for start := 0; start < len(missing); start += maxUserLookup {
			batch := missing[start:min(start+maxUserLookup, len(missing))]
			list, err := users.Users(lookupCtx, channelID, batch, nil)
			if err != nil {
				s.log.Debug("super agent could not name Feishu people", "channel", channelID, "err", err)
				break
			}
			for _, user := range list {
				if user.Name != "" {
					found[user.OpenID] = personEntry{name: user.Name, avatar: user.Avatar}
				}
			}
		}
	}
	// Without the contact scope, the group's member list still names the
	// people in it.
	if chatID != "" && len(found) < len(missing) {
		places := s.feishuPlacesFor(lookupCtx, channelID, []string{chatID})
		for _, person := range places.people[chatID] {
			if person.Name != "" && slices.Contains(missing, person.OpenID) && found[person.OpenID].name == "" {
				found[person.OpenID] = personEntry{name: person.Name}
			}
		}
	}

	s.mu.Lock()
	if s.people == nil || len(s.people)+len(missing) > maxPeopleCached {
		s.people = map[string]personEntry{}
	}
	for _, openID := range missing {
		entry := found[openID]
		entry.at = now
		s.people[channelID+"|"+openID] = entry
		if entry.name != "" {
			result[openID] = entry
		}
	}
	s.mu.Unlock()
	return result
}
