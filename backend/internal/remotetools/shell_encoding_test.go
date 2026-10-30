package remotetools_test

import (
	"strings"
	"testing"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

// TestRemoteShellPreservesUTF8Output pins the Windows code-page regression:
// PowerShell inherits the host's GBK console encoding unless the shell payload
// sets [Console]::OutputEncoding=UTF8 first. The remote shell now always runs
// through the background job manager, so both paths must keep that prefix.
func TestRemoteShellPreservesUTF8Output(t *testing.T) {
	root := newTestWorkspace(t)
	cfg := newOpsConfig(t, root, []string{"echo"})
	if cfg.ResolvedShell.Type != runtimeexecutor.ShellTypePwsh &&
		cfg.ResolvedShell.Type != runtimeexecutor.ShellTypePowerShell {
		t.Skipf("UTF-8 regression only applies to PowerShell, got %q", cfg.ResolvedShell.Type)
	}

	server, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	const want = "中文编码-OK"
	body := callTool(t, ctx, session, "remote_dev__shell", map[string]interface{}{
		"command": "echo " + want,
	})
	if !strings.Contains(body, want) {
		t.Fatalf("remote shell output was not UTF-8:\n%q", body)
	}
}
