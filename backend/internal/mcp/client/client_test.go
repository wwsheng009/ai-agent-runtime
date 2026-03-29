package client

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/transport"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestConvertSDKTool_ParsesRawSchema(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`)
	sdkTool := &mcp.Tool{
		Name:        "bash",
		Description: "execute shell",
		InputSchema: schema,
	}

	tool := convertSDKTool(sdkTool)
	if tool == nil {
		t.Fatal("expected tool, got nil")
	}
	if tool.InputSchema == nil {
		t.Fatal("expected InputSchema to be set")
	}
	props, ok := tool.InputSchema["properties"].(map[string]interface{})
	if !ok || len(props) == 0 {
		t.Fatalf("expected properties in InputSchema, got %v", tool.InputSchema)
	}
	if _, ok := props["command"]; !ok {
		t.Fatalf("expected command property, got %v", props)
	}
}

type fakeSession struct {
	id string
}

func (f *fakeSession) ID() string { return f.id }

func (f *fakeSession) ListTools(ctx context.Context, params *mcp.ListToolsParams) (*mcp.ListToolsResult, error) {
	return &mcp.ListToolsResult{}, nil
}

func (f *fakeSession) CallTool(ctx context.Context, params *mcp.CallToolParams) (*mcp.CallToolResult, error) {
	return &mcp.CallToolResult{}, nil
}

func (f *fakeSession) ListResources(ctx context.Context, params *mcp.ListResourcesParams) (*mcp.ListResourcesResult, error) {
	return &mcp.ListResourcesResult{}, nil
}

func (f *fakeSession) ReadResource(ctx context.Context, params *mcp.ReadResourceParams) (*mcp.ReadResourceResult, error) {
	return &mcp.ReadResourceResult{}, nil
}

func (f *fakeSession) Close() error { return nil }

type fakeObservedTransport struct {
	transportType string
	connectErr    error
	observers     []transport.LifecycleObserver
}

func (f *fakeObservedTransport) Type() string { return f.transportType }

func (f *fakeObservedTransport) Config() interface{} { return nil }

func (f *fakeObservedTransport) AddLifecycleObserver(observer transport.LifecycleObserver) {
	if observer == nil {
		return
	}
	f.observers = append(f.observers, observer)
}

func (f *fakeObservedTransport) ToMCPSdkTransport(ctx context.Context) mcp.Transport {
	traceID := transport.TraceIDFromContext(ctx)
	observers := append([]transport.LifecycleObserver(nil), f.observers...)
	return &fakeSDKTransport{
		traceID:       traceID,
		transportType: f.transportType,
		observers:     observers,
		connectErr:    f.connectErr,
	}
}

type fakeSDKTransport struct {
	traceID       string
	transportType string
	observers     []transport.LifecycleObserver
	connectErr    error
}

func (f *fakeSDKTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	traceID := transport.TraceIDFromContext(ctx)
	if traceID == "" {
		traceID = f.traceID
	}
	f.emit("mcp.transport.connecting", traceID, "session-123")
	if f.connectErr != nil {
		f.emit("mcp.transport.connect_failed", traceID, "")
		return nil, f.connectErr
	}
	f.emit("mcp.transport.connected", traceID, "session-123")
	return &fakeConnection{sessionID: "session-123"}, nil
}

func (f *fakeSDKTransport) emit(eventType, traceID, sessionID string) {
	for _, observer := range f.observers {
		observer(transport.LifecycleEvent{
			Type:          eventType,
			TraceID:       traceID,
			TransportType: f.transportType,
			SessionID:     sessionID,
			Payload: map[string]interface{}{
				"target": "fake-target",
			},
		})
	}
}

type fakeConnection struct {
	sessionID string
}

func (f *fakeConnection) Read(ctx context.Context) (jsonrpc.Message, error) { return nil, nil }
func (f *fakeConnection) Write(ctx context.Context, msg jsonrpc.Message) error {
	return nil
}
func (f *fakeConnection) Close() error      { return nil }
func (f *fakeConnection) SessionID() string { return f.sessionID }

