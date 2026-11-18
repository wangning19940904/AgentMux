package superagent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/wangning19940904/AgentMux/core"
)

func feishuTrigger(actor, channelID, chatID string) core.SuperAgentTrigger {
	reply, _ := json.Marshal(feishuReply{ChannelID: channelID, ChatID: chatID})
	return core.SuperAgentTrigger{Actor: actor, Reply: reply}
}

func TestNameTriggerUsesContactsAndCaches(t *testing.T) {
	api, calls := fakeFeishuDirectory(t, false)
	service := New(Options{Directory: api})
	trigger := feishuTrigger("ou_a", "c", "oc_1")
	service.nameTrigger(context.Background(), &trigger)
	if trigger.ActorName != "Name of ou_a" || trigger.ActorAvatar != "https://img.example/240/ou_a" {
		t.Fatalf("a Feishu sender is named with their portrait: %+v", trigger)
	}
	before := len(calls())
	again := feishuTrigger("ou_a", "c", "oc_2")
	service.nameTrigger(context.Background(), &again)
	if again.ActorName != "Name of ou_a" || len(calls()) != before {
		t.Fatalf("a second message from the same person is named from cache: %+v, %d calls after %d", again, len(calls()), before)
	}

	named := feishuTrigger("ou_b", "c", "oc_1")
	named.ActorName, named.ActorAvatar = "Bob B.", "https://img.example/own"
	service.nameTrigger(context.Background(), &named)
	if named.ActorName != "Bob B." || named.ActorAvatar != "https://img.example/own" {
		t.Fatalf("a name the platform gave is kept: %+v", named)
	}
	other := core.SuperAgentTrigger{Actor: "someone@acme.com"}
	service.nameTrigger(context.Background(), &other)
	if other.ActorName != "" {
		t.Fatalf("only Feishu open_ids are looked up: %+v", other)
	}
}

func TestNameTriggerFallsBackToGroupMembers(t *testing.T) {
	api, _ := fakeFeishuDirectory(t, true)
	service := New(Options{Directory: api})
	trigger := feishuTrigger("ou_a", "c", "oc_1")
	service.nameTrigger(context.Background(), &trigger)
	if trigger.ActorName != "Alice" || trigger.ActorAvatar != "" {
		t.Fatalf("without the contact scope the group's member list names the sender: %+v", trigger)
	}
	stranger := feishuTrigger("ou_z", "c", "oc_1")
	service.nameTrigger(context.Background(), &stranger)
	if stranger.ActorName != "" {
		t.Fatalf("a person nobody can name stays unnamed: %+v", stranger)
	}
}

func TestNameTriggersFillsStoredHistory(t *testing.T) {
	api, _ := fakeFeishuDirectory(t, false)
	service := New(Options{Directory: api})
	triggers := []core.SuperAgentTrigger{
		feishuTrigger("ou_a", "c", "oc_1"),
		feishuTrigger("ou_b", "c", "oc_1"),
		{Actor: "console"},
	}
	triggers[1].ActorName = "ou_b"
	service.nameTriggers(context.Background(), triggers)
	if triggers[0].ActorName != "Name of ou_a" || triggers[1].ActorName != "Name of ou_b" || triggers[1].ActorAvatar == "" || triggers[2].ActorName != "" {
		t.Fatalf("stored triggers are named for the Console: %+v", triggers)
	}
}

func TestValidAvatar(t *testing.T) {
	for _, avatar := range []string{"", "preset:fox", "preset:rocket-2", "data:image/png;base64,iVBORw0KGgo="} {
		if err := validAvatar(avatar); err != nil {
			t.Errorf("validAvatar(%q) = %v", avatar, err)
		}
	}
	for _, avatar := range []string{"preset:", "preset:Fox!", "https://example.com/a.png", "data:text/html;base64,PGI+", "data:image/png;base64,***", "data:image/png;base64," + strings.Repeat("A", maxAvatarBytes*2)} {
		if err := validAvatar(avatar); err == nil {
			t.Errorf("validAvatar(%.40q) should fail", avatar)
		}
	}
}
