//go:build windows

package ui

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"
)

func TestWaitForInteractiveInputReady_InvalidHandleIsUnsupported(t *testing.T) {
	ready, err := waitForInteractiveInputReady(-1, 0)
	if ready {
		t.Fatal("expected invalid handle not to report ready")
	}
	if err != errInteractiveInputReadinessUnsupported {
		t.Fatalf("expected unsupported readiness error, got %v", err)
	}
}

func TestWaitForInteractiveInputReady_DetectsPipeInput(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer reader.Close()
	defer writer.Close()

	ready, err := waitForInteractiveInputReady(int(reader.Fd()), time.Millisecond)
	if err != nil {
		t.Fatalf("waitForInteractiveInputReady before write: %v", err)
	}
	if ready {
		t.Fatal("expected empty pipe not to report ready")
	}

	if _, err := writer.Write([]byte("x")); err != nil {
		t.Fatalf("write pipe: %v", err)
	}
	ready, err = waitForInteractiveInputReady(int(reader.Fd()), time.Second)
	if err != nil {
		t.Fatalf("waitForInteractiveInputReady after write: %v", err)
	}
	if !ready {
		t.Fatal("expected pipe with buffered input to report ready")
	}
}

func TestNextInteractiveKeyReadsFromPipeBackedStdin(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer reader.Close()
	defer writer.Close()

	result := make(chan struct {
		key editorKey
		ok  bool
		err error
	}, 1)
	pending := make([]byte, 0, 16)

	go func() {
		key, ok, err := nextInteractiveKey(context.Background(), reader, &pending, reader)
		result <- struct {
			key editorKey
			ok  bool
			err error
		}{key: key, ok: ok, err: err}
	}()

	time.Sleep(20 * time.Millisecond)
	if _, err := writer.Write([]byte("x")); err != nil {
		t.Fatalf("write pipe: %v", err)
	}

	select {
	case got := <-result:
		if got.err != nil {
			t.Fatalf("nextInteractiveKey returned error: %v", got.err)
		}
		if !got.ok || got.key.kind != editorKeyRune || got.key.r != 'x' {
			t.Fatalf("expected rune x, got key=%#v ok=%v", got.key, got.ok)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for pipe-backed input")
	}
}

func TestConsoleKeyEventCanProduceInputAcceptsCtrlVClipboardShortcut(t *testing.T) {
	ctrlV := &consoleKeyEventRecord{
		KeyDown:         1,
		VirtualKeyCode:  windowsVKV,
		UnicodeChar:     0,
		ControlKeyState: windowsLeftCtrlPressed,
	}
	if !consoleKeyEventCanProduceInput(ctrlV) {
		t.Fatal("expected Ctrl+V key event to mark stdin ready for clipboard paste")
	}
	if !consoleKeyEventIsCtrlVDown(ctrlV) {
		t.Fatal("expected Ctrl+V key event to be consumable as explicit clipboard paste")
	}

	ctrlVWithUnicode := *ctrlV
	ctrlVWithUnicode.UnicodeChar = 'v'
	if consoleKeyEventIsCtrlVDown(&ctrlVWithUnicode) {
		t.Fatal("expected Ctrl+V recognizer not to consume normal Unicode input")
	}
}

func TestConsoleKeyEventRecognizesShiftEnterAsNewline(t *testing.T) {
	shiftEnter := &consoleKeyEventRecord{
		KeyDown:         1,
		VirtualKeyCode:  windowsVKReturn,
		UnicodeChar:     '\r',
		ControlKeyState: windowsShiftPressed,
	}
	if !consoleKeyEventIsModifiedEnterDown(shiftEnter) {
		t.Fatal("expected Shift+Enter console event to insert a newline")
	}
	plainEnter := *shiftEnter
	plainEnter.ControlKeyState = 0
	if consoleKeyEventIsModifiedEnterDown(&plainEnter) {
		t.Fatal("expected plain Enter to retain submit behavior")
	}
}

func TestConsoleKeyEventCanProduceInputAcceptsCharactersAndEditingKeys(t *testing.T) {
	character := &consoleKeyEventRecord{
		KeyDown:        1,
		VirtualKeyCode: 0x41,
		UnicodeChar:    'a',
	}
	if !consoleKeyEventCanProduceInput(character) {
		t.Fatal("expected key event with Unicode character to mark stdin ready")
	}

	leftArrow := &consoleKeyEventRecord{
		KeyDown:        1,
		VirtualKeyCode: 0x25, // VK_LEFT
		UnicodeChar:    0,
	}
	if !consoleKeyEventCanProduceInput(leftArrow) {
		t.Fatal("expected navigation key to mark stdin ready")
	}

	keyUp := &consoleKeyEventRecord{
		KeyDown:        0,
		VirtualKeyCode: 0x41,
		UnicodeChar:    'a',
	}
	if consoleKeyEventCanProduceInput(keyUp) {
		t.Fatal("expected key-up event not to mark stdin ready")
	}
}

// TestReadInteractiveLineForcedReadWhenPeekAlwaysEmpty 模拟 MobaXterm 的
// cygwin 桥接管道：PeekNamedPipe 永远报告「无数据」（waitForInteractive
// InputReady 恒返回 false,nil），但字节实际已在管道里。回归验证：编辑器
// 空转超过 nonConsoleForcedReadTicks 后必须切换为直接阻塞读，仍能读到键。
//
// 该兜底分支是 Windows 专属：inputbox_editor.go 的强制直读条件包含
// !interactiveStdinNeedsPolledReadiness()，而 Unix 实现恒返回 true（Poll
// 对 tty/管道都可靠），所以此回归必须在 windows 构建下验证，不能放在
// 无 build tag 的测试文件里（否则 Linux CI 必然 5s 超时）。
func TestReadInteractiveLineForcedReadWhenPeekAlwaysEmpty(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer reader.Close()
	defer writer.Close()

	oldWait := waitForInteractiveInputReady
	waitForInteractiveInputReady = func(int, time.Duration) (bool, error) {
		return false, nil
	}
	defer func() {
		waitForInteractiveInputReady = oldWait
	}()

	if _, err := writer.WriteString("hello cygwin\n"); err != nil {
		t.Fatalf("seed input: %v", err)
	}

	var output bytes.Buffer
	type lineResult struct {
		line string
		err  error
	}
	result := make(chan lineResult, 1)
	go func() {
		line, err := readInteractiveLine(reader, &output, UserPromptText(0), nil, nil)
		result <- lineResult{line, err}
	}()

	select {
	case r := <-result:
		if r.err != nil {
			t.Fatalf("readInteractiveLine: %v", r.err)
		}
		if r.line != "hello cygwin" {
			t.Fatalf("expected 'hello cygwin', got %q", r.line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("editor starved: peek always empty and forced direct read never engaged")
	}
}
