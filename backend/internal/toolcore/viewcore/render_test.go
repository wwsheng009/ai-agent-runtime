package viewcore

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
)

// renderTestRuntime is a surface with both optional extensions the render path
// uses: it answers a request itself, and it records what the handler told it
// about the answer.
type renderTestRuntime struct {
	*viewBenchRuntime
	rendered   *toolcore.Result
	delivered  []toolcore.ReadWindow
	remembered []toolcore.ReadWindow
}

func (r *renderTestRuntime) RenderContent(context.Context, toolcore.RenderRequest) (*toolcore.Result, bool) {
	if r.rendered == nil {
		return nil, false
	}
	return r.rendered, true
}

func (r *renderTestRuntime) ReadDelivered(_ string, _ string, window toolcore.ReadWindow) {
	r.delivered = append(r.delivered, window)
}

func (r *renderTestRuntime) ReadRemembered(_ string, _ string, window toolcore.ReadWindow) {
	r.remembered = append(r.remembered, window)
}

func (r *renderTestRuntime) RememberedStub(string, string, int, int) (*toolcore.Result, func(), bool) {
	return nil, nil, false
}

func renderedWindowResult(metadata map[string]any) *toolcore.Result {
	return &toolcore.Result{Success: true, OutputKind: toolcore.KindText, Content: "rendered", Metadata: metadata}
}

// TestRenderedWindowKeepsTheBookkeepingContract: a derived render (document,
// notebook) publishes the same window metadata as the text path, so it must be
// remembered under the same rule - and a window that folded a long line must
// never be, or the next identical read returns a stub for content the model
// never saw.
func TestRenderedWindowKeepsTheBookkeepingContract(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "derived.ipynb")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		metadata map[string]any
		want     int
	}{
		{
			name: "complete window",
			metadata: map[string]any{
				"offset": 0, "limit": 10, "lines_read": 10, "total_lines": 10,
				"eof": true, "is_truncated": false,
			},
			want: 1,
		},
		{
			name: "clamp-folded window",
			metadata: map[string]any{
				"offset": 0, "limit": 10, "lines_read": 10, "total_lines": 10,
				"eof": true, "is_truncated": false, "long_lines_truncated": 1,
			},
			want: 0,
		},
		{
			name: "window that stopped early",
			metadata: map[string]any{
				"offset": 0, "limit": 10, "lines_read": 10, "total_lines": 40,
				"eof": false, "is_truncated": true,
			},
			want: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runtime := &renderTestRuntime{
				viewBenchRuntime: &viewBenchRuntime{paths: viewBenchPaths{root: root}},
				rendered:         renderedWindowResult(tc.metadata),
			}
			handler := NewHandler(toolcore.Definition{Name: "view", DefaultOutputBudget: DefaultByteBudget})
			result, err := handler.Execute(context.Background(), &toolcore.Call{
				Args:    map[string]any{"file_path": path},
				Runtime: runtime,
			})
			if err != nil || result == nil || !result.Success {
				t.Fatalf("rendered read failed: err=%v result=%+v", err, result)
			}
			if result.Content != "rendered" {
				t.Fatalf("the rendered answer must be delivered as-is, got %q", result.Content)
			}
			if len(runtime.delivered) != 1 {
				t.Fatalf("a delivered render must be recorded in the ledger once, got %d", len(runtime.delivered))
			}
			if got := len(runtime.remembered); got != tc.want {
				t.Fatalf("remembered windows = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestRenderedResultWithoutWindowMetadataIsNotRemembered: an image attachment is
// not a window (it declares no lines_read), so it must not create a read-ledger
// entry that later makes a write look safe.
func TestRenderedResultWithoutWindowMetadataIsNotRemembered(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "shot.png")
	if err := os.WriteFile(path, []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := &renderTestRuntime{
		viewBenchRuntime: &viewBenchRuntime{paths: viewBenchPaths{root: root}},
		rendered: &toolcore.Result{
			Success:    true,
			OutputKind: toolcore.KindText,
			Content:    "图片文件: shot.png",
			Metadata:   map[string]any{"file_path": path, "image_passthrough": true},
		},
	}
	handler := NewHandler(toolcore.Definition{Name: "view", DefaultOutputBudget: DefaultByteBudget})
	result, err := handler.Execute(context.Background(), &toolcore.Call{
		Args:    map[string]any{"file_path": path},
		Runtime: runtime,
	})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("rendered read failed: err=%v result=%+v", err, result)
	}
	if len(runtime.delivered) != 0 || len(runtime.remembered) != 0 {
		t.Fatalf("an attachment must not be recorded as a read: delivered=%d remembered=%d",
			len(runtime.delivered), len(runtime.remembered))
	}
}
