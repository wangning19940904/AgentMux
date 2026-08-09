package core

import "testing"

func TestMeetingEventSubscriptionPreservesEventsAndUnsubscribes(t *testing.T) {
	engine := NewEngine(nil, nil)
	updates, unsubscribe := engine.SubscribeMeetingEvents()

	engine.publishMeetingEvent("channel-1")
	engine.publishMeetingEvent("channel-1")
	if len(updates) != 2 {
		t.Fatalf("buffered updates = %d, want both notifications", len(updates))
	}
	event := <-updates
	if event.ChannelID != "channel-1" || event.CreatedAt.IsZero() {
		t.Fatalf("event = %+v", event)
	}
	<-updates

	unsubscribe()
	engine.publishMeetingEvent("channel-2")
	select {
	case event := <-updates:
		t.Fatalf("received event after unsubscribe: %+v", event)
	default:
	}
}
