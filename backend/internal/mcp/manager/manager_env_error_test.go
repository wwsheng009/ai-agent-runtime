package manager

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
)

// EnvError 的 server 必须被隔离：不发起连接、状态里带出可行动的 LastError，
// 且不影响同一配置里的其它 server。
func TestManagerStartSkipsServerWithEnvError(t *testing.T) {
	cfg := &config.Config{
		MCPServers: map[string]config.MCPConfig{
			"bad-env": {
				Name:     "bad-env",
				Type:     "streamable",
				URL:      "https://example.com/mcp",
				Enabled:  true,
				EnvError: "引用未设置的环境变量: TOKEN（可写成 ${VAR:-默认值} 提供默认值，或 $${VAR} 表示字面量）",
			},
			"good": {
				Name:    "good",
				Type:    "stdio",
				Enabled: true,
				Timeout: config.Duration{Duration: time.Second},
			},
		},
		Global: config.GlobalConfig{ConnectTimeout: config.Duration{Duration: time.Second}},
	}
	mgr := newTestManager(cfg)
	defer mgr.Stop()

	require.NoError(t, mgr.Start(context.Background()))

	bad, err := mgr.GetMCPStatus("bad-env")
	require.NoError(t, err)
	require.True(t, bad.Enabled)
	require.False(t, bad.Connected)
	require.Contains(t, bad.LastError, "TOKEN")

	good, err := mgr.GetMCPStatus("good")
	require.NoError(t, err)
	require.True(t, good.Connected)
	require.Empty(t, good.LastError)
}
