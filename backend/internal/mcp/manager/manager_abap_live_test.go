package manager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManager_ABAPAdtMCP_LoginSmoke(t *testing.T) {
	if os.Getenv("LIVE_ABAP_MCP_TEST") != "1" {
		t.Skip("set LIVE_ABAP_MCP_TEST=1 to enable ABAP ADT MCP smoke test")
	}

	entry := `E:/projects/abap/mcp-abap-abap-adt-api/dist/index.js`
	if _, err := os.Stat(entry); err != nil {
		t.Skipf("abap mcp entry not found: %v", err)
	}

	configPath := filepath.Join(t.TempDir(), "mcp-abap.json")
	config := fmt.Sprintf(`{
  "mcpServers": {
    "ecc1809": {
      "command": "node",
      "args": [
        %q
      ],
      "env": {
        "MCP_TOOLS": "standard"
      },
      "disabled": false,
      "enabled": true,
      "type": "stdio",
      "timeout": "60s",
      "maxRetry": 1
    }
  },
  "global": {
    "autoConnect": true,
    "healthCheckInterval": "1m",
    "connectTimeout": "10s"
  }
}`, filepath.ToSlash(entry))
	require.NoError(t, os.WriteFile(configPath, []byte(config), 0o644))

	mgr := NewManager()
	require.NoError(t, mgr.LoadConfig(configPath))
	require.NoError(t, mgr.Start(context.Background()))
	t.Cleanup(func() {
		_ = mgr.Stop()
	})

	tools := mgr.ListTools()
	require.NotEmpty(t, tools)
	t.Logf("abap mcp exposed %d tools", len(tools))
	toolNames := make([]string, 0, len(tools))
	for _, tool := range tools {
		toolNames = append(toolNames, tool.Tool.Name)
	}
	t.Logf("abap mcp tools: %v", toolNames)

	loginTool, err := mgr.FindTool("login")
	require.NoError(t, err)
	assert.Equal(t, "ecc1809", loginTool.MCPName)

	loginResult, err := mgr.CallTool(context.Background(), "ecc1809", "login", map[string]interface{}{})
	require.NoError(t, err)
	require.NotNil(t, loginResult)
	require.NotEmpty(t, loginResult.Content)
	t.Logf("login result: %s", loginResult.Content[0].Text)

	logoutResult, err := mgr.CallTool(context.Background(), "ecc1809", "logout", map[string]interface{}{})
	if err == nil && logoutResult != nil && len(logoutResult.Content) > 0 {
		t.Logf("logout result: %s", logoutResult.Content[0].Text)
	}
}
