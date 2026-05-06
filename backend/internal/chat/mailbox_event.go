package chat

import (
	"context"
	"fmt"
	"strings"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

// MailboxActorFallback is the optional live-actor delivery path used when a
// mailbox event cannot be written directly to the runtime event store.
type MailboxActorFallback func(ctx context.Context, sessionID string, message team.MailMessage) error

// MailboxStore is the durable mailbox substrate used by agent collaboration.
// The current implementation can be backed by the session runtime event store;
// a future AgentControl mailbox table can implement this interface directly.
type MailboxStore interface {
	AppendMailbox(ctx context.Context, sessionID string, message team.MailMessage) (runtimeevents.Event, int64, error)
}

// MailboxReaderStore exposes durable mailbox reads by per-session mailbox seq.
type MailboxReaderStore interface {
	ListMailbox(ctx context.Context, sessionID string, afterSeq int64, limit int) ([]team.MailMessage, error)
}

// MailboxWatcherStore exposes in-process wake notifications for mailbox rows.
// Callers must still use ListMailbox with AfterSeq for durable catch-up.
type MailboxWatcherStore interface {
	WatchMailbox(ctx context.Context, sessionID string) (<-chan team.MailMessage, func())
}

// MailboxSequenceStore exposes the durable high-water mark for mailbox rows.
type MailboxSequenceStore interface {
	LastMailboxSeq(ctx context.Context, sessionID string) (int64, error)
}

// SessionEventMailboxStore adapts the existing session runtime event store into
// the mailbox substrate interface.
type SessionEventMailboxStore struct {
	Events EventStore
}

// NewSessionEventMailboxStore returns a mailbox store backed by session events.
func NewSessionEventMailboxStore(eventStore EventStore) MailboxStore {
	if eventStore == nil {
		return nil
	}
	if mailboxStore, ok := eventStore.(MailboxStore); ok {
		return mailboxStore
	}
	return SessionEventMailboxStore{Events: eventStore}
}

// AppendMailbox stores a mailbox message as a mailbox_received session event.
func (s SessionEventMailboxStore) AppendMailbox(ctx context.Context, sessionID string, message team.MailMessage) (runtimeevents.Event, int64, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return runtimeevents.Event{}, 0, fmt.Errorf("session id is required")
	}
	if s.Events == nil {
		return runtimeevents.Event{}, 0, fmt.Errorf("session event store not configured")
	}
	runtimeEvent := NewMailboxReceivedEvent(sessionID, message)
	appendCtx := ctx
	if appendCtx == nil {
		appendCtx = context.Background()
	}
	seq, err := s.Events.AppendEvent(appendCtx, runtimeEvent)
	if err != nil {
		return runtimeEvent, 0, err
	}
	if runtimeEvent.Payload == nil {
		runtimeEvent.Payload = map[string]interface{}{}
	}
	runtimeEvent.Payload["seq"] = seq
	return runtimeEvent, seq, nil
}

// NewMailboxReceivedEvent builds the runtime event emitted when a session
// receives an agent/team mailbox message.
func NewMailboxReceivedEvent(sessionID string, message team.MailMessage) runtimeevents.Event {
	payload := map[string]interface{}{
		"team_id":    strings.TrimSpace(message.TeamID),
		"message_id": strings.TrimSpace(message.ID),
		"from_agent": strings.TrimSpace(message.FromAgent),
		"to_agent":   strings.TrimSpace(message.ToAgent),
		"kind":       strings.TrimSpace(message.Kind),
		"body":       strings.TrimSpace(message.Body),
	}
	if payload["to_agent"] == "" {
		payload["to_agent"] = "*"
	}
	if payload["kind"] == "" {
		payload["kind"] = "info"
	}
	if message.TaskID != nil && strings.TrimSpace(*message.TaskID) != "" {
		payload["task_id"] = strings.TrimSpace(*message.TaskID)
	}
	if message.Seq > 0 {
		payload["mailbox_seq"] = message.Seq
	}
	if !message.CreatedAt.IsZero() {
		payload["created_at"] = message.CreatedAt.UTC()
	}
	if len(message.Metadata) > 0 {
		metadata := make(map[string]interface{}, len(message.Metadata))
		for key, value := range message.Metadata {
			metadata[key] = value
		}
		payload["metadata"] = metadata
	}
	return runtimeevents.Event{
		Type:      EventMailboxReceived,
		SessionID: strings.TrimSpace(sessionID),
		Payload:   payload,
		Timestamp: time.Now().UTC(),
	}
}

// DeliverMailboxEventFirst records a mailbox message as a session event before
// notifying the runtime bus. It only falls back to live actor delivery when the
// event store path is unavailable or fails, keeping mailbox delivery durable by
// default while still supporting older in-memory-only hosts.
func DeliverMailboxEventFirst(ctx context.Context, eventStore EventStore, eventBus runtimeevents.Publisher, fallback MailboxActorFallback, sessionID string, message team.MailMessage) error {
	return DeliverMailboxStoreFirst(ctx, NewSessionEventMailboxStore(eventStore), eventBus, fallback, sessionID, message)
}

// DeliverMailboxStoreFirst records a mailbox message through the mailbox store
// before notifying the runtime bus. It only falls back to live actor delivery
// when the durable mailbox path is unavailable or fails.
func DeliverMailboxStoreFirst(ctx context.Context, mailboxStore MailboxStore, eventBus runtimeevents.Publisher, fallback MailboxActorFallback, sessionID string, message team.MailMessage) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("session id is required")
	}
	runtimeEvent := NewMailboxReceivedEvent(sessionID, message)
	if mailboxStore != nil {
		storedEvent, _, err := mailboxStore.AppendMailbox(ctx, sessionID, message)
		if err == nil {
			runtimeEvent = storedEvent
			if eventBus != nil {
				eventBus.Publish(runtimeEvent)
			}
			return nil
		}
		if fallback == nil {
			return err
		}
	}
	if fallback != nil {
		if ctx == nil {
			ctx = context.Background()
		}
		if err := fallback(ctx, sessionID, message); err != nil {
			return err
		}
		return nil
	}
	if eventBus != nil {
		eventBus.Publish(runtimeEvent)
		return nil
	}
	return fmt.Errorf("mailbox delivery is not configured")
}
