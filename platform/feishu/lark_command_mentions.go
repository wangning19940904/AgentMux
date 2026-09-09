package feishu

import (
	"strings"
	"unicode"
	"unicode/utf8"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func isBotMention(mention *larkim.MentionEvent, botOpenID string) bool {
	if mention == nil {
		return false
	}
	if botOpenID == "" {
		return mention.MentionedType != nil && strings.EqualFold(*mention.MentionedType, "app")
	}
	if mention.Id == nil {
		return false
	}
	return (mention.Id.OpenId != nil && *mention.Id.OpenId == botOpenID) ||
		(mention.Id.UserId != nil && *mention.Id.UserId == botOpenID) ||
		(mention.Id.UnionId != nil && *mention.Id.UnionId == botOpenID)
}

// Commands addressed to the bot must reach the engine as commands. Use the
// event's mention identity, never a regex that could erase another recipient
// or turn ordinary prose containing /clear into a destructive command.
func stripBotCommandMention(msg *larkim.EventMessage, botOpenID, text string) string {
	if botOpenID == "" {
		return text
	}
	var prefixes []string
	for _, mention := range msg.Mentions {
		if !isBotMention(mention, botOpenID) {
			continue
		}
		if mention.Key != nil && *mention.Key != "" {
			prefixes = append(prefixes, *mention.Key)
		}
		// Rich-text posts render at elements by display name (or ID).
		if msg.MessageType != nil && *msg.MessageType == "post" {
			if mention.Name != nil && *mention.Name != "" {
				prefixes = append(prefixes, "@"+*mention.Name)
			}
			if mention.Id != nil && mention.Id.OpenId != nil {
				prefixes = append(prefixes, "@"+*mention.Id.OpenId)
			}
		}
	}
	candidate := strings.TrimSpace(text)
	for {
		stripped := false
		for _, prefix := range prefixes {
			if !strings.HasPrefix(candidate, prefix) {
				continue
			}
			rest := strings.TrimPrefix(candidate, prefix)
			first, _ := utf8.DecodeRuneInString(rest)
			if rest != "" && !unicode.IsSpace(first) && first != '/' {
				continue
			}
			candidate = strings.TrimSpace(rest)
			stripped = true
			break
		}
		if !stripped {
			break
		}
	}
	if strings.HasPrefix(candidate, "/") || candidate == "停止" {
		return candidate
	}
	return text
}
