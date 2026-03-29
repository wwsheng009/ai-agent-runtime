package manager

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	echo "github.com/ai-gateway/ai-agent-runtime/internal/mcp/server/echo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManager_RemoteEchoConnectivity(t *testing.T) {
	if os.Getenv("LIVE_MCP_TEST") != "1" {
		t.Skip("set LIVE_MCP_TEST=1 to enable remote echo MCP connectivity test")
	}

	addr := reserveManagerLocalAddr(t)
	echoServer := echo.NewEchoServer(addr)
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- echoServer.Start()
	}()
	waitForManagerTCP(t, addr, 5*time.Second)
	t.Cleanup(func() {
		_ = echoServer.Stop()
		select {
		case err := <-serverErr:
			if err != nil && err != http.ErrServerClosed {
				t.Logf("echo server stopped with error: %v", err)
			}
		case <-time.After(2 * time.Second):
		}
	})

	configPath := filepath.Join(t.TempDir(), "mcp.yaml")
	content := fmt.Sprintf(`mcpServers:
  echo-test:
    name: echo-test
    description: remote echo test
    type: websocket
    url: ws://%s/mcp
    env: {}
    enabled: true
    disabled: false
    timeout: 30s
    maxRetry: 1
global:
  autoConnect: true
  healthCheckInterval: 1m
  connectTimeout: 10s
`, addr)
	require.NoError(t, os.WriteFile(configPath, []byte(content), 0o644))

	mgr := NewManager()
	require.NoError(t, mgr.LoadConfig(configPath))
	require.NoError(t, mgr.Start(context.Background()))
	t.Cleanup(func() {
		require.NoError(t, mgr.Stop())
	})

	status, err := mgr.GetMCPStatus("echo-test")
	require.NoError(t, err)
	assert.True(t, status.Connected)
	assert.GreaterOrEqual(t, status.ToolCount, 2)

	tool, err := mgr.FindTool("echo")
	require.NoError(t, err)
	assert.Equal(t, "echo-test", tool.MCPName)

	result, err := mgr.CallTool(context.Background(), "echo-test", "echo", map[string]interface{}{"message": "MANAGER_CONNECT_OK"})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotEmpty(t, result.Content)
	assert.Contains(t, result.Content[0].Text, "MANAGER_CONNECT_OK")
}

func reserveManagerLocalAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

func waitForManagerTCP(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for tcp listener on %s", addr)
}
