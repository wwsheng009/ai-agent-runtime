package commands

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestReadPipeInteractiveLineFromPipe 验证 stdin 是管道（MobaXterm/cygwin
// pty 等在 Windows 侧的呈现）时，pipe 逐键编辑器分支能从管道读取一行文本
// 并正常提交，而不会把内容原样吞掉。
func TestReadPipeInteractiveLineFromPipe(t *testing.T) {
	origStdin := os.Stdin
	defer func() { os.Stdin = origStdin }()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer r.Close()
	os.Stdin = r
	if _, err := w.WriteString("hello world\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	w.Close()

	if !pipeConsoleLineEditorSupported() {
		t.Skipf("stdout 不是管道/字符设备（%v），跳过", os.Stdout)
	}
	line, ok, err := readPipeInteractiveLineFn(context.Background())
	if err != nil {
		t.Fatalf("readPipeInteractiveLineFn: %v", err)
	}
	if !ok {
		t.Fatalf("expected pipe editor to take over, got ok=false")
	}
	if strings.TrimRight(line, "\r\n") != "hello world" {
		t.Fatalf("unexpected line: %q", line)
	}
}

// TestPipeConsoleLineEditorSupported_PlainFile 验证 stdout 重定向到普通文件时
// 不会启用 pipe 逐键编辑器（保持原有 buffered 回退读取行为）。
func TestPipeConsoleLineEditorSupported_PlainFile(t *testing.T) {
	origStdout := os.Stdout
	defer func() { os.Stdout = origStdout }()

	f, err := os.CreateTemp(t.TempDir(), "stdout-dump-*.txt")
	if err != nil {
		t.Fatalf("os.CreateTemp: %v", err)
	}
	defer f.Close()
	os.Stdout = f
	if pipeConsoleLineEditorSupported() {
		t.Fatalf("expected pipe editor disabled when stdout is a plain file")
	}
}