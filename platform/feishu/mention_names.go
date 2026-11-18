package feishu

import (
	"strings"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// inboundMessageText turns a received Feishu message into the text the
// router reads and the text shown to people. Mention keys such as @_user_1
// are unambiguous, so bot detection, command stripping and preview cleanup
// run on them first; display names replace the remaining keys last.
func inboundMessageText(msg *larkim.EventMessage, botOpenID string) (text, display string, mentionedBot, mentionAll bool) {
	if msg == nil || msg.MessageType == nil || msg.Content == nil {
		return "", "", false, false
	}
	text = extractText(*msg.MessageType, *msg.Content)
	if text == "" {
		return "", "", false, false
	}
	mentionedBot, mentionAll = mentionState(msg, botOpenID, text)
	text = stripBotCommandMention(msg, botOpenID, text)
	display = inboundCardPreviewText(msg, botOpenID, text)
	display = strings.TrimSpace(replaceMentionKeys(display, msg.Mentions))
	return replaceMentionKeys(text, msg.Mentions), display, mentionedBot, mentionAll
}

// replaceMentionKeys rewrites Feishu mention keys (@_user_1) to @<name>.
// A key is matched as a whole token, so @_user_1 never rewrites part of
// @_user_10. Keys without a known name are kept as they are.
func replaceMentionKeys(text string, mentions []*larkim.MentionEvent) string {
	names := make(map[string]string, len(mentions))
	for _, mention := range mentions {
		if mention == nil || mention.Key == nil || mention.Name == nil {
			continue
		}
		key := *mention.Key
		name := strings.TrimSpace(*mention.Name)
		if len(key) < 2 || key[0] != '@' || name == "" {
			continue
		}
		names[key] = name
	}
	if len(names) == 0 || !strings.Contains(text, "@") {
		return text
	}
	var result strings.Builder
	result.Grow(len(text))
	for i := 0; i < len(text); {
		if text[i] == '@' {
			end := i + 1
			for end < len(text) && isMentionKeyByte(text[end]) {
				end++
			}
			if name, ok := names[text[i:end]]; ok {
				result.WriteByte('@')
				result.WriteString(name)
				i = end
				continue
			}
		}
		result.WriteByte(text[i])
		i++
	}
	return result.String()
}

func isMentionKeyByte(b byte) bool {
	return b == '_' || ('0' <= b && b <= '9') || ('a' <= b && b <= 'z') || ('A' <= b && b <= 'Z')
}
