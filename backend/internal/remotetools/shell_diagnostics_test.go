package remotetools_test

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

// TestRemoteShellCommandDiagnosticsDoNotChangeBody pins the P1-6 diagnostic
// contract: the metadata records the exact command bytes (TAB vs backslash),
// while the model-visible body remains the raw shell output.
func TestRemoteShellCommandDiagnosticsDoNotChangeBody(t *testing.T) {
	root := newTestWorkspace(t)
	cfg := newOpsConfig(t, root, []string{"*"})
	server, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	t.Run("literal backslash stays in the command and body", func(t *testing.T) {
		result, body := callShellWithResult(t, ctx, session, `Write-Output 'common\tenantctx'`)
		if !strings.Contains(body, `common\tenantctx`) {
			t.Fatalf("body lost the backslash: %q", body)
		}
		if strings.Contains(body, "common\ttenantctx") {
			t.Fatalf("body decoded the backslash as a TAB: %q", body)
		}
		if got := boolMeta(t, result.Meta, "command_has_tab"); got {
			t.Fatalf("command_has_tab = true for a literal backslash: %#v", result.Meta)
		}
		if got := boolMeta(t, result.Meta, "command_has_backslash"); !got {
			t.Fatalf("command_has_backslash = false: %#v", result.Meta)
		}
		escaped, _ := result.Meta["command_escaped"].(string)
		if !strings.Contains(escaped, `common\\tenantctx`) {
			t.Fatalf("command_escaped = %q, want a doubled backslash", escaped)
		}
	})

	t.Run("literal tab is visible as a client input defect", func(t *testing.T) {
		result, _ := callShellWithResult(t, ctx, session, "Write-Output 'a\tb'")
		if got := boolMeta(t, result.Meta, "command_has_tab"); !got {
			t.Fatalf("command_has_tab = false for a literal TAB: %#v", result.Meta)
		}
		if got := boolMeta(t, result.Meta, "command_has_backslash"); got {
			t.Fatalf("command_has_backslash = true for a TAB-only command: %#v", result.Meta)
		}
		escaped, _ := result.Meta["command_escaped"].(string)
		if !strings.Contains(escaped, `a\tb`) {
			t.Fatalf("command_escaped = %q, want a quoted TAB", escaped)
		}
	})
}

func callShellWithResult(
	t *testing.T,
	ctx context.Context,
	session *mcp.ClientSession,
	command string,
) (*mcp.CallToolResult, string) {
	t.Helper()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "remote_dev__shell",
		Arguments: map[string]interface{}{
			"command": command,
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result == nil || len(result.Content) == 0 {
		t.Fatalf("empty tool result: %#v", result)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content type = %T, want *mcp.TextContent", result.Content[0])
	}
	return result, text.Text
}

func boolMeta(t *testing.T, meta map[string]any, key string) bool {
	t.Helper()
	value, ok := meta[key].(bool)
	if !ok {
		t.Fatalf("metadata[%q] = %#v, want bool", key, meta[key])
	}
	return value
}
