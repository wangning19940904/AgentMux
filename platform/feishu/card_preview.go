package feishu

import (
	"strings"
	"unicode"
	"unicode/utf8"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/wangning19940904/AgentMux/core"
)

const cardPreviewQueryLimit = 50

// inboundCardPreviewText uses mention identities to remove only this bot.
// Agent input and command routing continue to use the original text.
func inboundCardPreviewText(msg *larkim.EventMessage, botOpenID, text string) string {
	var botIDs []string
	if botOpenID != "" {
		botIDs = append(botIDs, botOpenID)
	}
	for _, mention := range msg.Mentions {
		if !isBotMention(mention, botOpenID) {
			continue
		}
		if mention.Key != nil {
			text = removeCardPreviewMention(text, *mention.Key)
		}
		if mention.Id != nil {
			for _, id := range []*string{mention.Id.OpenId, mention.Id.UserId, mention.Id.UnionId} {
				if id != nil && *id != "" {
					botIDs = append(botIDs, *id)
				}
			}
		}
	}
	// Post mentions have explicit element boundaries, even when a name and
	// the following text are adjacent. Filter by ID before rendering them.
	if msg.MessageType != nil && *msg.MessageType == "post" && msg.Content != nil {
		return extractPostText(*msg.Content, botIDs...)
	}
	return text
}

func removeCardPreviewMention(text, mention string) string {
	if mention == "" {
		return text
	}
	var result strings.Builder
	for {
		index := strings.Index(text, mention)
		if index < 0 {
			result.WriteString(text)
			return result.String()
		}
		result.WriteString(text[:index])
		text = text[index+len(mention):]
		next, _ := utf8.DecodeRuneInString(text)
		if next <= unicode.MaxASCII && (unicode.IsLetter(next) || unicode.IsNumber(next) || next == '_') {
			result.WriteString(mention)
		} else {
			result.WriteByte(' ')
		}
	}
}

func replyCardSummary(msg *core.Message) string {
	text := msg.Text
	if msg.DisplayText != nil {
		text = *msg.DisplayText
	}
	query := normalizeCardPreviewQuery(text, len(msg.Images) > 0)
	characters := []rune(query)
	if len(characters) > cardPreviewQueryLimit {
		query = string(characters[:cardPreviewQueryLimit]) + "…"
	}
	return "回复：" + query
}

func normalizeCardPreviewQuery(text string, hasImages bool) string {
	query := strings.Join(strings.Fields(text), " ")
	// Rich-text messages use these placeholders for non-text elements.
	withoutMedia := strings.NewReplacer("[图片]", "", "[媒体]", "", "[文件]", "", "[附件]", "").Replace(query)
	mediaOnly := query != "" && strings.TrimSpace(withoutMedia) == ""
	if mediaOnly {
		if !strings.Contains(query, "[媒体]") && !strings.Contains(query, "[文件]") && !strings.Contains(query, "[附件]") {
			return "图片提问"
		}
		return "附件提问"
	}
	if query == "" {
		if hasImages {
			return "图片提问"
		}
		return "用户提问"
	}
	return query
}
