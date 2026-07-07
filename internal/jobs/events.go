package jobs

import (
	"encoding/json"
	"sync"
	"time"
)

type Event struct {
	Type      string `json:"type"`
	JobID     int64  `json:"jobId,omitempty"`
	Payload   any    `json:"payload,omitempty"`
	Timestamp string `json:"timestamp"`
}

type EventBus struct {
	mu          sync.Mutex
	subscribers map[chan Event]struct{}
}

// maxEventSubscribers bounds concurrent SSE subscribers so an unauthenticated
// client cannot exhaust memory/goroutines by opening unlimited long-lived
// /api/events connections.
const maxEventSubscribers = 64

func NewEventBus() *EventBus {
	return &EventBus{subscribers: make(map[chan Event]struct{})}
}

func (bus *EventBus) Subscribe() (chan Event, bool) {
	bus.mu.Lock()
	defer bus.mu.Unlock()
	if len(bus.subscribers) >= maxEventSubscribers {
		return nil, false
	}
	channel := make(chan Event, 16)
	bus.subscribers[channel] = struct{}{}
	return channel, true
}

func (bus *EventBus) Unsubscribe(channel chan Event) {
	bus.mu.Lock()
	delete(bus.subscribers, channel)
	close(channel)
	bus.mu.Unlock()
}

func (bus *EventBus) Publish(event Event) {
	if event.Timestamp == "" {
		event.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	bus.mu.Lock()
	defer bus.mu.Unlock()
	for channel := range bus.subscribers {
		select {
		case channel <- event:
		default:
		}
	}
}

func (event Event) MarshalSSE() []byte {
	payload, _ := json.Marshal(event)
	return append([]byte("data: "), append(payload, []byte("\n\n")...)...)
}
