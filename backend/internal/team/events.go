package team

import (
	"sync"
	"time"
)

// TeamEvent describes a team-level lifecycle event.
type TeamEvent struct {
	Type      string                 `json:"type"`
	TeamID    string                 `json:"team_id"`
	Payload   map[string]interface{} `json:"payload,omitempty"`
	Timestamp time.Time              `json:"timestamp"`
}

// TeamEventRecord represents a persisted team event with sequence metadata.
type TeamEventRecord struct {
	Seq int64 `json:"seq"`
	TeamEvent
}

// TeamEventFilter describes a query for persisted team events.
type TeamEventFilter struct {
	TeamID    string
	AfterSeq  int64
	Limit     int
	EventType string
	Since     *time.Time
	Until     *time.Time
}

// TeamEventHandler consumes a TeamEvent.
type TeamEventHandler func(TeamEvent)

// TeamEventBus is a lightweight event bus for team events.
type TeamEventBus struct {
	mu          sync.RWMutex
	subscribers map[string][]TeamEventHandler
	all         []TeamEventHandler
}

// NewTeamEventBus creates a new event bus.
func NewTeamEventBus() *TeamEventBus {
	return &TeamEventBus{
		subscribers: make(map[string][]TeamEventHandler),
		all:         make([]TeamEventHandler, 0),
	}
}

// Subscribe registers a handler for an event type. Empty event type subscribes to all.
func (b *TeamEventBus) Subscribe(eventType string, handler TeamEventHandler) {
	if b == nil || handler == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	if eventType == "" {
		b.all = append(b.all, handler)
		return
	}
	b.subscribers[eventType] = append(b.subscribers[eventType], handler)
}

// Publish broadcasts a team event to subscribers.
func (b *TeamEventBus) Publish(event TeamEvent) {
	if b == nil {
		return
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}

	b.mu.RLock()
	all := append([]TeamEventHandler(nil), b.all...)
	typed := append([]TeamEventHandler(nil), b.subscribers[event.Type]...)
	b.mu.RUnlock()

	for _, handler := range all {
		handler(event)
	}
	for _, handler := range typed {
		handler(event)
	}
}
