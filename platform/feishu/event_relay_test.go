package feishu

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

func TestRelayCapturesRawNonTextBeforeChatFilters(t *testing.T) {
	var events []core.RelayEvent
	c := &larkClient{platform: "feishu", appID: "app", botOpenID: "bot", eventIngress: core.PlatformEventIngress{Publish: func(_ context.Context, e core.RelayEvent) error { events = append(events, e); return nil }}}
	inbound := make(chan *core.Message, 1)
	handler := c.eventDispatcher(context.Background(), "project", inbound, []string{"drive.file.bitable_record_changed_v1"})
	bodies := []string{
		`{"schema":"2.0","header":{"event_type":"im.message.receive_v1","event_id":"m1","token":"auth-secret"},"event":{"message":{"message_type":"image","content":"{}","chat_id":"chat"}}}`,
		`{"schema":"2.0","header":{"event_type":"drive.file.bitable_record_changed_v1","event_id":"b1","token":"auth-secret"},"event":{"file_token":"file","table_id":"table","revision":2}}`,
	}
	for _, body := range bodies {
		if _, err := handler.Do(context.Background(), []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if len(events) != 2 || strings.Contains(string(events[0].Data), "auth-secret") || events[1].Attributes["file_token"] != "file" {
		t.Fatalf("events=%+v", events)
	}
	if events[0].Attributes["chat_id"] != "chat" || events[1].SourceEventID != "b1" {
		t.Fatal("missing routing fields")
	}
}
func TestRelayFailureRetriesNotificationsButPreservesCardResponse(t *testing.T) {
	c := &larkClient{platform: "feishu", appID: "app", botOpenID: "bot", eventIngress: core.PlatformEventIngress{Publish: func(ctx context.Context, _ core.RelayEvent) error {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 500*time.Millisecond {
			t.Error("unbounded ingress")
		}
		return errors.New("store unavailable")
	}}}
	handler := c.eventDispatcher(context.Background(), "project", make(chan *core.Message, 1), []string{"drive.file.bitable_record_changed_v1"})
	notification := []byte(`{"schema":"2.0","header":{"event_type":"drive.file.bitable_record_changed_v1","event_id":"id"},"event":{}}`)
	if _, err := handler.Do(context.Background(), notification); err == nil {
		t.Fatal("notification persistence failure was acknowledged")
	}
	card := []byte(`{"schema":"2.0","header":{"event_type":"card.action.trigger","event_id":"card"},"event":{"token":"card-secret","action":{"value":{}}}}`)
	if _, err := handler.Do(context.Background(), card); err != nil {
		t.Fatal("persistence failure broke synchronous card response", err)
	}
}
func TestCardCredentialRemovedAndUnknownTypeRegistration(t *testing.T) {
	var captured core.RelayEvent
	c := &larkClient{botOpenID: "bot", eventIngress: core.PlatformEventIngress{Publish: func(_ context.Context, e core.RelayEvent) error { captured = e; return nil }}}
	handler := c.eventDispatcher(context.Background(), "p", make(chan *core.Message, 1), []string{"custom.change_v1", "im.message.receive_v1", "card.action.trigger"})
	_, err := handler.Do(context.Background(), []byte(`{"schema":"2.0","header":{"event_type":"card.action.trigger"},"event":{"token":"secret","action":{"value":{}}}}`))
	if err != nil || strings.Contains(string(captured.Data), "secret") {
		t.Fatalf("card secret: %s %v", captured.Data, err)
	}
	_, err = handler.Do(context.Background(), []byte(`{"schema":"2.0","header":{"event_type":"custom.change_v1"},"event":{"table_id":"t"}}`))
	if err != nil || captured.Type != "custom.change_v1" {
		t.Fatal("custom registration", err)
	}
}
