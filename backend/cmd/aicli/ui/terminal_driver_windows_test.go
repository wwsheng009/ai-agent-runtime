//go:build windows

package ui

import (
	"os"
	"testing"
)

// TestEnsureConsoleUTF8OutputRedirectedNoOp 验证 stdout 不是控制台
// （管道/文件重定向）时，不切换全局输出代码页且 restorer 为 nil。
func TestEnsureConsoleUTF8OutputRedirectedNoOp(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "aicli-console-cp-*.txt")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer f.Close()

	restore := platformEnsureConsoleUTF8Output(f)
	if restore != nil {
		t.Fatalf("restore is non-nil, want nil for redirected (non-console) stdout")
	}
	// 只恢复它自己设过的状态；这里没设过，恢复函数必须为 nil。
}

// TestEnsureConsoleUTF8OutputNilNoOp 验证 nil stdout 空操作。
func TestEnsureConsoleUTF8OutputNilNoOp(t *testing.T) {
	if restore := platformEnsureConsoleUTF8Output(nil); restore != nil {
		t.Fatalf("restore is non-nil, want nil for nil stdout")
	}
}