func TestMCPClient_EmitsLifecycleEventsOnConnectAndClose(t *testing.T) {
	raw, err := NewClient("test-mcp", &config.MCPConfig{
		Type:    "stdio",
		Command: "fake-command",
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	c := raw.(*mcpClient)
	c.newTransport = func(cfg *transport.Config) (transport.Transport, error) {
		return &fakeObservedTransport{transportType: "stdio"}, nil
	}
	c.connectSession = func(ctx context.Context, mcpTransport mcp.Transport) (mcpSession, error) {
		if _, err := mcpTransport.Connect(ctx); err != nil {
			return nil, err
		}
		return &fakeSession{id: "session-123"}, nil
	}

	var events []LifecycleEvent
	c.AddLifecycleObserver(func(event LifecycleEvent) {
		events = append(events, event)
	})

	err = c.Connect(WithTraceID(context.Background(), "trace-client"))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	assertHasEvent(t, events, "mcp.client.connecting", "trace-client")
	assertHasEvent(t, events, "mcp.client.transport.created", "trace-client")
	assertHasEvent(t, events, "mcp.client.session.connecting", "trace-client")
	assertHasEvent(t, events, "mcp.transport.connecting", "trace-client")
	assertHasEvent(t, events, "mcp.transport.connected", "trace-client")
	assertHasEvent(t, events, "mcp.client.session.connected", "trace-client")
	assertHasEvent(t, events, "mcp.client.session.closed", "trace-client")

	var transportConnected *LifecycleEvent
	var sessionConnected *LifecycleEvent
	for i := range events {
		if events[i].Type == "mcp.transport.connected" {
			transportConnected = &events[i]
		}
		if events[i].Type == "mcp.client.session.connected" {
			sessionConnected = &events[i]
		}
	}
	if transportConnected == nil {
		t.Fatal("expected transport connected event")
	}
	if transportConnected.TransportType != "stdio" {
		t.Fatalf("expected stdio transport, got %s", transportConnected.TransportType)
	}
	if transportConnected.SessionID != "session-123" {
		t.Fatalf("expected session id on transport connected event, got %s", transportConnected.SessionID)
	}
	if sessionConnected == nil || sessionConnected.SessionID != "session-123" {
		t.Fatalf("expected session id on client connected event, got %#v", sessionConnected)
	}
}

func TestMCPClient_EmitsLifecycleEventOnConnectFailure(t *testing.T) {
	raw, err := NewClient("test-mcp", &config.MCPConfig{
		Type:    "stdio",
		Command: "fake-command",
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	c := raw.(*mcpClient)
	c.newTransport = func(cfg *transport.Config) (transport.Transport, error) {
		return &fakeObservedTransport{
			transportType: "stdio",
			connectErr:    errors.New("dial failed"),
		}, nil
	}
	c.connectSession = func(ctx context.Context, mcpTransport mcp.Transport) (mcpSession, error) {
		_, err := mcpTransport.Connect(ctx)
		return nil, err
	}

	var events []LifecycleEvent
	c.AddLifecycleObserver(func(event LifecycleEvent) {
		events = append(events, event)
	})

	err = c.Connect(WithTraceID(context.Background(), "trace-failed"))
	if err == nil {
		t.Fatal("expected connect failure")
	}
	assertHasEvent(t, events, "mcp.transport.connect_failed", "trace-failed")
	assertHasEvent(t, events, "mcp.client.session.connect_failed", "trace-failed")
}

func assertHasEvent(t *testing.T, events []LifecycleEvent, eventType, traceID string) {
	t.Helper()
	for _, event := range events {
		if event.Type == eventType && event.TraceID == traceID {
			return
		}
	}
	t.Fatalf("expected event %s with trace %s, got %#v", eventType, traceID, events)
}
