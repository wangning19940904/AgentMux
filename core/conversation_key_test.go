package core

import "testing"

func TestResolveConversationKey(t *testing.T) {
	tests := []struct {
		name string
		msg  Message
		want string
	}{
		{"dm", Message{ChatID: "oc_dm", ChatType: "p2p", ID: "om_1", Platform: "feishu"}, "chat:oc_dm"},
		{"thread", Message{ChatID: "oc_group", ChatType: "group", ThreadID: "omt_1", RootID: "om_root"}, "thread:omt_1"},
		{"root", Message{ChatID: "oc_group", ChatType: "group", RootID: "om_root"}, "root:om_root"},
		{"feishu top level", Message{ChatID: "oc_group", ChatType: "group", ID: "om_top", Platform: "feishu"}, "root:om_top"},
		{"classic group fallback", Message{ChatID: "group-1", ChatType: "group", ID: "m1", Platform: "slack"}, "chat:group-1"},
		{"callback explicit", Message{ChatID: "oc_group", ConversationKey: "root:om_original"}, "root:om_original"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ResolveConversationKey(&test.msg); got != test.want {
				t.Fatalf("ResolveConversationKey() = %q, want %q", got, test.want)
			}
		})
	}
}
