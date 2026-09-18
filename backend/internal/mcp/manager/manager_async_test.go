package manager

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/client"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
)

// blockingClient 允许测试精确控制 Connect 的阻塞与释放时机。
type blockingClient struct {
	name        string
	connected   atomic.Bool
	closed      atomic.Bool
	entered     chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
}

func newBlockingClient(name string) *blockingClient {
	return &blockingClient{
		name:    name,
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
}

func (c *blockingClient) Connect(ctx context.Context) error {
	select {
	case c.entered <- struct{}{}:
	default:
	}
	select {
	case <-c.release:
		if c.closed.Load() {
			return context.Canceled
		}
		c.connected.Store(true)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *blockingClient) Name() string { return c.name }

func (c *blockingClient) ListTools(ctx context.Context) ([]*protocol.Tool, error) {
	return []*protocol.Tool{{Name: "ping", InputSchema: map[string]interface{}{"type": "object"}}}, nil
}

func (c *blockingClient) CallTool(ctx context.Context, name string, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return &protocol.CallToolResult{}, nil
}

func (c *blockingClient) ListResources(ctx context.Context, cursor *string) (*protocol.ListResourcesResult, error) {
	return &protocol.ListResourcesResult{}, nil
}

func (c *blockingClient) ReadResource(ctx context.Context, uri string) (*protocol.ReadResourceResult, error) {
	return &protocol.ReadResourceResult{}, nil
}

func (c *blockingClient) Close() error {
	c.closed.Store(true)
	c.releaseConnect()
	c.connected.Store(false)
	return nil
}

// releaseConnect 放行阻塞中的 Connect（幂等）。
func (c *blockingClient) releaseConnect() {
	c.releaseOnce.Do(func() { close(c.release) })
}

func (c *blockingClient) IsConnected() bool { return c.connected.Load() }

// stubbornClient 模拟无视 ctx 取消、只响应 Close 的慢握手客户端。
type stubbornClient struct {
	name      string
	entered   chan struct{}
	release   chan struct{}
	closed    atomic.Bool
	closeOnce sync.Once
}

func newStubbornClient(name string) *stubbornClient {
	return &stubbornClient{
		name:    name,
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
}

func (c *stubbornClient) Connect(ctx context.Context) error {
	select {
	case c.entered <- struct{}{}:
	default:
	}
	<-c.release
	return nil
}

func (c *stubbornClient) Name() string { return c.name }

func (c *stubbornClient) ListTools(ctx context.Context) ([]*protocol.Tool, error) {
	return nil, context.Canceled
}

func (c *stubbornClient) CallTool(ctx context.Context, name string, args map[string]interface{}) (*protocol.CallToolResult, error) {
	return nil, context.Canceled
}

func (c *stubbornClient) ListResources(ctx context.Context, cursor *string) (*protocol.ListResourcesResult, error) {
	return nil, context.Canceled
}

func (c *stubbornClient) ReadResource(ctx context.Context, uri string) (*protocol.ReadResourceResult, error) {
	return nil, context.Canceled
}

func (c *stubbornClient) Close() error {
	c.closed.Store(true)
	c.closeOnce.Do(func() { close(c.release) })
	return nil
}

func (c *stubbornClient) IsConnected() bool { return !c.closed.Load() }

func testConfig(names ...string) *config.Config {
	servers := make(map[string]config.MCPConfig, len(names))
	for _, name := range names {
		servers[name] = config.MCPConfig{
			Name:    name,
			Type:    "stdio",
			Enabled: true,
			Timeout: config.Duration{Duration: 2 * time.Second},
		}
	}
	return &config.Config{
		MCPServers: servers,
		Global:     config.GlobalConfig{ConnectTimeout: config.Duration{Duration: 2 * time.Second}},
	}
}

func TestManagerStartAsync_ReturnsImmediatelyAndWaitReadyAwaitsConnect(t *testing.T) {
	mgr := newTestManager(testConfig("slow"))
	slow := newBlockingClient("slow")
	mgr.newClient = func(name string, cfg *config.MCPConfig) (client.Client, error) { return slow, nil }

	start := time.Now()
	require.NoError(t, mgr.StartAsync(context.Background()))
	require.Less(t, time.Since(start), 500*time.Millisecond, "StartAsync must not wait for MCP connect")
	require.Equal(t, 0, len(mgr.ListTools()), "tools must not be published before connect finishes")

	select {
	case <-slow.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("background connect did not start")
	}

	ready := make(chan error, 1)
	go func() { ready <- mgr.WaitReady(context.Background()) }()
	select {
	case err := <-ready:
		t.Fatalf("WaitReady returned before the connect finished: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	slow.releaseConnect()
	select {
	case err := <-ready:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("WaitReady did not return after connect finished")
	}
	require.True(t, slow.IsConnected())
	require.Len(t, mgr.ListTools(), 1)
	status, err := mgr.GetMCPStatus("slow")
	require.NoError(t, err)
	require.True(t, status.Connected)
}

func TestManagerStart_ConnectsServersInParallel(t *testing.T) {
	mgr := newTestManager(testConfig("alpha", "beta"))
	alpha := newBlockingClient("alpha")
	beta := newBlockingClient("beta")
	mgr.newClient = func(name string, cfg *config.MCPConfig) (client.Client, error) {
		if name == "alpha" {
			return alpha, nil
		}
		return beta, nil
	}

	startDone := make(chan error, 1)
	go func() { startDone <- mgr.Start(context.Background()) }()

	// 串行实现只会有一个 Connect 进入；两个都进入说明确实是并行建连。
	entered := map[string]bool{}
	for len(entered) < 2 {
		select {
		case <-alpha.entered:
			entered["alpha"] = true
		case <-beta.entered:
			entered["beta"] = true
		case <-time.After(2 * time.Second):
			t.Fatalf("MCP connects did not run in parallel, entered=%v", entered)
		}
	}

	alpha.releaseConnect()
	beta.releaseConnect()

	select {
	case err := <-startDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after both connects finished")
	}
	require.True(t, alpha.IsConnected())
	require.True(t, beta.IsConnected())
	for _, name := range []string{"alpha", "beta"} {
		status, err := mgr.GetMCPStatus(name)
		require.NoError(t, err)
		require.True(t, status.Connected, "server %s should be connected", name)
		require.Empty(t, status.LastError, "server %s should have no connect error", name)
	}
}

func TestManagerStop_CancelsInflightAsyncConnect(t *testing.T) {
	mgr := newTestManager(testConfig("slow"))
	slow := newBlockingClient("slow")
	mgr.newClient = func(name string, cfg *config.MCPConfig) (client.Client, error) { return slow, nil }

	require.NoError(t, mgr.StartAsync(context.Background()))
	select {
	case <-slow.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("background connect did not start")
	}

	stopDone := make(chan error, 1)
	go func() { stopDone <- mgr.Stop() }()
	select {
	case err := <-stopDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not cancel the in-flight connect")
	}

	require.Empty(t, mgr.ListTools())
	status, err := mgr.GetMCPStatus("slow")
	require.NoError(t, err)
	require.False(t, status.Connected)
}

func TestManagerStop_ClosesPendingClientToAbortSlowHandshake(t *testing.T) {
	mgr := newTestManager(testConfig("stubborn"))
	stubborn := newStubbornClient("stubborn")
	mgr.newClient = func(name string, cfg *config.MCPConfig) (client.Client, error) { return stubborn, nil }

	require.NoError(t, mgr.StartAsync(context.Background()))
	select {
	case <-stubborn.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("background connect did not start")
	}

	start := time.Now()
	require.NoError(t, mgr.Stop())
	require.Less(t, time.Since(start), 2*time.Second,
		"Stop must close pending clients instead of waiting out the shutdown grace")
	require.True(t, stubborn.closed.Load(), "pending client should be closed by Stop")
}

func TestManagerStartAsync_RejectsSecondStart(t *testing.T) {
	mgr := newTestManager(testConfig("alpha"))
	alpha := newBlockingClient("alpha")
	mgr.newClient = func(name string, cfg *config.MCPConfig) (client.Client, error) { return alpha, nil }

	require.NoError(t, mgr.StartAsync(context.Background()))
	require.Error(t, mgr.StartAsync(context.Background()))
	alpha.releaseConnect()
	require.NoError(t, mgr.WaitReady(context.Background()))
	require.NoError(t, mgr.Stop())
}
