package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	runtimetoolkit "github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// TestDeviceGuardCoversCleanedSpellings: "." segments and repeated separators
// name the same kernel object, so the name layer must not only match the
// canonical spelling (2026-09-27 review).
func TestDeviceGuardCoversCleanedSpellings(t *testing.T) {
	for _, candidate := range []string{
		"/proc/self/./fd/3",
		"/proc/self/fd/./3",
		"//proc//self//fd//3",
		"/dev/./zero",
		"/dev//null",
	} {
		if reason := unsupportedPathNameReason(candidate); reason == "" {
			t.Fatalf("expected %q to be refused", candidate)
		}
	}
	// 正常路径不能被清理逻辑误伤。
	for _, candidate := range []string{"/dev/ttyS0", "/proc/self/status"} {
		if reason := unsupportedPathNameReason(candidate); reason != "" {
			t.Fatalf("ordinary path %q was refused: %s", candidate, reason)
		}
	}
}

// TestWriteToolsRejectFIFOBeforeReading: edit/multiedit/append_write opened the
// target before any file-type check, so an unread FIFO blocked the tool forever
// (2026-09-27 review). The calls are bounded here so a regression fails the test
// instead of hanging the package.
func TestWriteToolsRejectFIFOBeforeReading(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("FIFO creation needs mkfifo")
	}
	mkfifo, err := exec.LookPath("mkfifo")
	if err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	root := t.TempDir()
	fifo := filepath.Join(root, "blocking.pipe")
	if out, err := exec.Command(mkfifo, fifo).CombinedOutput(); err != nil {
		t.Skipf("mkfifo failed: %v (%s)", err, string(out))
	}

	type writeTool interface {
		SetBasePath(string)
		Execute(context.Context, map[string]interface{}) (*runtimetoolkit.ToolResult, error)
	}
	calls := []struct {
		name    string
		newTool func(root string) writeTool
		params  map[string]interface{}
	}{
		{
			name: "edit",
			newTool: func(root string) writeTool {
				tool := NewEditTool()
				tool.SetBasePath(root)
				return tool
			},
			params: map[string]interface{}{"file_path": "blocking.pipe", "old_string": "a", "new_string": "b"},
		},
		{
			name: "multiedit",
			newTool: func(root string) writeTool {
				tool := NewMultieditTool()
				tool.SetBasePath(root)
				return tool
			},
			params: map[string]interface{}{"file_path": "blocking.pipe", "edits": []interface{}{
				map[string]interface{}{"old_string": "a", "new_string": "b"},
			}},
		},
		{
			name: "append_write",
			newTool: func(root string) writeTool {
				tool := NewAppendWriteTool()
				tool.SetBasePath(root)
				return tool
			},
			params: map[string]interface{}{"file_path": "blocking.pipe", "content": "x"},
		},
	}
	for _, call := range calls {
		t.Run(call.name, func(t *testing.T) {
			tool := call.newTool(root)
			done := make(chan struct{})
			go func() {
				defer close(done)
				_, _ = tool.Execute(context.Background(), call.params)
			}()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatalf("%s blocked on the FIFO instead of refusing it", call.name)
			}
		})
	}
}

// TestViewSpellingHealRespectsSymlinkTargetPolicy: the healed candidate passed
// the lexical sandbox check while pointing outside the allowlist, and the link
// target was read. The candidate must now be refused by the resolved-target
// check (2026-09-27 review).
func TestViewSpellingHealRespectsSymlinkTargetPolicy(t *testing.T) {
	t.Setenv("AICLI_VIEW_DEDUP", "")
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "outside.txt")
	if err := os.WriteFile(target, []byte("OUTSIDE-ALLOWLIST-CONTENT\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "report\u202Ffinal.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	tool := newViewToolAt(t, root)
	tool.SetSandbox(runtimeexecutor.NewSandbox(&runtimeexecutor.SandboxConfig{
		Enabled:      true,
		AllowedPaths: []string{root},
		DeniedPaths:  []string{outside},
	}))
	result, err := tool.Execute(context.Background(), map[string]interface{}{"file_path": "report final.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if result != nil && result.Success && strings.Contains(result.Content, "OUTSIDE-ALLOWLIST-CONTENT") {
		t.Fatalf("spelling healing followed a symlink out of the sandbox: %s", result.Content)
	}
}
