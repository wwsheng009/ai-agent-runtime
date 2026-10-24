package viewcore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
)

func writeBatchFixture(t *testing.T, root string) {
	t.Helper()
	files := map[string]string{
		"a.txt": "a1\na2\na3\n",
		"b.txt": "b1\nb2\nb3\nb4\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// TestHandlerBatchFilesAndCompact proves files[]/compact are handler-level
// features of the shared core, not surface-specific additions: the same
// handler that serves the local tool serves the remote runtime.
func TestHandlerBatchFilesAndCompact(t *testing.T) {
	root := t.TempDir()
	writeBatchFixture(t, root)

	h := NewHandler(toolcore.Definition{Name: "view"})
	runtime := &viewBenchRuntime{paths: viewBenchPaths{root: root}}
	result, err := h.Execute(context.Background(), &toolcore.Call{
		Args: map[string]any{
			"files": []any{
				map[string]any{"file_path": "a.txt"},
				map[string]any{"file_path": "b.txt", "offset": 1, "limit": 1},
			},
			"compact": true,
		},
		Runtime: runtime,
	})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("batch view failed: err=%v result=%+v", err, result)
	}
	for _, want := range []string{"===== a.txt =====", "1: a1", "===== b.txt =====", "2: b2", "lines_read=1"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("batch output missing %q:\n%s", want, result.Content)
		}
	}
	if count, _ := result.Metadata["request_count"].(int); count != 2 {
		t.Fatalf("request_count = %v, want 2", result.Metadata["request_count"])
	}
	items, _ := result.Metadata["items"].([]map[string]interface{})
	if len(items) != 2 {
		t.Fatalf("items metadata = %#v, want 2 entries", result.Metadata["items"])
	}
}

// TestHandlerBatchKeepsGoingAfterOneBadFile pins the batch contract: one
// missing file is reported in place and the remaining files still render.
func TestHandlerBatchKeepsGoingAfterOneBadFile(t *testing.T) {
	root := t.TempDir()
	writeBatchFixture(t, root)

	h := NewHandler(toolcore.Definition{Name: "view"})
	runtime := &viewBenchRuntime{paths: viewBenchPaths{root: root}}
	result, err := h.Execute(context.Background(), &toolcore.Call{
		Args: map[string]any{"files": []any{
			map[string]any{"file_path": "missing.txt"},
			map[string]any{"file_path": "a.txt"},
		}},
		Runtime: runtime,
	})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("batch view failed: err=%v result=%+v", err, result)
	}
	if !strings.Contains(result.Content, "missing.txt") || !strings.Contains(result.Content, "1: a1") {
		t.Fatalf("batch must report the failure and keep the successful file:\n%s", result.Content)
	}
	if failed, _ := result.Metadata["failed_count"].(int); failed != 1 {
		t.Fatalf("failed_count = %v, want 1", result.Metadata["failed_count"])
	}
}

// TestHandlerRequiresPathOrFiles keeps the canonical schema (required: [])
// honest: the handler, not the schema, rejects a call that names no file.
func TestHandlerRequiresPathOrFiles(t *testing.T) {
	h := NewHandler(toolcore.Definition{Name: "view"})
	runtime := &viewBenchRuntime{paths: viewBenchPaths{root: t.TempDir()}}
	result, err := h.Execute(context.Background(), &toolcore.Call{Args: map[string]any{}, Runtime: runtime})
	if err != nil || result == nil || result.Success {
		t.Fatalf("expected argument error, got err=%v result=%+v", err, result)
	}
	if result.ErrorCode != toolcore.ErrInvalidArgument {
		t.Fatalf("error code = %s, want %s", result.ErrorCode, toolcore.ErrInvalidArgument)
	}
}
