package remotetools_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

// TestRemoteShellResolvesGoBuildCache reproduces the failure that motivated
// environment inheritance: with a narrow allowlist Go could not see
// LOCALAPPDATA, so its default GOCACHE lookup failed inside the remote shell.
func TestRemoteShellResolvesGoBuildCache(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go is not on PATH: %v", err)
	}
	local, err := exec.Command("go", "env", "GOCACHE").Output()
	if err != nil {
		t.Skipf("local go env GOCACHE failed: %v", err)
	}
	want := strings.TrimSpace(string(local))
	if want == "" {
		t.Skip("local go env GOCACHE is empty")
	}

	root := newTestWorkspace(t)
	server, err := remotetools.NewServer(newOpsConfig(t, root, []string{"go"}), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	body := callTool(t, ctx, session, "remote_dev__shell", map[string]interface{}{
		"command": "go env GOCACHE",
	})
	if !strings.Contains(body, want) {
		t.Fatalf("remote shell GOCACHE = %q, want %q", strings.TrimSpace(body), want)
	}
}
