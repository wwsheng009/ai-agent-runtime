package manager

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/client"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/registry"
	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/stretchr/testify/require"
)

type fakeClient struct {
	name           string
	connected      bool
	connectErr     error
	listToolsErr   error
	tools          []*protocol.Tool
	toolErrors     map[string]error
	resourceErrors map[string]error
	observers      []client.LifecycleObserver
}

func (f *fakeClient) Connect(ctx context.Context) error {
	if f.connectErr != nil {
		return f.connectErr
	}
	f.connected = true
	for _, observer := range f.observers {
		observer(client.LifecycleEvent{
			Type:          "mcp.transport.connected",
			TraceID:       client.TraceIDFromContext(ctx),
			ClientName:    f.name,
			TransportType: "stdio",
			SessionID:     "fake-session",
			Payload: map[string]interface{}{
				"target": "fake-command",
			},
		})
		observer(client.LifecycleEvent{
			Type:          "mcp.client.session.connected",
			TraceID:       client.TraceIDFromContext(ctx),
			ClientName:    f.name,
			TransportType: "stdio",
			SessionID:     "fake-session",
			Payload: map[string]interface{}{
				"target": "fake-command",
			},
		})
	}
	return nil
}

func (f *fakeClient) Name() string { return f.name }

func (f *fakeClient) ListTools(ctx context.Context) ([]*protocol.Tool, error) {
	if f.listToolsErr != nil {
		return nil, f.listToolsErr
	}
	if f.tools != nil {
		return f.tools, nil
	}
	return []*protocol.Tool{}, nil
}

func (f *fakeClient) CallTool(ctx context.Context, name string, args map[string]interface{}) (*protocol.CallToolResult, error) {
	if f.toolErrors != nil {
		if err, ok := f.toolErrors[name]; ok && err != nil {
			return nil, err
		}
	}
	return &protocol.CallToolResult{IsError: false}, nil
}

func (f *fakeClient) ListResources(ctx context.Context, cursor *string) (*protocol.ListResourcesResult, error) {
	return nil, errors.New("not implemented")
}

func (f *fakeClient) ReadResource(ctx context.Context, uri string) (*protocol.ReadResourceResult, error) {
	if f.resourceErrors != nil {
		if err, ok := f.resourceErrors[uri]; ok && err != nil {
			return nil, err
		}
	}
	return &protocol.ReadResourceResult{}, nil
}

func (f *fakeClient) Close() error {
	f.connected = false
	return nil
}

func (f *fakeClient) IsConnected() bool { return f.connected }

func (f *fakeClient) AddLifecycleObserver(observer client.LifecycleObserver) {
	if observer == nil {
		return
	}
	f.observers = append(f.observers, observer)
}

func newTestManager(cfg *config.Config) *manager {
	return &manager{
		cfg:       cfg,
		registry:  registry.NewRegistry(),
		clients:   make(map[string]client.Client),
		status:    make(map[string]*config.MCPStatus),
		newClient: func(name string, cfg *config.MCPConfig) (client.Client, error) { return &fakeClient{name: name}, nil },
	}
}

func TestManagerHealthCheck_UpdatesStatus(t *testing.T) {
	cfg := &config.Config{
		MCPServers: map[string]config.MCPConfig{
			"test-mcp": {Name: "test-mcp", Type: "stdio", Enabled: true, Timeout: config.Duration{Duration: 50 * time.Millisecond}},
		},
		Global: config.GlobalConfig{ConnectTimeout: config.Duration{Duration: 50 * time.Millisecond}},
	}
	mgr := newTestManager(cfg)
	cli := &fakeClient{name: "test-mcp", connected: true}
	mgr.clients["test-mcp"] = cli
	mgr.registry.RegisterClient("test-mcp", cli)

	mgr.healthCheckOnce()

	status, err := mgr.GetMCPStatus("test-mcp")
	require.NoError(t, err)
	require.False(t, status.HealthCheck.IsZero())
	require.Empty(t, status.LastError)
}

