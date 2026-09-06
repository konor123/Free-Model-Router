package usage

import (
	"sync"
	"time"
)

// EventType identifies a redacted usage lifecycle notification.
type EventType string

const (
	EventRequestStarted EventType = "request_started"
	EventAttemptStarted EventType = "attempt_started"
	EventCompleted      EventType = "completed"
)

// Event contains operational usage metadata only. It has no prompt, response,
// authorization header, or secret representation.
type Event struct {
	Type      EventType      `json:"type"`
	At        time.Time      `json:"at"`
	RequestID string         `json:"requestId,omitempty"`
	Attempt   *AttemptRecord `json:"attempt,omitempty"`
	Record    *RequestRecord `json:"record,omitempty"`
}

// EventPublisher is the optional gateway-to-UI live event boundary.
type EventPublisher interface {
	Publish(Event)
}

// EventSubscriber is the control-plane boundary used by the SSE transport.
type EventSubscriber interface {
	Subscribe() (<-chan Event, func())
}

// Broadcaster delivers bounded, non-blocking live events to subscribers.
type Broadcaster struct {
	mu     sync.Mutex
	nextID uint64
	buffer int
	subs   map[uint64]chan Event
}

func NewBroadcaster(buffer int) *Broadcaster {
	if buffer <= 0 {
		buffer = 16
	}
	return &Broadcaster{buffer: buffer, subs: make(map[uint64]chan Event)}
}

func (b *Broadcaster) Publish(event Event) {
	if b == nil {
		return
	}
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, subscriber := range b.subs {
		select {
		case subscriber <- cloneEvent(event):
		default:
			// A slow desktop must not block inference or the control listener.
		}
	}
}

func (b *Broadcaster) Subscribe() (<-chan Event, func()) {
	if b == nil {
		closed := make(chan Event)
		close(closed)
		return closed, func() {}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextID++
	id := b.nextID
	channel := make(chan Event, b.buffer)
	b.subs[id] = channel
	var once sync.Once
	return channel, func() {
		once.Do(func() {
			b.mu.Lock()
			if current, ok := b.subs[id]; ok {
				delete(b.subs, id)
				close(current)
			}
			b.mu.Unlock()
		})
	}
}

func cloneEvent(event Event) Event {
	clone := event
	if event.Attempt != nil {
		attempt := *event.Attempt
		clone.Attempt = &attempt
	}
	if event.Record != nil {
		record := cloneRecord(*event.Record)
		clone.Record = &record
	}
	return clone
}
