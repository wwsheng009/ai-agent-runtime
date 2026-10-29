package remotetools_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

// TestMCPDescriptionsNameResolvedShell proves the model-facing prompt is built
// from the shell actually selected at startup, not a static "cmd.exe" hint.
func TestMCPDescriptionsNameResolvedShell(t *testing.T) {
	root := newTestWorkspace(t)
	cfg := newJobsConfig(t, root, []string{"echo"})
	server, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	want := []string{"Shell:", string(cfg.ResolvedShell.Type), filepath.Base(cfg.ResolvedShell.Path)}
	for _, name := range []string{"remote_dev__shell", "remote_dev__job_start"} {
		var description string
		for _, tool := range tools.Tools {
			if tool.Name == name {
				description = tool.Description
				break
			}
		}
		if description == "" {
			t.Fatalf("tool %s is missing", name)
		}
		for _, fragment := range want {
			if !strings.Contains(description, fragment) {
				t.Fatalf("%s description lacks %q:\n%s", name, fragment, description)
			}
		}
	}
}
