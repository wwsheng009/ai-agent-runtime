package transport

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeTransport struct {
	conn mcp.Connection
	err  error
}

func (f *fakeTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.conn, nil
}

type fakeConnection struct {
	sessionID string
	readMsg   jsonrpc.Message
	readErr   error
	writeErr  error
	closed    bool
}

func (f *fakeConnection) Read(ctx context.Context) (jsonrpc.Message, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	return f.readMsg, nil
}

func (f *fakeConnection) Write(ctx context.Context, msg jsonrpc.Message) error {
	return f.writeErr
}

func (f *fakeConnection) Close() error {
	f.closed = true
	return nil
}

func (f *fakeConnection) SessionID() string {
	return f.sessionID
}

func TestObservedTransport_EmitsConnectionReadWriteAndCloseEvents(t *testing.T) {
	emitter := &lifecycleEmitter{}
	conn := &fakeConnection{sessionID: "session-1"}
	msg := &jsonrpc.Request{Method: "ping"}
	conn.readMsg = msg

	var events []LifecycleEvent
	emitter.AddLifecycleObserver(func(event LifecycleEvent) {
		events = append(events, event)
	})

	transport := newObservedMCPTransport("stdio", "fake-command", &fakeTransport{conn: conn}, emitter)
	observedConn, err := transport.Connect(WithTraceID(context.Background(), "trace-transport"))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := observedConn.Read(context.Background()); err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := observedConn.Write(context.Background(), msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := observedConn.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	expected := []string{
		"mcp.transport.connecting",
		"mcp.transport.connected",
		"mcp.transport.read",
		"mcp.transport.write",
		"mcp.transport.closed",
	}
	if len(events) != len(expected) {
		t.Fatalf("expected %d events, got %d", len(expected), len(events))
	}
	for index, eventType := range expected {
		if events[index].Type != eventType {
			t.Fatalf("expected event[%d]=%s, got %s", index, eventType, events[index].Type)
		}
		if events[index].TraceID != "trace-transport" {
			t.Fatalf("expected trace-transport on %s, got %s", eventType, events[index].TraceID)
		}
	}
	if events[1].SessionID != "session-1" {
		t.Fatalf("expected session id on connected event, got %s", events[1].SessionID)
	}
	if events[2].Payload["bytes"] == 0 {
		t.Fatalf("expected read bytes to be recorded, got %#v", events[2].Payload)
	}
	if !conn.closed {
		t.Fatal("expected underlying connection to be closed")
	}
}

func TestObservedTransport_EmitsConnectFailure(t *testing.T) {
	emitter := &lifecycleEmitter{}
	var events []LifecycleEvent
	emitter.AddLifecycleObserver(func(event LifecycleEvent) {
		events = append(events, event)
	})

	transport := newObservedMCPTransport("websocket", "ws://localhost/mcp", &fakeTransport{err: context.DeadlineExceeded}, emitter)
	_, err := transport.Connect(WithTraceID(context.Background(), "trace-failed"))
	if err == nil {
		t.Fatal("expected connect failure")
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[1].Type != "mcp.transport.connect_failed" {
		t.Fatalf("expected connect_failed event, got %s", events[1].Type)
	}
	if events[1].TraceID != "trace-failed" {
		t.Fatalf("expected trace-failed on connect_failed, got %s", events[1].TraceID)
	}
}
