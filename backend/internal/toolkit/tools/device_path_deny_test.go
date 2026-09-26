package tools

import (
	stderrors "errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
)

// TestPathGuardExtendedDeviceNames pins the additions made when the guard was
// promoted from the read tool to every file tool: CONIN$/CONOUT$, the NT
// namespace prefix, and /dev/tty + /dev/console.
func TestPathGuardExtendedDeviceNames(t *testing.T) {
	for _, candidate := range []string{
		`C:\work\CONIN$`, "CONOUT$", `\??\C:\work\file`, "/dev/tty", "/dev/console",
		"/dev/tty/0",
	} {
		if reason := unsupportedPathNameReason(candidate); reason == "" {
			t.Fatalf("expected %q to be refused", candidate)
		}
	}
	// 普通扩展长度路径仍是合法文件路径（既有设计：只拒绝设备/GLOBALROOT）。
	if reason := unsupportedPathNameReason(`\\?\C:\dir\file.txt`); reason != "" {
		t.Fatalf("extended-length regular paths must stay usable, got %q", reason)
	}
	if reason := unsupportedPathNameReason("/dev/ttyS0"); reason != "" {
		t.Fatalf("serial device names are not the tty alias, got %q", reason)
	}
}

// TestCheckPathRefusesDevicePathWithoutSandbox proves the deny is a path-shape
// invariant: checkPath used to return nil immediately when no sandbox was
// configured, leaving write/edit/glob/grep unguarded.
func TestCheckPathRefusesDevicePathWithoutSandbox(t *testing.T) {
	policy := &sandboxPolicy{}
	target := "NUL"
	if runtime.GOOS != "windows" {
		target = "/dev/zero"
	}

	err := policy.checkPath(runtimeexecutor.OpWrite, target)
	if err == nil {
		t.Fatalf("expected %q to be refused without an active sandbox", target)
	}
	var carrier interface {
		GetContext() map[string]interface{}
	}
	if !stderrors.As(err, &carrier) {
		t.Fatalf("expected a runtime error with metadata, got %T", err)
	}
	ctx := carrier.GetContext()
	if ctx["policy"] != "device_path" || ctx["failure_class"] != "device_path" || ctx["path_refused"] != true {
		t.Fatalf("expected machine-readable device_path metadata, got %#v", ctx)
	}
	if next, _ := ctx["next_action"].(string); !strings.Contains(next, "ls/glob") {
		t.Fatalf("expected a recovery route, got %#v", ctx["next_action"])
	}

	if err := policy.checkPath(runtimeexecutor.OpWrite, filepath.Join(t.TempDir(), "note.txt")); err != nil {
		t.Fatalf("regular paths must pass without a sandbox: %v", err)
	}
}

// TestWriteRefusesDevicePathEndToEnd checks the promoted guard through a write
// tool call: no silent success, no file created.
func TestWriteRefusesDevicePathEndToEnd(t *testing.T) {
	base := t.TempDir()
	tool := NewWriteTool()
	tool.SetBasePath(base)

	target := "NUL"
	if runtime.GOOS != "windows" {
		target = "/dev/null"
	}
	result, err := tool.Execute(t.Context(), map[string]interface{}{
		"file_path": target,
		"content":   "should never be written",
	})
	if err != nil {
		t.Fatalf("unexpected outer error: %v", err)
	}
	if result == nil || result.Success {
		t.Fatalf("expected a refusal result for %q, got %+v", target, result)
	}
	if result.Error == nil || !strings.Contains(result.Error.Error(), "不支持的特殊文件") {
		t.Fatalf("expected the shared refusal message, got %v", result.Error)
	}
}

// TestViewKeepsPathRefusedMetadata guards the view-side ordering: view must
// still answer with its own structured refusal (path_refused/refusal_reason)
// rather than the generic checkPath error.
func TestViewKeepsPathRefusedMetadata(t *testing.T) {
	tool := NewViewTool()
	tool.SetBasePath(t.TempDir())

	target := "NUL"
	if runtime.GOOS != "windows" {
		target = "/dev/null"
	}
	result, err := tool.Execute(t.Context(), map[string]interface{}{"file_path": target})
	if err != nil {
		t.Fatalf("unexpected outer error: %v", err)
	}
	if result == nil || result.Success {
		t.Fatalf("expected a refusal result, got %+v", result)
	}
	if result.Metadata["path_refused"] != true {
		t.Fatalf("expected path_refused metadata, got %#v", result.Metadata)
	}
	if reason, _ := result.Metadata["refusal_reason"].(string); reason == "" {
		t.Fatalf("expected refusal_reason metadata, got %#v", result.Metadata)
	}
}
