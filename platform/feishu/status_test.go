package feishu

import (
	"encoding/json"
	"testing"
)

func TestStatusCardIsSharedAndFollowsTheWork(t *testing.T) {
	for _, tc := range []struct {
		done, failed    bool
		template, title string
	}{
		{false, false, "blue", "AgentMux · 进行中"},
		{true, false, "green", "AgentMux · 已结束"},
		{true, true, "red", "AgentMux · 未完成"},
	} {
		var card struct {
			Config map[string]any `json:"config"`
			Header struct {
				Template string `json:"template"`
				Title    struct {
					Content string `json:"content"`
				} `json:"title"`
			} `json:"header"`
			Elements []map[string]any `json:"elements"`
		}
		if err := json.Unmarshal([]byte(buildStatusCard("on it\n\n⏳ 进行中", tc.done, tc.failed)), &card); err != nil {
			t.Fatal(err)
		}
		// Feishu patches a card for everyone only when it is shared.
		if card.Config["update_multi"] != true {
			t.Fatalf("config = %v", card.Config)
		}
		if card.Header.Template != tc.template || card.Header.Title.Content != tc.title {
			t.Fatalf("done=%v failed=%v: header = %+v", tc.done, tc.failed, card.Header)
		}
		if len(card.Elements) != 1 || card.Elements[0]["content"] != "on it\n\n⏳ 进行中" {
			t.Fatalf("elements = %v", card.Elements)
		}
	}
}
