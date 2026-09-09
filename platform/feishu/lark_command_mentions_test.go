package feishu

import (
	"encoding/json"
	"testing"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func TestBotMentionCommands(t *testing.T) {
	for _, tc := range []struct {
		name, typ, content, mentions, want string
	}{
		{"clear", "text", `{"text":"@_user_1 /clear"}`, `[{"key":"@_user_1","id":{"open_id":"ou_bot"}}]`, "/clear"},
		{"stop", "text", `{"text":"@_user_1 停止"}`, `[{"key":"@_user_1","id":{"open_id":"ou_bot"}}]`, "停止"},
		{"settings", "text", `{"text":"@_user_1　/model gpt-5"}`, `[{"key":"@_user_1","id":{"open_id":"ou_bot"}}]`, "/model gpt-5"},
		{"repeated", "text", `{"text":"@_user_1 @_user_1 /reset"}`, `[{"key":"@_user_1","id":{"open_id":"ou_bot"}}]`, "/reset"},
		{"other recipient", "text", `{"text":"@_user_2 /clear"}`, `[{"key":"@_user_2","id":{"open_id":"ou_person"}}]`, "@_user_2 /clear"},
		{"other bot", "text", `{"text":"@_user_2 /clear"}`, `[{"key":"@_user_2","id":{"open_id":"ou_other"},"mentioned_type":"app"}]`, "@_user_2 /clear"},
		{"keep human mention", "text", `{"text":"@_user_1 @_user_2 /clear"}`, `[{"key":"@_user_1","id":{"open_id":"ou_bot"}},{"key":"@_user_2","id":{"open_id":"ou_person"}}]`, "@_user_1 @_user_2 /clear"},
		{"ordinary prompt", "text", `{"text":"@_user_1 explain /clear"}`, `[{"key":"@_user_1","id":{"open_id":"ou_bot"}}]`, "@_user_1 explain /clear"},
		{"no metadata", "text", `{"text":"@包包 /clear"}`, `[]`, "@包包 /clear"},
		{"key boundary", "text", `{"text":"@_user_10 /clear"}`, `[{"key":"@_user_1","id":{"open_id":"ou_bot"}}]`, "@_user_10 /clear"},
		{"post", "post", `{"content":[[{"tag":"at","user_id":"ou_bot","user_name":"包包"},{"tag":"text","text":" /clear"}]]}`, `[{"key":"@_user_1","name":"包包","id":{"open_id":"ou_bot"}}]`, "/clear"},
		{"post adjacent", "post", `{"content":[[{"tag":"at","user_id":"ou_bot","user_name":"My Bot"},{"tag":"text","text":"/new"}]]}`, `[{"key":"@_user_1","name":"My Bot","id":{"open_id":"ou_bot"}}]`, "/new"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := &larkim.EventMessage{MessageType: &tc.typ}
			if err := json.Unmarshal([]byte(tc.mentions), &msg.Mentions); err != nil {
				t.Fatal(err)
			}
			text := extractText(tc.typ, tc.content)
			before, allBefore := mentionState(msg, "ou_bot", text)
			got := stripBotCommandMention(msg, "ou_bot", text)
			if got != tc.want {
				t.Fatalf("command text = %q, want %q", got, tc.want)
			}
			after, allAfter := mentionState(msg, "ou_bot", got)
			if before != after || allBefore != allAfter {
				t.Fatal("command normalization changed mention routing")
			}
		})
	}
}
