package usage

import (
	"testing"
	"time"
)

func TestBroadcasterPublishesAndUnsubscribes(t *testing.T) {
	broadcaster := NewBroadcaster(2)
	events, unsubscribe := broadcaster.Subscribe()
	defer unsubscribe()

	broadcaster.Publish(Event{Type: EventRequestStarted, RequestID: "request-1", At: time.Now().UTC()})
	select {
	case event := <-events:
		if event.Type != EventRequestStarted || event.RequestID != "request-1" {
			t.Fatalf("event = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for usage event")
	}
	unsubscribe()
	broadcaster.Publish(Event{Type: EventRequestStarted, RequestID: "request-2"})
}

func TestStorePublishesRedactedCompletionEvent(t *testing.T) {
	store := NewMemory()
	events, unsubscribe := store.Subscribe()
	defer unsubscribe()

	if err := store.Append(RequestRecord{ID: "request-2", Provider: "opencode", Result: ResultSuccess}); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-events:
		if event.Type != EventCompleted || event.Record == nil || event.Record.ID != "request-2" {
			t.Fatalf("event = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for completion event")
	}
}
