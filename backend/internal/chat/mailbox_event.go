package chat

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
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

// AgentControlMailboxStore exposes the canonical write surface for mailbox
// rows that carry the AgentControl control envelope. Implementations may still
// mirror into compatibility session mailbox/event rows while callers depend on
// the AgentControl-specific contract.
type AgentControlMailboxStore interface {
	AppendAgentControlMailbox(ctx context.Context, sessionID string, message team.MailMessage) (runtimeevents.Event, int64, error)
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

// AgentControlMailboxReaderStore exposes durable reads for mailbox rows that
// carry the canonical AgentControl envelope metadata.
type AgentControlMailboxReaderStore interface {
	ListAgentControlMailbox(ctx context.Context, sessionID string, afterSeq int64, limit int) ([]team.MailMessage, error)
}

// AgentControlMailboxWatcherStore exposes wake notifications for AgentControl
// mailbox rows. Callers still need ListAgentControlMailbox for durable catch-up.
type AgentControlMailboxWatcherStore interface {
	WatchAgentControlMailbox(ctx context.Context, sessionID string) (<-chan team.MailMessage, func())
}

// AgentControlMailboxSequenceStore exposes the high-water mark for durable
// AgentControl mailbox rows.
type AgentControlMailboxSequenceStore interface {
	LastAgentControlMailboxSeq(ctx context.Context, sessionID string) (int64, error)
}

// IsAgentControlMailboxMessage reports whether a mailbox message carries the
// standard AgentControl envelope metadata.
func IsAgentControlMailboxMessage(message team.MailMessage) bool {
	return agentcontrol.HasEnvelopeMetadata(message.Metadata)
}

// MergeMailboxMessagesBySeq combines mailbox rows while preferring primary
// rows on duplicate durable sequence values. Results are sorted by seq and then
// id so callers can safely merge AgentControl rows with legacy mailbox rows.
func MergeMailboxMessagesBySeq(primary, secondary []team.MailMessage, limit int) []team.MailMessage {
	byKey := make(map[string]team.MailMessage, len(primary)+len(secondary))
	appendMessage := func(message team.MailMessage, overwrite bool) {
		key := mailboxMergeKey(message)
		if key == "" {
			return
		}
		if _, exists := byKey[key]; exists && !overwrite {
			return
		}
		byKey[key] = cloneTeamMailMessage(message)
	}
	for _, message := range secondary {
		appendMessage(message, false)
	}
	for _, message := range primary {
		appendMessage(message, true)
	}
	merged := make([]team.MailMessage, 0, len(byKey))
	for _, message := range byKey {
		merged = append(merged, message)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].Seq != merged[j].Seq {
			return merged[i].Seq < merged[j].Seq
		}
		return strings.TrimSpace(merged[i].ID) < strings.TrimSpace(merged[j].ID)
	})
	if limit > 0 && len(merged) > limit {
		merged = merged[:limit]
	}
	return merged
}

// ListMailboxAgentControlFirst reads mailbox rows with AgentControl control
// rows as the primary source and legacy session mailbox rows as compatibility
// rows. The returned hasRows flag is based on each substrate's durable
// high-water mark, so callers can distinguish "supported but no new rows" from
// "no mailbox substrate configured".
func ListMailboxAgentControlFirst(ctx context.Context, store interface{}, sessionID string, afterSeq int64, limit int) ([]team.MailMessage, bool, bool, error) {
	var messages []team.MailMessage
	supported := false
	hasRows := false
	if reader, ok := store.(AgentControlMailboxReaderStore); ok && reader != nil {
		controlMessages, err := reader.ListAgentControlMailbox(ctx, sessionID, afterSeq, limit)
		if err != nil {
			return nil, true, false, err
		}
		messages = controlMessages
		supported = true
		hasRows = len(controlMessages) > 0
		if sequencer, ok := store.(AgentControlMailboxSequenceStore); ok && sequencer != nil {
			seq, err := sequencer.LastAgentControlMailboxSeq(ctx, sessionID)
			if err != nil {
				return nil, true, false, err
			}
			hasRows = hasRows || seq > 0
		}
	}
	if reader, ok := store.(MailboxReaderStore); ok && reader != nil {
		legacyMessages, err := reader.ListMailbox(ctx, sessionID, afterSeq, limit)
		if err != nil {
			return nil, true, false, err
		}
		messages = MergeMailboxMessagesBySeq(messages, legacyMessages, limit)
		supported = true
		hasRows = hasRows || len(legacyMessages) > 0
		if !hasRows {
			if sequencer, ok := store.(MailboxSequenceStore); ok && sequencer != nil {
				seq, err := sequencer.LastMailboxSeq(ctx, sessionID)
				if err != nil {
					return nil, true, false, err
				}
				hasRows = seq > 0
			}
		}
	}
	if !supported {
		return nil, false, false, nil
	}
	return messages, true, hasRows, nil
}

