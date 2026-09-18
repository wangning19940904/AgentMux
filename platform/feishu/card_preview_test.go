package feishu

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/wangning19940904/AgentMux/core"
)

func TestReplyCardSummary(t *testing.T) {
	for _, tc := range []struct {
		name, text, want string
		images           [][]byte
	}{
		{name: "question", text: "记账打车32", want: "回复：记账打车32"},
		{name: "whitespace", text: " \n记账\t打车　32\r\n 元  ", want: "回复：记账 打车 32 元"},
		{name: "limit", text: strings.Repeat("问", 50), want: "回复：" + strings.Repeat("问", 50)},
		{name: "unicode truncation", text: strings.Repeat("问", 49) + "🚕32元", want: "回复：" + strings.Repeat("问", 49) + "🚕…"},
		{name: "empty", text: " \n ", want: "回复：用户提问"},
		{name: "image", images: [][]byte{{1}}, want: "回复：图片提问"},
		{name: "post images", text: "[图片][图片]\n[图片]", want: "回复：图片提问"},
		{name: "attachment", text: "[文件]", want: "回复：附件提问"},
		{name: "mixed media", text: "[图片] [附件]", want: "回复：附件提问"},
		{name: "question with image", text: "解释这张图 [图片]", want: "回复：解释这张图 [图片]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := replyCardSummary(&core.Message{Text: tc.text, Images: tc.images})
			if got != tc.want || !utf8.ValidString(got) {
				t.Fatalf("summary = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestInboundCardPreviewRemovesOnlyBotMentions(t *testing.T) {
	for _, tc := range []struct {
		name, typ, content, mentions, botID, want string
	}{
		{"bot and person", "text", `{"text":"@_user_1 问 @_user_2 打车 32"}`, `[{"key":"@_user_1","id":{"open_id":"ou_bot"}},{"key":"@_user_2","id":{"open_id":"ou_person"}}]`, "ou_bot", "回复：问 @_user_2 打车 32"},
		{"mention anywhere", "text", `{"text":"帮忙 @_user_1 记账 @_user_1"}`, `[{"key":"@_user_1","id":{"open_id":"ou_bot"}}]`, "ou_bot", "回复：帮忙 记账"},
		{"key boundary", "text", `{"text":"@_user_10 @_user_1 提问"}`, `[{"key":"@_user_1","id":{"open_id":"ou_bot"}}]`, "ou_bot", "回复：@_user_10 提问"},
		{"post name", "post", `{"content":[[{"tag":"at","user_id":"ou_bot","user_name":"My Bot"},{"tag":"text","text":" 记账打车32"}]]}`, `[{"key":"@_user_1","name":"My Bot","id":{"open_id":"ou_bot"}}]`, "ou_bot", "回复：记账打车32"},
		{"post adjacent names", "post", `{"zh_cn":{"content":[[{"tag":"at","user_id":"ou_bot","user_name":"包包"},{"tag":"text","text":"记账 "},{"tag":"at","user_id":"ou_person","user_name":"包包助手"}]]}}`, `[]`, "ou_bot", "回复：记账 @包包助手"},
		{"text adjacent", "text", `{"text":"@_user_1记账"}`, `[{"key":"@_user_1","id":{"open_id":"ou_bot"}}]`, "ou_bot", "回复：记账"},
		{"mention only", "text", `{"text":"@_user_1"}`, `[{"key":"@_user_1","id":{"open_id":"ou_bot"}}]`, "ou_bot", "回复：用户提问"},
		{"missing bot id", "text", `{"text":"@_user_1 记账"}`, `[{"key":"@_user_1","mentioned_type":"app"}]`, "", "回复：记账"},
		{"literal at", "text", `{"text":"给 user@example.com 发邮件 @小王"}`, `[]`, "ou_bot", "回复：给 user@example.com 发邮件 @小王"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := &larkim.EventMessage{MessageType: &tc.typ, Content: &tc.content}
			if err := json.Unmarshal([]byte(tc.mentions), &msg.Mentions); err != nil {
				t.Fatal(err)
			}
			text := extractText(tc.typ, tc.content)
			preview := inboundCardPreviewText(msg, tc.botID, text)
			got := replyCardSummary(&core.Message{Text: text, DisplayText: &preview})
			if got != tc.want {
				t.Fatalf("summary = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestReplyCardsKeepQuestionPreviewAcrossStates(t *testing.T) {
	for _, taskID := range []string{"", "task-1"} {
		platform := &Platform{client: &cardImageTestClient{}}
		question := "记账打车32"
		msg := &core.Message{ChatID: "oc_1", Text: "internal agent prompt", DisplayText: &question}
		var reply core.ReplyStream
		var err error
		if taskID == "" {
			reply, err = platform.BeginReply(context.Background(), msg)
		} else {
			reply, err = platform.BeginFeedbackTaskReply(context.Background(), msg, core.ChannelTask{ID: taskID, FeedbackNonce: "nonce"})
		}
		if err != nil {
			t.Fatal(err)
		}
		stream := reply.(*cardStream)
		for _, state := range []struct{ done, failed bool }{{false, false}, {true, false}, {true, true}} {
			for name, card := range map[string]string{
				"native": buildStreamCardJSON("answer", state.done, state.failed, stream.control),
				"legacy": buildCardWithImages("answer", state.done, state.failed, nil, stream.control),
			} {
				var parsed struct {
					Config struct{ Summary struct{ Content string } }
				}
				if err := json.Unmarshal([]byte(card), &parsed); err != nil {
					t.Fatal(err)
				}
				if parsed.Config.Summary.Content != "回复：记账打车32" {
					t.Fatalf("%s summary = %q", name, parsed.Config.Summary.Content)
				}
				if taskID == "" && strings.Contains(card, "agentmux_action") {
					t.Fatalf("%s plain reply acquired task buttons: %s", name, card)
				}
			}
		}
		_ = reply.Close(context.Background())
	}
}
