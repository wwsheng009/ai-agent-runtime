package remotetools_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit/tools"
)

func runLocalLs(t *testing.T, root string, args map[string]interface{}) *toolkit.ToolResult {
	t.Helper()
	tool := tools.NewLsTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("local ls execute: %v", err)
	}
	return result
}

// TestLocalCoreRuntimeAgreesWithRemoteLs drives the public local entry point and
// the remote MCP surface with the same arguments. ls runs one handler, so any
// difference here is a real surface divergence rather than a second
// implementation being exercised.
func TestLocalCoreRuntimeAgreesWithRemoteLs(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"src/nested", "docs"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(dir)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{"README.md", "src/main.go", "src/nested/deep.go", "docs/guide.md"} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(file)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	server, err := remotetools.NewServer(newTestConfig(t, root), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	cases := []struct {
		name string
		args map[string]interface{}
	}{
		{"root one level", map[string]interface{}{}},
		{"depth two", map[string]interface{}{"depth": 2}},
		{"depth three sub path", map[string]interface{}{"path": "src", "depth": 3}},
		{"empty dir", map[string]interface{}{"path": "empty"}},
		{"file path", map[string]interface{}{"path": "README.md"}},
		{"limit one", map[string]interface{}{"limit": 1}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			remoteOut := callTool(t, ctx, session, "remote_dev__ls", tc.args)
			local := runLocalLs(t, root, tc.args)
			if !local.Success {
				t.Fatalf("local ls failed: %+v", local.Error)
			}
			if local.Content != remoteOut {
				t.Fatalf("local and remote content differ:\nlocal:\n%s\nremote:\n%s", local.Content, remoteOut)
			}
		})
	}
}

// TestRemoteLsMissingPathIsClassified pins that a missing directory is a
// NOT_FOUND failure with the local message shape, not an empty listing: the two
// lead a caller to different next actions.
func TestRemoteLsMissingPathIsClassified(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "project", "settings"), 0o755); err != nil {
		t.Fatal(err)
	}
	server, err := remotetools.NewServer(newTestConfig(t, root), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	local := runLocalLs(t, root, map[string]interface{}{"path": "project/setting"})
	if local.Success {
		t.Fatalf("expected local ls to fail on a missing path")
	}
	if !strings.Contains(local.Error.Error(), "路径不存在") || !strings.Contains(local.Error.Error(), "settings") {
		t.Fatalf("local error must name the path and its candidate: %v", local.Error)
	}

	// The remote surface must reach the same classification with the same
	// message shape; only the candidate's root differs (each surface's workspace).
	remoteOut := callToolExpectingError(t, ctx, session, "remote_dev__ls", map[string]interface{}{"path": "project/setting"})
	if !strings.Contains(remoteOut, "路径不存在") {
		t.Fatalf("remote error must use the same path message, got %q", remoteOut)
	}
	if !strings.Contains(remoteOut, "settings") {
		t.Fatalf("remote error must carry the candidate hint, got %q", remoteOut)
	}
}