// WatchMailboxAgentControlFirst subscribes to AgentControl mailbox wake events
// first and legacy mailbox wake events second. It de-duplicates by durable
// sequence/message id before forwarding notifications.
func WatchMailboxAgentControlFirst(ctx context.Context, store interface{}, sessionID string) (<-chan team.MailMessage, func(), bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	watchCtx, cancel := context.WithCancel(ctx)
	ch := make(chan team.MailMessage, 1)
	unsubscribes := make([]func(), 0, 2)
	seen := map[string]struct{}{}
	var seenMu sync.Mutex
	forward := func(source <-chan team.MailMessage, unwatch func()) {
		if source == nil {
			return
		}
		unsubscribes = append(unsubscribes, unwatch)
		go func() {
			for {
				select {
				case <-watchCtx.Done():
					return
				case message, ok := <-source:
					if !ok {
						return
					}
					key := mailboxMergeKey(message)
					if key != "" {
						seenMu.Lock()
						_, exists := seen[key]
						if !exists {
							seen[key] = struct{}{}
						}
						seenMu.Unlock()
						if exists {
							continue
						}
					}
					select {
					case ch <- cloneTeamMailMessage(message):
					default:
					}
				}
			}
		}()
	}
	if watcher, ok := store.(AgentControlMailboxWatcherStore); ok && watcher != nil {
		source, unwatch := watcher.WatchAgentControlMailbox(watchCtx, sessionID)
		forward(source, unwatch)
	}
	if watcher, ok := store.(MailboxWatcherStore); ok && watcher != nil {
		source, unwatch := watcher.WatchMailbox(watchCtx, sessionID)
		forward(source, unwatch)
	}
	if len(unsubscribes) == 0 {
		cancel()
		return ch, func() {}, false
	}
	return ch, func() {
		cancel()
		for _, unsubscribe := range unsubscribes {
			if unsubscribe != nil {
				unsubscribe()
			}
		}
	}, true
}

func mailboxMergeKey(message team.MailMessage) string {
	if message.Seq > 0 {
		return fmt.Sprintf("seq:%d", message.Seq)
	}
	if id := strings.TrimSpace(message.ID); id != "" {
		return "id:" + id
	}
	return ""
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

// AppendAgentControlMailbox stores a mailbox message through the AgentControl
// control-plane write surface when the wrapped store supports it.
func (s SessionEventMailboxStore) AppendAgentControlMailbox(ctx context.Context, sessionID string, message team.MailMessage) (runtimeevents.Event, int64, error) {
	if err := validateAgentControlMailboxMessage(message); err != nil {
		return runtimeevents.Event{}, 0, err
	}
	if store, ok := s.Events.(AgentControlMailboxStore); ok && store != nil {
		return store.AppendAgentControlMailbox(ctx, sessionID, message)
	}
	return s.AppendMailbox(ctx, sessionID, message)
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
		storedEvent, _, err := appendMailboxThroughPreferredStore(ctx, mailboxStore, sessionID, message)
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

func appendMailboxThroughPreferredStore(ctx context.Context, mailboxStore MailboxStore, sessionID string, message team.MailMessage) (runtimeevents.Event, int64, error) {
	if mailboxStore == nil {
		return runtimeevents.Event{}, 0, fmt.Errorf("mailbox store not configured")
	}
	if IsAgentControlMailboxMessage(message) {
		if controlStore, ok := mailboxStore.(AgentControlMailboxStore); ok && controlStore != nil {
			return controlStore.AppendAgentControlMailbox(ctx, sessionID, message)
		}
	}
	return mailboxStore.AppendMailbox(ctx, sessionID, message)
}

func validateAgentControlMailboxMessage(message team.MailMessage) error {
	if !IsAgentControlMailboxMessage(message) {
		return fmt.Errorf("agent control mailbox requires envelope metadata")
	}
	return nil
}
