package chat

import (
	"context"
	"errors"
	"testing"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

type failingMailboxEventStore struct {
	err error
}

func (s failingMailboxEventStore) AppendEvent(context.Context, runtimeevents.Event) (int64, error) {
	if s.err != nil {
		return 0, s.err
	}
	return 0, errors.New("append failed")
}

func (s failingMailboxEventStore) ListEvents(context.Context, string, int64, int) ([]runtimeevents.Event, error) {
	return nil, nil
}

func TestDeliverMailboxEventFirstPersistsBeforePublishing(t *testing.T) {
	store := NewInMemoryRuntimeStore(8)
	bus := runtimeevents.NewBusWithRetention(8)

	if err := DeliverMailboxEventFirst(context.Background(), store, bus, nil, "session-1", team.MailMessage{
		ID:        "mail-1",
		FromAgent: "parent",
		ToAgent:   "session-1",
		Kind:      "agent_message",
		Body:      "hello",
	}); err != nil {
		t.Fatalf("DeliverMailboxEventFirst: %v", err)
	}

	events, err := store.ListEvents(context.Background(), "session-1", 0, 10)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected persisted event, got %#v", events)
	}
	if events[0].Type != EventMailboxReceived || events[0].Payload["seq"] != int64(1) {
		t.Fatalf("unexpected persisted mailbox event: %#v", events[0])
	}

	recent := bus.Recent(10)
	if len(recent) != 1 {
		t.Fatalf("expected published event, got %#v", recent)
	}
	if recent[0].Payload["seq"] != int64(1) {
		t.Fatalf("expected published event to include durable seq, got %#v", recent[0])
	}
}

func TestDeliverMailboxStoreFirstUsesMailboxStoreSubstrate(t *testing.T) {
	store := NewInMemoryRuntimeStore(8)
	bus := runtimeevents.NewBusWithRetention(8)

	if err := DeliverMailboxStoreFirst(context.Background(), NewSessionEventMailboxStore(store), bus, nil, "session-1", team.MailMessage{
		ID:        "mail-1",
		FromAgent: "parent",
		ToAgent:   "session-1",
		Kind:      "agent_message",
		Body:      "hello substrate",
	}); err != nil {
		t.Fatalf("DeliverMailboxStoreFirst: %v", err)
	}

	events, err := store.ListEvents(context.Background(), "session-1", 0, 10)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 || events[0].Type != EventMailboxReceived || events[0].Payload["body"] != "hello substrate" {
		t.Fatalf("unexpected persisted mailbox event: %#v", events)
	}
	recent := bus.Recent(10)
	if len(recent) != 1 || recent[0].Payload["seq"] != int64(1) {
		t.Fatalf("unexpected published mailbox event: %#v", recent)
	}
}

func TestNewSessionEventMailboxStorePrefersNativeMailboxStore(t *testing.T) {
	store := NewInMemoryRuntimeStore(8)
	mailboxStore := NewSessionEventMailboxStore(store)
	if mailboxStore == nil {
		t.Fatal("expected mailbox store")
	}
	if mailboxStore != store {
		t.Fatalf("expected native mailbox store, got %T", mailboxStore)
	}
}

func TestDeliverMailboxEventFirstFallsBackWhenStoreFails(t *testing.T) {
	storeErr := errors.New("store down")
	called := false

	err := DeliverMailboxEventFirst(context.Background(), failingMailboxEventStore{err: storeErr}, nil, func(ctx context.Context, sessionID string, message team.MailMessage) error {
		called = true
		if sessionID != "session-1" || message.Body != "fallback body" {
			t.Fatalf("unexpected fallback args: session=%q message=%#v", sessionID, message)
		}
		return nil
	}, "session-1", team.MailMessage{Body: "fallback body"})
	if err != nil {
		t.Fatalf("DeliverMailboxEventFirst fallback: %v", err)
	}
	if !called {
		t.Fatal("expected actor fallback to be called")
	}
}

func TestDeliverMailboxEventFirstReturnsStoreErrorWithoutFallback(t *testing.T) {
	storeErr := errors.New("store down")
	err := DeliverMailboxEventFirst(context.Background(), failingMailboxEventStore{err: storeErr}, nil, nil, "session-1", team.MailMessage{Body: "lost"})
	if !errors.Is(err, storeErr) {
		t.Fatalf("expected store error, got %v", err)
	}
}
