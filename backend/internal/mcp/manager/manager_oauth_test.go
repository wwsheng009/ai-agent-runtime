package manager

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/auth"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/client"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
)

func oauthTestConfig() *config.Config {
	return &config.Config{
		MCPServers: map[string]config.MCPConfig{
			"oauth-mcp": {
				Name:    "oauth-mcp",
				Type:    "streamable",
				URL:     "http://127.0.0.1:1/mcp",
				Enabled: true,
				Timeout: config.Duration{Duration: time.Second},
				Auth:    &config.MCPAuthConfig{Type: "oauth"},
			},
		},
		Global: config.GlobalConfig{ConnectTimeout: config.Duration{Duration: time.Second}},
	}
}

// 未登录的 oauth server 必须被隔离为「需认证」：不发起连接、状态可行动。
func TestManagerOAuthWithoutTokenIsolatesServer(t *testing.T) {
	t.Setenv("AICLI_MCP_TOKENS_FILE", filepath.Join(t.TempDir(), "mcp-tokens.json"))

	mgr := newTestManager(oauthTestConfig())
	connectCalls := 0
	mgr.newClient = func(name string, _ *config.MCPConfig) (client.Client, error) {
		connectCalls++
		return &fakeClient{name: name}, nil
	}

	require.NoError(t, mgr.Start(context.Background()))

	status, err := mgr.GetMCPStatus("oauth-mcp")
	require.NoError(t, err)
	require.True(t, status.RequiresAuth)
	require.False(t, status.Connected)
	require.Contains(t, status.LastError, "aicli mcp auth oauth-mcp")
	require.Zero(t, connectCalls, "未登录时不应尝试连接")
	require.Empty(t, mgr.ListTools())
}

// 已有未过期令牌时正常连接，并把 TokenSource 注入给传输层。
func TestManagerOAuthWithTokenConnectsAndInjectsProvider(t *testing.T) {
	t.Setenv("AICLI_MCP_TOKENS_FILE", filepath.Join(t.TempDir(), "mcp-tokens.json"))
	store, err := auth.NewTokenStore("")
	require.NoError(t, err)
	require.NoError(t, store.Put("oauth-mcp", &auth.Token{
		ServerURL:    "http://127.0.0.1:1/mcp",
		AccessToken:  "access-1",
		RefreshToken: "refresh-1",
		ExpiresAt:    time.Now().Add(time.Hour),
	}))

	mgr := newTestManager(oauthTestConfig())
	var gotProvider config.AccessTokenProvider
	connectCalls := 0
	mgr.newClient = func(name string, cfg *config.MCPConfig) (client.Client, error) {
		connectCalls++
		gotProvider = cfg.TokenSource
		return &fakeClient{name: name}, nil
	}

	require.NoError(t, mgr.Start(context.Background()))

	status, err := mgr.GetMCPStatus("oauth-mcp")
	require.NoError(t, err)
	require.True(t, status.Connected)
	require.False(t, status.RequiresAuth)
	require.Empty(t, status.LastError)
	require.Equal(t, 1, connectCalls)
	require.NotNil(t, gotProvider, "oauth server 必须携带 TokenSource")
	token, err := gotProvider.AccessToken(context.Background())
	require.NoError(t, err)
	require.Equal(t, "access-1", token)
}

// 令牌文件不可用时（如损坏）应把该 server 标记为需认证，而不是让 Start 失败。
func TestManagerOAuthBrokenTokenFileIsolatesServer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp-tokens.json")
	require.NoError(t, writeFile(path, "{broken"))
	t.Setenv("AICLI_MCP_TOKENS_FILE", path)

	mgr := newTestManager(oauthTestConfig())
	connectCalls := 0
	mgr.newClient = func(name string, _ *config.MCPConfig) (client.Client, error) {
		connectCalls++
		return &fakeClient{name: name}, nil
	}

	require.NoError(t, mgr.Start(context.Background()))
	status, err := mgr.GetMCPStatus("oauth-mcp")
	require.NoError(t, err)
	require.True(t, status.RequiresAuth)
	require.Contains(t, status.LastError, "OAuth 初始化失败")
	require.Zero(t, connectCalls)
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}
