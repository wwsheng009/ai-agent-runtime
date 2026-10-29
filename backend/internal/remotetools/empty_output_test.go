package remotetools_test

import (
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

func TestRemoteShellReportsEmptySuccess(t *testing.T) {
	root := newTestWorkspace(t)
	cfg := newOpsConfig(t, root, []string{"*"})
	server, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	body := callTool(t, ctx, session, "remote_dev__shell", map[string]interface{}{
		"command": emptySuccessCommand(cfg.ResolvedShell.Type, root),
	})
	if !strings.Contains(body, "Command completed successfully with no output.") {
		t.Fatalf("empty success was not explained: %s", body)
	}
}

func TestRemoteShellPreservesBackslashPathsInOutput(t *testing.T) {
	root := newTestWorkspace(t)
	cfg := newOpsConfig(t, root, []string{"echo"})
	cfg.Limits.ModelVisibleCap = 1024
	server, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	command := `echo common\tenantctx ` + strings.Repeat("A", 2000)
	body := callTool(t, ctx, session, "remote_dev__shell", map[string]interface{}{
		"command": command,
	})
	if !strings.Contains(body, `common\tenantctx`) {
		t.Fatalf("backslash path was altered: %q", body)
	}
	if strings.Contains(body, "common\ttenantctx") {
		t.Fatalf("backslash was decoded as a tab: %q", body)
	}
}

func TestJobOutputReportsEmptyExit(t *testing.T) {
	root := newTestWorkspace(t)
	cfg := newJobsConfig(t, root, []string{"*"})
	server, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	body := callTool(t, ctx, session, "remote_dev__job_start", map[string]interface{}{
		"command": emptySuccessCommand(cfg.ResolvedShell.Type, root),
	})
	jobID := extractJobID(t, body)

	deadline := time.Now().Add(10 * time.Second)
	var output string
	for time.Now().Before(deadline) {
		output = callTool(t, ctx, session, "remote_dev__job_output", map[string]interface{}{
			"job_id": jobID,
		})
		if strings.Contains(output, "job exited with code") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(output, "no output; job exited with code 0") {
		t.Fatalf("empty job exit was not explained: %s", output)
	}
	if strings.Contains(output, "no output yet") {
		t.Fatalf("finished empty job still looks running: %s", output)
	}
}
