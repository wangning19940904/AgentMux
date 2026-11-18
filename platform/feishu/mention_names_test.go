package feishu

import (
	"encoding/json"
	"testing"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

const (
	testBotMention    = `{"key":"@_user_1","name":"Work Agent","id":{"open_id":"ou_bot"},"mentioned_type":"app"}`
	testPersonMention = `{"key":"@_user_2","name":"王宁","id":{"open_id":"ou_person"},"mentioned_type":"user"}`
)

func TestInboundMessageTextResolvesMentionNames(t *testing.T) {
	for _, tc := range []struct {
		name, typ, content, mentions string
		wantText, wantDisplay        string
		wantBot, wantAll             bool
	}{
		{
			name: "text bot and person", typ: "text",
			content:  `{"text":"@_user_1 你今天到现在都干了啥 @_user_2"}`,
			mentions: `[` + testBotMention + `,` + testPersonMention + `]`,
			wantText: "@Work Agent 你今天到现在都干了啥 @王宁", wantDisplay: "你今天到现在都干了啥 @王宁", wantBot: true,
		},
		{
			name: "text adjacent and boundary", typ: "text",
			content:  `{"text":"@_user_2问@_user_10 和 @_user_1"}`,
			mentions: `[` + testBotMention + `,` + testPersonMention + `]`,
			wantText: "@王宁问@_user_10 和 @Work Agent", wantDisplay: "@王宁问@_user_10 和", wantBot: true,
		},
		{
			name: "text command keeps stripping bot", typ: "text",
			content:  `{"text":"@_user_1 /clear"}`,
			mentions: `[` + testBotMention + `]`,
			wantText: "/clear", wantDisplay: "/clear", wantBot: true,
		},
		{
			name: "text command addressed to person", typ: "text",
			content:  `{"text":"@_user_1 @_user_2 /clear"}`,
			mentions: `[` + testBotMention + `,` + testPersonMention + `]`,
			wantText: "@Work Agent @王宁 /clear", wantDisplay: "@王宁 /clear", wantBot: true,
		},
		{
			name: "text mention without name keeps key", typ: "text",
			content:  `{"text":"@_user_1 问 @_user_3"}`,
			mentions: `[` + testBotMention + `,{"key":"@_user_3","name":"  ","id":{"open_id":"ou_x"}}]`,
			wantText: "@Work Agent 问 @_user_3", wantDisplay: "问 @_user_3", wantBot: true,
		},
		{
			name: "text mention all", typ: "text",
			content:  `{"text":"@_all 开会"}`,
			mentions: `[{"key":"@_all","name":"所有人"}]`,
			wantText: "@所有人 开会", wantDisplay: "@所有人 开会", wantAll: true,
		},
		{
			name: "post keyed at elements", typ: "post",
			content:  `{"title":"","content":[[{"tag":"at","user_id":"@_user_1","user_name":""},{"tag":"text","text":" 看看 "},{"tag":"at","user_id":"@_user_2","user_name":""}]]}`,
			mentions: `[` + testBotMention + `,` + testPersonMention + `]`,
			wantText: "@Work Agent 看看 @王宁", wantDisplay: "看看 @王宁", wantBot: true,
		},
		{
			name: "post command with keyed bot", typ: "post",
			content:  `{"content":[[{"tag":"at","user_id":"@_user_1","user_name":""},{"tag":"text","text":" /new"}]]}`,
			mentions: `[` + testBotMention + `]`,
			wantText: "/new", wantDisplay: "/new", wantBot: true,
		},
		{
			name: "post named at elements", typ: "post",
			content:  `{"content":[[{"tag":"at","user_id":"ou_bot","user_name":"Work Agent"},{"tag":"text","text":" 问 "},{"tag":"at","user_id":"ou_person","user_name":"王宁"}]]}`,
			mentions: `[` + testBotMention + `,` + testPersonMention + `]`,
			wantText: "@Work Agent 问 @王宁", wantDisplay: "问 @王宁", wantBot: true,
		},
		{
			name: "post mention without name keeps key", typ: "post",
			content:  `{"content":[[{"tag":"text","text":"cc "},{"tag":"at","user_id":"@_user_3","user_name":""}]]}`,
			mentions: `[{"key":"@_user_3","id":{"open_id":"ou_x"}}]`,
			wantText: "cc @_user_3", wantDisplay: "cc @_user_3",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := &larkim.EventMessage{MessageType: &tc.typ, Content: &tc.content}
			if err := json.Unmarshal([]byte(tc.mentions), &msg.Mentions); err != nil {
				t.Fatal(err)
			}
			text, display, bot, all := inboundMessageText(msg, "ou_bot")
			if text != tc.wantText || display != tc.wantDisplay {
				t.Fatalf("text = %q, display = %q; want %q, %q", text, display, tc.wantText, tc.wantDisplay)
			}
			if bot != tc.wantBot || all != tc.wantAll {
				t.Fatalf("mentionedBot = %v, mentionAll = %v; want %v, %v", bot, all, tc.wantBot, tc.wantAll)
			}
		})
	}
}

func TestInboundMessageTextSkipsEmptyAndUnsupported(t *testing.T) {
	image, imageContent := "image", `{"image_key":"img_1"}`
	for _, msg := range []*larkim.EventMessage{
		nil,
		{},
		{MessageType: &image, Content: &imageContent},
	} {
		if text, display, _, _ := inboundMessageText(msg, "ou_bot"); text != "" || display != "" {
			t.Fatalf("text = %q, display = %q", text, display)
		}
	}
}

func TestReplaceMentionKeysWithoutMentions(t *testing.T) {
	const text = "给 user@example.com 发邮件 @_user_1"
	if got := replaceMentionKeys(text, nil); got != text {
		t.Fatalf("got %q", got)
	}
}
