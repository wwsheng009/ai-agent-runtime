package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestManager_EnvDBackend_LiveSmoke drives the real envd backend (aicli-remote-tools
// serve --stdio --backend envd) through the MCP manager against a real envd target.
// It is the manager-level e2e that unit tests cannot cover: config load → child
// process spawn → MCP handshake → tool call.
//
// Gated because it needs both a built server binary and a reachable envd:
//
//	AICLI_ENVD_TEST_SERVER    absolute path to aicli-remote-tools(.exe)
//	ENVD_TEST_ADDR            envd host:port (e.g. 10.0.0.9:49983)
//	ENVD_TEST_USER            OS user envd executes as (e.g. n9e)
//	ENVD_TEST_WORKSPACE       absolute workspace on the target (e.g. /home/n9e)
//	ENVD_TEST_TOKEN           access token (optional when envd has none)
func TestManager_EnvDBackend_LiveSmoke(t *testing.T) {
	server := strings.TrimSpace(os.Getenv("AICLI_ENVD_TEST_SERVER"))
	addr := strings.TrimSpace(os.Getenv("ENVD_TEST_ADDR"))
	user := strings.TrimSpace(os.Getenv("ENVD_TEST_USER"))
	workspace := strings.TrimSpace(os.Getenv("ENVD_TEST_WORKSPACE"))
	if server == "" || addr == "" || user == "" || workspace == "" {
		t.Skip("set AICLI_ENVD_TEST_SERVER, ENVD_TEST_ADDR, ENVD_TEST_USER and ENVD_TEST_WORKSPACE to run")
	}

	args := []string{
		"serve", "--stdio", "--backend", "envd",
		"--addr", addr,
		"--user", user,
		"--workspace", workspace,
		"--profile", "readonly",
		"--tool-prefix", "remote_envd",
		// The envd backend's token-env has no default, so without this flag the
		// child never reads the env var the config below sets: the MCP handshake
		// still succeeds, and every tool call comes back as an HTTP 401 error.
		"--token-env", "AICLI_ENVD_TOKEN",
	}
	if caFile := strings.TrimSpace(os.Getenv("ENVD_TEST_CA_FILE")); caFile != "" {
		args = append(args, "--ca-file", caFile)
	}
	argsJSON, err := json.Marshal(args)
	require.NoError(t, err)

	configPath := filepath.Join(t.TempDir(), "envd-mcp.json")
	config := fmt.Sprintf(`{
  "mcpServers": {
    "envdtest": {
      "command": %q,
      "args": %s,
      "env": {"AICLI_ENVD_TOKEN": %q},
      "enabled": true,
      "type": "stdio",
      "timeout": "30s",
      "maxRetry": 1
    }
  },
  "global": {
    "healthCheckInterval": "1m",
    "connectTimeout": "15s"
  }
}`, server, argsJSON, os.Getenv("ENVD_TEST_TOKEN"))
	require.NoError(t, os.WriteFile(configPath, []byte(config), 0o644))

	mgr := NewManager()
	require.NoError(t, mgr.LoadConfig(configPath))
	require.NoError(t, mgr.Start(context.Background()))
	t.Cleanup(func() { _ = mgr.Stop() })

	tools := mgr.ListTools()
	require.NotEmpty(t, tools, "envd backend exposed no tools")

	var listDirTool string
	for _, tool := range tools {
		name := tool.Tool.Name
		require.True(t, strings.HasPrefix(name, "remote_envd__"),
			"tool %q should carry the configured prefix", name)
		if strings.HasSuffix(name, "__list_dir") {
			listDirTool = name
		}
	}
	require.NotEmpty(t, listDirTool, "envd backend did not expose a list_dir tool")

	result, err := mgr.CallTool(context.Background(), "envdtest", listDirTool, map[string]interface{}{"path": "."})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotEmpty(t, result.Content)
	text := result.Content[0].Text
	// A handshake-only success is not a success: a 401 comes back as a normal
	// content block, so checking "content is non-empty" alone would let the
	// broken wiring above pass. The result must be a real listing.
	require.False(t, result.IsError, "tool call returned an error result: %.400s", text)
	require.NotContains(t, text, "INTERNAL_ERROR", "tool call surfaced a failure: %.400s", text)
	t.Logf("%s returned %d content block(s), first: %.200s", listDirTool, len(result.Content), text)
}
