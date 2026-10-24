package viewcore

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
)

func viewcoreCall(t *testing.T, root string, args map[string]any) *toolcore.Result {
	t.Helper()
	h := NewHandler(toolcore.Definition{Name: "view", DefaultOutputBudget: DefaultByteBudget})
	result, err := h.Execute(context.Background(), &toolcore.Call{
		Args:    args,
		Runtime: &viewBenchRuntime{paths: viewBenchPaths{root: root}},
	})
	if err != nil || result == nil {
		t.Fatalf("handler failed: err=%v result=%+v", err, result)
	}
	return result
}

// TestHandlerRefusesSpecialPath pins the refusal the handler owes a surface
// whose own resolver is happy to admit /dev/null: the guard is in the handler,
// not only in one sandbox, so a remote runtime refuses it the same way.
func TestHandlerRefusesSpecialPath(t *testing.T) {
	target := "NUL"
	if runtime.GOOS != "windows" {
		target = "/dev/null"
	}
	result := viewcoreCall(t, t.TempDir(), map[string]any{"file_path": target})

	if result.Success {
		t.Fatalf("expected a refusal, got %+v", result)
	}
	if result.ErrorCode != toolcore.ErrInvalidArgument {
		t.Fatalf("error code = %s, want %s", result.ErrorCode, toolcore.ErrInvalidArgument)
	}
	if result.Metadata["path_refused"] != true {
		t.Fatalf("expected path_refused metadata, got %#v", result.Metadata)
	}
	if reason, _ := result.Metadata["refusal_reason"].(string); reason == "" {
		t.Fatalf("expected refusal_reason metadata, got %#v", result.Metadata)
	}
	if !strings.Contains(result.Content, "ls/glob") {
		t.Fatalf("expected the recovery route in the message, got %q", result.Content)
	}
	if result.NextAction == "" {
		t.Fatal("expected a next_action on the refusal")
	}
}

// TestHandlerListsDirectories: pointing view at a directory lists it instead of
// failing, and says so in metadata.
func TestHandlerListsDirectories(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub", "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "a.txt"), []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result := viewcoreCall(t, root, map[string]any{"file_path": "sub"})
	if !result.Success {
		t.Fatalf("directory listing must succeed: %+v", result)
	}
	if result.Metadata["is_directory"] != true || result.Metadata["auto_listed"] != true {
		t.Fatalf("expected directory metadata, got %#v", result.Metadata)
	}
	if !strings.Contains(result.Content, "1: a.txt") || !strings.Contains(result.Content, "2: nested") {
		t.Fatalf("expected a shallow listing, got %q", result.Content)
	}
}

// TestHandlerNotesBinaryWindows: a NUL-heavy window becomes one MIME line with a
// recovery route instead of unreadable content handed to the model.
func TestHandlerNotesBinaryWindows(t *testing.T) {
	root := t.TempDir()
	payload := append([]byte{0x7F, 'E', 'L', 'F'}, make([]byte, 256)...)
	if err := os.WriteFile(filepath.Join(root, "blob.bin"), payload, 0o600); err != nil {
		t.Fatal(err)
	}

	result := viewcoreCall(t, root, map[string]any{"file_path": "blob.bin"})
	if !result.Success {
		t.Fatalf("binary note must not fail the call: %+v", result)
	}
	if result.Metadata["binary_note"] != true || result.Metadata["is_binary"] != true {
		t.Fatalf("expected binary note metadata, got %#v", result.Metadata)
	}
	if result.Metadata["file_size"] != int64(len(payload)) {
		t.Fatalf("file_size = %v, want %d", result.Metadata["file_size"], len(payload))
	}
	if !strings.Contains(result.Content, "MIME:") || !strings.Contains(result.Content, "download") {
		t.Fatalf("expected MIME and a recovery route, got %q", result.Content)
	}
}
