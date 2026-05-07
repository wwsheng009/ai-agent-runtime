package chat

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
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

type recordingAgentControlMailboxStore struct {
	mailboxCalls int
	controlCalls int
	eventCalls   int
}

func (s *recordingAgentControlMailboxStore) AppendMailbox(ctx context.Context, sessionID string, message team.MailMessage) (runtimeevents.Event, int64, error) {
	s.mailboxCalls++
	return NewMailboxReceivedEvent(sessionID, message), 1, nil
}

func (s *recordingAgentControlMailboxStore) AppendAgentControlMailbox(ctx context.Context, sessionID string, message team.MailMessage) (runtimeevents.Event, int64, error) {
	s.controlCalls++
	if err := validateAgentControlMailboxMessage(message); err != nil {
		return runtimeevents.Event{}, 0, err
	}
	return NewMailboxReceivedEvent(sessionID, message), 1, nil
}

func (s *recordingAgentControlMailboxStore) AppendEvent(context.Context, runtimeevents.Event) (int64, error) {
	s.eventCalls++
	return 1, nil
}

func (s *recordingAgentControlMailboxStore) ListEvents(context.Context, string, int64, int) ([]runtimeevents.Event, error) {
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

func TestDeliverMailboxStoreFirstPrefersAgentControlWriterForEnvelope(t *testing.T) {
	store := &recordingAgentControlMailboxStore{}
	message := team.MailMessage{
		ID:        "mail-1",
		FromAgent: "parent",
		ToAgent:   "session-1",
		Kind:      agentcontrol.MailboxKindAgentMessage,
		Body:      "hello control writer",
		Metadata: agentcontrol.Envelope{
			MessageType:     agentcontrol.MessageTypeAgentMessage,
			ControlAction:   agentcontrol.ActionAgentMessage,
			Workflow:        agentcontrol.WorkflowSpawnAgent,
			MailboxDelivery: agentcontrol.DeliverySessionMailbox,
			MailboxKind:     agentcontrol.MailboxKindAgentMessage,
		}.Metadata(),
	}

	if err := DeliverMailboxStoreFirst(context.Background(), store, nil, nil, "session-1", message); err != nil {
		t.Fatalf("DeliverMailboxStoreFirst: %v", err)
	}
	if store.controlCalls != 1 || store.mailboxCalls != 0 {
		t.Fatalf("expected control writer only, mailbox=%d control=%d", store.mailboxCalls, store.controlCalls)
	}
}

func TestDeliverMailboxEventFirstPrefersAgentControlWriterForEnvelope(t *testing.T) {
	store := &recordingAgentControlMailboxStore{}
	bus := runtimeevents.NewBusWithRetention(8)
	message := team.MailMessage{
		ID:        "mail-1",
		FromAgent: "parent",
		ToAgent:   "session-1",
		Kind:      agentcontrol.MailboxKindAgentMessage,
		Body:      "hello control writer",
		Metadata: agentcontrol.Envelope{
			MessageType:     agentcontrol.MessageTypeAgentMessage,
			ControlAction:   agentcontrol.ActionAgentMessage,
			Workflow:        agentcontrol.WorkflowSpawnAgent,
			MailboxDelivery: agentcontrol.DeliverySessionMailbox,
			MailboxKind:     agentcontrol.MailboxKindAgentMessage,
		}.Metadata(),
	}

	if err := DeliverMailboxEventFirst(context.Background(), store, bus, nil, "session-1", message); err != nil {
		t.Fatalf("DeliverMailboxEventFirst: %v", err)
	}
	if store.controlCalls != 1 || store.mailboxCalls != 0 || store.eventCalls != 0 {
		t.Fatalf("expected control writer only, mailbox=%d control=%d events=%d", store.mailboxCalls, store.controlCalls, store.eventCalls)
	}
	recent := bus.Recent(10)
	if len(recent) != 1 || recent[0].Payload["body"] != "hello control writer" {
		t.Fatalf("expected published control mailbox event, got %#v", recent)
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

func TestMergeMailboxMessagesBySeqPrefersPrimaryAndSorts(t *testing.T) {
	primary := []team.MailMessage{
		{Seq: 3, ID: "control-3", Body: "control three"},
		{Seq: 1, ID: "control-1", Body: "control one"},
	}
	secondary := []team.MailMessage{
		{Seq: 2, ID: "legacy-2", Body: "legacy two"},
		{Seq: 1, ID: "legacy-1", Body: "legacy one"},
	}

	merged := MergeMailboxMessagesBySeq(primary, secondary, 0)
	if len(merged) != 3 {
		t.Fatalf("expected 3 merged messages, got %#v", merged)
	}
	for index, expectedSeq := range []int64{1, 2, 3} {
		if merged[index].Seq != expectedSeq {
			t.Fatalf("expected seq %d at %d, got %#v", expectedSeq, index, merged)
		}
	}
	if merged[0].Body != "control one" {
		t.Fatalf("expected primary row to win duplicate seq, got %#v", merged[0])
	}

	limited := MergeMailboxMessagesBySeq(primary, secondary, 2)
	if len(limited) != 2 || limited[0].Seq != 1 || limited[1].Seq != 2 {
		t.Fatalf("unexpected limited merge: %#v", limited)
	}
}

func TestListMailboxAgentControlFirstMergesControlAndLegacyRows(t *testing.T) {
	store := NewInMemoryRuntimeStore(8)
	if _, _, err := store.AppendMailbox(context.Background(), "session-1", team.MailMessage{
		ID:        "legacy-1",
		FromAgent: "parent",
		ToAgent:   "child",
		Kind:      "info",
		Body:      "legacy row",
	}); err != nil {
		t.Fatalf("append legacy mailbox: %v", err)
	}
	if _, _, err := store.AppendAgentControlMailbox(context.Background(), "session-1", team.MailMessage{
		ID:        "control-1",
		FromAgent: "parent",
		ToAgent:   "child",
		Kind:      agentcontrol.MailboxKindAgentMessage,
		Body:      "control row",
		Metadata: agentcontrol.Envelope{
			MessageType:     agentcontrol.MessageTypeAgentMessage,
			ControlAction:   agentcontrol.ActionAgentMessage,
			Workflow:        agentcontrol.WorkflowSpawnAgent,
			MailboxDelivery: agentcontrol.DeliverySessionMailbox,
			MailboxKind:     agentcontrol.MailboxKindAgentMessage,
		}.Metadata(),
	}); err != nil {
		t.Fatalf("append control mailbox: %v", err)
	}

	messages, supported, hasRows, err := ListMailboxAgentControlFirst(context.Background(), store, "session-1", 0, 10)
	if err != nil {
		t.Fatalf("ListMailboxAgentControlFirst: %v", err)
	}
	if !supported || !hasRows {
		t.Fatalf("expected supported mailbox rows, supported=%v hasRows=%v", supported, hasRows)
	}
	if len(messages) != 2 {
		t.Fatalf("expected merged legacy and control rows, got %#v", messages)
	}
	if messages[0].Body != "legacy row" || messages[1].Body != "control row" {
		t.Fatalf("unexpected merged mailbox order: %#v", messages)
	}
	if messageType := agentcontrol.MetadataString(messages[1].Metadata, agentcontrol.MetadataKeyMessageType); messageType != agentcontrol.MessageTypeAgentMessage {
		t.Fatalf("expected control metadata on primary row, got %#v", messages[1])
	}
}

func TestWatchMailboxAgentControlFirstWakesFromMailboxRows(t *testing.T) {
	store := NewInMemoryRuntimeStore(8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watch, unwatch, supported := WatchMailboxAgentControlFirst(ctx, store, "session-1")
	defer unwatch()
	if !supported {
		t.Fatal("expected mailbox watch support")
	}

	if _, _, err := store.AppendMailbox(context.Background(), "session-1", team.MailMessage{
		ID:        "legacy-1",
		FromAgent: "parent",
		ToAgent:   "child",
		Kind:      "info",
		Body:      "legacy wake",
	}); err != nil {
		t.Fatalf("append mailbox: %v", err)
	}
	select {
	case message := <-watch:
		if message.Body != "legacy wake" {
			t.Fatalf("unexpected watched message: %#v", message)
		}
	case <-time.After(time.Second):
		t.Fatal("mailbox watch did not wake")
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
