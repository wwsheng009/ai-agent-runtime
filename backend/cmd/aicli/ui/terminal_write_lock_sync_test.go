package ui

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

// captureSyncFrameStdout redirects os.Stdout for the duration of fn and returns
// everything written through the raw stdout path. The redirect must be in place
// before fn runs.
func captureSyncFrameStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	fn()
	_ = w.Close()
	os.Stdout = old
	out := <-done
	_ = r.Close()
	return out
}

// TestWithTerminalWriteLockNeverWrapsBatches：DEC 2026 帧包裹属已退役的
// legacy surface 路径（L3-1 整链路删除）。通用写锁只负责串行化，不再产生
// 任何 2026 括号（历史「legacy 开启 / 环境急停」断言收敛为「永不包裹」）。
func TestWithTerminalWriteLockNeverWrapsBatches(t *testing.T) {
	out := captureSyncFrameStdout(t, func() {
		WithTerminalWriteLock(func() {
			fmt.Print("row-1\n")
			fmt.Print("row-2\n")
		})
	})
	if out != "row-1\nrow-2\n" {
		t.Fatalf("write-lock batches must not be wrapped: got %q", out)
	}
	if strings.Contains(out, "?2026") {
		t.Fatalf("DEC 2026 brackets must not be emitted: %q", out)
	}
}

func TestTerminalDriver_SynchronizedOutputConservativeOffNonTTY(t *testing.T) {
	// A driver over the test process' pipes is not an interactive TTY, so the
	// conservative default must leave synchronized output off.
	d := NewTerminalDriver(os.Stdin, os.Stdout)
	if d.Capabilities().SynchronizedOutput {
		t.Fatal("non-interactive terminal must not advertise SynchronizedOutput")
	}
}