func TestManagerStart_EmitsLifecycleEventsWithTrace(t *testing.T) {
	cfg := &config.Config{
		MCPServers: map[string]config.MCPConfig{
			"test-mcp": {Name: "test-mcp", Type: "stdio", Enabled: true, Timeout: config.Duration{Duration: 50 * time.Millisecond}},
		},
		Global: config.GlobalConfig{ConnectTimeout: config.Duration{Duration: 50 * time.Millisecond}},
	}
	mgr := newTestManager(cfg)
	mgr.newClient = func(name string, cfg *config.MCPConfig) (client.Client, error) {
		return &fakeClient{
			name: name,
			tools: []*protocol.Tool{
				{Name: "read_logs"},
			},
		}, nil
	}

	var events []LifecycleEvent
	mgr.AddLifecycleObserver(func(event LifecycleEvent) {
		events = append(events, event)
	})

	err := mgr.Start(WithTraceID(context.Background(), "trace-start"))
	require.NoError(t, err)

	require.NotEmpty(t, events)
	require.Equal(t, "mcp.starting", events[0].Type)
	require.Equal(t, "trace-start", events[0].TraceID)

	var sawConnected bool
	var sawToolsLoaded bool
	for _, event := range events {
		require.Equal(t, "trace-start", event.TraceID)
		if event.Type == "mcp.connected" {
			sawConnected = true
		}
		if event.Type == "mcp.tools.loaded" {
			sawToolsLoaded = true
			require.Equal(t, 1, event.Payload["tool_count"])
		}
	}
	require.True(t, sawConnected)
	require.True(t, sawToolsLoaded)
}

func TestManagerStart_BridgesClientLifecycleEvents(t *testing.T) {
	cfg := &config.Config{
		MCPServers: map[string]config.MCPConfig{
			"test-mcp": {Name: "test-mcp", Type: "stdio", Enabled: true, Timeout: config.Duration{Duration: 50 * time.Millisecond}},
		},
		Global: config.GlobalConfig{ConnectTimeout: config.Duration{Duration: 50 * time.Millisecond}},
	}
	mgr := newTestManager(cfg)

	var events []LifecycleEvent
	mgr.AddLifecycleObserver(func(event LifecycleEvent) {
		events = append(events, event)
	})

	err := mgr.Start(WithTraceID(context.Background(), "trace-client-bridge"))
	require.NoError(t, err)

	var bridged *LifecycleEvent
	var transportConnected *LifecycleEvent
	for _, event := range events {
		if event.Type == "mcp.client.session.connected" {
			bridged = &event
		}
		if event.Type == "mcp.transport.connected" {
			transportConnected = &event
		}
	}
	require.NotNil(t, bridged)
	require.Equal(t, "trace-client-bridge", bridged.TraceID)
	require.Equal(t, "test-mcp", bridged.MCPName)
	require.Equal(t, "stdio", bridged.Payload["transport_type"])
	require.Equal(t, "fake-session", bridged.Payload["session_id"])
	require.NotNil(t, transportConnected)
	require.Equal(t, "trace-client-bridge", transportConnected.TraceID)
	require.Equal(t, "stdio", transportConnected.Payload["transport_type"])
}

