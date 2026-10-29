package remotetools_test

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

func TestRemoteShellPreservesNativeExitCode(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("cmd.exe exit code fixture")
	}
	root := newTestWorkspace(t)
	server, err := remotetools.NewServer(newOpsConfig(t, root, []string{"cmd"}), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "remote_dev__shell",
		Arguments: map[string]interface{}{"command": "cmd /c exit 3"},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected a non-zero command to be an error result: %s", textContent(result))
	}
	body := textContent(result)
	if !strings.Contains(body, "exit status 3") {
		t.Fatalf("real exit code was lost: %s", body)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if !strings.Contains(string(encoded), `"exit_code":3`) {
		t.Fatalf("metadata lost exit_code=3: %s", encoded)
	}
}

func TestRemoteShellFailureIncludesStderr(t *testing.T) {
	root := newTestWorkspace(t)
	command := "cat /aicli-definitely-missing-file-xyz"
	if runtime.GOOS == "windows" {
		command = `type C:\aicli-definitely-missing-file-xyz`
	}
	server, err := remotetools.NewServer(newOpsConfig(t, root, []string{strings.Fields(command)[0]}), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "remote_dev__shell",
		Arguments: map[string]interface{}{"command": command},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !result.IsError {
		t.Fatalf("expected a missing file to fail: %s", textContent(result))
	}
	body := textContent(result)
	if !strings.Contains(body, "COMMAND_FAILED") {
		t.Fatalf("failure code missing: %s", body)
	}
	if !strings.Contains(body, "aicli-definitely-missing-file-xyz") {
		t.Fatalf("stderr/path detail missing: %s", body)
	}
}
