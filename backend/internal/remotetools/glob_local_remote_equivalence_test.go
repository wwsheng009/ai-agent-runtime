package remotetools_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/searchcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit/tools"
)

func runLocalCoreGlob(t *testing.T, root string, args map[string]interface{}) *toolkit.ToolResult {
	t.Helper()
	tool := tools.NewGlobTool()
	tool.SetBasePath(root)
	handler := searchcore.NewGlobHandler(toolcore.Definition{Name: "glob", Version: "1.0.0"})
	result, err := handler.Execute(context.Background(), &toolcore.Call{
		Args:    args,
		Runtime: tool.CoreRuntime(context.Background()),
	})
	if err != nil {
		t.Fatalf("local core glob: %v", err)
	}
	return toolkit.ResultToToolkit(result)
}

// TestLocalCoreRuntimeAgreesWithRemoteGlob is the glob counterpart of the grep
// equivalence test: the same searchcore handler, driven through the local
// sandbox adapter and through the remote MCP server, must produce the same page
// for the same arguments. The adapter is what a local switch to the core handler
// would use, so agreement here is the precondition for deleting the legacy glob
// path rather than a claim that it is already deleted.
func TestLocalCoreRuntimeAgreesWithRemoteGlob(t *testing.T) {
	root := t.TempDir()
	base := time.Now().Add(-3 * time.Hour)
	files := []struct {
		path string
		mod  time.Duration
	}{
		{"a.go", 0},
		{"b.go", 10 * time.Minute},
		{"notes.txt", 20 * time.Minute},
		{"nested/c.go", 30 * time.Minute},
		{"nested/deep/d.go", 40 * time.Minute},
		{"nested/e.ts", 50 * time.Minute},
		{"MixedCase.TXT", 60 * time.Minute},
	}
	for _, file := range files {
		full := filepath.Join(root, filepath.FromSlash(file.path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		mod := base.Add(file.mod)
		if err := os.Chtimes(full, mod, mod); err != nil {
			t.Fatal(err)
		}
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
		{"recursive", map[string]interface{}{"pattern": "**/*.go"}},
		{"depth one", map[string]interface{}{"pattern": "*.go"}},
		{"paged first", map[string]interface{}{"pattern": "**/*.*", "limit": 2}},
		{"paged second", map[string]interface{}{"pattern": "**/*.*", "limit": 2, "offset": 2}},
		{"ignore case", map[string]interface{}{"pattern": "*.txt", "case_insensitive": true}},
		{"sub path", map[string]interface{}{"pattern": "**/*.go", "path": "nested"}},
		{"brace", map[string]interface{}{"pattern": "**/*.{go,ts}"}},
		{"no match", map[string]interface{}{"pattern": "**/*.missing"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			remoteOut := callTool(t, ctx, session, "remote_dev__glob", tc.args)
			local := runLocalCoreGlob(t, root, tc.args)
			if !local.Success {
				t.Fatalf("local glob failed: %+v", local.Error)
			}
			if local.Content != remoteOut {
				t.Fatalf("local and remote content differ:\nlocal:\n%s\nremote:\n%s", local.Content, remoteOut)
			}
		})
	}
}
