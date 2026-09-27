package tools

import (
	"bytes"
	"context"
	"runtime"
	"strings"
	"testing"
)

// TestRunGrepCommandKeepsStderrOutOfStdout 锁定 2026-09-27 修复：
// rg 的报错写在 stderr，不能混进 stdout 证据；否则「0 匹配 + stderr 报错」
// 会被 searchWithRipgrep 当成有输出，退化为 partial success 的伪成功。
func TestRunGrepCommandKeepsStderrOutOfStdout(t *testing.T) {
	var binary string
	var args []string
	if runtime.GOOS == "windows" {
		binary, args = "cmd", []string{"/c", "echo boom 1>&2 & exit /b 2"}
	} else {
		binary, args = "sh", []string{"-c", "echo boom >&2; exit 2"}
	}

	stdout, err := runGrepCommand(context.Background(), binary, t.TempDir(), args)
	if err == nil {
		t.Fatal("expected non-zero exit to surface as an error")
	}
	if len(bytes.TrimSpace(stdout)) != 0 {
		t.Fatalf("stderr must not be merged into stdout evidence, got %q", stdout)
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("stderr details should stay in the error for diagnostics, got %v", err)
	}
}