func TestManagerHealthCheck_ReconnectsOnFailure(t *testing.T) {
	cfg := &config.Config{
		MCPServers: map[string]config.MCPConfig{
			"test-mcp": {Name: "test-mcp", Type: "stdio", Enabled: true, Timeout: config.Duration{Duration: 50 * time.Millisecond}},
		},
		Global: config.GlobalConfig{ConnectTimeout: config.Duration{Duration: 50 * time.Millisecond}},
	}
	mgr := newTestManager(cfg)

	failing := &fakeClient{name: "test-mcp", connected: true, listToolsErr: errors.New("boom")}
	reconnected := &fakeClient{name: "test-mcp"}

	mgr.clients["test-mcp"] = failing
	mgr.registry.RegisterClient("test-mcp", failing)
	mgr.newClient = func(name string, cfg *config.MCPConfig) (client.Client, error) {
		reconnected.tools = []*protocol.Tool{{Name: "read_logs"}}
		return reconnected, nil
	}

	var events []LifecycleEvent
	mgr.AddLifecycleObserver(func(event LifecycleEvent) {
		events = append(events, event)
	})

	mgr.healthCheckOnce()

	status, err := mgr.GetMCPStatus("test-mcp")
	require.NoError(t, err)
	require.False(t, status.LastConnect.IsZero())
	require.Empty(t, status.LastError)
	require.True(t, reconnected.IsConnected())
	require.NotEmpty(t, events)
	require.Equal(t, "mcp.health.failed", events[0].Type)
	require.Contains(t, []string{"mcp.reconnect.started", "mcp.reconnected", "mcp.tools.loaded"}, events[len(events)-1].Type)
}

func TestManagerHealthCheck_ToolProbeDisablesTool(t *testing.T) {
	cfg := &config.Config{
		MCPServers: map[string]config.MCPConfig{
			"test-mcp": {
				Name:    "test-mcp",
				Type:    "stdio",
				Enabled: true,
				Timeout: config.Duration{Duration: 50 * time.Millisecond},
				HealthCheck: &config.MCPHealthCheckConfig{
					Tools: []string{"ok-tool", "bad-tool"},
				},
			},
		},
		Global: config.GlobalConfig{ConnectTimeout: config.Duration{Duration: 50 * time.Millisecond}},
	}
	mgr := newTestManager(cfg)

	cli := &fakeClient{
		name:      "test-mcp",
		connected: true,
		toolErrors: map[string]error{
			"bad-tool": errors.New("probe failed"),
		},
	}
	mgr.clients["test-mcp"] = cli
	mgr.registry.RegisterClient("test-mcp", cli)
	mgr.registry.RegisterTool("test-mcp", &protocol.Tool{Name: "ok-tool"}, true)
	mgr.registry.RegisterTool("test-mcp", &protocol.Tool{Name: "bad-tool"}, true)

	mgr.healthCheckOnce()

	require.True(t, mgr.registry.ToolEnabled("test-mcp", "ok-tool"))
	require.False(t, mgr.registry.ToolEnabled("test-mcp", "bad-tool"))
}

func TestManagerCallTool_WrapsGovernanceContext(t *testing.T) {
	cfg := &config.Config{
		MCPServers: map[string]config.MCPConfig{
			"remote-mcp": {
				Name:       "remote-mcp",
				Type:       "sse",
				URL:        "http://localhost:3000/sse",
				TrustLevel: config.MCPTrustLevelTrustedRemote,
				Enabled:    true,
				Timeout:    config.Duration{Duration: 5 * time.Second},
			},
		},
	}
	mgr := newTestManager(cfg)
	cli := &fakeClient{
		name:      "remote-mcp",
		connected: true,
		toolErrors: map[string]error{
			"lookup": errors.New("upstream denied"),
		},
	}
	mgr.clients["remote-mcp"] = cli
	mgr.registry.RegisterClient("remote-mcp", cli)

	_, err := mgr.CallTool(context.Background(), "remote-mcp", "lookup", map[string]interface{}{})
	require.Error(t, err)

	var runtimeErr *runtimeerrors.RuntimeError
	require.ErrorAs(t, err, &runtimeErr)
	require.Equal(t, runtimeerrors.ErrToolExecution, runtimeErr.Code)
	require.Equal(t, "remote-mcp", runtimeErr.Context["mcp_name"])
	require.Equal(t, "trusted_remote", runtimeErr.Context["mcp_trust_level"])
	require.Equal(t, "remote_mcp", runtimeErr.Context["execution_mode"])
}
