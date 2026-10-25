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

func runLocalCoreGrep(t *testing.T, root string, args map[string]interface{}) *toolkit.ToolResult {
	t.Helper()
	tool := tools.NewGrepTool()
	tool.SetBasePath(root)
	handler := searchcore.NewGrepHandler(toolcore.Definition{Name: "grep", Version: "1.0.0"})
	result, err := handler.Execute(context.Background(), &toolcore.Call{
		Args:    args,
		Runtime: tool.CoreRuntime(context.Background()),
	})
	if err != nil {
		t.Fatalf("local core grep: %v", err)
	}
	return toolkit.ResultToToolkit(result)
}

// TestLocalCoreRuntimeAgreesWithRemoteGrep is the "one implementation, two
// surfaces" proof: the same searchcore handler, fed through the local sandbox
// adapter and through the remote MCP server, must produce the same content and
// the same page metadata for the same arguments. The local side deliberately
// has no ripgrep extension, so agreement also proves the two engines render
// identically.
func TestLocalCoreRuntimeAgreesWithRemoteGrep(t *testing.T) {
	root := t.TempDir()
	for path, content := range map[string]string{
		"a.go":        "needle a\nmiss\n",
		"b.txt":       "needle b\nneedle again\n",
		"c.md":        "needle md\n",
		"nested/c.go": "needle c\n",
	} {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Modified-time ordering must be a total order with no tie, or the two
	// engines could legitimately disagree on the tie-break.
	stamps := map[string]time.Time{
		"a.go":        time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		"b.txt":       time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC),
		"c.md":        time.Date(2020, 1, 3, 0, 0, 0, 0, time.UTC),
		"nested/c.go": time.Date(2020, 1, 4, 0, 0, 0, 0, time.UTC),
	}
	for path, stamp := range stamps {
		if err := os.Chtimes(filepath.Join(root, filepath.FromSlash(path)), stamp, stamp); err != nil {
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
		{"plain", map[string]interface{}{"pattern": "needle", "path": "."}},
		{"ignore case", map[string]interface{}{"pattern": "NEEDLE", "ignore_case": true}},
		{"include", map[string]interface{}{"pattern": "needle", "include": "*.go"}},
		{"literal with glob", map[string]interface{}{"pattern": "needle", "glob": "*.txt", "literal": true}},
		{"files mode", map[string]interface{}{"pattern": "needle", "mode": "files"}},
		{"count mode", map[string]interface{}{"pattern": "needle", "mode": "count"}},
		{"context", map[string]interface{}{"pattern": "needle", "context": 1}},
		{"only matching", map[string]interface{}{"pattern": "needle", "only_matching": true}},
		{"patterns", map[string]interface{}{"patterns": []interface{}{"needle", "miss"}}},
		{"stats", map[string]interface{}{"pattern": "needle", "stats": true}},
		{"max count", map[string]interface{}{"pattern": "needle", "max_count": 1}},
		{"type filter", map[string]interface{}{"pattern": "needle", "type": "go"}},
		{"type add", map[string]interface{}{"pattern": "needle", "type": "doc", "type_add": "doc:*.md"}},
		{"type clear", map[string]interface{}{"pattern": "needle", "type": "go", "type_add": "go:*.txt", "type_clear": "go"}},
		{"sort path", map[string]interface{}{"pattern": "needle", "sort": "path"}},
		{"sort reverse path", map[string]interface{}{"pattern": "needle", "sortr": "path"}},
		{"sort modified", map[string]interface{}{"pattern": "needle", "sort": "modified"}},
		{"sort files", map[string]interface{}{"pattern": "needle", "sort_files": true}},
		{"passthru", map[string]interface{}{"pattern": "needle", "passthru": true}},
		{"paths", map[string]interface{}{"pattern": "needle", "paths": []interface{}{"nested", "."}, "glob": "*.go"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			remoteOut := callTool(t, ctx, session, "remote_dev__grep", tc.args)
			local := runLocalCoreGrep(t, root, tc.args)
			if !local.Success {
				t.Fatalf("local grep failed: %+v", local.Error)
			}
			if local.Content != remoteOut {
				t.Fatalf("local and remote content differ:\nlocal:\n%s\nremote:\n%s", local.Content, remoteOut)
			}
			if local.Metadata["matches"] == nil || local.Metadata["files_scanned"] == nil {
				t.Fatalf("local result lost the shared metadata: %+v", local.Metadata)
			}
		})
	}
}
